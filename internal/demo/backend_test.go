package demo

import (
	"context"
	"errors"
	"testing"

	"gitlabvars/internal/gitlab"
)

func TestTrustedBackendIncludesSecretsForAppPolicy(t *testing.T) {
	b := New()
	variables, err := b.Variables(context.Background(), "demo", 101)
	if err != nil {
		t.Fatal(err)
	}
	if len(variables) != 7 {
		t.Fatalf("got %d variables, want 7", len(variables))
	}
	for _, key := range []string{"DEMO_MASKED", "DEMO_HIDDEN"} {
		before, err := b.Variable(context.Background(), "demo", 101, key, "*")
		if err != nil || before.Value == "" || !before.Masked {
			t.Fatalf("read %s: %+v, %v", key, before, err)
		}
		after, err := b.UpdateValue(context.Background(), "demo", 101, key, "*", "changed-secret")
		if err != nil || after.Value != "changed-secret" || after.Masked != before.Masked || after.Hidden != before.Hidden {
			t.Fatalf("update changed visibility %s: %+v, %v", key, after, err)
		}
	}
}

func TestGroupTreeIncludesOnlyMatchingDescendants(t *testing.T) {
	b := New()
	ctx := context.Background()
	groups, next, err := b.Groups(ctx, "demo", "platform", 1)
	if err != nil || len(groups) != 3 || next != 0 {
		t.Fatalf("groups %+v %d %v", groups, next, err)
	}
	root, err := b.Group(ctx, "demo", "platform")
	if err != nil || root.ID != 201 {
		t.Fatalf("root %+v %v", root, err)
	}
	projects, err := b.GroupProjects(ctx, "demo", 201)
	if err != nil || len(projects) != 3 {
		t.Fatalf("root projects %+v %v", projects, err)
	}
	children, err := b.DescendantGroups(ctx, "demo", 201)
	if err != nil || len(children) != 2 {
		t.Fatalf("children %+v %v", children, err)
	}
	infra, err := b.Group(ctx, "demo", "203")
	if err != nil || infra.FullPath != "platform/infrastructure" {
		t.Fatalf("infra %+v %v", infra, err)
	}
	projects, err = b.GroupProjects(ctx, "demo", 203)
	if err != nil || len(projects) != 1 || projects[0].ID != 103 {
		t.Fatalf("subgroup projects %+v %v", projects, err)
	}
	children, err = b.DescendantGroups(ctx, "demo", 203)
	if err != nil || len(children) != 0 {
		t.Fatalf("leaf groups %+v %v", children, err)
	}
	if _, err = b.Group(ctx, "demo", "platform/missing"); err == nil {
		t.Fatal("missing group accepted")
	}
	*infra.ParentID = 999
	again, _ := b.Group(ctx, "demo", "203")
	if *again.ParentID != 201 {
		t.Fatal("parent ID escaped mutable backend state")
	}
}

func TestCreateDeleteExactScopedGroupVariable(t *testing.T) {
	b := New()
	ctx := context.Background()
	desc := "Deployment credential"
	input := gitlab.CreateVariable{Key: "NEW_TOKEN", Value: "fictional-demo-token", EnvironmentScope: "production", VariableType: "env_var", Masked: true, Hidden: true, Protected: true, Raw: true, Description: &desc}
	v, err := b.CreateResourceVariable(ctx, "demo", "groups", 201, input)
	if err != nil || v.Key != input.Key || !v.Masked || !v.Hidden || !v.Protected || !v.Raw {
		t.Fatalf("create %+v %v", v, err)
	}
	*v.Description = "outside mutation"
	desc = "mutated input"
	original, err := b.ResourceVariable(ctx, "demo", "groups", 201, "NEW_TOKEN", "production")
	if err != nil || *original.Description != "Deployment credential" {
		t.Fatalf("description escaped: %+v %v", original, err)
	}
	if _, err = b.CreateResourceVariable(ctx, "demo", "groups", 201, input); status(err) != 409 {
		t.Fatalf("duplicate returned %v", err)
	}
	input.EnvironmentScope = "staging"
	input.Hidden = false
	if _, err = b.CreateResourceVariable(ctx, "demo", "groups", 201, input); err != nil {
		t.Fatal(err)
	}
	if err = b.DeleteResourceVariable(ctx, "demo", "groups", 201, "NEW_TOKEN", "production"); err != nil {
		t.Fatal(err)
	}
	if _, err = b.ResourceVariable(ctx, "demo", "groups", 201, "NEW_TOKEN", "production"); status(err) != 404 {
		t.Fatalf("deleted variable still exists: %v", err)
	}
	if v, err = b.ResourceVariable(ctx, "demo", "groups", 201, "NEW_TOKEN", "staging"); err != nil || v.Hidden {
		t.Fatalf("other scope removed: %+v %v", v, err)
	}
	if err = b.DeleteResourceVariable(ctx, "demo", "groups", 201, "NEW_TOKEN", "production"); status(err) != 404 {
		t.Fatalf("repeated delete %v", err)
	}
}

func TestProjectCreateDoesNotOverwriteExistingVariable(t *testing.T) {
	b := New()
	ctx := context.Background()
	input := gitlab.CreateVariable{Key: "APP_ENV", Value: "new-value", EnvironmentScope: "*", VariableType: "env_var"}
	if _, err := b.CreateResourceVariable(ctx, "demo", "projects", 101, input); status(err) != 409 {
		t.Fatalf("existing value create: %v", err)
	}
	v, _ := b.Variable(ctx, "demo", 101, "APP_ENV", "*")
	if v.Value != "production" {
		t.Fatal("create overwrote existing variable")
	}
	input.Key = "EMPTY_VALUE"
	input.Value = ""
	if _, err := b.CreateResourceVariable(ctx, "demo", "projects", 101, input); err != nil {
		t.Fatal("empty regular value should be allowed", err)
	}
}

func TestInvalidResourceAndMaskedValidation(t *testing.T) {
	b := New()
	ctx := context.Background()
	if _, err := b.ResourceVariables(ctx, "demo", "instances", 101); status(err) != 400 {
		t.Fatalf("invalid kind %v", err)
	}
	if _, err := b.ResourceVariables(ctx, "demo", "groups", 999); status(err) != 404 {
		t.Fatalf("unknown group %v", err)
	}
	for _, input := range []gitlab.CreateVariable{
		{Key: "INVALID-KEY", Value: "value"},
		{Key: "TOKEN", Value: "short", Masked: true},
		{Key: "TOKEN", Value: "invalid whitespace", Masked: true},
		{Key: "TOKEN", Value: "hidden-value", Hidden: true},
		{Key: "TOKEN", Value: "value", VariableType: "other"},
	} {
		if _, err := b.CreateResourceVariable(ctx, "demo", "projects", 101, input); status(err) != 422 {
			t.Errorf("invalid input accepted: %+v %v", input, err)
		}
	}
	if _, err := b.UpdateResourceValue(ctx, "demo", "projects", 101, "DEMO_HIDDEN", "*", "short"); status(err) != 422 {
		t.Fatalf("masked constraint ignored: %v", err)
	}
}

func status(err error) int {
	var apiErr *gitlab.Error
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return 0
}

func TestUpdateChangesOnlyExactScopedValue(t *testing.T) {
	b := New()
	ctx := context.Background()
	updated, err := b.UpdateValue(ctx, "demo", 101, "LOG_LEVEL", "staging", "trace")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Value != "trace" || updated.EnvironmentScope != "staging" || updated.VariableType != "env_var" || !updated.Raw {
		t.Fatalf("unexpected updated variable: %+v", updated)
	}
	production, err := b.Variable(ctx, "demo", 101, "LOG_LEVEL", "production")
	if err != nil || production.Value != "info" {
		t.Fatalf("production variable changed: %+v, %v", production, err)
	}
	updated, err = b.UpdateValue(ctx, "demo", 101, "DEPLOY_REGION", "production", "eu-west-1")
	if err != nil || !updated.Protected || !updated.Raw {
		t.Fatalf("metadata changed: %+v, %v", updated, err)
	}
}

func TestReturnedDescriptionDoesNotMutateBackend(t *testing.T) {
	b := New()
	ctx := context.Background()
	v, err := b.Variable(ctx, "demo", 101, "APP_ENV", "*")
	if err != nil {
		t.Fatal(err)
	}
	*v.Description = "changed outside backend"
	original, err := b.Variable(ctx, "demo", 101, "APP_ENV", "*")
	if err != nil || *original.Description != "Окружение приложения" {
		t.Fatal("description escaped mutable backend state")
	}
}

func TestProjectSearchAndMissingProject(t *testing.T) {
	b := New()
	projects, nextPage, err := b.Projects(context.Background(), "demo", "PAYMENTS", 1)
	if err != nil || len(projects) != 1 || projects[0].ID != 101 || nextPage != 0 {
		t.Fatalf("search got %+v, next=%d, error=%v", projects, nextPage, err)
	}
	_, err = b.Variables(context.Background(), "demo", 999)
	var apiErr *gitlab.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Fatalf("missing project returned %v", err)
	}
}
