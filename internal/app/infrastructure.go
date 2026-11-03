package app

import (
	"net/http"

	"gitlabvars/internal/infrastructure"
)

func (s *Server) generateInfrastructure(w http.ResponseWriter, r *http.Request, _ session) {
	var draft infrastructure.Draft
	if !readJSON(w, r, &draft) {
		return
	}
	result, err := infrastructure.Generate(draft, s.cfg.GitLabURL)
	if err != nil {
		fail(w, 400, "infrastructure", err.Error())
		return
	}
	writeJSON(w, 200, result)
}
