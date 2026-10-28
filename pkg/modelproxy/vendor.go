package modelproxy

// What differs between one vendor's proxy and another's, gathered into
// one value so that everything else here can be written once.
//
// The three parts that are genuinely per vendor are the ones this type
// carries: where a forwarded request goes, how a credential is
// recognised on the way in and presented on the way out, and the JSON a
// refusal is written in. Everything else -- the token registry, the
// endpoint, the handler, the capability that mints into it -- is the
// same code for all three, which is the whole point of the split.
//
// A Vendor is a value, not an interface, because there is nothing to
// implement: every field is either a constant about an API or a small
// function over an *http.Request, and a deployment never defines one --
// grain ships Claude, OpenAI and Gemini below and no way to add a
// fourth from configuration. A vendor grain has not heard of would need
// its own error shape and its own credential rule written down anyway,
// which is a change to this package rather than a setting.

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	pathpkg "path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/agent/credcheck"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// Denial is why this proxy is refusing a request, in the only kinds a
// vendor's error shape has to tell apart: the caller's own credential
// was no good (401, and the remedy is in the sandbox), the caller asked
// for an endpoint this proxy does not forward to (403), sent too much
// (413) or too many at once (429), or something on this side of the
// proxy was (502, and the remedy is an operator's).
//
// Deliberately not the vendor's own error taxonomy. Each vendor spells
// these two differently -- "authentication_error" against
// "invalid_api_key" against "UNAUTHENTICATED" -- and the spelling is
// exactly what Vendor.WriteError exists to do, so the rest of this
// package names the reason and lets the vendor write it.
type Denial int

const (
	// DenialAuth is a caller this proxy will not answer for: no token,
	// or one it never minted.
	DenialAuth Denial = iota
	// DenialUpstream is this side of the proxy failing the caller: no
	// credential configured to forward with, a misconfigured upstream,
	// or a vendor that could not be reached.
	DenialUpstream
	// DenialTooLarge is a request body over the proxy's size limit
	// (Proxy.MaxBodyBytes). The caller's to fix, and a 413 says so in
	// the status every client already knows.
	DenialTooLarge
	// DenialForbidden is an authenticated caller asking for a path or
	// query parameter outside the vendor's model-inference endpoints
	// (Vendor.Routes, Vendor.Query), or with a method that path is not
	// forwarded for.
	DenialForbidden
	// DenialTooManyRequests is an authenticated caller already holding
	// as many requests open through this proxy as one run may
	// (Proxy.MaxConcurrent), none of which finished within
	// Proxy.SlotWait. A 429, which every one of these vendors' clients
	// already reads as "back off and retry".
	DenialTooManyRequests
)

// Status is the HTTP status a denial is answered with.
func (d Denial) Status() int {
	switch d {
	case DenialAuth:
		return http.StatusUnauthorized
	case DenialTooLarge:
		return http.StatusRequestEntityTooLarge
	case DenialForbidden:
		return http.StatusForbidden
	case DenialTooManyRequests:
		return http.StatusTooManyRequests
	}
	return http.StatusBadGateway
}

// Vendor is one model API this deployment can proxy to.
type Vendor struct {
	// Name is the vendor's id here -- "claude", "openai", "gemini". It
	// is what the capability granting a run access to this proxy is
	// named after (pkg/capability/modelproxy) and what the files placed
	// in a sandbox are named after, so it is short, stable and
	// lowercase.
	Name string
	// Display is what a sentence written for a human calls this API
	// ("Claude", "OpenAI", "Gemini" -- "the %s API" reads correctly for
	// all three), and Company is who is at the far end of it
	// ("Anthropic", "OpenAI", "Google"). Two fields because they are two
	// different words for two of the three vendors: the credential in
	// "no Anthropic credential" is the company's, and the API in "call
	// the Claude API" is not.
	Display string
	Company string
	// Upstream is the API every forwarded request goes to -- the same
	// host the CLI a dispatch forks talks to, and the same constant
	// pkg/agent/credcheck tests this deployment's own credential
	// against.
	Upstream string
	// Secret is the legacy deployment-wide secret for this vendor
	// (secrets.ClaudeOAuthTokenSecret and friends). Nothing here forwards
	// with it any longer: a request is authenticated with its run's team
	// connection (Endpoint.SetCredential), and this secret reaches that
	// only as the default team's connection it was migrated into
	// (ui.Client.MigrateAgentBackendKeys). It is kept to name the
	// vendor's legacy setting, not to be read.
	Secret string
	// Framework is the agent framework whose credential this vendor takes
	// (model.AgentFrameworkClaude and friends): a team's connections of
	// this framework are the ones a request to this vendor may be
	// forwarded with, and the one a usage limit met here is reported
	// against.
	Framework string
	// BaseURLSuffix is appended to the proxy's own address to make the
	// base URL a client of this vendor expects. Empty for Anthropic and
	// Google, whose clients are given a bare origin and append their own
	// versioned path; "/v1" for OpenAI, whose OPENAI_BASE_URL includes
	// it.
	BaseURLSuffix string
	// BaseURLEnv and TokenEnv are the two environment variables a client
	// of this vendor reads, named in the prompt the capability writes.
	BaseURLEnv string
	TokenEnv   string
	// Endpoints and Clients are the two halves of "and it really is the
	// vendor's own API behind this": the paths a run may call (see
	// Routes, which is what is actually enforced), and a few clients that
	// will work unchanged. Prompt text, phrased to be
	// dropped into a sentence.
	//
	// A client belongs in Clients only if it really does read BaseURLEnv
	// and TokenEnv and nothing else -- one that needs more goes in Setup.
	Endpoints string
	Clients   string
	// Setup is the prompt's recipe for a client that does not work
	// unchanged from the two variables, given the paths the address and
	// token are placed at: a paragraph, ending in a newline, or "" for a
	// vendor whose clients all do. It exists for the codex CLI, which
	// ignores OPENAI_BASE_URL for its built-in provider and sends no
	// credential until it has been logged in.
	Setup func(urlPath, tokenPath string) string

	// Routes is every method and request path this proxy forwards to
	// the vendor; any other is refused before this deployment's
	// credential is attached. These are the model-inference endpoints
	// and nothing else: the vendor's files, fine-tuning, batch and
	// organization-management APIs are reachable with the same
	// credential, and a sandbox calling a model for itself has no
	// business with any of them (bwsalmon/grain#2403). Nor with every
	// method on the paths it does call: DELETE /v1/models/<id> deletes
	// one of this deployment's fine-tuned models, and GET
	// /v1/chat/completions lists every completion it has stored, so a
	// path is allowed for the methods its inference use needs and no
	// others.
	Routes []Route
	// Query is every query parameter a forwarded request may carry. A
	// request carrying any other is refused rather than forwarded with
	// it quietly dropped, so a client finds out which one.
	Query []string

	// Token reads the proxy's own token off an inbound request --
	// whichever of the several ways this vendor's clients present a
	// credential the caller used. false where the caller presented none.
	Token func(r *http.Request) (string, bool)
	// Authorize replaces the caller's credentials on the outbound
	// request with this deployment's own. It is handed a request whose
	// caller credentials have not been removed yet, and is responsible
	// for removing every one of them: a token that is worth nothing
	// upstream must never be forwarded as though it were a credential,
	// and a key a run set for itself must never reach the vendor in
	// place of the one this deployment chose.
	Authorize func(out *http.Request, credential string)
	// UsageLimit reports whether a response the vendor answered is a
	// usage limit on the credential it was sent with -- the budget behind
	// the key is spent for a window, as opposed to one request being
	// throttled -- and when the vendor said it lifts. Such a response
	// pauses that connection (Endpoint.SetPause). It may read the body,
	// and must leave it readable for the caller. nil reports nothing.
	UsageLimit func(resp *http.Response) (*agent.UsageLimitError, bool)
	// WriteError writes a refusal in this vendor's own error shape, so a
	// client library refused by this proxy raises the error it expects
	// rather than choking on JSON from somebody else's API.
	WriteError func(w http.ResponseWriter, kind Denial, message string)
}

// Vendors is every vendor grain ships a proxy for, in the order a
// listing shows them.
func Vendors() []Vendor { return []Vendor{Claude, OpenAI, Gemini} }

// Claude is api.anthropic.com: the Claude API, authenticated with the
// Claude Code OAuth token (or plain API key) this deployment's own
// claude framework runs as.
var Claude = Vendor{
	Name:       "claude",
	Display:    "Claude",
	Company:    "Anthropic",
	Upstream:   credcheck.DefaultAnthropicBaseURL,
	Secret:     secrets.ClaudeOAuthTokenSecret,
	Framework:  model.AgentFrameworkClaude,
	BaseURLEnv: "ANTHROPIC_BASE_URL",
	TokenEnv:   "ANTHROPIC_AUTH_TOKEN",
	Endpoints:  "/v1/messages, /v1/messages/count_tokens and /v1/models",
	Clients:    "the Anthropic SDKs, the claude CLI, plain curl",
	// Messages and token counting, and the model listing a client reads
	// to choose one. Not /v1/messages/batches, /v1/files or the
	// organization APIs.
	Routes: []Route{
		{Method: http.MethodPost, Path: "/v1/messages"},
		{Method: http.MethodPost, Path: "/v1/messages/count_tokens"},
		{Method: http.MethodGet, Path: "/v1/models"},
		{Method: http.MethodGet, Path: "/v1/models/"},
	},
	// beta=true is how the claude CLI marks a request using beta
	// features; the rest page the model listing.
	Query: []string{"beta", "limit", "before_id", "after_id"},

	// Both headers are accepted because both are how an Anthropic client
	// authenticates, and which one a given client uses is not this
	// proxy's to choose: ANTHROPIC_AUTH_TOKEN becomes an Authorization
	// bearer, ANTHROPIC_API_KEY becomes an x-api-key, and the same run
	// may use one library that reads the first and another that reads
	// the second.
	Token: func(r *http.Request) (string, bool) {
		if key, ok := headerToken(r, "x-api-key"); ok {
			return key, true
		}
		return bearerToken(r)
	},

	// The beta header is merged rather than replaced. A Claude Code
	// OAuth token is presented with one of its own
	// (credcheck.AuthorizeAnthropic), and a client asking for an
	// unrelated beta feature has every right to its own -- dropping the
	// caller's would silently turn its request into a different one.
	Authorize: func(out *http.Request, credential string) {
		callerBetas := out.Header.Values("anthropic-beta")
		out.Header.Del("Authorization")
		out.Header.Del("x-api-key")
		out.Header.Del("anthropic-beta")
		credcheck.AuthorizeAnthropic(out, credential)
		for _, beta := range mergeBetas(out.Header.Values("anthropic-beta"), callerBetas) {
			out.Header.Add("anthropic-beta", beta)
		}
	},

	// A subscription's (OAuth token's) window is spent when Anthropic
	// answers 429 with its unified rate limit rejected, and says in
	// anthropic-ratelimit-unified-reset (a Unix epoch) when it reopens. A
	// plain 429 on an API key is per-minute throttling the client retries
	// through, and pauses nothing -- unless its message says it is a
	// usage limit, which is how the claude CLI recognises one too.
	UsageLimit: func(resp *http.Response) (*agent.UsageLimitError, bool) {
		if resp.StatusCode != http.StatusTooManyRequests {
			return nil, false
		}
		message := errorMessage(resp)
		rejected := strings.EqualFold(strings.TrimSpace(resp.Header.Get("anthropic-ratelimit-unified-status")), "rejected")
		if !rejected && !strings.Contains(strings.ToLower(message), "usage limit") {
			return nil, false
		}
		limit := &agent.UsageLimitError{Message: agent.TrimLimitMessage(message)}
		if epoch, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("anthropic-ratelimit-unified-reset")), 10, 64); err == nil && epoch > 0 {
			limit.ResetAt = time.Unix(epoch, 0).UTC()
		}
		limit.RetryAfter = retryAfterHeader(resp)
		return limit, true
	},

	WriteError: func(w http.ResponseWriter, kind Denial, message string) {
		errType := "authentication_error"
		switch kind {
		case DenialUpstream:
			errType = "api_error"
		case DenialTooLarge:
			errType = "request_too_large"
		case DenialForbidden:
			errType = "permission_error"
		case DenialTooManyRequests:
			errType = "rate_limit_error"
		}
		writeJSON(w, kind.Status(), map[string]any{
			"type": "error",
			"error": map[string]string{
				"type":    errType,
				"message": message,
			},
		})
	},
}

// OpenAI is api.openai.com: the API the codex CLI and the OpenAI SDKs
// authenticate against with OPENAI_API_KEY.
var OpenAI = Vendor{
	Name:      "openai",
	Display:   "OpenAI",
	Company:   "OpenAI",
	Upstream:  credcheck.DefaultOpenAIBaseURL,
	Secret:    secrets.OpenAIAPIKeySecret,
	Framework: model.AgentFrameworkCodex,
	// OPENAI_BASE_URL is the versioned base, not the origin: the SDKs
	// default it to https://api.openai.com/v1 and append /chat/
	// completions to whatever they are given. So the address placed in a
	// sandbox carries the /v1 too, and the proxy behind it forwards that
	// path upstream unchanged like any other.
	BaseURLSuffix: "/v1",
	BaseURLEnv:    "OPENAI_BASE_URL",
	TokenEnv:      "OPENAI_API_KEY",
	Endpoints:     "/responses, /chat/completions, /completions, /embeddings and /models",
	Clients:       "the OpenAI SDKs, plain curl",
	// Not the codex CLI, whose built-in openai provider reads neither
	// variable (codex-cli 0.153.0, the Dockerfile pin): it connects to
	// api.openai.com whatever OPENAI_BASE_URL says, and sends no
	// Authorization even with OPENAI_API_KEY set until `codex login` has
	// stored a key. What does work is openai_base_url in config.toml plus
	// `codex login --with-api-key`, in a CODEX_HOME of its own so that a
	// run whose own agent is codex keeps its configuration. Measured
	// against a stub vendor behind this proxy, WebSocket and HTTPS both.
	Setup: codexSetup,
	// The Responses API (including retrieving and cancelling a response
	// by id, compacting, and counting input tokens), chat and text
	// completions, embeddings and the model listing. Not /v1/files,
	// /v1/fine_tuning, /v1/batches or /v1/organization -- and not DELETE
	// on a response or a model (deleting a fine-tuned model is DELETE
	// /v1/models/<id>), nor GET on /v1/chat/completions, which lists
	// every stored completion on this deployment's account rather than
	// creating one, nor a stored response's input_items.
	Routes: []Route{
		{Method: http.MethodPost, Path: "/v1/responses"},
		// The same endpoint over a WebSocket, which is the transport the
		// codex CLI tries first (0.153.0 opens ws://<base>/responses and
		// only falls back to HTTPS after five refused handshakes). Only
		// the handshake: a plain GET on this path is not inference.
		{Method: http.MethodGet, Path: "/v1/responses", Upgrade: "websocket"},
		// Retrieving a response by id, and cancelling one, are how a
		// `background: true` response is polled for and stopped, and how a
		// dropped stream is resumed (?stream=true&starting_after=). They
		// are not scoped to the run that created the response: every run
		// shares this deployment's account, so a run that learned another's
		// response id could read or cancel it. Ids are random and never
		// handed out by this proxy, which is what keeps that a narrow
		// exposure -- and why GET .../input_items, which only replays a
		// stored conversation, is not forwarded at all.
		{Method: http.MethodGet, Path: "/v1/responses/*"},
		{Method: http.MethodPost, Path: "/v1/responses/*/cancel"},
		{Method: http.MethodPost, Path: "/v1/responses/compact"},
		{Method: http.MethodPost, Path: "/v1/responses/input_tokens"},
		{Method: http.MethodPost, Path: "/v1/chat/completions"},
		{Method: http.MethodPost, Path: "/v1/completions"},
		{Method: http.MethodPost, Path: "/v1/embeddings"},
		{Method: http.MethodGet, Path: "/v1/models"},
		{Method: http.MethodGet, Path: "/v1/models/"},
	},
	// Streaming a stored response back, and expanding its fields.
	Query: []string{"stream", "starting_after", "include", "include[]", "include_obfuscation"},

	// One header, because this API has one way in: a bearer. A client
	// that sent an api-key header instead is talking to Azure's OpenAI
	// service, which is a different API this proxy does not front.
	Token: bearerToken,

	Authorize: func(out *http.Request, credential string) {
		out.Header.Del("Authorization")
		// Which project and organization a request is billed to are the
		// deployment credential's to decide, not a sandbox's: a run that
		// set these for itself would be spending this deployment's key
		// against somebody else's project.
		out.Header.Del("OpenAI-Organization")
		out.Header.Del("OpenAI-Project")
		credcheck.AuthorizeOpenAI(out, credential)
	},

	// insufficient_quota is OpenAI's "this key's budget is spent" -- a
	// 429 like its per-minute rate_limit_exceeded, which a client simply
	// retries and which pauses nothing.
	UsageLimit: func(resp *http.Response) (*agent.UsageLimitError, bool) {
		if resp.StatusCode != http.StatusTooManyRequests {
			return nil, false
		}
		var body struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    any    `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(peekBody(resp), &body)
		code, _ := body.Error.Code.(string)
		if body.Error.Type != "insufficient_quota" && code != "insufficient_quota" {
			return nil, false
		}
		return &agent.UsageLimitError{Message: agent.TrimLimitMessage(body.Error.Message), RetryAfter: retryAfterHeader(resp)}, true
	},

	// OpenAI's own error envelope, down to the two null fields its
	// clients read: `param` and `code` are part of the shape, and a
	// library that unmarshals them strictly should find them where it
	// expects.
	WriteError: func(w http.ResponseWriter, kind Denial, message string) {
		body := map[string]any{"message": message, "param": nil, "type": "api_error", "code": nil}
		switch kind {
		case DenialAuth:
			body["type"] = "invalid_request_error"
			body["code"] = "invalid_api_key"
		case DenialTooLarge, DenialForbidden:
			body["type"] = "invalid_request_error"
		case DenialTooManyRequests:
			body["type"] = "requests"
			body["code"] = "rate_limit_exceeded"
		}
		writeJSON(w, kind.Status(), map[string]any{"error": body})
	},
}

// CodexHome is the CODEX_HOME OpenAI's Setup has a run give the codex
// CLI it calls through the proxy -- not ~/.codex, which is where a run
// whose own agent is codex keeps the configuration that agent runs on.
const CodexHome = "/home/debian/.codex-openai-proxy"

// codexSetup is OpenAI's Setup: the codex CLI's own way of being pointed
// at a base URL and given a key, since it reads neither OpenAI variable.
// codex reads the key from stdin, so the token goes from its file to
// codex's auth.json without passing through a command line.
func codexSetup(urlPath, tokenPath string) string {
	return "The codex CLI is the exception: its built-in OpenAI provider ignores OPENAI_BASE_URL and sends " +
		"no credential until it has been logged in, so give it a CODEX_HOME of its own, pointed at the " +
		"proxy, and log that in with the token:\n\n" +
		"    export CODEX_HOME=" + CodexHome + "\n" +
		"    mkdir -p \"$CODEX_HOME\"\n" +
		`    printf 'openai_base_url = "%s"\n' "$(cat ` + urlPath + `)" > "$CODEX_HOME/config.toml"` + "\n" +
		"    codex login --with-api-key < " + tokenPath + "\n\n" +
		"after which `codex exec` with that CODEX_HOME goes through the proxy, WebSocket and all.\n"
}

// Gemini is generativelanguage.googleapis.com: the Generative Language
// API agy authenticates against with a Gemini API key.
//
// The one vendor here whose credential is not a header. This API takes
// its key as a `key=` query parameter (and, equivalently, as an
// x-goog-api-key header), which is why Vendor.Authorize is handed the
// whole outbound request rather than its headers -- and why no Entry
// this package records, and no message it writes, ever carries a query
// string.
var Gemini = Vendor{
	Name:       "gemini",
	Display:    "Gemini",
	Company:    "Google",
	Upstream:   credcheck.DefaultGeminiBaseURL,
	Secret:     secrets.GeminiAPIKeySecret,
	Framework:  model.AgentFrameworkAntigravity,
	BaseURLEnv: "GOOGLE_GEMINI_BASE_URL",
	TokenEnv:   "GEMINI_API_KEY",
	Endpoints:  "/v1beta/models/<model>:generateContent, :streamGenerateContent, :countTokens, :embedContent, :batchEmbedContents and the model listing",
	Clients:    "google-genai, the Gemini SDKs, plain curl with ?key=",
	// The synchronous methods on a model -- generateContent,
	// streamGenerateContent, countTokens, embedContent and
	// batchEmbedContents (which, despite its name, answers in the same
	// response) -- and the model listing, under each API version the SDKs
	// use. Not files, cachedContents, tunedModels or corpora, and not the
	// methods that start work outliving the request: batchGenerateContent
	// and asyncBatchEmbedContent create a Batch API job on this
	// deployment's account that runs on after the run's token is revoked,
	// predictLongRunning starts a long-running (video) generation, and
	// models/<model>/operations lists every such operation on the account.
	// So a model's methods are named one by one rather than admitted as a
	// prefix. Reading a model is a GET and calling one is a POST; nothing
	// an inference client does here is a PATCH or a DELETE.
	Routes: geminiRoutes("/v1beta", "/v1", "/v1alpha"),
	// key is the caller's own token (stripped before forwarding); alt=sse
	// is how a stream is asked for; the rest page the model listing.
	Query: []string{"key", "alt", "pageSize", "pageToken"},

	// All three ways a caller might have presented it, since all three
	// are ways this API takes a key: the query parameter curl and the
	// older SDKs use, the header google-genai sends, and a bearer, which
	// is what a client configured for a Vertex-style endpoint sends.
	Token: func(r *http.Request) (string, bool) {
		if key := strings.TrimSpace(r.URL.Query().Get("key")); key != "" {
			return key, true
		}
		if key, ok := headerToken(r, "x-goog-api-key"); ok {
			return key, true
		}
		return bearerToken(r)
	},

	// Every one of those three is removed before this deployment's own
	// key is put in -- the query parameter included, which is the reason
	// this hook rewrites the URL.
	Authorize: func(out *http.Request, credential string) {
		out.Header.Del("Authorization")
		out.Header.Del("x-goog-api-key")
		stripQueryKey(out.URL)
		credcheck.AuthorizeGemini(out, credential)
	},

	// RESOURCE_EXHAUSTED is a quota on the key, with the delay it lifts
	// after in a RetryInfo detail ("retryDelay": "56s") -- the same reading
	// agent/antigravity makes of agy's own report of one.
	UsageLimit: googleResourceExhausted,

	// Google's API error envelope: an HTTP status echoed as `code`, and
	// the canonical status name its clients switch on.
	WriteError: func(w http.ResponseWriter, kind Denial, message string) {
		status := "UNAUTHENTICATED"
		switch kind {
		case DenialUpstream:
			status = "UNAVAILABLE"
		case DenialTooLarge:
			status = "INVALID_ARGUMENT"
		case DenialForbidden:
			status = "PERMISSION_DENIED"
		case DenialTooManyRequests:
			status = "RESOURCE_EXHAUSTED"
		}
		writeJSON(w, kind.Status(), map[string]any{
			"error": map[string]any{
				"code":    kind.Status(),
				"message": message,
				"status":  status,
			},
		})
	},
}

// googleResourceExhausted reads Google's RESOURCE_EXHAUSTED quota error
// -- {"error":{"status":"RESOURCE_EXHAUSTED",...}}, which Vertex AI
// sometimes answers wrapped in a one-element array -- with the delay it
// lifts after from a RetryInfo detail ("retryDelay": "56s") where there
// is one: the same reading agent/antigravity makes of agy's own report
// of one.
func googleResourceExhausted(resp *http.Response) (*agent.UsageLimitError, bool) {
	if resp.StatusCode != http.StatusTooManyRequests {
		return nil, false
	}
	type envelope struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
			Details []struct {
				RetryDelay string `json:"retryDelay"`
			} `json:"details"`
		} `json:"error"`
	}
	var body envelope
	raw := peekBody(resp)
	if err := json.Unmarshal(raw, &body); err != nil {
		var wrapped []envelope
		if json.Unmarshal(raw, &wrapped) == nil && len(wrapped) > 0 {
			body = wrapped[0]
		}
	}
	if body.Error.Status != "RESOURCE_EXHAUSTED" {
		return nil, false
	}
	limit := &agent.UsageLimitError{Message: agent.TrimLimitMessage(body.Error.Message), RetryAfter: retryAfterHeader(resp)}
	for _, detail := range body.Error.Details {
		if d, err := time.ParseDuration(strings.TrimSpace(detail.RetryDelay)); err == nil && d > 0 {
			limit.RetryAfter = d
			break
		}
	}
	return limit, true
}

// geminiActions are the methods on a model Gemini's routes forward: the
// synchronous inference ones, and none that leaves a job behind.
var geminiActions = []string{"generateContent", "streamGenerateContent", "countTokens", "embedContent", "batchEmbedContents"}

// geminiRoutes is the model listing, one model, and a call to one of
// geminiActions on it, under each of the given API versions.
func geminiRoutes(versions ...string) []Route {
	var routes []Route
	for _, version := range versions {
		routes = append(routes,
			Route{Method: http.MethodGet, Path: version + "/models"},
			Route{Method: http.MethodGet, Path: version + "/models/*"},
			Route{Method: http.MethodPost, Path: version + "/models/*", Actions: geminiActions},
		)
	}
	return routes
}

// Route is one method and path this proxy forwards. A Path ending in "/"
// admits everything beneath it. A Path with "*" segments admits paths
// with exactly as many segments, each "*" standing for one segment that
// is not empty and carries no ":" -- a model name or a response id, and
// never a `name:method` call on one. Any other Path admits exactly
// itself.
type Route struct {
	Method string
	Path   string
	// Actions, on a Path whose last segment is "*", turns that segment
	// into a call: it must be `<name>:<action>` for one of these actions.
	// It is how a Gemini route names the methods on a model it forwards
	// (generateContent, but not batchGenerateContent or
	// predictLongRunning), since a model's methods all share its path.
	Actions []string
	// Upgrade, when set, admits only a request asking to switch to that
	// protocol (its Upgrade header, compared case-insensitively) -- a
	// WebSocket handshake on a path whose plain GET is something else.
	Upgrade string
}

// admits reports whether this route forwards a request with method and
// the given Upgrade header, on a path it already matches.
func (r Route) admits(method, upgrade string) bool {
	if r.Method != method {
		return false
	}
	return r.Upgrade == "" || strings.EqualFold(strings.TrimSpace(upgrade), r.Upgrade)
}

func (r Route) matches(p string) bool {
	if prefix, ok := strings.CutSuffix(r.Path, "/"); ok {
		return strings.HasPrefix(p, prefix+"/") && len(p) > len(prefix)+1
	}
	if !strings.Contains(r.Path, "*") {
		return p == r.Path
	}
	want, got := strings.Split(r.Path, "/"), strings.Split(p, "/")
	if len(want) != len(got) {
		return false
	}
	for i, segment := range want {
		if segment != "*" {
			if got[i] != segment {
				return false
			}
			continue
		}
		name := got[i]
		if i == len(want)-1 && len(r.Actions) > 0 {
			var action string
			var ok bool
			if name, action, ok = strings.Cut(name, ":"); !ok || !slices.Contains(r.Actions, action) {
				return false
			}
		}
		if name == "" || strings.Contains(name, ":") {
			return false
		}
	}
	return true
}

// permits reports whether this proxy forwards r, and if not, a sentence
// saying why. The path must already be in its
// canonical form -- no "..", no "//", no "." segment, no trailing slash,
// and no percent-encoded slash, backslash or dot that the vendor's front
// end might decode into one -- so that what is checked here is the path
// the vendor serves, not one it normalises into something else.
func (v Vendor) permits(r *http.Request) (string, bool) {
	method, u := r.Method, r.URL
	p := u.Path
	if !strings.HasPrefix(p, "/") || pathpkg.Clean(p) != p || strings.ContainsAny(p, "\\\x00") {
		return "the request path is not in canonical form", false
	}
	escaped := strings.ToLower(u.EscapedPath())
	for _, encoded := range []string{"%2f", "%5c", "%2e", "%00"} {
		if strings.Contains(escaped, encoded) {
			return "the request path carries an encoded \"/\", \"\\\", \".\" or NUL", false
		}
	}
	if methods, ok := v.permitsRoute(method, r.Header.Get("Upgrade"), p); !ok {
		if len(methods) == 0 {
			return p + " is not a model endpoint this proxy forwards to", false
		}
		return method + " " + p + " is not forwarded; this proxy forwards only " +
			strings.Join(methods, " and ") + " there", false
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "the query string does not parse", false
	}
	for name := range query {
		if !slices.Contains(v.Query, name) {
			return "the query parameter \"" + name + "\" is not one this proxy forwards", false
		}
	}
	return "", true
}

// permitsRoute reports whether a route admits method (with the request's
// Upgrade header) on p, and if not, which methods the routes matching p
// do admit -- none, for a path this proxy does not forward at all.
func (v Vendor) permitsRoute(method, upgrade, p string) ([]string, bool) {
	var methods []string
	for _, route := range v.Routes {
		if !route.matches(p) {
			continue
		}
		if route.admits(method, upgrade) {
			return nil, true
		}
		allowed := route.Method
		if route.Upgrade != "" {
			allowed += " (" + route.Upgrade + " upgrade only)"
		}
		if !slices.Contains(methods, allowed) {
			methods = append(methods, allowed)
		}
	}
	return methods, false
}

// bearerToken reads an Authorization bearer.
func bearerToken(r *http.Request) (string, bool) {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if token, ok := strings.CutPrefix(auth, "Bearer "); ok {
		if token = strings.TrimSpace(token); token != "" {
			return token, true
		}
	}
	return "", false
}

// headerToken reads a whole header as a credential.
func headerToken(r *http.Request, name string) (string, bool) {
	if value := strings.TrimSpace(r.Header.Get(name)); value != "" {
		return value, true
	}
	return "", false
}

// stripQueryKey removes the `key=` parameter from a URL, leaving the
// rest of the query exactly as the caller wrote it -- alt=sse, and
// whatever else this build has not heard of, are the client's own.
func stripQueryKey(u *url.URL) {
	if u == nil || u.RawQuery == "" {
		return
	}
	q := u.Query()
	if !q.Has("key") {
		return
	}
	q.Del("key")
	u.RawQuery = q.Encode()
}

// mergeBetas folds the caller's anthropic-beta values in after the
// credential's own, dropping duplicates and preserving order. Each
// header value may itself be a comma-separated list, which is how
// clients usually send several.
func mergeBetas(ours, theirs []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, list := range append(append([]string{}, ours...), theirs...) {
		for _, value := range strings.Split(list, ",") {
			value = strings.TrimSpace(value)
			if value == "" || seen[value] {
				continue
			}
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

// maxPeekBytes bounds how much of an error response UsageLimit reads to
// decide what it is. A vendor's error envelope is a few hundred bytes.
const maxPeekBytes = 64 << 10

// peekBody reads up to maxPeekBytes of resp's body and puts them back in
// front of the rest, so the caller still receives the whole response
// exactly as the vendor sent it. What it returns is decoded where the
// vendor gzipped it -- which it does whenever the caller's own client
// asked, since the proxy forwards that client's Accept-Encoding.
func peekBody(resp *http.Response) []byte {
	if resp.Body == nil || resp.Body == http.NoBody {
		return nil
	}
	head, _ := io.ReadAll(io.LimitReader(resp.Body, maxPeekBytes))
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), resp.Body), resp.Body}
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("Content-Encoding")), "gzip") {
		zr, err := gzip.NewReader(bytes.NewReader(head))
		if err != nil {
			return nil
		}
		decoded, _ := io.ReadAll(io.LimitReader(zr, maxPeekBytes))
		return decoded
	}
	return head
}

// errorMessage is the message of a vendor's JSON error envelope --
// Anthropic's {"type":"error","error":{"message":...}} and the
// {"error":{"message":...}} the other two share -- or "" for a body that
// is not one.
func errorMessage(resp *http.Response) string {
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(peekBody(resp), &body)
	return body.Error.Message
}

// retryAfterHeader reads a Retry-After given in seconds, zero for none.
func retryAfterHeader(resp *http.Response) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return 0
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
