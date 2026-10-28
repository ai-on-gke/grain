package modelproxy

// Claude on Vertex AI: the one place this proxy translates rather than
// swaps a credential.
//
// A team whose Claude connection authenticates through Vertex AI holds no
// Anthropic key, only a Google service account, and Vertex does not
// serve the Messages API at Anthropic's paths. It serves the same request
// bodies, though, at a path naming the project, region and model, with
// the model moved out of the body and an anthropic_version Vertex
// recognises put in it -- which is exactly what Anthropic's own Vertex
// SDK clients do to a request before sending it. So a run's client keeps
// speaking the Claude API to this proxy, and the proxy does that
// rewriting on its way out, with a Google access token minted from the
// connection's own service account (Endpoint.SetVertex).
//
// Only the Messages API is translated: POST /v1/messages and
// /v1/messages/count_tokens. The model listing is answered here, from
// the models this deployment's claude framework offers on Vertex
// (VertexTarget.Models), because Vertex has no Anthropic-shaped listing
// to translate one into -- its own publisher-model API answers a
// different question in a different shape, and needs a role a service
// account granted to call models need not have.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// VertexAnthropicVersion is the anthropic_version Vertex AI requires in
// a Messages request body, set by this proxy where the caller's client
// -- written for Anthropic's API, where the version is a header -- did
// not set one.
const VertexAnthropicVersion = "vertex-2023-10-16"

// OutcomeAnswered is a request this proxy answered itself on the
// connection's behalf without forwarding it -- the model listing of a
// connection on Vertex AI. Like OutcomeForwarded it is the ordinary case,
// not a refusal.
const OutcomeAnswered = "answered"

// VertexTarget is where, and as whom, a request on a connection that
// authenticates through Vertex AI is forwarded: the Google Cloud project
// and region the connection's Claude models are called in, an OAuth
// access token minted from its service account, and the models a client
// listing them is told about.
type VertexTarget struct {
	Project string
	Region  string
	Token   string
	Models  []VertexModel
}

// VertexModel is one model the listing answers with.
type VertexModel struct {
	ID          string
	DisplayName string
}

// vertexKey carries the outbound Vertex AI URL from ServeHTTP to the
// rewrite and the response hook: a request carrying one is a translated
// one.
type vertexKey struct{}

// SetVertex publishes how a connection that authenticates through Vertex
// AI is forwarded. The resolver answers nil, nil for a connection that
// does not -- which then goes on to SetCredential's key as before -- a
// target for one that does, or a *CredentialRefusal for one that does
// and cannot be forwarded on. Called per request, like SetCredential, so
// a service account replaced in Team settings reaches the next call.
func (e *Endpoint) SetVertex(resolve func(ctx context.Context, connection Connection) (*VertexTarget, error)) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.vertex = resolve
}

// ResolveVertex is connection's Vertex AI target through the resolver
// SetVertex published: nil, nil for an endpoint with none wired, or a
// connection that authenticates some other way.
func (e *Endpoint) ResolveVertex(ctx context.Context, connection Connection) (*VertexTarget, error) {
	if e == nil || connection.ID == "" {
		return nil, nil
	}
	e.mu.RLock()
	resolve := e.vertex
	e.mu.RUnlock()
	if resolve == nil {
		return nil, nil
	}
	return resolve(ctx, connection.normalized())
}

// vertexOrigin is the Vertex AI API origin for region: the global
// endpoint has no region in its host, every other region does.
func vertexOrigin(region string) string {
	if region == "global" {
		return "https://aiplatform.googleapis.com"
	}
	return "https://" + region + "-aiplatform.googleapis.com"
}

// vertexName is what a project, region or model may be spelled as here.
// They are path segments of the outbound URL, and a model is the
// caller's to choose: one spelled "../.." or "x:predict" would otherwise
// address something on Vertex that is not a Claude model.
var vertexName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]*$`)

// serveVertex answers r on a connection that authenticates through
// Vertex AI: the model listing here, and a Messages call translated and
// forwarded with target's access token. Everything ServeHTTP checks
// before resolving a credential has already been checked.
func (p *Proxy) serveVertex(w http.ResponseWriter, r *http.Request, owner string, connection Connection, target *VertexTarget) {
	if p.Vendor.Name != Claude.Name {
		p.deny(w, r, owner, connection, OutcomeNoCredential, DenialUpstream,
			"connection "+connection.ID+" authenticates through Vertex AI, which this grain "+
				p.Vendor.Display+" proxy cannot forward to.")
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		p.record(p.entry(r, owner, connection, OutcomeAnswered))
		writeVertexModels(w, target.Models)
		return
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/models/"):
		p.record(p.entry(r, owner, connection, OutcomeAnswered))
		writeVertexModel(w, target.Models, strings.TrimPrefix(r.URL.Path, "/v1/models/"))
		return
	case r.Method == http.MethodPost && (r.URL.Path == "/v1/messages" || r.URL.Path == "/v1/messages/count_tokens"):
	default:
		// Only reachable under Unrestricted: nothing else of Anthropic's
		// API has a Vertex counterpart to translate it into.
		p.deny(w, r, owner, connection, OutcomeForbidden, DenialForbidden,
			"connection "+connection.ID+" authenticates through Vertex AI, and this grain "+
				"Claude proxy forwards only /v1/messages and /v1/messages/count_tokens to Vertex AI.")
		return
	}
	if !vertexName.MatchString(target.Project) || !vertexName.MatchString(target.Region) || strings.TrimSpace(target.Token) == "" {
		p.deny(w, r, owner, connection, OutcomeNoCredential, DenialUpstream,
			"connection "+connection.ID+" authenticates through Vertex AI, but this grain "+
				"proxy could not tell which Google Cloud project and region to call Claude in, or mint an access token for it.")
		return
	}

	limit := p.maxBodyBytes()
	if r.ContentLength > limit {
		p.deny(w, r, owner, connection, OutcomeTooLarge, DenialTooLarge, p.tooLarge(limit))
		return
	}
	var raw []byte
	if r.Body != nil && r.Body != http.NoBody {
		var err error
		raw, err = io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			p.deny(w, r, owner, connection, OutcomeTooLarge, DenialTooLarge, p.tooLarge(tooLarge.Limit))
			return
		}
		if err != nil {
			p.deny(w, r, owner, connection, OutcomeNoCredential, DenialUpstream,
				"This grain Claude proxy could not read the request body: "+err.Error())
			return
		}
	}
	body, outbound, problem := vertexRequest(raw, r.URL.Path, p.vertexOrigin(target.Region), target)
	if problem != "" {
		p.record(p.entry(r, owner, connection, OutcomeInvalid))
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", problem)
		return
	}

	reverse, err := p.proxy()
	if err != nil {
		p.deny(w, r, owner, connection, OutcomeNoCredential, DenialUpstream,
			"This grain model proxy is misconfigured and has no upstream to forward to: "+err.Error())
		return
	}
	release, ok := p.awaitSlot(r.Context(), owner)
	if !ok {
		p.tooMany(w, r, owner, connection, "Claude")
		return
	}
	defer release()
	p.record(p.entry(r, owner, connection, OutcomeForwarded))
	ctx := context.WithValue(r.Context(), credentialKey{}, strings.TrimSpace(target.Token))
	ctx = context.WithValue(ctx, ownerKey{}, owner)
	ctx = context.WithValue(ctx, connectionKey{}, connection)
	ctx = context.WithValue(ctx, vertexKey{}, outbound)
	out := r.WithContext(ctx)
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.ContentLength = int64(len(body))
	out.Header = r.Header.Clone()
	out.Header.Del("Content-Length")
	reverse.ServeHTTP(w, out)
}

// OutcomeInvalid is a request on a Vertex AI connection whose body could
// not be translated -- not JSON, or naming no usable model. Answered with
// a 400, as Anthropic would answer it.
const OutcomeInvalid = "invalid-request"

// vertexOrigin is VertexUpstream where a test set one, and the region's
// own Vertex AI origin otherwise.
func (p *Proxy) vertexOrigin(region string) string {
	if p.VertexUpstream != "" {
		return strings.TrimSuffix(p.VertexUpstream, "/")
	}
	return vertexOrigin(region)
}

// vertexRequest translates a Claude API request body for path into the
// Vertex AI one, the way Anthropic's Vertex SDK clients do: a Messages
// call moves its model into the URL, choosing streamRawPredict for a
// streamed one, and a token count goes to Vertex's count-tokens model
// with its model left in the body, since that is where Vertex reads it.
// Both get anthropic_version where the caller set none. problem is the
// sentence a request that cannot be translated is refused with.
func vertexRequest(raw []byte, path, origin string, target *VertexTarget) (body []byte, outbound *url.URL, problem string) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, nil, "This grain Claude proxy forwards to Vertex AI, which needs the request body to be a JSON object: it could not be read as one."
	}
	var model string
	if err := json.Unmarshal(fields["model"], &model); err != nil || !vertexName.MatchString(model) {
		return nil, nil, "model: a Claude model name is required, such as claude-sonnet-4-6 (this grain proxy forwards to Vertex AI, which takes it as part of the request path)."
	}
	if _, ok := fields["anthropic_version"]; !ok {
		fields["anthropic_version"] = json.RawMessage(`"` + VertexAnthropicVersion + `"`)
	}
	call := "count-tokens:rawPredict"
	if path == "/v1/messages" {
		delete(fields, "model")
		var stream bool
		_ = json.Unmarshal(fields["stream"], &stream)
		call = model + ":rawPredict"
		if stream {
			call = model + ":streamRawPredict"
		}
	}
	body, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, "This grain Claude proxy could not re-encode the request body for Vertex AI: " + err.Error()
	}
	outbound, err = url.Parse(origin + "/v1/projects/" + target.Project + "/locations/" + target.Region +
		"/publishers/anthropic/models/" + call)
	if err != nil {
		return nil, nil, "This grain Claude proxy could not build a Vertex AI address for this request: " + err.Error()
	}
	return body, outbound, ""
}

// authorizeVertex points out at the Vertex AI URL ServeHTTP built and
// presents the connection's access token, removing whatever credential
// the caller sent. The caller's anthropic-beta header is left as it is:
// Vertex reads it the way Anthropic does.
func authorizeVertex(out *http.Request, outbound *url.URL, token string) {
	out.URL = outbound
	out.Host = outbound.Host
	out.Header.Del("x-api-key")
	out.Header.Set("Authorization", "Bearer "+token)
}

// vertexUsageLimit reads a usage limit in a Vertex AI answer: Google's
// own RESOURCE_EXHAUSTED quota error -- which is what Vertex answers when
// the project's Claude quota is spent, in the same envelope the Gemini
// API uses, sometimes wrapped in a one-element array -- or, failing that,
// Anthropic's own reading of a 429 passed through from the model.
func (p *Proxy) vertexUsageLimit(resp *http.Response) (*agent.UsageLimitError, bool) {
	if limit, ok := googleResourceExhausted(resp); ok {
		return limit, true
	}
	if p.Vendor.UsageLimit == nil {
		return nil, false
	}
	return p.Vendor.UsageLimit(resp)
}

// writeVertexModels answers GET /v1/models in the shape of Anthropic's
// own listing, one page holding every model.
func writeVertexModels(w http.ResponseWriter, models []VertexModel) {
	data := make([]map[string]string, 0, len(models))
	for _, m := range models {
		data = append(data, anthropicModel(m))
	}
	listing := map[string]any{"data": data, "has_more": false, "first_id": nil, "last_id": nil}
	if len(models) > 0 {
		listing["first_id"], listing["last_id"] = models[0].ID, models[len(models)-1].ID
	}
	writeJSON(w, http.StatusOK, listing)
}

// writeVertexModel answers GET /v1/models/<id>: the model, or the 404
// Anthropic answers for one it does not serve.
func writeVertexModel(w http.ResponseWriter, models []VertexModel, id string) {
	for _, m := range models {
		if m.ID == id {
			writeJSON(w, http.StatusOK, anthropicModel(m))
			return
		}
	}
	writeAnthropicError(w, http.StatusNotFound, "not_found_error", "model: "+id+" is not one of the Claude models this grain proxy offers on Vertex AI.")
}

// anthropicModel is m as Anthropic's model listing writes one. Vertex
// says nothing about when a model was released, and created_at is
// required of the shape, so it is the epoch.
func anthropicModel(m VertexModel) map[string]string {
	name := m.DisplayName
	if name == "" {
		name = m.ID
	}
	return map[string]string{"type": "model", "id": m.ID, "display_name": name, "created_at": "1970-01-01T00:00:00Z"}
}

func writeAnthropicError(w http.ResponseWriter, status int, errType, message string) {
	writeJSON(w, status, map[string]any{
		"type":  "error",
		"error": map[string]string{"type": errType, "message": message},
	})
}
