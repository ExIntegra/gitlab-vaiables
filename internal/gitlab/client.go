// Package gitlab is the only boundary that talks to the GitLab REST API.
package gitlab

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	pageSize         = 100
	maxPages         = 1000
	maxResponseBytes = 8 << 20
)

var (
	ErrSensitive = errors.New("masked and hidden variables cannot be accessed")
	errResponse  = errors.New("invalid response from GitLab")
	variableKey  = regexp.MustCompile(`^[A-Za-z0-9_]{1,255}$`)
)

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

type Project struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	PathWithNamespace string `json:"path_with_namespace"`
	WebURL            string `json:"web_url"`
	Archived          bool   `json:"archived"`
}

type Group struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	FullPath string `json:"full_path"`
	WebURL   string `json:"web_url"`
	ParentID *int64 `json:"parent_id"`
}

type CreateVariable struct {
	Key              string  `json:"key"`
	Value            string  `json:"value"`
	EnvironmentScope string  `json:"environment_scope"`
	VariableType     string  `json:"variable_type"`
	Masked           bool    `json:"masked"`
	Hidden           bool    `json:"hidden"`
	Protected        bool    `json:"protected"`
	Raw              bool    `json:"raw"`
	Description      *string `json:"description"`
}

type Variable struct {
	Key              string  `json:"key"`
	Value            string  `json:"value"`
	EnvironmentScope string  `json:"environment_scope"`
	VariableType     string  `json:"variable_type"`
	Protected        bool    `json:"protected"`
	Masked           bool    `json:"masked"`
	Hidden           bool    `json:"hidden"`
	Raw              bool    `json:"raw"`
	Description      *string `json:"description"`
}

// UnmarshalJSON fails closed if GitLab omits the visibility flag. Older GitLab
// versions do not have hidden, so only masked is mandatory.
func (v *Variable) UnmarshalJSON(data []byte) error {
	type plain Variable
	var wire struct {
		*plain
		Masked *bool `json:"masked"`
	}
	wire.plain = (*plain)(v)
	if err := json.Unmarshal(data, &wire); err != nil || wire.Masked == nil {
		return errResponse
	}
	v.Masked = *wire.Masked
	if !variableKey.MatchString(v.Key) || v.EnvironmentScope == "" {
		return errResponse
	}
	return nil
}

// Error deliberately excludes upstream response bodies, which may contain
// variable values or credentials. Status and RetryAfter support safe UI errors.
type Error struct {
	Status     int
	RetryAfter string
}

// TransportError describes connectivity without retaining the original error,
// which can contain request details. Kind is network, timeout or tls.
type TransportError struct{ Kind string }

func (e *TransportError) Error() string { return "GitLab connection failed (" + e.Kind + ")" }

func (e *Error) Error() string {
	return fmt.Sprintf("GitLab request failed (HTTP %d)", e.Status)
}

type Client struct {
	base *url.URL
	http *http.Client
}

func New(baseURL string, httpClient *http.Client) (*Client, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base.Hostname() == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("GitLab URL must be an absolute HTTPS instance URL")
	}
	ip := net.ParseIP(base.Hostname())
	loopback := strings.EqualFold(base.Hostname(), "localhost") || (ip != nil && ip.IsLoopback())
	if base.Scheme != "https" && !(base.Scheme == "http" && loopback) {
		return nil, errors.New("GitLab URL requires HTTPS (HTTP is allowed only on loopback)")
	}
	base.Path = strings.TrimSuffix(strings.TrimRight(base.Path, "/"), "/api/v4") + "/api/v4"
	base.RawPath = ""
	client := http.Client{Timeout: 20 * time.Second}
	if httpClient != nil {
		client = *httpClient
		if client.Timeout <= 0 {
			client.Timeout = 20 * time.Second
		}
	}
	// Even a same-host redirect can downgrade TLS or change the configured path.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base: base, http: &client}, nil
}

func (c *Client) CurrentUser(ctx context.Context, token string) (User, error) {
	var user User
	_, err := c.request(ctx, token, http.MethodGet, "/user", nil, nil, &user)
	if err == nil && (user.ID <= 0 || user.Username == "") {
		err = errResponse
	}
	return user, err
}

func (c *Client) Projects(ctx context.Context, token, search string, page int) ([]Project, int, error) {
	if page < 1 || page > 1000000 {
		return nil, 0, errors.New("invalid project page")
	}
	query := url.Values{
		"membership": {"true"}, "min_access_level": {"40"},
		"per_page": {strconv.Itoa(pageSize)}, "page": {strconv.Itoa(page)},
		"order_by": {"name"}, "sort": {"asc"}, "simple": {"true"},
		"search_namespaces": {"true"},
	}
	if search != "" {
		query.Set("search", search)
	}
	projects := make([]Project, 0)
	header, err := c.request(ctx, token, http.MethodGet, "/projects", query, nil, &projects)
	if err != nil {
		return nil, 0, err
	}
	next, err := nextPage(header, page, len(projects))
	return projects, next, err
}

// Variables is the project-specific compatibility wrapper. Disclosure policy
// belongs to the application layer: this adapter returns all GitLab variables.
func (c *Client) Variables(ctx context.Context, token string, projectID int64) ([]Variable, error) {
	return c.ResourceVariables(ctx, token, "projects", projectID)
}

func (c *Client) Variable(ctx context.Context, token string, projectID int64, key, scope string) (Variable, error) {
	return c.ResourceVariable(ctx, token, "projects", projectID, key, scope)
}

// UpdateValue changes only the value; visibility, type, protection and scope are
// left to GitLab. GitLab has no atomic compare-and-swap for this endpoint, so an
// external edit can still occur between the safety read and the PUT.
func (c *Client) UpdateValue(ctx context.Context, token string, projectID int64, key, scope, value string) (Variable, error) {
	return c.UpdateResourceValue(ctx, token, "projects", projectID, key, scope, value)
}

func matchingVariable(variable Variable, key, scope string) (Variable, error) {
	if variable.Key != key || variable.EnvironmentScope != scope {
		return Variable{}, errResponse
	}
	return variable, nil
}

func validateIdentity(projectID int64, key, scope string) error {
	if projectID <= 0 || !variableKey.MatchString(key) || scope == "" || utf8.RuneCountInString(scope) > 255 {
		return errors.New("invalid variable identity")
	}
	return nil
}

func variablesPath(projectID int64) string {
	return "/projects/" + strconv.FormatInt(projectID, 10) + "/variables"
}

func nextPage(header http.Header, current, batchSize int) (int, error) {
	values, present := header["X-Next-Page"]
	if present {
		if len(values) != 1 {
			return 0, errResponse
		}
		value := strings.TrimSpace(values[0])
		if value == "" {
			return 0, nil
		}
		next, err := strconv.Atoi(value)
		if err != nil || next <= current || next > 1000000 {
			return 0, errResponse
		}
		return next, nil
	}
	// Some reverse proxies drop pagination headers; a full page may have a successor.
	if batchSize >= pageSize {
		return current + 1, nil
	}
	return 0, nil
}

func (c *Client) request(ctx context.Context, token, method, endpoint string, query url.Values, body any, target any) (http.Header, error) {
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("invalid GitLab token")
	}
	u := *c.base
	// Endpoints are escaped paths. Preserve encoded slashes in full group paths;
	// assigning an escaped string to URL.Path would double-escape them.
	u.RawPath = c.base.EscapedPath() + endpoint
	u.Path, _ = url.PathUnescape(u.RawPath)
	u.RawQuery = query.Encode()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, errors.New("cannot encode GitLab request")
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, errors.New("cannot create GitLab request")
	}
	request.Header.Set("PRIVATE-TOKEN", token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "gitlabvars/1.0")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var netError net.Error
		if errors.As(err, &netError) && netError.Timeout() {
			return nil, &TransportError{Kind: "timeout"}
		}
		var certificate x509.UnknownAuthorityError
		var invalidCertificate x509.CertificateInvalidError
		var hostname x509.HostnameError
		if errors.As(err, &certificate) || errors.As(err, &invalidCertificate) || errors.As(err, &hostname) {
			return nil, &TransportError{Kind: "tls"}
		}
		return nil, &TransportError{Kind: "network"}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		retryAfter := response.Header.Get("Retry-After")
		if len(retryAfter) > 128 || strings.ContainsAny(retryAfter, "\r\n") {
			retryAfter = ""
		}
		return nil, &Error{Status: response.StatusCode, RetryAfter: retryAfter}
	}
	if target == nil {
		return response.Header, nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes || bytes.Equal(bytes.TrimSpace(data), []byte("null")) || json.Unmarshal(data, target) != nil {
		return nil, errResponse
	}
	return response.Header, nil
}
