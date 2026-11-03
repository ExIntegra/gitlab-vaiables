package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInfrastructureEndpointSecurityAndNoGitLabChanges(t *testing.T) {
	s, backend := fixture()
	h := s.Handler()
	body := `{"groups":[{"name":"backend","visibility_level":"private","members":[],"projects":[]}]}`
	if w := call(h, "POST", "/api/infrastructure/generate", body, "", nil, s.cfg.Origin); w.Code != 401 {
		t.Fatalf("anonymous: %d", w.Code)
	}
	cookie, csrf := signIn(t, h)
	if w := call(h, "POST", "/api/infrastructure/generate", body, "", cookie, s.cfg.Origin); w.Code != 403 {
		t.Fatalf("csrf: %d", w.Code)
	}
	if w := call(h, "POST", "/api/infrastructure/generate", body, csrf, cookie, "https://foreign.example"); w.Code != 403 {
		t.Fatalf("origin: %d", w.Code)
	}
	w := call(h, "POST", "/api/infrastructure/generate", body, csrf, cookie, s.cfg.Origin)
	if w.Code != 200 {
		t.Fatalf("generate: %d %s", w.Code, w.Body)
	}
	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["archive_base64"] == "" || !strings.Contains(result["tfvars_json"].(string), "backend") {
		t.Fatal("incomplete response")
	}
	if backend.writes != 0 || strings.Contains(w.Body.String(), "test-token") {
		t.Fatal("generation touched GitLab or returned a credential")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("generation response is cacheable")
	}
	if w := call(h, "POST", "/api/infrastructure/generate", `{"groups":[],"token":"secret"}`, csrf, cookie, s.cfg.Origin); w.Code != 400 {
		t.Fatalf("unknown input: %d", w.Code)
	}
}

func TestInfrastructureAssetsAreServed(t *testing.T) {
	s, _ := fixture()
	for path, contentType := range map[string]string{"/infrastructure.js": "text/javascript", "/infrastructure.css": "text/css"} {
		w := call(s.Handler(), "GET", path, "", "", nil, "")
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), contentType) || w.Body.Len() == 0 {
			t.Fatalf("asset %s: %d %s", path, w.Code, w.Header().Get("Content-Type"))
		}
	}
}
