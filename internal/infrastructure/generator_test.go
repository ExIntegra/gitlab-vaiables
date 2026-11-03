package infrastructure

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func sample() Draft {
	return Draft{Groups: []Group{{Name: "backend", ParentGroup: "company/platform", Description: "Backend", VisibilityLevel: "private", Members: []Member{{Username: "ivan", Role: "maintainer"}}, Projects: []Project{{Name: "api", Description: "API", VisibilityLevel: "private", InitializeWithReadme: true, DefaultBranch: "main", MergeMethod: "merge", ProtectedBranches: []Branch{{Name: "main", PushAccessLevel: "no one", MergeAccessLevel: "maintainer"}}}}}}}
}

func TestGenerateCompleteBundle(t *testing.T) {
	result, err := Generate(sample(), "https://gitlab.example.com/team")
	if err != nil {
		t.Fatal(err)
	}
	var variables map[string]any
	if err := json.Unmarshal([]byte(result.TFVarsJSON), &variables); err != nil {
		t.Fatal(err)
	}
	groups := variables["infrastructure"].(map[string]any)
	backend := groups["backend"].(map[string]any)
	if backend["parent_group"] != "company/platform" || backend["members"].(map[string]any)["ivan"] != "maintainer" {
		t.Fatal("lost parent or membership")
	}
	project := backend["projects"].(map[string]any)["api"].(map[string]any)
	if project["protected_branches"].(map[string]any)["main"].(map[string]any)["push_access_level"] != "no one" {
		t.Fatal("lost branch protection")
	}
	if result.Groups != 1 || result.Projects != 1 || result.Members != 1 || result.BranchRules != 1 {
		t.Fatal("wrong counts")
	}
	archive, err := base64.StdEncoding.DecodeString(result.ArchiveBase64)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, file := range reader.File {
		if strings.Contains(file.Name, "..") || strings.HasPrefix(file.Name, "/") {
			t.Fatal("unsafe archive path")
		}
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[file.Name] = string(data)
		if strings.Contains(string(data), "glpat-") {
			t.Fatal("credential in archive")
		}
	}
	for _, name := range []string{"main.tf", "variables.tf", "README.md", "terraform.tfvars.json", "gitlab.auto.tfvars.json", "modules/create_gitlab_group/main.tf", "modules/create_gitlab_project/main.tf", "modules/add_members_in_group/main.tf"} {
		if files[name] == "" {
			t.Fatalf("missing %s", name)
		}
	}
	if files["terraform.tfvars.json"] != result.TFVarsJSON || !strings.Contains(files["gitlab.auto.tfvars.json"], "https://gitlab.example.com/team/api/v4/") {
		t.Fatal("archive inputs differ")
	}
	if !strings.Contains(files["modules/create_gitlab_project/main.tf"], `resource "gitlab_branch_protection"`) || !strings.Contains(files["variables.tf"], "protected_branches") {
		t.Fatal("rules would be discarded")
	}
}

func TestRejectAmbiguousOrInvalidDrafts(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Draft)
	}{
		{"empty", func(d *Draft) { d.Groups = nil }},
		{"duplicate group", func(d *Draft) { d.Groups = append(d.Groups, d.Groups[0]) }},
		{"duplicate case", func(d *Draft) { g := d.Groups[0]; g.Name = "Backend"; d.Groups = append(d.Groups, g) }},
		{"duplicate project", func(d *Draft) { d.Groups[0].Projects = append(d.Groups[0].Projects, d.Groups[0].Projects[0]) }},
		{"duplicate member", func(d *Draft) { d.Groups[0].Members = append(d.Groups[0].Members, d.Groups[0].Members[0]) }},
		{"unknown role", func(d *Draft) { d.Groups[0].Members[0].Role = "root" }},
		{"parent URL", func(d *Draft) { d.Groups[0].ParentGroup = "https://gitlab.com/company" }},
		{"empty parent component", func(d *Draft) { d.Groups[0].ParentGroup = "company//team" }},
		{"bad ref", func(d *Draft) { d.Groups[0].Projects[0].DefaultBranch = "main..prod" }},
		{"wildcard default", func(d *Draft) { d.Groups[0].Projects[0].DefaultBranch = "release/*" }},
		{"bad branch rule", func(d *Draft) { d.Groups[0].Projects[0].ProtectedBranches[0].Name = "refs/.hidden" }},
		{"duplicate rule", func(d *Draft) {
			p := &d.Groups[0].Projects[0]
			p.ProtectedBranches = append(p.ProtectedBranches, p.ProtectedBranches[0])
		}},
		{"contradictory force push", func(d *Draft) { d.Groups[0].Projects[0].ProtectedBranches[0].AllowForcePush = true }},
		{"invalid visibility", func(d *Draft) { d.Groups[0].VisibilityLevel = "everyone" }},
		{"NUL description", func(d *Draft) { d.Groups[0].Description = "x\x00y" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			draft := sample()
			test.change(&draft)
			if _, err := Generate(draft, "https://gitlab.com"); err == nil {
				t.Fatal("invalid draft accepted")
			}
		})
	}
}

func TestLiteralTemplatesAndEmptyRepositories(t *testing.T) {
	draft := sample()
	draft.Groups[0].Description = `literal ${file("secret")} and %{ if true }`
	p := &draft.Groups[0].Projects[0]
	p.InitializeWithReadme = false
	p.DefaultBranch = ""
	p.ProtectedBranches[0].Name = "release/*"
	result, err := Generate(draft, "https://gitlab.com")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.TFVarsJSON, "default_branch") {
		t.Fatal("empty repo should omit default branch")
	}
	if !strings.Contains(result.TFVarsJSON, "${file(") || !strings.Contains(result.TerrakubeValue, "$${file(") || !strings.Contains(result.TerrakubeValue, "%%{ if true }") {
		t.Fatal("wrong literal escaping for JSON vs HCL")
	}
	var hclValue map[string]any
	if err := json.Unmarshal([]byte(result.TerrakubeValue), &hclValue); err != nil {
		t.Fatal(err)
	}
	if _, nested := hclValue["infrastructure"]; nested {
		t.Fatal("Terrakube value has an extra wrapper")
	}
}

func TestSameProjectAndMemberInDifferentGroups(t *testing.T) {
	draft := sample()
	other := draft.Groups[0]
	other.Name = "frontend"
	draft.Groups = append(draft.Groups, other)
	result, err := Generate(draft, "https://gitlab.com")
	if err != nil {
		t.Fatal(err)
	}
	if result.Projects != 2 || result.Members != 2 {
		t.Fatal("projects or memberships got merged")
	}
}

func TestHyphenatedGroupCanBeGeneratedWithoutProjects(t *testing.T) {
	for _, name := range []string{"deef-fke", "feed-fke"} {
		t.Run(name, func(t *testing.T) {
			draft := sample()
			draft.Groups[0].Name = name
			draft.Groups[0].Projects = nil
			draft.Groups[0].Members = nil
			result, err := Generate(draft, "https://gitlab.com")
			if err != nil {
				t.Fatal(err)
			}
			if result.Groups != 1 || result.Projects != 0 || !strings.Contains(result.TFVarsJSON, `"`+name+`"`) {
				t.Fatal("group without projects was not exported")
			}
		})
	}
}

func TestEmptyProjectErrorIdentifiesTheMissingField(t *testing.T) {
	draft := sample()
	draft.Groups[0].Name = "deef-fke"
	draft.Groups[0].Projects[0].Name = ""
	_, err := Generate(draft, "https://gitlab.com")
	if err == nil || !strings.Contains(err.Error(), `"deef-fke"`) || !strings.Contains(err.Error(), "не заполнено имя проекта №1") || !strings.Contains(err.Error(), "удалите пустую карточку") {
		t.Fatalf("missing-field guidance: %v", err)
	}
}
