// Package infrastructure builds Terraform input without calling GitLab or Terraform.
package infrastructure

import (
	"archive/zip"
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

//go:embed templates
var templates embed.FS

type Draft struct {
	Groups []Group `json:"groups"`
}

type Group struct {
	Name            string    `json:"name"`
	ParentGroup     string    `json:"parent_group"`
	Description     string    `json:"description"`
	VisibilityLevel string    `json:"visibility_level"`
	Members         []Member  `json:"members"`
	Projects        []Project `json:"projects"`
}

type Member struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

type Project struct {
	Name                 string   `json:"name"`
	Description          string   `json:"description"`
	VisibilityLevel      string   `json:"visibility_level"`
	InitializeWithReadme bool     `json:"initialize_with_readme"`
	DefaultBranch        string   `json:"default_branch"`
	MergeMethod          string   `json:"merge_method"`
	ProtectedBranches    []Branch `json:"protected_branches"`
}

type Branch struct {
	Name             string `json:"name"`
	PushAccessLevel  string `json:"push_access_level"`
	MergeAccessLevel string `json:"merge_access_level"`
	AllowForcePush   bool   `json:"allow_force_push"`
}

type Result struct {
	TFVarsJSON     string `json:"tfvars_json"`
	TerrakubeValue string `json:"terrakube_value"`
	ArchiveBase64  string `json:"archive_base64"`
	Groups         int    `json:"groups"`
	Projects       int    `json:"projects"`
	Members        int    `json:"members"`
	BranchRules    int    `json:"branch_rules"`
}

var slug = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,99}$`)
var username = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,254}$`)

func validSlug(value string) bool {
	lower := strings.ToLower(value)
	return slug.MatchString(value) && !strings.HasSuffix(value, ".") && !strings.HasSuffix(lower, ".git") && !strings.HasSuffix(lower, ".atom")
}

func choice(value string, choices ...string) bool {
	for _, item := range choices {
		if value == item {
			return true
		}
	}
	return false
}

func text(value string) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= 2000 && !strings.ContainsRune(value, '\x00')
}

// A protection rule may include '*'; other ref-name restrictions remain in force.
func validBranch(value string, pattern bool) bool {
	if value == "" || len(value) > 255 || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") || strings.Contains(value, "@{") || value == "@" || strings.Contains(value, "//") {
		return false
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(`~^:?\[\\`, r) || (r == '*' && !pattern) {
			return false
		}
	}
	for _, part := range strings.Split(value, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func marshal(value any) string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value) // Values consist only of maps, strings and booleans.
	return buffer.String()
}

// Terrakube parses its HCL field as native HCL. Escape literal template markers
// there, while leaving ordinary tfvars JSON strings unchanged.
func hclLiteral(value any) any {
	switch item := value.(type) {
	case string:
		return strings.ReplaceAll(strings.ReplaceAll(item, "${", "$${"), "%{", "%%{")
	case map[string]any:
		copy := make(map[string]any, len(item))
		for key, child := range item {
			copy[hclLiteral(key).(string)] = hclLiteral(child)
		}
		return copy
	default:
		return value
	}
}

func Generate(draft Draft, gitlabURL string) (Result, error) {
	result := Result{}
	if len(draft.Groups) == 0 || len(draft.Groups) > 25 {
		return result, fmt.Errorf("Добавьте от 1 до 25 групп.")
	}
	infrastructure := map[string]any{}
	seenGroups := map[string]bool{}
	for _, group := range draft.Groups {
		if !validSlug(group.Name) {
			return result, fmt.Errorf("Имя группы: используйте до 100 латинских букв, цифры, точку, дефис или подчёркивание. Первый символ - буква или цифра.")
		}
		groupKey := strings.ToLower(group.Name)
		if seenGroups[groupKey] {
			return result, fmt.Errorf("Группа %q указана дважды. Ключи групп должны различаться.", group.Name)
		}
		seenGroups[groupKey] = true
		if group.ParentGroup != "" {
			if len(group.ParentGroup) > 500 {
				return result, fmt.Errorf("Слишком длинный путь родительской группы.")
			}
			for _, part := range strings.Split(group.ParentGroup, "/") {
				if !validSlug(part) {
					return result, fmt.Errorf("Укажите полный путь существующей родительской группы, например company/platform.")
				}
			}
		}
		if !text(group.Description) || !choice(group.VisibilityLevel, "private", "internal", "public") {
			return result, fmt.Errorf("Проверьте описание и видимость группы %q.", group.Name)
		}
		if len(group.Members) > 200 || len(group.Projects) > 100 {
			return result, fmt.Errorf("В одной группе допускается до 200 участников и 100 проектов.")
		}
		members, projects := map[string]any{}, map[string]any{}
		seenMembers, seenProjects := map[string]bool{}, map[string]bool{}
		for _, member := range group.Members {
			memberKey := strings.ToLower(member.Username)
			if !username.MatchString(member.Username) || strings.HasSuffix(member.Username, ".") || seenMembers[memberKey] {
				return result, fmt.Errorf("Проверьте username участников группы %q: пустые и повторяющиеся имена недопустимы.", group.Name)
			}
			if !choice(member.Role, "guest", "reporter", "developer", "maintainer", "owner") {
				return result, fmt.Errorf("Неизвестная роль участника %q.", member.Username)
			}
			seenMembers[memberKey] = true
			members[member.Username] = member.Role
			result.Members++
		}
		for projectIndex, project := range group.Projects {
			projectKey := strings.ToLower(project.Name)
			if strings.TrimSpace(project.Name) == "" {
				return result, fmt.Errorf("В группе %q не заполнено имя проекта №%d. Укажите имя или удалите пустую карточку проекта.", group.Name, projectIndex+1)
			}
			if !validSlug(project.Name) {
				return result, fmt.Errorf("Некорректное имя проекта %q в группе %q. Используйте до 100 латинских букв, цифры, точку, дефис или подчёркивание; первый символ - буква или цифра.", project.Name, group.Name)
			}
			if seenProjects[projectKey] {
				return result, fmt.Errorf("Проект %q повторяется в группе %q. Укажите другое имя или удалите повторную карточку.", project.Name, group.Name)
			}
			seenProjects[projectKey] = true
			if !text(project.Description) || !choice(project.VisibilityLevel, "private", "internal", "public") || !choice(project.MergeMethod, "merge", "rebase_merge", "ff") {
				return result, fmt.Errorf("Проверьте настройки проекта %q.", project.Name)
			}
			if project.InitializeWithReadme && !validBranch(project.DefaultBranch, false) {
				return result, fmt.Errorf("Укажите корректную основную ветку проекта %q.", project.Name)
			}
			if len(project.ProtectedBranches) > 20 {
				return result, fmt.Errorf("В проекте допускается до 20 правил защиты веток.")
			}
			branches := map[string]any{}
			for _, branch := range project.ProtectedBranches {
				if !validBranch(branch.Name, true) || branches[branch.Name] != nil {
					return result, fmt.Errorf("Проверьте правила веток проекта %q: имена и шаблоны должны быть корректными и уникальными.", project.Name)
				}
				if !choice(branch.PushAccessLevel, "no one", "developer", "maintainer") || !choice(branch.MergeAccessLevel, "no one", "developer", "maintainer") {
					return result, fmt.Errorf("Неизвестный уровень доступа в правиле ветки %q.", branch.Name)
				}
				if branch.AllowForcePush && branch.PushAccessLevel == "no one" {
					return result, fmt.Errorf("Force push нельзя разрешить, когда push запрещён всем: %s/%s.", project.Name, branch.Name)
				}
				branches[branch.Name] = map[string]any{"push_access_level": branch.PushAccessLevel, "merge_access_level": branch.MergeAccessLevel, "allow_force_push": branch.AllowForcePush}
				result.BranchRules++
			}
			settings := map[string]any{"description": project.Description, "visibility_level": project.VisibilityLevel, "initialize_with_readme": project.InitializeWithReadme, "merge_method": project.MergeMethod}
			if project.InitializeWithReadme {
				settings["default_branch"] = project.DefaultBranch
			}
			if len(branches) > 0 {
				settings["protected_branches"] = branches
			}
			projects[project.Name] = settings
			result.Projects++
		}
		settings := map[string]any{"description": group.Description, "visibility_level": group.VisibilityLevel, "members": members, "projects": projects}
		if group.ParentGroup != "" {
			settings["parent_group"] = group.ParentGroup
		}
		infrastructure[group.Name] = settings
		result.Groups++
	}
	if result.Projects > 250 || result.Members > 1000 {
		return Result{}, fmt.Errorf("В одном комплекте допускается до 250 проектов и 1000 назначений участников.")
	}
	result.TFVarsJSON = marshal(map[string]any{"infrastructure": infrastructure})
	result.TerrakubeValue = marshal(hclLiteral(infrastructure))
	archive, err := bundle(result.TFVarsJSON, gitlabURL)
	if err != nil {
		return Result{}, err
	}
	result.ArchiveBase64 = base64.StdEncoding.EncodeToString(archive)
	return result, nil
}

func bundle(tfvars, gitlabURL string) ([]byte, error) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	add := func(name string, data []byte) error {
		entry, err := writer.Create(name)
		if err != nil {
			return err
		}
		_, err = entry.Write(data)
		return err
	}
	err := fs.WalkDir(templates, "templates", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := templates.ReadFile(path)
		if err != nil {
			return err
		}
		return add(strings.TrimPrefix(path, "templates/"), data)
	})
	if err != nil {
		return nil, err
	}
	if err = add("terraform.tfvars.json", []byte(tfvars)); err != nil {
		return nil, err
	}
	// JSON variable-definition strings are literal; no token is included.
	if err = add("gitlab.auto.tfvars.json", []byte(marshal(map[string]any{"gitlab_base_url": strings.TrimRight(gitlabURL, "/") + "/api/v4/"}))); err != nil {
		return nil, err
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
