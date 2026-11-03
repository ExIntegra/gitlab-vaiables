package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlabvars/internal/gitlab"
)

type fakeBackend struct {
	mu     sync.Mutex
	vars   []gitlab.Variable
	writes int
	err    error
}

func (f *fakeBackend) CurrentUser(ctx context.Context, token string) (gitlab.User, error) {
	if f.err != nil {
		return gitlab.User{}, f.err
	}
	if token != "test-token" && token != "demo" {
		return gitlab.User{}, &gitlab.Error{Status: 401}
	}
	return gitlab.User{ID: 1, Username: "alex", Name: "Alex"}, nil
}
func (f *fakeBackend) Projects(ctx context.Context, token, search string, page int) ([]gitlab.Project, int, error) {
	return []gitlab.Project{{ID: 42, Name: "api", PathWithNamespace: "platform/api"}}, 0, f.err
}
func (f *fakeBackend) Variables(ctx context.Context, token string, id int64) ([]gitlab.Variable, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gitlab.Variable(nil), f.vars...), f.err
}
func (f *fakeBackend) Variable(ctx context.Context, token string, id int64, key, scope string) (gitlab.Variable, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return gitlab.Variable{}, f.err
	}
	for _, v := range f.vars {
		if v.Key == key && v.EnvironmentScope == scope {
			return v, nil
		}
	}
	return gitlab.Variable{}, &gitlab.Error{Status: 404}
}
func (f *fakeBackend) UpdateValue(ctx context.Context, token string, id int64, key, scope, value string) (gitlab.Variable, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	for i, v := range f.vars {
		if v.Key == key && v.EnvironmentScope == scope {
			f.vars[i].Value = value
			return f.vars[i], nil
		}
	}
	return gitlab.Variable{}, &gitlab.Error{Status: 404}
}

func fixture() (*Server, *fakeBackend) {
	desc := "Runtime log level"
	f := &fakeBackend{vars: []gitlab.Variable{
		{Key: "LOG_LEVEL", Value: "info", EnvironmentScope: "production", VariableType: "env_var", Protected: true, Raw: true, Description: &desc},
		{Key: "LOG_LEVEL", Value: "debug", EnvironmentScope: "staging", VariableType: "env_var"},
		{Key: "PASSWORD", Value: "masked-secret", EnvironmentScope: "*", Masked: true},
		{Key: "HIDDEN", Value: "hidden-secret", EnvironmentScope: "*", Hidden: true},
	}}
	c := Config{Addr: "127.0.0.1:8080", Origin: "http://localhost:8080", GitLabURL: "https://gitlab.com", SessionTTL: time.Hour, Demo: true}
	return New(c, f, &fakeBackend{vars: []gitlab.Variable{{Key: "DEMO", Value: "demo-value", EnvironmentScope: "*"}}}), f
}

func call(h http.Handler, method, path, body, csrf string, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func signIn(t *testing.T, h http.Handler) (*http.Cookie, string) {
	t.Helper()
	w := call(h, "POST", "/api/session", `{"token":"test-token"}`, "", nil, "http://localhost:8080")
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	var data struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.Body.String(), "test-token") {
		t.Fatal("token in response")
	}
	return w.Result().Cookies()[0], data.CSRF
}
func getVars(t *testing.T, h http.Handler, c *http.Cookie) []variableView {
	t.Helper()
	w := call(h, "GET", "/api/projects/42/variables", "", "", c, "")
	if w.Code != 200 {
		t.Fatalf("variables: %d %s", w.Code, w.Body)
	}
	var data struct {
		Variables []variableView `json:"variables"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	return data.Variables
}
func updateBody(v variableView, value string) string {
	b, _ := json.Marshal(map[string]any{"value": value, "environment_scope": v.EnvironmentScope, "version": v.Version})
	return string(b)
}

func TestSessionSecurityAndLogout(t *testing.T) {
	s, _ := fixture()
	h := s.Handler()
	cookie, csrf := signIn(t, h)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
		t.Fatalf("insecure cookie: %v", cookie)
	}
	if strings.Contains(cookie.String(), "test-token") {
		t.Fatal("token in cookie")
	}
	w := call(h, "GET", "/api/session", "", "", cookie, "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing session or cache policy")
	}
	w = call(h, "DELETE", "/api/session", "", csrf, cookie, s.cfg.Origin)
	if w.Code != 204 {
		t.Fatalf("logout: %d", w.Code)
	}
	if call(h, "GET", "/api/session", "", "", cookie, "").Code != 401 {
		t.Fatal("logout did not invalidate session")
	}
}

func TestAuthorizationOriginAndCSRF(t *testing.T) {
	s, f := fixture()
	h := s.Handler()
	if call(h, "GET", "/api/projects", "", "", nil, "").Code != 401 {
		t.Fatal("anonymous access")
	}
	c, csrf := signIn(t, h)
	v := getVars(t, h, c)[0]
	for _, tt := range []struct{ origin, csrf string }{{"https://evil.example", csrf}, {"", csrf}, {s.cfg.Origin, ""}, {s.cfg.Origin, "bad"}} {
		w := call(h, "PUT", "/api/projects/42/variables/LOG_LEVEL", updateBody(v, "trace"), tt.csrf, c, tt.origin)
		if w.Code != 403 {
			t.Errorf("origin=%q csrf=%q: %d", tt.origin, tt.csrf, w.Code)
		}
	}
	if f.writes != 0 {
		t.Fatal("unauthorized write reached backend")
	}
}

func TestListNeverExposesSensitiveValues(t *testing.T) {
	s, _ := fixture()
	h := s.Handler()
	c, _ := signIn(t, h)
	w := call(h, "GET", "/api/projects/42/variables", "", "", c, "")
	for _, secret := range []string{"masked-secret", "hidden-secret"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("leak: %s", secret)
		}
	}
	vars := getVars(t, h, c)
	if len(vars) != 4 || vars[2].Key != "PASSWORD" || !vars[2].Secret || vars[2].Value != nil || !vars[3].Hidden || vars[3].Value != nil {
		t.Fatalf("secret metadata missing or value disclosed: %+v", vars)
	}
}

func TestUpdateExactEnvironmentAndPreservesMetadata(t *testing.T) {
	s, f := fixture()
	h := s.Handler()
	c, csrf := signIn(t, h)
	v := getVars(t, h, c)[0]
	w := call(h, "PUT", "/api/projects/42/variables/LOG_LEVEL", updateBody(v, "warn\nline 2"), csrf, c, s.cfg.Origin)
	if w.Code != 200 {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}
	if f.vars[0].Value != "warn\nline 2" || f.vars[1].Value != "debug" {
		t.Fatal("wrong scoped value changed")
	}
	if !f.vars[0].Protected || !f.vars[0].Raw || f.vars[0].Description == nil || *f.vars[0].Description != "Runtime log level" {
		t.Fatal("metadata modified")
	}
	if f.writes != 1 {
		t.Fatalf("writes %d", f.writes)
	}
}

func TestConflictDoesNotOverwriteExternalEdit(t *testing.T) {
	s, f := fixture()
	h := s.Handler()
	c, csrf := signIn(t, h)
	v := getVars(t, h, c)[0]
	f.vars[0].Value = "external change"
	w := call(h, "PUT", "/api/projects/42/variables/LOG_LEVEL", updateBody(v, "trace"), csrf, c, s.cfg.Origin)
	if w.Code != 409 || f.writes != 0 || f.vars[0].Value != "external change" {
		t.Fatalf("conflict lost: %d writes %d", w.Code, f.writes)
	}
}

func TestVersionCannotBeUsedForAnotherScopeOrSession(t *testing.T) {
	s, f := fixture()
	h := s.Handler()
	c, csrf := signIn(t, h)
	v := getVars(t, h, c)[0]
	v.EnvironmentScope = "staging"
	if w := call(h, "PUT", "/api/projects/42/variables/LOG_LEVEL", updateBody(v, "trace"), csrf, c, s.cfg.Origin); w.Code != 409 {
		t.Fatalf("scope replay: %d", w.Code)
	}
	c2, csrf2 := signIn(t, h)
	v.EnvironmentScope = "production"
	if w := call(h, "PUT", "/api/projects/42/variables/LOG_LEVEL", updateBody(v, "trace"), csrf2, c2, s.cfg.Origin); w.Code != 409 {
		t.Fatalf("session replay: %d", w.Code)
	}
	if f.writes != 0 {
		t.Fatal("replay wrote value")
	}
}

func TestSecretWriteRequiresItsOwnCurrentVersion(t *testing.T) {
	s, f := fixture()
	h := s.Handler()
	c, csrf := signIn(t, h)
	v := getVars(t, h, c)[0]
	for _, key := range []string{"PASSWORD", "HIDDEN"} {
		v.EnvironmentScope = "*"
		w := call(h, "PUT", "/api/projects/42/variables/"+key, updateBody(v, "not allowed"), csrf, c, s.cfg.Origin)
		if w.Code != 409 {
			t.Errorf("%s status %d", key, w.Code)
		}
		if strings.Contains(w.Body.String(), "secret") {
			t.Fatal("secret in error")
		}
	}
	v = getVars(t, h, c)[0]
	f.vars[0].Masked = true
	if w := call(h, "PUT", "/api/projects/42/variables/LOG_LEVEL", updateBody(v, "not allowed"), csrf, c, s.cfg.Origin); w.Code != 409 {
		t.Fatalf("visibility transition %d", w.Code)
	}
	if f.writes != 0 {
		t.Fatal("sensitive write")
	}
}

func TestEmptyValueAndMalformedRequests(t *testing.T) {
	s, f := fixture()
	h := s.Handler()
	c, csrf := signIn(t, h)
	v := getVars(t, h, c)[0]
	if w := call(h, "PUT", "/api/projects/42/variables/LOG_LEVEL", updateBody(v, ""), csrf, c, s.cfg.Origin); w.Code != 200 {
		t.Fatalf("empty value rejected: %d", w.Code)
	}
	for _, body := range []string{
		`{`, `{} {}`, `{"unknown":true}`, `{"value":null,"environment_scope":"production","version":"` + v.Version + `"}`,
		updateBody(v, strings.Repeat("я", 10001)), `{"value":"` + strings.Repeat("x", 130000) + `"}`,
	} {
		if w := call(h, "PUT", "/api/projects/42/variables/LOG_LEVEL", body, csrf, c, s.cfg.Origin); w.Code != 400 {
			t.Errorf("malformed status=%d", w.Code)
		}
	}
	if f.writes != 1 {
		t.Fatalf("malformed write: %d", f.writes)
	}
}

func TestSessionExpiresAndHTTPSCookie(t *testing.T) {
	s, _ := fixture()
	s.cfg.Origin = "https://vars.example.com"
	h := s.Handler()
	w := call(h, "POST", "/api/session", `{"token":"test-token"}`, "", nil, s.cfg.Origin)
	if w.Code != 200 {
		t.Fatalf("login %d", w.Code)
	}
	c := w.Result().Cookies()[0]
	if !c.Secure {
		t.Fatal("HTTPS cookie not secure")
	}
	s.sessions.mu.Lock()
	for k, v := range s.sessions.items {
		v.Expires = time.Now().Add(-time.Second)
		s.sessions.items[k] = v
	}
	s.sessions.mu.Unlock()
	if call(h, "GET", "/api/session", "", "", c, "").Code != 401 {
		t.Fatal("expired session accepted")
	}
}

func TestDemoAndLiveAreIsolated(t *testing.T) {
	s, f := fixture()
	h := s.Handler()
	w := call(h, "POST", "/api/demo-session", `{}`, "", nil, s.cfg.Origin)
	if w.Code != 200 {
		t.Fatalf("demo login: %d", w.Code)
	}
	c := w.Result().Cookies()[0]
	vars := getVars(t, h, c)
	if len(vars) != 1 || vars[0].Key != "DEMO" {
		t.Fatalf("demo has live vars: %+v", vars)
	}
	if len(f.vars) != 4 {
		t.Fatal("live data modified")
	}
	s.cfg.Demo = false
	if call(h, "POST", "/api/demo-session", `{}`, "", nil, s.cfg.Origin).Code != 404 {
		t.Fatal("disabled demo accepted")
	}
}

func TestUpstreamStatusMappingAndRevocation(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s, f := fixture()
			h := s.Handler()
			c, _ := signIn(t, h)
			f.err = &gitlab.Error{Status: status, RetryAfter: "15"}
			w := call(h, "GET", "/api/projects", "", "", c, "")
			want := status
			if status == 500 {
				want = 502
			}
			if w.Code != want {
				t.Fatalf("got %d want %d", w.Code, want)
			}
			if status == 429 && w.Header().Get("Retry-After") != "15" {
				t.Fatal("rate limit not preserved")
			}
			if status == 401 && call(h, "GET", "/api/session", "", "", c, "").Code != 401 {
				t.Fatal("revoked token session kept")
			}
		})
	}
}

func TestLoginRateLimit(t *testing.T) {
	s, _ := fixture()
	h := s.Handler()
	for i := 0; i < 10; i++ {
		if w := call(h, "POST", "/api/session", `{"token":"bad-token"}`, "", nil, s.cfg.Origin); w.Code != 401 {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	if w := call(h, "POST", "/api/session", `{"token":"bad-token"}`, "", nil, s.cfg.Origin); w.Code != 429 {
		t.Fatalf("rate limit %d", w.Code)
	}
}

func TestConcurrentWritesAcceptOnlyOneVersion(t *testing.T) {
	s, f := fixture()
	h := s.Handler()
	c1, csrf1 := signIn(t, h)
	c2, csrf2 := signIn(t, h)
	v1 := getVars(t, h, c1)[0]
	v2 := getVars(t, h, c2)[0]
	start := make(chan struct{})
	statuses := make(chan int, 2)
	for _, input := range []struct {
		cookie   *http.Cookie
		csrf     string
		variable variableView
		value    string
	}{{c1, csrf1, v1, "warn"}, {c2, csrf2, v2, "trace"}} {
		go func() {
			<-start
			w := call(h, "PUT", "/api/projects/42/variables/LOG_LEVEL", updateBody(input.variable, input.value), input.csrf, input.cookie, s.cfg.Origin)
			statuses <- w.Code
		}()
	}
	close(start)
	a, b := <-statuses, <-statuses
	if !((a == 200 && b == 409) || (a == 409 && b == 200)) || f.writes != 1 {
		t.Fatalf("concurrent results: %d, %d; writes %d", a, b, f.writes)
	}
}

func TestEmbeddedAssetsAndSecurityHeaders(t *testing.T) {
	s, _ := fixture()
	h := s.Handler()
	for _, path := range []string{"/", "/app.js", "/styles.css", "/healthz"} {
		w := call(h, "GET", path, "", "", nil, "")
		if w.Code != 200 || w.Body.Len() == 0 {
			t.Fatalf("asset %s: %d", path, w.Code)
		}
		if w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("missing headers: %s", path)
		}
	}
	for _, path := range []string{"/missing", "/api/missing", "/go.mod", "/internal/app/server.go"} {
		if w := call(h, "GET", path, "", "", nil, ""); w.Code != 404 {
			t.Fatalf("unexpected file exposed: %s (%d)", path, w.Code)
		}
	}
}
