package gitlab

import (
	"context"
	"errors"
	"net/http"
	"strconv"
)

func (c *Client) Project(ctx context.Context, token string, id int64) (Project, error) {
	if id <= 0 {
		return Project{}, errors.New("invalid project ID")
	}
	var project Project
	_, err := c.request(ctx, token, http.MethodGet, "/projects/"+strconv.FormatInt(id, 10), nil, nil, &project)
	if err == nil && (project.ID != id || project.PathWithNamespace == "") {
		return Project{}, errResponse
	}
	return project, err
}
