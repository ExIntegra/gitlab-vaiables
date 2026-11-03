package app

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitlabvars/internal/demo"
	"gitlabvars/internal/gitlab"
)

type deniedVariablesBackend struct{ *demo.Backend }

func (b deniedVariablesBackend) ResourceVariables(context.Context, string, string, int64) ([]gitlab.Variable, error) {
	return nil, &gitlab.Error{Status: http.StatusForbidden}
}

func TestGroupVariableDenialExplainsGroupPermissionWithoutEscalation(t *testing.T) {
	b := deniedVariablesBackend{demo.New()}
	s := New(Config{Origin: "http://localhost:8080", GitLabURL: "https://gitlab.com", SessionTTL: time.Hour}, b, nil)
	h := s.Handler()
	cookie, _ := signIn(t, h)
	for _, tt := range []struct{ path, expected string }{{"/api/groups/201/variables", "переменным выбранной группы"}, {"/api/projects/101/variables", "переменным выбранного проекта"}} {
		w := call(h, "GET", tt.path, "", "", cookie, "")
		if w.Code != 403 || !strings.Contains(w.Body.String(), tt.expected) {
			t.Fatalf("incorrect denial: %d %s", w.Code, w.Body)
		}
		if strings.Contains(tt.path, "/groups/") && (!strings.Contains(w.Body.String(), "Owner") || !strings.Contains(w.Body.String(), "отдельное право")) {
			t.Fatal("group denial ignores group or custom permissions")
		}
	}
}
