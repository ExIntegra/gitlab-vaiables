package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlabvars/internal/demo"
	"gitlabvars/internal/gitlab"
)

func managementFixture(t *testing.T) (*Server, *demo.Backend, http.Handler, *http.Cookie, string) {
	t.Helper()
	b := demo.New()
	s := New(Config{Addr: "127.0.0.1:8080", Origin: "http://localhost:8080", GitLabURL: "https://gitlab.com", SessionTTL: time.Hour}, b, nil)
	h := s.Handler()
	cookie, csrf := signIn(t, h)
	return s, b, h, cookie, csrf
}

func resourceVars(t *testing.T, h http.Handler, c *http.Cookie, kind string, id int64) []variableView {
	t.Helper()
	w := call(h, "GET", fmt.Sprintf("/api/%s/%d/variables", kind, id), "", "", c, "")
	if w.Code != 200 {
		t.Fatalf("list %s/%d: %d %s", kind, id, w.Code, w.Body)
	}
	var result struct {
		Variables []variableView `json:"variables"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Variables
}
func findVariable(t *testing.T, vars []variableView, key, scope string) variableView {
	t.Helper()
	for _, v := range vars {
		if v.Key == key && v.EnvironmentScope == scope {
			return v
		}
	}
	t.Fatalf("missing variable %s/%s", key, scope)
	return variableView{}
}
func identityBody(v variableView) string {
	b, _ := json.Marshal(map[string]string{"environment_scope": v.EnvironmentScope, "version": v.Version})
	return string(b)
}

func TestManagementGroupSearchAndTreeIncludesSubgroups(t *testing.T) {
	_, _, h, c, _ := managementFixture(t)
	for _, query := range []string{"platform", "platform%2Ffrontend"} {
		w := call(h, "GET", "/api/groups?search="+query, "", "", c, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), "platform/frontend") {
			t.Fatalf("search %q: %d %s", query, w.Code, w.Body)
		}
	}
	w := call(h, "GET", "/api/groups/201/tree", "", "", c, "")
	if w.Code != 200 {
		t.Fatalf("tree: %d %s", w.Code, w.Body)
	}
	var tree struct {
		Group    gitlab.Group     `json:"group"`
		Groups   []gitlab.Group   `json:"groups"`
		Projects []gitlab.Project `json:"projects"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &tree); err != nil {
		t.Fatal(err)
	}
	if tree.Group.ID != 201 || len(tree.Groups) != 3 || len(tree.Projects) != 3 {
		t.Fatalf("incomplete tree: %+v", tree)
	}
	if tree.Projects[1].PathWithNamespace != "platform/frontend/web-app" {
		t.Fatal("subgroup project not returned")
	}
	groupVars := resourceVars(t, h, c, "groups", 201)
	v := findVariable(t, groupVars, "GROUP_DEPLOY_TOKEN", "*")
	if !v.Masked || !v.Secret || v.Value != nil {
		t.Fatal("group secret metadata unsafe")
	}
}

func TestManagementCreateAndDeleteExactScopeForProjectsAndGroups(t *testing.T) {
	for _, resource := range []struct {
		kind string
		id   int64
	}{{"projects", 101}, {"groups", 201}} {
		t.Run(resource.kind, func(t *testing.T) {
			s, b, h, c, csrf := managementFixture(t)
			base := fmt.Sprintf("/api/%s/%d/variables", resource.kind, resource.id)
			for _, scope := range []string{"staging", "production"} {
				payload := fmt.Sprintf(`{"key":"NEW_CONFIG","value":"%s","environment_scope":"%s","variable_type":"file","protected":true,"raw":false,"description":"test config"}`, scope, scope)
				w := call(h, "POST", base, payload, csrf, c, s.cfg.Origin)
				if w.Code != 201 {
					t.Fatalf("create %s: %d %s", scope, w.Code, w.Body)
				}
			}
			vars := resourceVars(t, h, c, resource.kind, resource.id)
			production := findVariable(t, vars, "NEW_CONFIG", "production")
			if production.Value == nil || *production.Value != "production" || production.VariableType != "file" || !production.Protected || production.Raw {
				t.Fatalf("creation metadata changed: %+v", production)
			}
			w := call(h, "DELETE", base+"/NEW_CONFIG", identityBody(production), csrf, c, s.cfg.Origin)
			if w.Code != 204 || w.Body.Len() != 0 {
				t.Fatalf("delete: %d %s", w.Code, w.Body)
			}
			if _, err := b.ResourceVariable(context.Background(), "test-token", resource.kind, resource.id, "NEW_CONFIG", "production"); err == nil {
				t.Fatal("deleted scope remains")
			}
			remaining, err := b.ResourceVariable(context.Background(), "test-token", resource.kind, resource.id, "NEW_CONFIG", "staging")
			if err != nil || remaining.Value != "staging" {
				t.Fatal("delete touched another environment")
			}
		})
	}
}

func TestMaskedRevealRequiresExplicitMutationAndReturnsShortLifetime(t *testing.T) {
	s, _, h, c, csrf := managementFixture(t)
	v := findVariable(t, resourceVars(t, h, c, "projects", 101), "DEMO_MASKED", "*")
	if v.Value != nil || !v.Secret {
		t.Fatal("secret exposed in list")
	}
	path := "/api/projects/101/variables/DEMO_MASKED/reveal"
	if w := call(h, "GET", path, "", "", c, ""); w.Code == 200 {
		t.Fatal("GET revealed secret")
	}
	if w := call(h, "POST", path, identityBody(v), "", c, s.cfg.Origin); w.Code != 403 {
		t.Fatalf("missing CSRF: %d", w.Code)
	}
	w := call(h, "POST", path, identityBody(v), csrf, c, s.cfg.Origin)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("reveal: %d %s", w.Code, w.Body)
	}
	var result struct {
		Value   string `json:"value"`
		Expires int    `json:"expires_in"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Value != "fictional-masked-value" || result.Expires != 30 {
		t.Fatalf("unexpected disclosure lifetime %d", result.Expires)
	}
	if findVariable(t, resourceVars(t, h, c, "projects", 101), "DEMO_MASKED", "*").Value != nil {
		t.Fatal("reveal changed list disclosure policy")
	}
}

func TestMaskedAndHiddenCanBeReplacedAndDeletedWithoutDisclosingValues(t *testing.T) {
	for _, resource := range []struct {
		kind   string
		id     int64
		key    string
		hidden bool
	}{{"projects", 101, "DEMO_MASKED", false}, {"projects", 101, "DEMO_HIDDEN", true}, {"groups", 203, "GROUP_HIDDEN_KEY", true}} {
		t.Run(resource.key, func(t *testing.T) {
			s, b, h, c, csrf := managementFixture(t)
			v := findVariable(t, resourceVars(t, h, c, resource.kind, resource.id), resource.key, "*")
			base := fmt.Sprintf("/api/%s/%d/variables/%s", resource.kind, resource.id, resource.key)
			if resource.hidden {
				w := call(h, "POST", base+"/reveal", identityBody(v), csrf, c, s.cfg.Origin)
				if w.Code != 403 || strings.Contains(w.Body.String(), "fictional-hidden-value") || strings.Contains(w.Body.String(), "demo-hidden-group-key") {
					t.Fatalf("hidden disclosure: %d %s", w.Code, w.Body)
				}
			}
			w := call(h, "PUT", base, updateBody(v, "replacement-secret-123"), csrf, c, s.cfg.Origin)
			if w.Code != 200 || strings.Contains(w.Body.String(), "replacement-secret-123") {
				t.Fatalf("unsafe secret replacement: %d %s", w.Code, w.Body)
			}
			var result struct {
				Variable variableView `json:"variable"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Variable.Value != nil || !result.Variable.Masked || result.Variable.Hidden != resource.hidden {
				t.Fatal("secret flags or disclosure changed")
			}
			stored, err := b.ResourceVariable(context.Background(), "token", resource.kind, resource.id, resource.key, "*")
			if err != nil || stored.Value != "replacement-secret-123" || !stored.Masked || stored.Hidden != resource.hidden {
				t.Fatal("replacement failed or changed flags")
			}
			w = call(h, "DELETE", base, identityBody(result.Variable), csrf, c, s.cfg.Origin)
			if w.Code != 204 {
				t.Fatalf("secret delete: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestBulkCreateReportsPartialResultsAndNeverOverwritesExisting(t *testing.T) {
	s, b, h, c, csrf := managementFixture(t)
	payload := `{"project_ids":[101,102,999],"variable":{"key":"APP_ENV","value":"new-value","environment_scope":"*"}}`
	w := call(h, "POST", "/api/variables/bulk", payload, csrf, c, s.cfg.Origin)
	if w.Code != 200 {
		t.Fatalf("bulk: %d %s", w.Code, w.Body)
	}
	var result struct {
		Created int          `json:"created"`
		Failed  int          `json:"failed"`
		Results []bulkResult `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || result.Failed != 2 || len(result.Results) != 3 || result.Results[0].Status != "failed" || result.Results[1].Status != "created" || result.Results[2].Code != "not_found" {
		t.Fatalf("wrong partial result: %+v", result)
	}
	existing, err := b.ResourceVariable(context.Background(), "token", "projects", 101, "APP_ENV", "*")
	if err != nil || existing.Value != "production" {
		t.Fatal("bulk overwrote existing variable")
	}
	created, err := b.ResourceVariable(context.Background(), "token", "projects", 102, "APP_ENV", "*")
	if err != nil || created.Value != "new-value" {
		t.Fatal("bulk did not create missing variable")
	}
}

func TestBulkSecretCreationDoesNotReturnValuesAndValidatesTargets(t *testing.T) {
	s, b, h, c, csrf := managementFixture(t)
	for _, ids := range []string{`[]`, `[101,101]`, `[101,0]`} {
		payload := `{"project_ids":` + ids + `,"variable":{"key":"BULK_SECRET","value":"bulk-secret-123","masked":true}}`
		w := call(h, "POST", "/api/variables/bulk", payload, csrf, c, s.cfg.Origin)
		if w.Code != 400 {
			t.Errorf("invalid target list %s: %d", ids, w.Code)
		}
	}
	if _, err := b.ResourceVariable(context.Background(), "token", "projects", 101, "BULK_SECRET", "*"); err == nil {
		t.Fatal("invalid targets caused partial creation")
	}
	w := call(h, "POST", "/api/variables/bulk", `{"project_ids":[101,102],"variable":{"key":"BULK_SECRET","value":"bulk-secret-123","hidden":true}}`, csrf, c, s.cfg.Origin)
	if w.Code != 200 || strings.Contains(w.Body.String(), "bulk-secret-123") {
		t.Fatalf("unsafe bulk response: %d %s", w.Code, w.Body)
	}
	for _, id := range []int64{101, 102} {
		v := findVariable(t, resourceVars(t, h, c, "projects", id), "BULK_SECRET", "*")
		if v.Value != nil || !v.Hidden || !v.Masked {
			t.Fatal("bulk hidden flag/disclosure policy broken")
		}
	}
}

func TestManagementMutationsAllCheckOriginAndCSRF(t *testing.T) {
	s, _, h, c, csrf := managementFixture(t)
	v := findVariable(t, resourceVars(t, h, c, "groups", 201), "GROUP_DEPLOY_TOKEN", "*")
	for _, op := range []struct{ method, path, body string }{
		{"POST", "/api/groups/201/variables", `{"key":"NEW","value":"value"}`},
		{"PUT", "/api/groups/201/variables/GROUP_DEPLOY_TOKEN", updateBody(v, "replacement-secret")},
		{"DELETE", "/api/groups/201/variables/GROUP_DEPLOY_TOKEN", identityBody(v)},
		{"POST", "/api/groups/201/variables/GROUP_DEPLOY_TOKEN/reveal", identityBody(v)},
		{"POST", "/api/variables/bulk", `{"project_ids":[101],"variable":{"key":"NEW","value":"value"}}`},
	} {
		for _, auth := range []struct{ origin, csrf string }{{"https://evil.example", csrf}, {s.cfg.Origin, ""}} {
			if w := call(h, op.method, op.path, op.body, auth.csrf, c, auth.origin); w.Code != 403 {
				t.Errorf("unprotected %s %s: %d", op.method, op.path, w.Code)
			}
		}
	}
}

func TestSecretVersionCannotBeForgedWithBrowserKnownCSRF(t *testing.T) {
	s, b, h, c, csrf := managementFixture(t)
	v := findVariable(t, resourceVars(t, h, c, "projects", 101), "DEMO_MASKED", "*")
	raw, err := b.ResourceVariable(context.Background(), "token", "projects", 101, "DEMO_MASKED", "*")
	if err != nil {
		t.Fatal(err)
	}
	// Model an attacker who knows the browser CSRF token and guesses the value.
	// That information was sufficient to compute v1 signatures; it must no longer be.
	mac := hmac.New(sha256.New, []byte(csrf))
	_ = json.NewEncoder(mac).Encode(struct {
		Session, Kind string
		ID            int64
		Variable      gitlab.Variable
	}{csrf, "projects", 101, raw})
	forged := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if forged == v.Version || strings.Contains(v.Version, raw.Value) {
		t.Fatal("secret version derives from browser-known key")
	}
	v.Version = forged
	if w := call(h, "PUT", "/api/projects/101/variables/DEMO_MASKED", updateBody(v, "attacker-change"), csrf, c, s.cfg.Origin); w.Code != 409 {
		t.Fatalf("forged version accepted: %d", w.Code)
	}
	if _, err = b.UpdateResourceValue(context.Background(), "token", "projects", 101, "DEMO_MASKED", "*", "external-change"); err != nil {
		t.Fatal(err)
	}
	old := findVariable(t, resourceVars(t, h, c, "projects", 101), "DEMO_MASKED", "*")
	if _, err = b.UpdateResourceValue(context.Background(), "token", "projects", 101, "DEMO_MASKED", "*", "second-external-change"); err != nil {
		t.Fatal(err)
	}
	if w := call(h, "PUT", "/api/projects/101/variables/DEMO_MASKED", updateBody(old, "stale-overwrite"), csrf, c, s.cfg.Origin); w.Code != 409 {
		t.Fatalf("stale secret write accepted: %d", w.Code)
	}
	v = findVariable(t, resourceVars(t, h, c, "projects", 101), "DEMO_MASKED", "*")
	c2, csrf2 := signIn(t, h)
	if w := call(h, "POST", "/api/projects/101/variables/DEMO_MASKED/reveal", identityBody(v), csrf2, c2, s.cfg.Origin); w.Code != 409 {
		t.Fatalf("cross-session secret version accepted: %d", w.Code)
	}
}

type identicalResourceBackend struct {
	*demo.Backend
	variable gitlab.Variable
}

func (b *identicalResourceBackend) ResourceVariables(context.Context, string, string, int64) ([]gitlab.Variable, error) {
	return []gitlab.Variable{b.variable}, nil
}
func (b *identicalResourceBackend) ResourceVariable(context.Context, string, string, int64, string, string) (gitlab.Variable, error) {
	return b.variable, nil
}

func TestVersionIsBoundToResourceKindEvenForSameIDAndValue(t *testing.T) {
	b := &identicalResourceBackend{Backend: demo.New(), variable: gitlab.Variable{Key: "SAME", Value: "secret-value", EnvironmentScope: "*", VariableType: "env_var", Masked: true}}
	s := New(Config{Origin: "http://localhost:8080", SessionTTL: time.Hour}, b, nil)
	h := s.Handler()
	c, csrf := signIn(t, h)
	project := findVariable(t, resourceVars(t, h, c, "projects", 42), "SAME", "*")
	group := findVariable(t, resourceVars(t, h, c, "groups", 42), "SAME", "*")
	if project.Version == group.Version {
		t.Fatal("resource kinds share version domain")
	}
	w := call(h, "POST", "/api/groups/42/variables/SAME/reveal", identityBody(project), csrf, c, s.cfg.Origin)
	if w.Code != 409 || strings.Contains(w.Body.String(), "secret-value") {
		t.Fatalf("cross-kind version accepted: %d %s", w.Code, w.Body)
	}
}

// Model an upstream race or unexpected response that reports a secret as public
// after a mutation. The stored demo variable remains secret; only the response
// loses its flags, which must never make the HTTP layer disclose its value.
type visibilityDroppingBackend struct{ *demo.Backend }

func (b *visibilityDroppingBackend) UpdateResourceValue(ctx context.Context, token, kind string, id int64, key, scope, value string) (gitlab.Variable, error) {
	v, err := b.Backend.UpdateResourceValue(ctx, token, kind, id, key, scope, value)
	v.Masked, v.Hidden = false, false
	return v, err
}

func (b *visibilityDroppingBackend) CreateResourceVariable(ctx context.Context, token, kind string, id int64, input gitlab.CreateVariable) (gitlab.Variable, error) {
	v, err := b.Backend.CreateResourceVariable(ctx, token, kind, id, input)
	v.Masked, v.Hidden = false, false
	return v, err
}

func TestMutationVisibilityMismatchNeverDisclosesSecret(t *testing.T) {
	for _, operation := range []string{"update", "create", "bulk"} {
		t.Run(operation, func(t *testing.T) {
			b := &visibilityDroppingBackend{Backend: demo.New()}
			s := New(Config{Origin: "http://localhost:8080", SessionTTL: time.Hour}, b, nil)
			h := s.Handler()
			cookie, csrf := signIn(t, h)
			method, path, body, want := "", "", "", 0
			switch operation {
			case "update":
				v := findVariable(t, resourceVars(t, h, cookie, "projects", 101), "DEMO_MASKED", "*")
				method, path, body, want = "PUT", "/api/projects/101/variables/DEMO_MASKED", updateBody(v, "must-stay-secret-123"), 409
			case "create":
				method, path, body, want = "POST", "/api/groups/201/variables", `{"key":"NEW_SECRET","value":"must-stay-secret-123","masked":true,"hidden":true}`, 502
			case "bulk":
				method, path, body, want = "POST", "/api/variables/bulk", `{"project_ids":[101,102],"variable":{"key":"BULK_SECRET","value":"must-stay-secret-123","masked":true,"hidden":true}}`, 200
			}
			w := call(h, method, path, body, csrf, cookie, s.cfg.Origin)
			if w.Code != want || strings.Contains(w.Body.String(), "must-stay-secret-123") {
				t.Fatalf("unsafe %s response: %d %s", operation, w.Code, w.Body)
			}
			if !strings.Contains(w.Body.String(), `"visibility_changed"`) {
				t.Fatalf("missing visibility warning: %s", w.Body)
			}
			if operation == "bulk" {
				var result struct {
					Created, Failed int
					Results         []bulkResult
				}
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.Created != 0 || result.Failed != 2 || len(result.Results) != 2 || result.Results[0].Code != "visibility_changed" || result.Results[1].Code != "visibility_changed" {
					t.Fatalf("unconfirmed secret creation marked successful: %+v", result)
				}
			}
		})
	}
}
