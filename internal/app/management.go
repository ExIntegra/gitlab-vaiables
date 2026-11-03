package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"gitlabvars/internal/gitlab"
)

type managementBackend interface {
	Backend
	ResourceVariables(context.Context, string, string, int64) ([]gitlab.Variable, error)
	ResourceVariable(context.Context, string, string, int64, string, string) (gitlab.Variable, error)
	UpdateResourceValue(context.Context, string, string, int64, string, string, string) (gitlab.Variable, error)
	DeleteResourceVariable(context.Context, string, string, int64, string, string) error
	CreateResourceVariable(context.Context, string, string, int64, gitlab.CreateVariable) (gitlab.Variable, error)
	Groups(context.Context, string, string, int) ([]gitlab.Group, int, error)
	Group(context.Context, string, string) (gitlab.Group, error)
	GroupProjects(context.Context, string, int64) ([]gitlab.Project, error)
	DescendantGroups(context.Context, string, int64) ([]gitlab.Group, error)
}

func resource(w http.ResponseWriter, r *http.Request) (string, int64, bool) {
	kind := r.PathValue("kind")
	if kind != "projects" && kind != "groups" {
		fail(w, 404, "not_found", "Неизвестный тип ресурса.")
		return "", 0, false
	}
	id, ok := projectID(w, r)
	return kind, id, ok
}

func (s *Server) listVariables(ctx context.Context, se session, kind string, id int64) ([]gitlab.Variable, error) {
	if b, ok := s.backend(se).(managementBackend); ok {
		return b.ResourceVariables(ctx, se.Token, kind, id)
	}
	if kind == "projects" {
		return s.backend(se).Variables(ctx, se.Token, id)
	}
	return nil, &gitlab.Error{Status: 404}
}
func (s *Server) readVariable(ctx context.Context, se session, kind string, id int64, key, scope string) (gitlab.Variable, error) {
	var v gitlab.Variable
	var err error
	if b, ok := s.backend(se).(managementBackend); ok {
		v, err = b.ResourceVariable(ctx, se.Token, kind, id, key, scope)
	} else if kind == "projects" {
		v, err = s.backend(se).Variable(ctx, se.Token, id, key, scope)
	} else {
		err = &gitlab.Error{Status: 404}
	}
	if err == nil && (v.Key != key || v.EnvironmentScope != scope) {
		return gitlab.Variable{}, errors.New("upstream variable identity mismatch")
	}
	return v, err
}

func (s *Server) resourceVersion(se session, kind string, id int64, v gitlab.Variable) string {
	m := hmac.New(sha256.New, s.signingKey[:])
	// A browser knows its CSRF token. The signing key must remain server-only so
	// secret versions cannot be used to test guesses of low-entropy secrets.
	_ = json.NewEncoder(m).Encode(struct {
		Session, Kind string
		ID            int64
		Variable      gitlab.Variable
	}{se.CSRF, kind, id, v})
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
func (s *Server) resourceView(se session, kind string, id int64, v gitlab.Variable) variableView {
	result := variableView{Key: v.Key, EnvironmentScope: v.EnvironmentScope, VariableType: v.VariableType, Protected: v.Protected, Raw: v.Raw, Description: v.Description, Masked: v.Masked, Hidden: v.Hidden, Secret: v.Masked || v.Hidden, Version: s.resourceVersion(se, kind, id, v)}
	if !result.Secret {
		value := v.Value
		result.Value = &value
	}
	return result
}
func (s *Server) resourceVariables(w http.ResponseWriter, r *http.Request, se session) {
	kind, id, ok := resource(w, r)
	if !ok {
		return
	}
	vars, err := s.listVariables(r.Context(), se, kind, id)
	if err != nil {
		s.upstreamError(w, r, err)
		return
	}
	views := make([]variableView, 0, len(vars))
	for _, v := range vars {
		views = append(views, s.resourceView(se, kind, id, v))
	}
	writeJSON(w, 200, map[string]any{"variables": views})
}

type mutationInput struct {
	Value            *string `json:"value,omitempty"`
	EnvironmentScope string  `json:"environment_scope"`
	Version          string  `json:"version"`
}

func parseMutation(w http.ResponseWriter, r *http.Request) (mutationInput, bool) {
	var input mutationInput
	if !readJSON(w, r, &input) {
		return input, false
	}
	if !keyPattern.MatchString(r.PathValue("key")) || input.EnvironmentScope == "" || utf8.RuneCountInString(input.EnvironmentScope) > 255 || len(input.Version) != 43 {
		fail(w, 400, "validation", "Проверьте ключ, окружение и версию переменной.")
		return input, false
	}
	return input, true
}
func (s *Server) variableLock(se session, kind string, id int64, key, scope string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = fmt.Fprintf(h, "%t/%s/%d/%s/%s", se.Demo, kind, id, key, scope)
	return &s.locks[h.Sum32()%uint32(len(s.locks))]
}
func (s *Server) currentVersion(w http.ResponseWriter, r *http.Request, se session, kind string, id int64, input mutationInput) (gitlab.Variable, bool) {
	v, err := s.readVariable(r.Context(), se, kind, id, r.PathValue("key"), input.EnvironmentScope)
	if err != nil {
		s.upstreamError(w, r, err)
		return v, false
	}
	if !hmac.Equal([]byte(input.Version), []byte(s.resourceVersion(se, kind, id, v))) {
		fail(w, 409, "conflict", "Переменная изменилась. Обновите список и проверьте актуальную версию.")
		return v, false
	}
	return v, true
}
func (s *Server) resourceUpdate(w http.ResponseWriter, r *http.Request, se session) {
	kind, id, ok := resource(w, r)
	if !ok {
		return
	}
	input, ok := parseMutation(w, r)
	if !ok {
		return
	}
	if input.Value == nil || utf8.RuneCountInString(*input.Value) > 10000 {
		fail(w, 400, "validation", "Введите значение длиной до 10 000 символов.")
		return
	}
	lock := s.variableLock(se, kind, id, r.PathValue("key"), input.EnvironmentScope)
	lock.Lock()
	defer lock.Unlock()
	current, ok := s.currentVersion(w, r, se, kind, id, input)
	if !ok {
		return
	}
	var updated gitlab.Variable
	var err error
	if b, ok := s.backend(se).(managementBackend); ok {
		updated, err = b.UpdateResourceValue(r.Context(), se.Token, kind, id, r.PathValue("key"), input.EnvironmentScope, *input.Value)
	} else {
		updated, err = s.backend(se).UpdateValue(r.Context(), se.Token, id, r.PathValue("key"), input.EnvironmentScope, *input.Value)
	}
	if err != nil {
		s.upstreamError(w, r, err)
		return
	}
	if updated.Masked != current.Masked || updated.Hidden != current.Hidden {
		fail(w, 409, "visibility_changed", "GitLab вернул другие параметры секретности. Значение могло сохраниться; проверьте переменную в GitLab перед повторной записью.")
		return
	}
	s.audit(se, "update", kind, id)
	writeJSON(w, 200, map[string]any{"variable": s.resourceView(se, kind, id, updated)})
}

func (s *Server) revealVariable(w http.ResponseWriter, r *http.Request, se session) {
	kind, id, ok := resource(w, r)
	if !ok {
		return
	}
	input, ok := parseMutation(w, r)
	if !ok {
		return
	}
	v, ok := s.currentVersion(w, r, se, kind, id, input)
	if !ok {
		return
	}
	if v.Hidden {
		s.upstreamError(w, r, gitlab.ErrSensitive)
		return
	}
	s.audit(se, "reveal", kind, id)
	writeJSON(w, 200, map[string]any{"value": v.Value, "expires_in": 30})
}

func (s *Server) deleteVariable(w http.ResponseWriter, r *http.Request, se session) {
	kind, id, ok := resource(w, r)
	if !ok {
		return
	}
	input, ok := parseMutation(w, r)
	if !ok {
		return
	}
	b, ok := s.backend(se).(managementBackend)
	if !ok {
		fail(w, 503, "unavailable", "Операция временно недоступна.")
		return
	}
	lock := s.variableLock(se, kind, id, r.PathValue("key"), input.EnvironmentScope)
	lock.Lock()
	defer lock.Unlock()
	if _, ok = s.currentVersion(w, r, se, kind, id, input); !ok {
		return
	}
	if err := b.DeleteResourceVariable(r.Context(), se.Token, kind, id, r.PathValue("key"), input.EnvironmentScope); err != nil {
		s.upstreamError(w, r, err)
		return
	}
	s.audit(se, "delete", kind, id)
	w.WriteHeader(204)
}

type createInput struct {
	Key              string  `json:"key"`
	Value            *string `json:"value"`
	EnvironmentScope string  `json:"environment_scope"`
	VariableType     string  `json:"variable_type"`
	Masked           bool    `json:"masked"`
	Hidden           bool    `json:"hidden"`
	Protected        bool    `json:"protected"`
	Raw              *bool   `json:"raw"`
	Description      *string `json:"description"`
}

func (input createInput) variable() (gitlab.CreateVariable, string) {
	if input.EnvironmentScope == "" {
		input.EnvironmentScope = "*"
	}
	if input.VariableType == "" {
		input.VariableType = "env_var"
	}
	if !keyPattern.MatchString(input.Key) || input.Value == nil || utf8.RuneCountInString(*input.Value) > 10000 || utf8.RuneCountInString(input.EnvironmentScope) > 255 || (input.VariableType != "env_var" && input.VariableType != "file") || (input.Description != nil && utf8.RuneCountInString(*input.Description) > 255) {
		return gitlab.CreateVariable{}, "Проверьте ключ, значение, окружение и тип переменной."
	}
	if input.Hidden {
		input.Masked = true
	}
	if input.Masked && (utf8.RuneCountInString(*input.Value) < 8 || strings.IndexFunc(*input.Value, unicode.IsSpace) >= 0) {
		return gitlab.CreateVariable{}, "Masked-значение должно содержать не менее 8 символов без пробелов и переносов строк."
	}
	raw := true
	if input.Raw != nil {
		raw = *input.Raw
	}
	return gitlab.CreateVariable{Key: input.Key, Value: *input.Value, EnvironmentScope: input.EnvironmentScope, VariableType: input.VariableType, Masked: input.Masked, Hidden: input.Hidden, Protected: input.Protected, Raw: raw, Description: input.Description}, ""
}
func (s *Server) createVariable(w http.ResponseWriter, r *http.Request, se session) {
	kind, id, ok := resource(w, r)
	if !ok {
		return
	}
	var input createInput
	if !readJSON(w, r, &input) {
		return
	}
	v, message := input.variable()
	if message != "" {
		fail(w, 400, "validation", message)
		return
	}
	b, ok := s.backend(se).(managementBackend)
	if !ok {
		fail(w, 503, "unavailable", "Операция временно недоступна.")
		return
	}
	created, err := b.CreateResourceVariable(r.Context(), se.Token, kind, id, v)
	if err != nil {
		s.upstreamError(w, r, err)
		return
	}
	if created.Masked != v.Masked || created.Hidden != v.Hidden {
		fail(w, 502, "visibility_changed", "GitLab не подтвердил запрошенные параметры секретности. Проверьте созданную переменную в GitLab; повторный запрос автоматически не отправляется.")
		return
	}
	s.audit(se, "create", kind, id)
	writeJSON(w, 201, map[string]any{"variable": s.resourceView(se, kind, id, created)})
}

type bulkResult struct {
	ProjectID int64  `json:"project_id"`
	Status    string `json:"status"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
}

func (s *Server) bulkCreate(w http.ResponseWriter, r *http.Request, se session) {
	var input struct {
		ProjectIDs []int64     `json:"project_ids"`
		Variable   createInput `json:"variable"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	v, message := input.Variable.variable()
	if message != "" {
		fail(w, 400, "validation", message)
		return
	}
	if len(input.ProjectIDs) == 0 || len(input.ProjectIDs) > 50 {
		fail(w, 400, "validation", "Выберите от 1 до 50 проектов.")
		return
	}
	seen := make(map[int64]bool)
	for _, id := range input.ProjectIDs {
		if id < 1 || seen[id] {
			fail(w, 400, "validation", "Список проектов содержит некорректные или повторяющиеся ID.")
			return
		}
		seen[id] = true
	}
	b, ok := s.backend(se).(managementBackend)
	if !ok {
		fail(w, 503, "unavailable", "Операция временно недоступна.")
		return
	}
	results := make([]bulkResult, len(input.ProjectIDs))
	jobs := make(chan int, len(results))
	for i := range results {
		jobs <- i
	}
	close(jobs)
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				id := input.ProjectIDs[i]
				results[i] = bulkResult{ProjectID: id, Status: "created"}
				if created, err := b.CreateResourceVariable(r.Context(), se.Token, "projects", id, v); err != nil {
					code, message := bulkError(err)
					results[i] = bulkResult{ProjectID: id, Status: "failed", Code: code, Message: message}
				} else if created.Masked != v.Masked || created.Hidden != v.Hidden {
					results[i] = bulkResult{ProjectID: id, Status: "failed", Code: "visibility_changed", Message: "GitLab не подтвердил секретность. Проверьте созданную переменную перед повторной попыткой."}
				} else {
					s.audit(se, "create", "projects", id)
				}
			}
		}()
	}
	wg.Wait()
	created := 0
	for _, result := range results {
		if result.Status == "created" {
			created++
		}
		if result.Code == "unauthorized" {
			s.sessions.remove(r)
			s.clearCookie(w)
		}
	}
	writeJSON(w, 200, map[string]any{"results": results, "created": created, "failed": len(results) - created})
}
func bulkError(err error) (string, string) {
	var upstream *gitlab.Error
	if errors.As(err, &upstream) {
		switch upstream.Status {
		case 401:
			return "unauthorized", "Токен GitLab отклонён. Войдите снова."
		case 403:
			return "forbidden", "Недостаточно прав на создание переменной."
		case 404:
			return "not_found", "Проект не найден или недоступен."
		case 400, 409, 422:
			return "rejected", "GitLab отклонил создание: возможно, ключ уже существует или значение не подходит под ограничения."
		case 429:
			return "rate_limit", "Достигнут лимит GitLab. Проверьте состояние перед повторной попыткой."
		}
	}
	return "unavailable", "Не удалось подтвердить создание. Проверьте список переменных перед повторной попыткой."
}

func (s *Server) groups(w http.ResponseWriter, r *http.Request, se session) {
	b, ok := s.backend(se).(managementBackend)
	if !ok {
		fail(w, 503, "unavailable", "Поиск групп недоступен.")
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("search"))
	if utf8.RuneCountInString(query) > 200 {
		fail(w, 400, "search", "Слишком длинное название группы.")
		return
	}
	page := 1
	if value := r.URL.Query().Get("page"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 100000 {
			fail(w, 400, "page", "Некорректный номер страницы.")
			return
		}
		page = n
	}
	// A full path can be resolved exactly; a short name remains a selectable search.
	if strings.Contains(query, "/") && page == 1 {
		group, err := b.Group(r.Context(), se.Token, query)
		if err != nil {
			s.upstreamError(w, r, err)
			return
		}
		writeJSON(w, 200, map[string]any{"groups": []gitlab.Group{group}, "next_page": 0})
		return
	}
	groups, next, err := b.Groups(r.Context(), se.Token, query, page)
	if err != nil {
		s.upstreamError(w, r, err)
		return
	}
	if groups == nil {
		groups = []gitlab.Group{}
	}
	writeJSON(w, 200, map[string]any{"groups": groups, "next_page": next})
}
func (s *Server) groupTree(w http.ResponseWriter, r *http.Request, se session) {
	id, ok := projectID(w, r)
	if !ok {
		return
	}
	b, ok := s.backend(se).(managementBackend)
	if !ok {
		fail(w, 503, "unavailable", "Поиск групп недоступен.")
		return
	}
	group, err := b.Group(r.Context(), se.Token, strconv.FormatInt(id, 10))
	if err != nil {
		s.upstreamError(w, r, err)
		return
	}
	var descendants []gitlab.Group
	var projects []gitlab.Project
	var groupErr, projectErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); descendants, groupErr = b.DescendantGroups(r.Context(), se.Token, id) }()
	go func() { defer wg.Done(); projects, projectErr = b.GroupProjects(r.Context(), se.Token, id) }()
	wg.Wait()
	if groupErr != nil {
		s.upstreamError(w, r, groupErr)
		return
	}
	if projectErr != nil {
		s.upstreamError(w, r, projectErr)
		return
	}
	if projects == nil {
		projects = []gitlab.Project{}
	}
	writeJSON(w, 200, map[string]any{"group": group, "groups": append([]gitlab.Group{group}, descendants...), "projects": projects})
}

func (s *Server) audit(se session, action, kind string, id int64) {
	// Only identifiers and operation names; never token, value or request body.
	log.Printf("audit user=%d demo=%t action=%s resource=%s/%d", se.User.ID, se.Demo, action, kind, id)
}
