package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/bwsalmon/grain/pkg/model"
)

// FromTemplateRequest is the body of POST /api/tasks/from-template: one
// task filed from a repository's own declared template
// (.grain/templates/<template>.yml), named by its stem or by its full
// declared id. Fields fills the template's input fields.
type FromTemplateRequest struct {
	Repo     string            `json:"repo"`
	Template string            `json:"template"`
	Fields   map[string]string `json:"fields,omitempty"`
	Approved bool              `json:"approved,omitempty"`
}

// maxFromTemplateBody bounds the one body the route reads.
const maxFromTemplateBody = 64 << 10

// handleCreateTaskFromTemplate is POST /api/tasks/from-template, the
// route of the narrow tasks:from-template scope: what it files is
// whatever POST /api/tasks files for the same template, repository and
// fields, and nothing more. An agent's call is held to POST /api/tasks's
// own rules as well (its gate, which this passes through), so the narrow scope
// never lets a run file what tasks:write would not; a CI job's token
// reaches it through pkg/capability/ciapi, which fixes the repository
// to the job's own and the template to what its grant names.
func (s *Server) handleCreateTaskFromTemplate(w http.ResponseWriter, r *http.Request) {
	var req FromTemplateRequest
	if !readJSONLimit(w, r, &req, maxFromTemplateBody) {
		return
	}
	repo, err := model.ParseRepo(strings.TrimSpace(req.Repo))
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("repo: %w", err))
		return
	}
	id, err := FromTemplateID(repo, req.Template)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	body, err := json.Marshal(struct {
		TemplateID string            `json:"templateId"`
		Fields     map[string]string `json:"fields,omitempty"`
		Repo       string            `json:"repo"`
		Approved   bool              `json:"approved,omitempty"`
	}{id, req.Fields, repo.String(), req.Approved})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Served as POST /api/tasks by the same mux, so the call passes that
	// route's own gate too: an agent's under tasks:write and every rule
	// on what a run may file (agentAPIGate).
	create := r.Clone(r.Context())
	create.URL = &url.URL{Path: "/api/tasks"}
	create.RequestURI = "/api/tasks"
	create.Body = io.NopCloser(bytes.NewReader(body))
	create.ContentLength = int64(len(body))
	create.Header.Set("Content-Type", "application/json")
	s.mux.ServeHTTP(w, create)
}

// FromTemplateID is the declared id of repo's template named by its
// stem ("release-notes") or by that id itself, or why template names
// none of repo's.
func FromTemplateID(repo model.RepoRef, template string) (string, error) {
	stem := FromTemplateStem(repo, template)
	if stem == "" {
		return "", fmt.Errorf("template %q is not one of %s's own .grain/templates: name one by its stem", template, repo)
	}
	return model.RepoDeclaredID(model.DeclaredTemplate, repo, stem), nil
}

// FromTemplateStem is the stem template names in repo -- itself for a
// stem, the stem of repo's own declared id -- or "" for a template that
// is not repo's, or is no stem at all.
func FromTemplateStem(repo model.RepoRef, template string) string {
	template = strings.TrimSpace(template)
	if rest, ok := strings.CutPrefix(template, model.RepoDeclaredID(model.DeclaredTemplate, repo, "")); ok {
		template = rest
	}
	if template == "" || strings.ContainsAny(template, ":/\\") || strings.HasPrefix(template, ".") {
		return ""
	}
	return template
}

// APIRoute is the route pattern r would be served by and the agent API
// scope a call to it needs (route_scopes.go), or false for a request no
// grantable route answers. It is how a listener in front of this server
// -- a CI job's token (pkg/capability/ciapi) -- holds a request to the
// same route table the agent API is held to.
func (s *Server) APIRoute(r *http.Request) (pattern string, scope APIScope, ok bool) {
	if s == nil || s.mux == nil {
		return "", "", false
	}
	_, pattern = s.mux.Handler(r)
	scope, ok = routeScopes[pattern]
	return pattern, scope, ok && scope != scopeInternal && scope != ""
}
