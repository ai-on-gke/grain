package ui

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/ai-on-gke/grain/pkg/agent/codex"
	"github.com/ai-on-gke/grain/pkg/agent/credcheck"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// The agent credentials this pane manages: one per agent.Framework grain
// can drive a run with, each stored in this deployment's own secrets
// database under the well-known name cmd/grain's daemon resolves it by
// (secrets.GeminiAPIKeySecret/ClaudeOAuthTokenSecret/OpenAIAPIKeySecret)
// before every dispatch.
//
// They are ordinary secrets, so the Secrets pane this predates could
// already set them by hand -- but only by knowing both the exact secret
// name and the key inside it, with nothing anywhere saying which names
// those are or whether the framework an operator just switched to has a
// credential at all. These handlers are that knowledge, moved into the
// one pane where the framework itself is chosen: set a key, clear a key,
// and report which of them are set, never what they hold (the same
// write-only contract secrets.Store.List gives everything else here).
//
// grain/task-110 took the same argument the rest of the way: every
// secret a capability resolves is reported beside that capability
// (CapabilityStatus.Secrets) and set from there, and the flat Secrets
// tab is gone. These two stay their own endpoints rather than joining
// that listing -- an agent credential belongs to a framework, not to a
// capability, and there is no CapabilitySpec.Requires naming it.
//
// Deliberately not model.Config fields: a credential is not
// configuration, and nothing that reaches the store's config row is
// write-only the way this must be.
// The mapping itself lives in pkg/agent/credcheck, which needs the same
// three names to say which secret a refused credential is stored in
// (agent_key_check.go beside this). One copy, so a fourth framework
// cannot be settable here and untestable there -- it normalizes the
// legacy "gemini" spelling on the way in exactly as this always did.
func agentKeySecret(framework string) (string, bool) {
	return credcheck.SecretFor(framework)
}

// agentKeysResponse is GET /api/agent-keys' body, and what setting or
// clearing one returns afterward -- the same respond-with-the-current-
// shape convention the secrets pane's own handlers follow. Enabled is
// false, with every flag false, when this UI has no local secrets
// directory to write to (Config.Secrets nil), so the pane can say so
// rather than offer a control that could only ever 404.
type agentKeysResponse struct {
	Enabled bool `json:"enabled"`
	// These report presence exactly as the daemon will find it: set
	// means the secret exists and resolves (secrets.Store.Resolve's
	// sole-key form), not merely that something of that name is in the
	// database.
	GeminiAPIKeySet     bool `json:"geminiApiKeySet"`
	ClaudeOAuthTokenSet bool `json:"claudeOAuthTokenSet"`
	OpenAIAPIKeySet     bool `json:"openaiApiKeySet"`
	CodexAuthJSONSet    bool `json:"codexAuthJSONSet"`
	// CodexAuthRefreshError is set when grain's refresher was refused
	// permanently for the deployment-wide login (backendCodexAuthResponse).
	CodexAuthRefreshError string `json:"codexAuthRefreshError,omitempty"`
}

// agentKeysSet reports which agent credentials this deployment has,
// leaving every flag false when there is no secrets store to ask (`grain
// demo`'s throwaway UI) or when listing it fails -- best-effort, the
// same reading capabilityStatuses gives the same listing, since a
// Settings response has never failed on this and an operator is better
// served by a pane that loads and says "not set" than by one that does
// not load.
func (c *Client) agentKeysSet() (gemini, claude, openai, codexAuth bool) {
	if c.Config.Secrets != nil {
		if list, err := c.Config.Secrets.List(); err == nil {
			resolvable := func(secret string) bool {
				return len(missingSecretsFor([]string{secret}, list)) == 0
			}
			gemini = resolvable(secrets.GeminiAPIKeySecret)
			claude = resolvable(secrets.ClaudeOAuthTokenSecret)
			openai = resolvable(secrets.OpenAIAPIKeySecret)
		}
	}
	if c.Config.CodexAuthFile != "" && codex.ValidateAuthFile(c.Config.CodexAuthFile) == nil {
		codexAuth = true
	}
	return
}

// handleListAgentKeys answers GET /api/agent-keys: which agent
// credentials this deployment holds, which is a system administrator's
// to read and nobody else's.
func (s *Server) handleListAgentKeys(w http.ResponseWriter, r *http.Request) {
	s.respondWithAgentKeys(w)
}

type setAgentKeyRequest struct {
	Value string `json:"value"`
}

// handleSetAgentKey writes one framework's credential. The value travels
// in the body, never in the path or a query parameter, for the same
// reason claude.WithOAuthToken passes a token through the environment
// rather than argv: neither belongs anywhere it would be logged.
func (s *Server) handleSetAgentKey(w http.ResponseWriter, r *http.Request) {
	store := s.tasks.Config.Secrets
	if store == nil {
		writeError(w, http.StatusNotFound, errSecretsUnavailable)
		return
	}
	secret, ok := agentKeySecret(r.PathValue("framework"))
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("no agent framework named "+r.PathValue("framework")))
		return
	}
	var req setAgentKeyRequest
	if !readJSON(w, r, &req) {
		return
	}
	// Trimmed on the way in, not on the way out: a token pasted out of a
	// terminal or an email carries whatever whitespace came with it, and
	// the daemon trims what it reads back anyway (cmd/grain's
	// agentCredential). Doing it here means "set" and "resolves to
	// something" cannot disagree -- a value of nothing but whitespace
	// would otherwise store as present and fail every run.
	value := strings.TrimSpace(req.Value)
	if value == "" {
		writeError(w, http.StatusBadRequest, errors.New("value is required"))
		return
	}
	if err := store.Set(secret, secrets.AgentCredentialKey, []byte(value)); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// Connections authenticating with this key may list other models now.
	s.tasks.kickModelDiscovery()
	s.respondWithAgentKeys(w)
}

func (s *Server) handleSetAgentBackendCredential(w http.ResponseWriter, r *http.Request) {
	if !s.requireAgentBackendAdmin(w, r) {
		return
	}
	store := s.tasks.Config.Secrets
	if store == nil {
		writeError(w, http.StatusNotFound, errSecretsUnavailable)
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" || strings.ContainsAny(id, " /\\") {
		writeError(w, http.StatusBadRequest, errors.New("invalid backend id"))
		return
	}
	var req setAgentKeyRequest
	if !readJSON(w, r, &req) {
		return
	}
	value := strings.TrimSpace(req.Value)
	if value == "" {
		writeError(w, http.StatusBadRequest, errors.New("value is required"))
		return
	}
	if err := store.Set(secrets.AgentBackendCredentialSecret(id), secrets.AgentCredentialKey, []byte(value)); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// A new key is the plainest fix for a spent one: the connection's
	// usage-limit pauses go with the old key (connection_pause.go).
	s.tasks.liftConnectionPauses(id)
	// A connection saved before its key could not be listed; now it can.
	s.tasks.kickModelDiscovery()
	writeJSON(w, http.StatusOK, map[string]bool{"set": true})
}

func (s *Server) handleDeleteAgentBackendCredential(w http.ResponseWriter, r *http.Request) {
	if !s.requireAgentBackendAdmin(w, r) {
		return
	}
	store := s.tasks.Config.Secrets
	if store == nil {
		writeError(w, http.StatusNotFound, errSecretsUnavailable)
		return
	}
	if err := store.DeleteSecret(secrets.AgentBackendCredentialSecret(strings.TrimSpace(r.PathValue("id")))); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"set": false})
}

// handleBackendCodexAuth manages only the named backend's login. Shared and
// operator-owned logins remain untouched when replacing or clearing it.
func (s *Server) handleBackendCodexAuth(w http.ResponseWriter, r *http.Request) {
	if !s.requireAgentBackendAdmin(w, r) {
		return
	}
	cfg, err := s.tasks.Store.GetConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	id := r.PathValue("id")
	found := false
	if cfg != nil {
		for _, backend := range cfg.AgentBackends {
			if backend.ID == id && model.NormalizeAgentFrameworkName(backend.Framework) == model.AgentFrameworkCodex {
				found = true
				break
			}
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, errors.New("no configured Codex backend with this id"))
		return
	}
	shared := s.tasks.Config.CodexAuthFile
	path := codex.BackendAuthFile(shared, id)
	if r.Method != http.MethodGet && path == "" {
		writeError(w, http.StatusNotFound, errors.New("Codex subscription authentication is unavailable"))
		return
	}
	switch r.Method {
	case http.MethodPut:
		var req setAgentKeyRequest
		if !readJSON(w, r, &req) {
			return
		}
		if err := codex.WriteAuthFile(path, []byte(req.Value)); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	case http.MethodDelete:
		if err := codex.RemoveAuthFile(path); err != nil && !os.IsNotExist(err) {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	if _, err := os.Stat(shared); os.IsNotExist(err) || shared == "" {
		shared = s.tasks.Config.CodexAuthFallbackFile
	}
	writeJSON(w, http.StatusOK, backendCodexAuthResponse{
		Enabled:            path != "",
		Set:                path != "" && codex.ValidateAuthFile(path) == nil,
		SharedSet:          shared != "" && codex.ValidateAuthFile(shared) == nil,
		RefreshError:       refreshFailure(path),
		SharedRefreshError: refreshFailure(shared),
	})
}

// backendCodexAuthResponse reports a backend's login and the shared one
// by presence, never content. A refresh error is the permanent refusal
// grain's login refresher recorded (codex.ReadRefreshFailure): the file
// is still there, but only a new upload makes it work again.
type backendCodexAuthResponse struct {
	Enabled            bool   `json:"enabled"`
	Set                bool   `json:"set"`
	SharedSet          bool   `json:"sharedSet"`
	RefreshError       string `json:"refreshError,omitempty"`
	SharedRefreshError string `json:"sharedRefreshError,omitempty"`
}

func refreshFailure(path string) string {
	if failure := codex.ReadRefreshFailure(path); failure != nil {
		return failure.String()
	}
	return ""
}

// handleSetCodexAuth preserves the legacy deployment-wide upload API.
func (s *Server) handleSetCodexAuth(w http.ResponseWriter, r *http.Request) {
	path := s.tasks.Config.CodexAuthFile
	if path == "" {
		writeError(w, http.StatusNotFound, errors.New("Codex subscription authentication is unavailable"))
		return
	}
	var req setAgentKeyRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := codex.WriteAuthFile(path, []byte(req.Value)); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.respondWithAgentKeys(w)
}

// handleDeleteCodexAuth removes only Grain's UI-managed copy. A host path
// supplied with -codex-auth-file is a separate deployment fallback and is
// never modified by this endpoint.
func (s *Server) handleDeleteCodexAuth(w http.ResponseWriter, r *http.Request) {
	path := s.tasks.Config.CodexAuthFile
	if path == "" {
		writeError(w, http.StatusNotFound, errors.New("Codex subscription authentication is unavailable"))
		return
	}
	if err := codex.RemoveAuthFile(path); err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, errors.New("no Codex subscription login is stored"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.respondWithAgentKeys(w)
}

// handleDeleteAgentKey clears one framework's credential. Deleting the
// whole secret, not just AgentCredentialKey inside it, so a secret left
// holding no keys cannot linger and read back as "exists but does not
// resolve" -- the one state agentKeysSet reports as unset and
// secrets.Store.Resolve refuses, which is a confusing way for a cleared
// key to look.
func (s *Server) handleDeleteAgentKey(w http.ResponseWriter, r *http.Request) {
	store := s.tasks.Config.Secrets
	if store == nil {
		writeError(w, http.StatusNotFound, errSecretsUnavailable)
		return
	}
	secret, ok := agentKeySecret(r.PathValue("framework"))
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("no agent framework named "+r.PathValue("framework")))
		return
	}
	if err := store.DeleteSecret(secret); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	s.respondWithAgentKeys(w)
}

func (s *Server) respondWithAgentKeys(w http.ResponseWriter) {
	gemini, claude, openai, codexAuth := s.tasks.agentKeysSet()
	writeJSON(w, http.StatusOK, agentKeysResponse{
		Enabled:               s.tasks.Config.Secrets != nil,
		GeminiAPIKeySet:       gemini,
		ClaudeOAuthTokenSet:   claude,
		OpenAIAPIKeySet:       openai,
		CodexAuthJSONSet:      codexAuth,
		CodexAuthRefreshError: refreshFailure(s.tasks.Config.CodexAuthFile),
	})
}
