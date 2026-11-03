package demo

import (
	"context"

	"gitlabvars/internal/gitlab"
)

func (b *Backend) Project(ctx context.Context, _ string, id int64) (gitlab.Project, error) {
	if err := ctx.Err(); err != nil {
		return gitlab.Project{}, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, project := range b.projects {
		if project.ID == id {
			return project, nil
		}
	}
	return gitlab.Project{}, &gitlab.Error{Status: 404}
}
