package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func variableJSON(key, scope, value string, masked, hidden bool) string {
	data, _ := json.Marshal(Variable{Key: key, EnvironmentScope: scope, Value: value, VariableType: "env_var", Masked: masked, Hidden: hidden, Raw: true})
	return string(data)
}

func TestVariablesAllPagesRetainVisibilityForApplicationPolicy(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "test-token" {
			t.Error("missing token header")
		}
		if r.URL.Path != "/api/v4/projects/42/variables" || r.URL.Query().Get("per_page") != "100" {
			t.Errorf("unexpected request: %v", r.URL)
		}
		switch r.URL.Query().Get("page") {
		case "1":
			w.Header().Set("X-Next-Page", "2")
			fmt.Fprintf(w, "[%s,%s,%s]", variableJSON("PUBLIC", "*", "visible", false, false), variableJSON("MASKED", "*", "must-not-leak", true, false), variableJSON("HIDDEN", "*", "also-secret", false, true))
		case "2":
			w.Header().Set("X-Next-Page", "")
			fmt.Fprintf(w, "[%s]", variableJSON("PUBLIC", "production", "prod", false, false))
		default:
			t.Errorf("unexpected page: %v", r.URL)
		}
	})
	variables, err := client.Variables(context.Background(), "test-token", 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(variables) != 4 || variables[3].EnvironmentScope != "production" || !variables[1].Masked || !variables[2].Hidden {
		t.Fatalf("wrong collection: %#v", variables)
	}
}

func TestMissingMaskedFlagFailsClosed(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"key":"UNKNOWN","value":"secret","environment_scope":"*"}]`)
	})
	variables, err := client.Variables(context.Background(), "token", 1)
	if err == nil || variables != nil {
		t.Fatal("missing visibility flag should reject the response")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("error leaked value")
	}
}

func TestHiddenOptionalForOlderGitLab(t *testing.T) {
	var variable Variable
	if err := json.Unmarshal([]byte(`{"key":"PUBLIC","value":"ok","masked":false,"environment_scope":"*"}`), &variable); err != nil {
		t.Fatal(err)
	}
	if variable.Hidden || variable.Masked {
		t.Fatal("older public variable should be accepted")
	}
}

func TestUpdateValueTargetsScopeAndPreservesMetadata(t *testing.T) {
	var gets, puts int
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/projects/42/variables/CONFIG" || r.URL.Query().Get("filter[environment_scope]") != "review/*" {
			t.Errorf("wrong variable identity: %v", r.URL)
		}
		switch r.Method {
		case http.MethodGet:
			gets++
			io.WriteString(w, variableJSON("CONFIG", "review/*", "old", false, false))
		case http.MethodPut:
			puts++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 1 || body["value"] != "line1\nline2" {
				t.Errorf("PUT must change only value: %#v", body)
			}
			io.WriteString(w, variableJSON("CONFIG", "review/*", "line1\nline2", false, false))
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	})
	variable, err := client.UpdateValue(context.Background(), "token", 42, "CONFIG", "review/*", "line1\nline2")
	if err != nil || variable.Value != "line1\nline2" || gets != 0 || puts != 1 {
		t.Fatalf("update failed: %#v %v; gets %d puts %d", variable, err, gets, puts)
	}
}

func TestSensitiveVariableCanBeReplacedWithoutChangingFlags(t *testing.T) {
	for _, flags := range []struct{ masked, hidden bool }{{true, false}, {false, true}} {
		t.Run(fmt.Sprint(flags), func(t *testing.T) {
			var puts int
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					puts++
				}
				io.WriteString(w, variableJSON("SECRET", "*", "never-return", flags.masked, flags.hidden))
			})
			variable, err := client.UpdateValue(context.Background(), "token", 1, "SECRET", "*", "new")
			if err != nil || puts != 1 || variable.Masked != flags.masked || variable.Hidden != flags.hidden {
				t.Fatalf("sensitive edit failed: %#v %v puts=%d", variable, err, puts)
			}
		})
	}
}

func TestWriteResponseRetainsFlagsForApplicationRedaction(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, variableJSON("CONFIG", "*", "never-return", r.Method == http.MethodPut, false))
	})
	variable, err := client.UpdateValue(context.Background(), "token", 1, "CONFIG", "*", "new")
	if err != nil || !variable.Masked {
		t.Fatalf("lost sensitive write response flags: %#v %v", variable, err)
	}
}

func TestWrongScopeResponseRejected(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, variableJSON("CONFIG", "staging", "other-value", false, false))
	})
	variable, err := client.Variable(context.Background(), "token", 1, "CONFIG", "production")
	if err == nil || variable.Value != "" {
		t.Fatal("mismatched environment response accepted")
	}
}

func TestErrorsDoNotExposeUpstreamBody(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":"test-token super-secret-value"}`)
	})
	_, err := client.CurrentUser(context.Background(), "test-token")
	var upstream *Error
	if !errors.As(err, &upstream) || upstream.Status != 429 || upstream.RetryAfter != "30" {
		t.Fatalf("lost safe upstream status: %v", err)
	}
	if strings.Contains(err.Error(), "test-token") || strings.Contains(err.Error(), "super-secret-value") {
		t.Fatal("error exposes upstream body")
	}
}

func TestRedirectsNeverSendToken(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer target.Close()
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) })
	_, err := client.CurrentUser(context.Background(), "secret")
	var upstream *Error
	if !errors.As(err, &upstream) || upstream.Status != http.StatusFound || redirected.Load() != 0 {
		t.Fatalf("redirect followed: %v count=%d", err, redirected.Load())
	}
}

func TestProjectsQueryAndSubpath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/gitlab/api/v4/projects" || q.Get("membership") != "true" || q.Get("min_access_level") != "40" || q.Get("search") != "team/a & b" || q.Get("page") != "3" {
			t.Errorf("unexpected request %v", r.URL)
		}
		w.Header().Set("X-Next-Page", "4")
		io.WriteString(w, `[{"id":42,"name":"Backend","path_with_namespace":"team/backend","web_url":"https://gitlab.example/team/backend","archived":false}]`)
	}))
	defer server.Close()
	client, err := New(server.URL+"/gitlab/", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	projects, next, err := client.Projects(context.Background(), "token", "team/a & b", 3)
	if err != nil || next != 4 || len(projects) != 1 || projects[0].ID != 42 {
		t.Fatalf("invalid result: %#v %d %v", projects, next, err)
	}
}

func TestPaginationRejectsLoopsAndBadHeaders(t *testing.T) {
	for _, value := range []string{"1", "-1", "bogus", "https://attacker.test", "1000001"} {
		t.Run(value, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Next-Page", value)
				io.WriteString(w, "[]")
			})
			if _, err := client.Variables(context.Background(), "token", 1); err == nil {
				t.Fatal("invalid next page accepted")
			}
		})
	}
}

func TestResponseBodyLimit(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat(" ", maxResponseBytes+1))
	})
	if _, err := client.CurrentUser(context.Background(), "token"); err == nil {
		t.Fatal("oversized response accepted")
	}
}

func TestContextCancellation(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.CurrentUser(ctx, "token"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("context not propagated: %v", err)
	}
}

func TestURLValidation(t *testing.T) {
	for _, address := range []string{"http://gitlab.com", "https://user:secret@gitlab.com", "https://gitlab.com?token=secret", "https://gitlab.com/#fragment", "/relative", "ftp://gitlab.com"} {
		if _, err := New(address, nil); err == nil {
			t.Errorf("unsafe URL accepted: %s", address)
		}
	}
	for _, address := range []string{"https://gitlab.com", "https://example.test/gitlab/", "http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		if _, err := New(address, nil); err != nil {
			t.Errorf("valid URL rejected: %s: %v", address, err)
		}
	}
}
