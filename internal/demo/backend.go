// Package demo supplies a small, in-memory GitLab substitute for trying the UI.
// It never makes network requests and contains only fictional values.
package demo

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"gitlabvars/internal/gitlab"
)

type Backend struct {
	mu             sync.RWMutex
	projects       []gitlab.Project
	variables      map[int64][]gitlab.Variable
	groups         []gitlab.Group
	groupVariables map[int64][]gitlab.Variable
}

func New() *Backend {
	return &Backend{
		projects: []gitlab.Project{
			{ID: 101, Name: "payments-api", PathWithNamespace: "platform/payments-api", WebURL: "https://gitlab.example.com/platform/payments-api"},
			{ID: 102, Name: "web-app", PathWithNamespace: "platform/frontend/web-app", WebURL: "https://gitlab.example.com/platform/frontend/web-app"},
			{ID: 103, Name: "deploy", PathWithNamespace: "platform/infrastructure/deploy", WebURL: "https://gitlab.example.com/platform/infrastructure/deploy"},
		},
		groups: []gitlab.Group{
			{ID: 201, Name: "platform", FullPath: "platform", WebURL: "https://gitlab.example.com/groups/platform"},
			{ID: 202, Name: "frontend", FullPath: "platform/frontend", WebURL: "https://gitlab.example.com/groups/platform/frontend", ParentID: groupParent(201)},
			{ID: 203, Name: "infrastructure", FullPath: "platform/infrastructure", WebURL: "https://gitlab.example.com/groups/platform/infrastructure", ParentID: groupParent(201)},
		},
		groupVariables: map[int64][]gitlab.Variable{
			201: {variable("REGISTRY_URL", "registry.example.com/platform", "*", "Общий реестр контейнеров"), {Key: "GROUP_DEPLOY_TOKEN", Value: "demo-group-token", EnvironmentScope: "*", VariableType: "env_var", Masked: true, Protected: true, Raw: true}},
			202: {variable("PUBLIC_API_URL", "https://api.example.com", "*", "Общий API для frontend-проектов")},
			203: {variable("DEPLOY_REGION", "eu-central-1", "production", "Регион проектов инфраструктуры"), {Key: "GROUP_HIDDEN_KEY", Value: "demo-hidden-group-key", EnvironmentScope: "*", VariableType: "env_var", Masked: true, Hidden: true, Protected: true, Raw: true}},
		},
		variables: map[int64][]gitlab.Variable{
			101: {
				variable("APP_ENV", "production", "*", "Окружение приложения"),
				variable("LOG_LEVEL", "debug", "staging", "Подробные логи для тестового окружения"),
				variable("LOG_LEVEL", "info", "production", "Уровень логирования рабочего окружения"),
				variable("SERVICE_PORT", "8080", "*", "Порт HTTP-сервиса"),
				{Key: "DEPLOY_REGION", Value: "eu-central-1", EnvironmentScope: "production", VariableType: "env_var", Protected: true, Raw: true},
				{Key: "DEMO_MASKED", Value: "fictional-masked-value", EnvironmentScope: "*", VariableType: "env_var", Masked: true},
				{Key: "DEMO_HIDDEN", Value: "fictional-hidden-value", EnvironmentScope: "*", VariableType: "env_var", Masked: true, Hidden: true},
			},
			102: {
				variable("API_URL", "https://api.example.com", "*", "Адрес публичного API"),
				variable("NODE_ENV", "production", "*", "Режим сборки"),
				variable("FEATURE_CHECKOUT_V2", "true", "staging", "Новая версия оформления заказа"),
				variable("PUBLIC_SITE_URL", "https://shop.example.com", "production", "Адрес сайта"),
			},
			103: {
				variable("DEPLOY_TARGET", "demo-server.example.com", "*", "Демонстрационный сервер"),
				variable("REPLICAS", "3", "production", "Количество реплик"),
				variable("HEALTHCHECK_PATH", "/healthz", "*", "Путь проверки состояния"),
				{Key: "RUNTIME_CONFIG", Value: "server:\n  port: 8080\nlogging:\n  level: info\n", EnvironmentScope: "*", VariableType: "file", Raw: true, Description: description("Пример многострочной файловой переменной")},
			},
		},
	}
}

func variable(key, value, scope, note string) gitlab.Variable {
	return gitlab.Variable{Key: key, Value: value, EnvironmentScope: scope, VariableType: "env_var", Raw: true, Description: description(note)}
}

func description(value string) *string { return &value }

func groupParent(id int64) *int64 { return &id }

func cloneGroup(group gitlab.Group) gitlab.Group {
	if group.ParentID != nil {
		group.ParentID = groupParent(*group.ParentID)
	}
	return group
}

func clone(v gitlab.Variable) gitlab.Variable {
	if v.Description != nil {
		v.Description = description(*v.Description)
	}
	return v
}

func (*Backend) CurrentUser(ctx context.Context, _ string) (gitlab.User, error) {
	if err := ctx.Err(); err != nil {
		return gitlab.User{}, err
	}
	return gitlab.User{ID: 1, Username: "demo", Name: "Демо-пользователь"}, nil
}

func (b *Backend) Projects(ctx context.Context, _, search string, page int) ([]gitlab.Project, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	result := make([]gitlab.Project, 0, len(b.projects))
	if page > 1 {
		return result, 0, nil
	}
	search = strings.ToLower(strings.TrimSpace(search))
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, project := range b.projects {
		if strings.Contains(strings.ToLower(project.PathWithNamespace+" "+project.Name), search) {
			result = append(result, project)
		}
	}
	return result, 0, nil
}

func (b *Backend) Variables(ctx context.Context, token string, projectID int64) ([]gitlab.Variable, error) {
	return b.ResourceVariables(ctx, token, "projects", projectID)
}

// ResourceVariables is a trusted backend boundary. The HTTP layer decides which
// values may be disclosed and never serializes a secret as part of a list.
func (b *Backend) ResourceVariables(ctx context.Context, _ string, kind string, id int64) ([]gitlab.Variable, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	variables, err := b.resourceLocked(kind, id)
	if err != nil {
		return nil, err
	}
	result := make([]gitlab.Variable, 0, len(variables))
	for _, v := range variables {
		result = append(result, clone(v))
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Key == result[j].Key {
			return result[i].EnvironmentScope < result[j].EnvironmentScope
		}
		return result[i].Key < result[j].Key
	})
	return result, nil
}

func (b *Backend) Variable(ctx context.Context, token string, projectID int64, key, scope string) (gitlab.Variable, error) {
	return b.ResourceVariable(ctx, token, "projects", projectID, key, scope)
}

func (b *Backend) ResourceVariable(ctx context.Context, _ string, kind string, id int64, key, scope string) (gitlab.Variable, error) {
	if err := ctx.Err(); err != nil {
		return gitlab.Variable{}, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	variables, err := b.resourceLocked(kind, id)
	if err != nil {
		return gitlab.Variable{}, err
	}
	for _, v := range variables {
		if v.Key == key && v.EnvironmentScope == scope {
			return clone(v), nil
		}
	}
	return gitlab.Variable{}, &gitlab.Error{Status: 404}
}

func (b *Backend) UpdateValue(ctx context.Context, token string, projectID int64, key, scope, value string) (gitlab.Variable, error) {
	return b.UpdateResourceValue(ctx, token, "projects", projectID, key, scope, value)
}

func (b *Backend) UpdateResourceValue(ctx context.Context, _ string, kind string, id int64, key, scope, value string) (gitlab.Variable, error) {
	if err := ctx.Err(); err != nil {
		return gitlab.Variable{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	variables, err := b.resourceLocked(kind, id)
	if err != nil {
		return gitlab.Variable{}, err
	}
	for i, v := range variables {
		if v.Key == key && v.EnvironmentScope == scope {
			if !validValue(value, v.Masked) {
				return gitlab.Variable{}, &gitlab.Error{Status: 422}
			}
			v.Value = value
			variables[i] = v
			return clone(v), nil
		}
	}
	return gitlab.Variable{}, &gitlab.Error{Status: 404}
}

func (b *Backend) resourceLocked(kind string, id int64) ([]gitlab.Variable, error) {
	var variables []gitlab.Variable
	var ok bool
	switch kind {
	case "projects":
		variables, ok = b.variables[id]
	case "groups":
		variables, ok = b.groupVariables[id]
	default:
		return nil, &gitlab.Error{Status: 400}
	}
	if !ok {
		return nil, &gitlab.Error{Status: 404}
	}
	return variables, nil
}

func (b *Backend) setResourceLocked(kind string, id int64, variables []gitlab.Variable) {
	if kind == "groups" {
		b.groupVariables[id] = variables
	} else {
		b.variables[id] = variables
	}
}

func (b *Backend) DeleteResourceVariable(ctx context.Context, _ string, kind string, id int64, key, scope string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	variables, err := b.resourceLocked(kind, id)
	if err != nil {
		return err
	}
	for i, v := range variables {
		if v.Key == key && v.EnvironmentScope == scope {
			copy(variables[i:], variables[i+1:])
			variables[len(variables)-1] = gitlab.Variable{}
			b.setResourceLocked(kind, id, variables[:len(variables)-1])
			return nil
		}
	}
	return &gitlab.Error{Status: 404}
}

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,255}$`)

func validValue(value string, masked bool) bool {
	if utf8.RuneCountInString(value) > 10000 {
		return false
	}
	if masked && (utf8.RuneCountInString(value) < 8 || strings.IndexFunc(value, unicode.IsSpace) >= 0) {
		return false
	}
	return true
}

func (b *Backend) CreateResourceVariable(ctx context.Context, _ string, kind string, id int64, input gitlab.CreateVariable) (gitlab.Variable, error) {
	if err := ctx.Err(); err != nil {
		return gitlab.Variable{}, err
	}
	if input.EnvironmentScope == "" {
		input.EnvironmentScope = "*"
	}
	if input.VariableType == "" {
		input.VariableType = "env_var"
	}
	if !keyPattern.MatchString(input.Key) || !validValue(input.Value, input.Masked) || utf8.RuneCountInString(input.EnvironmentScope) > 255 || (input.VariableType != "env_var" && input.VariableType != "file") || (input.Hidden && !input.Masked) || (input.Description != nil && utf8.RuneCountInString(*input.Description) > 255) {
		return gitlab.Variable{}, &gitlab.Error{Status: 422}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	variables, err := b.resourceLocked(kind, id)
	if err != nil {
		return gitlab.Variable{}, err
	}
	for _, v := range variables {
		if v.Key == input.Key && v.EnvironmentScope == input.EnvironmentScope {
			return gitlab.Variable{}, &gitlab.Error{Status: 409}
		}
	}
	v := clone(gitlab.Variable{Key: input.Key, Value: input.Value, EnvironmentScope: input.EnvironmentScope, VariableType: input.VariableType, Masked: input.Masked, Hidden: input.Hidden, Protected: input.Protected, Raw: input.Raw, Description: input.Description})
	b.setResourceLocked(kind, id, append(variables, v))
	return clone(v), nil
}

func (b *Backend) Groups(ctx context.Context, _ string, search string, page int) ([]gitlab.Group, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	result := []gitlab.Group{}
	if page > 1 {
		return result, 0, nil
	}
	search = strings.ToLower(strings.TrimSpace(search))
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, group := range b.groups {
		if strings.Contains(strings.ToLower(group.FullPath+" "+group.Name), search) {
			result = append(result, cloneGroup(group))
		}
	}
	return result, 0, nil
}

func (b *Backend) Group(ctx context.Context, _ string, ref string) (gitlab.Group, error) {
	if err := ctx.Err(); err != nil {
		return gitlab.Group{}, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.groupLocked(ref)
}

func (b *Backend) groupLocked(ref string) (gitlab.Group, error) {
	ref = strings.Trim(strings.TrimSpace(ref), "/")
	for _, group := range b.groups {
		if ref == strconv.FormatInt(group.ID, 10) || ref == group.FullPath {
			return cloneGroup(group), nil
		}
	}
	return gitlab.Group{}, &gitlab.Error{Status: 404}
}

func (b *Backend) GroupProjects(ctx context.Context, _ string, id int64) ([]gitlab.Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	group, err := b.groupLocked(strconv.FormatInt(id, 10))
	if err != nil {
		return nil, err
	}
	result := []gitlab.Project{}
	for _, project := range b.projects {
		if strings.HasPrefix(project.PathWithNamespace, group.FullPath+"/") {
			result = append(result, project)
		}
	}
	return result, nil
}

func (b *Backend) DescendantGroups(ctx context.Context, _ string, id int64) ([]gitlab.Group, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	group, err := b.groupLocked(strconv.FormatInt(id, 10))
	if err != nil {
		return nil, err
	}
	result := []gitlab.Group{}
	for _, candidate := range b.groups {
		if strings.HasPrefix(candidate.FullPath, group.FullPath+"/") {
			result = append(result, cloneGroup(candidate))
		}
	}
	return result, nil
}
