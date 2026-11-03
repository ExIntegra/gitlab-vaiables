package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxCollectionItems = 20000
const maxCollectionBytes = 64 << 20

func resourcePath(kind string, id int64) (string, error) {
	if (kind != "projects" && kind != "groups") || id <= 0 {
		return "", errors.New("invalid GitLab resource")
	}
	return "/" + kind + "/" + strconv.FormatInt(id, 10) + "/variables", nil
}

// collectPages fails rather than returning an incomplete collection. The size
// limits bound memory when a configured upstream sends unusually large data.
func collectPages[T any](ctx context.Context, c *Client, token, endpoint string, query url.Values) ([]T, error) {
	result := make([]T, 0)
	page, totalBytes := 1, 0
	if query == nil {
		query = make(url.Values)
	} else {
		query = cloneQuery(query)
	}
	query.Set("per_page", strconv.Itoa(pageSize))
	for count := 0; count < maxPages; count++ {
		query.Set("page", strconv.Itoa(page))
		var batch []T
		header, err := c.request(ctx, token, http.MethodGet, endpoint, query, nil, &batch)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(batch)
		if err != nil {
			return nil, errResponse
		}
		totalBytes += len(encoded)
		if len(result)+len(batch) > maxCollectionItems || totalBytes > maxCollectionBytes {
			return nil, errors.New("GitLab collection limit exceeded")
		}
		result = append(result, batch...)
		next, err := nextPage(header, page, len(batch))
		if err != nil {
			return nil, err
		}
		if next == 0 {
			return result, nil
		}
		page = next
	}
	return nil, errors.New("GitLab pagination limit exceeded")
}

func cloneQuery(query url.Values) url.Values {
	copy := make(url.Values, len(query))
	for key, values := range query {
		copy[key] = append([]string(nil), values...)
	}
	return copy
}

func (c *Client) ResourceVariables(ctx context.Context, token, kind string, id int64) ([]Variable, error) {
	path, err := resourcePath(kind, id)
	if err != nil {
		return nil, err
	}
	return collectPages[Variable](ctx, c, token, path, nil)
}

func (c *Client) ResourceVariable(ctx context.Context, token, kind string, id int64, key, scope string) (Variable, error) {
	path, err := resourcePath(kind, id)
	if err != nil {
		return Variable{}, err
	}
	if err := validateIdentity(id, key, scope); err != nil {
		return Variable{}, err
	}
	var variable Variable
	_, err = c.request(ctx, token, http.MethodGet, path+"/"+key, url.Values{"filter[environment_scope]": {scope}}, nil, &variable)
	if err != nil {
		return Variable{}, err
	}
	return matchingVariable(variable, key, scope)
}

// UpdateResourceValue sends only value. The caller performs its version and
// disclosure checks before invoking this non-conditional upstream operation.
func (c *Client) UpdateResourceValue(ctx context.Context, token, kind string, id int64, key, scope, value string) (Variable, error) {
	path, err := resourcePath(kind, id)
	if err != nil {
		return Variable{}, err
	}
	if err := validateIdentity(id, key, scope); err != nil {
		return Variable{}, err
	}
	if utf8.RuneCountInString(value) > 10000 {
		return Variable{}, errors.New("variable value is too long")
	}
	var variable Variable
	_, err = c.request(ctx, token, http.MethodPut, path+"/"+key, url.Values{"filter[environment_scope]": {scope}}, struct {
		Value string `json:"value"`
	}{value}, &variable)
	if err != nil {
		return Variable{}, err
	}
	return matchingVariable(variable, key, scope)
}

func (c *Client) DeleteResourceVariable(ctx context.Context, token, kind string, id int64, key, scope string) error {
	path, err := resourcePath(kind, id)
	if err != nil {
		return err
	}
	if err := validateIdentity(id, key, scope); err != nil {
		return err
	}
	_, err = c.request(ctx, token, http.MethodDelete, path+"/"+key, url.Values{"filter[environment_scope]": {scope}}, nil, nil)
	return err
}

func (c *Client) CreateResourceVariable(ctx context.Context, token, kind string, id int64, input CreateVariable) (Variable, error) {
	path, err := resourcePath(kind, id)
	if err != nil {
		return Variable{}, err
	}
	if err := validateIdentity(id, input.Key, input.EnvironmentScope); err != nil {
		return Variable{}, err
	}
	if utf8.RuneCountInString(input.Value) > 10000 || (input.VariableType != "env_var" && input.VariableType != "file") || (input.Hidden && !input.Masked) || (input.Description != nil && utf8.RuneCountInString(*input.Description) > 255) {
		return Variable{}, errors.New("invalid variable attributes")
	}
	body := struct {
		Key              string  `json:"key"`
		Value            string  `json:"value"`
		EnvironmentScope string  `json:"environment_scope"`
		VariableType     string  `json:"variable_type"`
		Masked           bool    `json:"masked"`
		MaskedAndHidden  bool    `json:"masked_and_hidden,omitempty"`
		Protected        bool    `json:"protected"`
		Raw              bool    `json:"raw"`
		Description      *string `json:"description,omitempty"`
	}{input.Key, input.Value, input.EnvironmentScope, input.VariableType, input.Masked, input.Hidden, input.Protected, input.Raw, input.Description}
	var variable Variable
	_, err = c.request(ctx, token, http.MethodPost, path, nil, body, &variable)
	if err != nil {
		return Variable{}, err
	}
	return matchingVariable(variable, input.Key, input.EnvironmentScope)
}

func (c *Client) Groups(ctx context.Context, token, search string, page int) ([]Group, int, error) {
	if page < 1 || page > 1000000 {
		return nil, 0, errors.New("invalid group page")
	}
	query := url.Values{"all_available": {"false"}, "per_page": {strconv.Itoa(pageSize)}, "page": {strconv.Itoa(page)}, "order_by": {"name"}, "sort": {"asc"}}
	if search != "" {
		query.Set("search", search)
	}
	groups := make([]Group, 0)
	header, err := c.request(ctx, token, http.MethodGet, "/groups", query, nil, &groups)
	if err != nil {
		return nil, 0, err
	}
	next, err := nextPage(header, page, len(groups))
	return groups, next, err
}

func (c *Client) Group(ctx context.Context, token, ref string) (Group, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || len(ref) > 2048 || strings.ContainsAny(ref, "\r\n?#") {
		return Group{}, errors.New("invalid group reference")
	}
	var group Group
	_, err := c.request(ctx, token, http.MethodGet, "/groups/"+url.PathEscape(ref), nil, nil, &group)
	if err != nil {
		return Group{}, err
	}
	if group.ID <= 0 || group.FullPath == "" {
		return Group{}, errResponse
	}
	return group, nil
}

func (c *Client) GroupProjects(ctx context.Context, token string, id int64) ([]Project, error) {
	if id <= 0 {
		return nil, errors.New("invalid group ID")
	}
	query := url.Values{"include_subgroups": {"true"}, "with_shared": {"false"}, "order_by": {"name"}, "sort": {"asc"}, "simple": {"true"}}
	return collectPages[Project](ctx, c, token, "/groups/"+strconv.FormatInt(id, 10)+"/projects", query)
}

func (c *Client) DescendantGroups(ctx context.Context, token string, id int64) ([]Group, error) {
	if id <= 0 {
		return nil, errors.New("invalid group ID")
	}
	return collectPages[Group](ctx, c, token, "/groups/"+strconv.FormatInt(id, 10)+"/descendant_groups", url.Values{"all_available": {"true"}})
}
