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
	"testing"
)

func TestGroupPathIsEncodedOnceWithInstanceSubpath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/gitlab/api/v4/groups/platform%2Ffrontend" {
			t.Errorf("wrong escaped path: %s", r.URL.EscapedPath())
		}
		io.WriteString(w, `{"id":202,"name":"Frontend","full_path":"platform/frontend","parent_id":201}`)
	}))
	defer server.Close()
	c, err := New(server.URL+"/gitlab", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	g, err := c.Group(context.Background(), "token", "platform/frontend")
	if err != nil || g.ID != 202 || g.ParentID == nil || *g.ParentID != 201 {
		t.Fatalf("group: %+v %v", g, err)
	}
}

func TestGroupDiscoveryIncludesSubgroupsAndEveryPage(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/groups/201/projects":
			q := r.URL.Query()
			if q.Get("include_subgroups") != "true" || q.Get("with_shared") != "false" || q.Has("min_access_level") {
				t.Errorf("bad group-project query: %s", r.URL)
			}
			if q.Get("page") == "1" {
				w.Header().Set("X-Next-Page", "2")
				io.WriteString(w, `[{"id":101,"name":"API","path_with_namespace":"platform/api"}]`)
			} else {
				w.Header().Set("X-Next-Page", "")
				io.WriteString(w, `[{"id":102,"name":"Web","path_with_namespace":"platform/frontend/web"}]`)
			}
		case "/api/v4/groups/201/descendant_groups":
			if r.URL.Query().Get("all_available") != "true" {
				t.Error("missing descendant visibility option")
			}
			io.WriteString(w, `[{"id":202,"name":"Frontend","full_path":"platform/frontend"},{"id":203,"name":"Infra","full_path":"platform/frontend/infra"}]`)
		default:
			t.Errorf("unexpected path %s", r.URL)
			w.WriteHeader(404)
		}
	})
	projects, err := c.GroupProjects(context.Background(), "token", 201)
	if err != nil || len(projects) != 2 || projects[1].ID != 102 {
		t.Fatalf("group projects: %+v %v", projects, err)
	}
	groups, err := c.DescendantGroups(context.Background(), "token", 201)
	if err != nil || len(groups) != 2 || groups[1].ID != 203 {
		t.Fatalf("descendants: %+v %v", groups, err)
	}
}

func TestGroupsSearchPagination(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/api/v4/groups" || q.Get("search") != "platform & team" || q.Get("page") != "2" || q.Get("per_page") != "100" || q.Get("all_available") != "false" {
			t.Errorf("bad search %s", r.URL)
		}
		w.Header().Set("X-Next-Page", "3")
		io.WriteString(w, `[{"id":201,"name":"Platform","full_path":"platform"}]`)
	})
	groups, next, err := c.Groups(context.Background(), "token", "platform & team", 2)
	if err != nil || next != 3 || len(groups) != 1 || groups[0].ID != 201 {
		t.Fatalf("search: %+v %d %v", groups, next, err)
	}
}

func TestResourceCRUDUsesExactKindAndScope(t *testing.T) {
	for _, kind := range []string{"projects", "groups"} {
		t.Run(kind, func(t *testing.T) {
			calls := []string{}
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method)
				if r.URL.Path != "/api/v4/"+kind+"/42/variables/SECRET" || r.URL.Query().Get("filter[environment_scope]") != "review/*" {
					t.Errorf("bad scoped identity %s", r.URL)
				}
				if r.Method == http.MethodDelete {
					w.WriteHeader(204)
					return
				}
				if r.Method == http.MethodPut {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if len(body) != 1 || body["value"] != "new-secret-value" {
						t.Errorf("unexpected PUT fields: %v", body)
					}
				}
				io.WriteString(w, variableJSON("SECRET", "review/*", "new-secret-value", true, true))
			})
			v, err := c.ResourceVariable(context.Background(), "token", kind, 42, "SECRET", "review/*")
			if err != nil || !v.Hidden || !v.Masked {
				t.Fatalf("get: %+v %v", v, err)
			}
			v, err = c.UpdateResourceValue(context.Background(), "token", kind, 42, "SECRET", "review/*", "new-secret-value")
			if err != nil || !v.Hidden {
				t.Fatalf("update: %+v %v", v, err)
			}
			if err := c.DeleteResourceVariable(context.Background(), "token", kind, 42, "SECRET", "review/*"); err != nil {
				t.Fatal(err)
			}
			if strings.Join(calls, ",") != "GET,PUT,DELETE" {
				t.Fatalf("unexpected requests %v", calls)
			}
		})
	}
}

func TestCreateHiddenUsesMaskedAndHiddenWireField(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v4/groups/42/variables" {
			t.Errorf("bad request %s %s", r.Method, r.URL)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["masked_and_hidden"] != true || body["masked"] != true || body["protected"] != true || body["environment_scope"] != "production" || body["raw"] != true {
			t.Errorf("missing creation attributes: %v", body)
		}
		if _, ok := body["hidden"]; ok {
			t.Error("hidden must not be sent; GitLab expects masked_and_hidden")
		}
		w.WriteHeader(201)
		io.WriteString(w, variableJSON("DEPLOY_TOKEN", "production", "", true, true))
	})
	v, err := c.CreateResourceVariable(context.Background(), "token", "groups", 42, CreateVariable{Key: "DEPLOY_TOKEN", Value: "new-secret", EnvironmentScope: "production", VariableType: "env_var", Masked: true, Hidden: true, Protected: true, Raw: true})
	if err != nil || !v.Hidden || v.Key != "DEPLOY_TOKEN" {
		t.Fatalf("create: %+v %v", v, err)
	}
}

func TestResourceKindsAndInputRejectedBeforeNetwork(t *testing.T) {
	requests := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(500) })
	for _, kind := range []string{"users", "../projects", "Projects", ""} {
		if _, err := c.ResourceVariables(context.Background(), "token", kind, 1); err == nil {
			t.Errorf("accepted kind %q", kind)
		}
		if err := c.DeleteResourceVariable(context.Background(), "token", kind, 1, "KEY", "*"); err == nil {
			t.Errorf("accepted kind %q", kind)
		}
	}
	for _, input := range []CreateVariable{
		{Key: "../KEY", Value: "x", EnvironmentScope: "*", VariableType: "env_var"},
		{Key: "KEY", Value: "x", EnvironmentScope: "*", VariableType: "env_var", Hidden: true},
		{Key: "KEY", Value: "x", EnvironmentScope: "*", VariableType: "unknown"},
	} {
		if _, err := c.CreateResourceVariable(context.Background(), "token", "projects", 1, input); err == nil {
			t.Errorf("accepted invalid input")
		}
	}
	if requests != 0 {
		t.Fatalf("invalid request reached network %d", requests)
	}
}

func TestNullCollectionIsRejected(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "null") })
	if _, err := c.ResourceVariables(context.Background(), "token", "groups", 1); err == nil {
		t.Fatal("null list accepted")
	}
}

func TestDeleteErrorDoesNotExposeUpstreamSecrets(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"error":"very-secret-value private-token"}`)
	})
	err := c.DeleteResourceVariable(context.Background(), "token", "groups", 1, "KEY", "*")
	var upstream *Error
	if !errors.As(err, &upstream) || upstream.Status != 403 || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe error: %v", err)
	}
}
