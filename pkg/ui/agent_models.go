package ui

import (
	"context"
	"net/http"
)

// AgentModel is one model an agent framework's own CLI says it can run,
// as GET /api/agent-models reports it. Deliberately its own type rather
// than antigravity.Model itself, for the same reason SandboxSnapshot is
// not orchestrator.SandboxHealth: this package does not import an agent
// framework, so pkg/cli/daemon.go's own adapter is the one place both
// types are in scope, converting one into the other field for field.
type AgentModel struct {
	// ID is what goes in the Gemini model setting, and what the CLI is
	// passed as --model.
	ID string `json:"id"`
	// Label is the CLI's own display name for it ("Gemini 3.1 Pro
	// (High)"), or empty for a listing that gave none.
	Label string `json:"label,omitempty"`
	// Effort is the reasoning effort the id carries as a suffix, and
	// Family the id without it -- the pair that lets a picker group one
	// model's efforts together. agy spells effort into the model name
	// rather than taking a separate flag (antigravity.DefaultModel has
	// why), so this is the whole of what "choose an effort" means here.
	Effort           string   `json:"effort,omitempty"`
	Family           string   `json:"family,omitempty"`
	SupportedEfforts []string `json:"supportedEfforts,omitempty"`
	DefaultEffort    string   `json:"defaultEffort,omitempty"`
}

// AgentModelCatalog is one framework's whole answer: the models it will
// accept, and the reasoning-effort vocabulary its own flags document.
type AgentModelCatalog struct {
	Models  []AgentModel
	Efforts []string
}

// AgentModelLister is implemented by whatever can ask an agent
// framework's CLI what it can run as the named backend -- pkg/cli/daemon.go's own
// agyModelLister over antigravity.Catalog, in a real deployment. See
// Config.AgentModels' own doc comment for the nil-means-unavailable
// contract this interface's zero value satisfies.
type AgentModelLister interface {
	ListModels(ctx context.Context, backendID string) (AgentModelCatalog, error)
}

// agentModelsResponse is GET /api/agent-models' whole body.
//
// The two failure shapes are deliberately different, because the pane
// does two different things with them. Available false is a UI that was
// never wired to a lister (`grain demo`, a UI not colocated with the
// daemon that has the binary): nothing to ask, so the model field is a
// plain text box exactly as it was before this endpoint existed.
// Available true with an Error is a lister that was asked and could not
// answer -- an agy that is not installed, a Gemini key that is missing or
// spent, a listing whose shape this build cannot parse -- and that is
// worth saying out loud, next to the same text box: the vocabulary is
// agy's, this deployment could not read it just now, and a model name
// typed by hand still saves.
//
// So a failure here never costs an operator the ability to set a model.
// That is the whole design constraint (grain/task-365): the picker is a
// convenience over a setting that has always been free text, and it must
// not become the only way to change one.
type agentModelsResponse struct {
	Available bool         `json:"available"`
	Models    []AgentModel `json:"models,omitempty"`
	Efforts   []string     `json:"efforts,omitempty"`
	Error     string       `json:"error,omitempty"`
}

// handleListAgentModels answers with the installed CLI's own catalog.
//
// 200 with an Error rather than a 5xx: nothing here is broken from the
// caller's side, the answer is "this deployment cannot read the
// vocabulary right now", and a pane that has to distinguish a failed
// fetch from a failed probe to render the same fallback would be reading
// the status code for something the body already says.
func (s *Server) handleListAgentModels(w http.ResponseWriter, r *http.Request) {
	lister := s.tasks.Config.AgentModels
	switch r.URL.Query().Get("framework") {
	case "", "gemini", "antigravity":
	case "codex":
		lister = s.tasks.Config.CodexModels
	case "claude":
		lister = s.tasks.Config.ClaudeModels
	default:
		http.Error(w, "unknown agent framework", http.StatusBadRequest)
		return
	}
	if lister == nil {
		writeJSON(w, http.StatusOK, agentModelsResponse{Available: false})
		return
	}
	catalog, err := lister.ListModels(r.Context(), r.URL.Query().Get("backend"))
	if err != nil {
		writeJSON(w, http.StatusOK, agentModelsResponse{Available: true, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, agentModelsResponse{
		Available: true,
		Models:    catalog.Models,
		Efforts:   catalog.Efforts,
	})
}
