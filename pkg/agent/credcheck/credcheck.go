// Package credcheck tests an agent framework's own credential against
// the service that issued it: one cheap, harmless call, authenticated
// exactly as a dispatch would authenticate, reported as a sentence a
// human can act on.
//
// It is the agent-credential half of what model.CredentialChecker does
// for capabilities (pkg/ui/capability_check.go, README.md's "Testing a
// credential, from the pane that calls it Ready"). Settings' Agents tab
// could already say whether a framework's credential is *set* -- the
// secret exists and resolves -- and that is the whole of what a pane
// reading grain's own store can see. A revoked Gemini key, a Claude
// OAuth token whose session was ended, an OpenAI key deleted in somebody
// else's console: every one of those leaves the chip reading "set", and
// the first thing that notices is a dispatched run failing to
// authenticate several minutes into somebody's task.
//
// This package is what turns that into a question askable before a run:
// list the models the credential can see. A listing is the cheapest
// authenticated call each of these three APIs has, it creates nothing, it
// spends no tokens, and it is safe to press twice -- the same rules the
// capability checks hold to, and for the same reason: this is a button.
//
// Deliberately not in the three framework packages themselves. What is
// being checked is the *credential*, not the CLI: the binary need not be
// installed, no subprocess is forked, and an operator pasting a key into
// Settings on a host whose `agy` is missing still gets a straight answer
// about the key. Running each CLI instead would have cost a real model
// call per press and answered two questions at once, which is the shape
// of test that leaves nobody knowing which half failed.
package credcheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/secrets"
)

// Where each framework's credential is checked. These are the vendors'
// own public API hosts -- the same ones the CLIs a dispatch forks talk
// to -- and they are constants rather than settings because a
// deployment that pointed one of them somewhere else would be testing a
// credential against a service that never issued it. Checker's fields
// override them for tests only.
const (
	// DefaultGeminiBaseURL is Google's Generative Language API, the one
	// service a grain-minted Gemini key is even restricted to
	// (geminikey.DefaultAPITargetService), so a key that cannot list
	// models here cannot do anything for agy either.
	DefaultGeminiBaseURL = "https://generativelanguage.googleapis.com"
	// DefaultAnthropicBaseURL is the API the claude CLI authenticates
	// against with CLAUDE_CODE_OAUTH_TOKEN.
	DefaultAnthropicBaseURL = "https://api.anthropic.com"
	// DefaultOpenAIBaseURL is the API the codex CLI authenticates
	// against with OPENAI_API_KEY.
	DefaultOpenAIBaseURL = "https://api.openai.com"
)

// AnthropicVersion is the dated API version header api.anthropic.com
// requires on every request, including the model listing below. It is
// not a model version and does not follow the model an operator picked.
//
// Exported for the same reason AuthorizeAnthropic is: agent/claude's own
// model catalog reads the same listing with the same credential, and two
// definitions of how this deployment addresses that API would be one to
// keep up to date and one to forget.
const AnthropicVersion = "2023-06-01"

// anthropicOAuthBeta is the beta header a Claude Code OAuth token is
// presented with. An OAuth access token and an ordinary API key are two
// different credentials to that API -- different header, different
// error text -- and this package accepts either, since what an operator
// pastes into the "Claude Code OAuth token" field is whatever their
// setup produced. See AuthorizeAnthropic.
const anthropicOAuthBeta = "oauth-2025-04-20"

// oauthTokenPrefix is how a Claude Code OAuth access token spells
// itself. It is the only way to tell one from an API key by looking, and
// getting it wrong costs nothing worse than a refusal naming the other
// credential type -- so this is a hint for choosing a header, never a
// validation of what was pasted.
const oauthTokenPrefix = "sk-ant-oat"

// DefaultTimeout bounds one check made with Checker's own client. A
// human is waiting on an HTTP handler for this, and the answer is worth
// nothing after they have given up on it, so it is short: an unreachable
// vendor should read as "could not ask" quickly rather than hang the
// pane.
const DefaultTimeout = 20 * time.Second

// maxResponseBytes caps what is read back from a vendor. The listings
// here are a few kilobytes; anything vastly larger is a proxy or a
// captive portal answering instead, and reading all of it into a
// Settings handler serves nobody.
const maxResponseBytes = 1 << 20

// Result is what a check that got through reports: which credential was
// used and what the far end said when it was.
type Result struct {
	// Framework is the normalized framework name checked -- "gemini"
	// arrives here as "antigravity", the same fold every other caller
	// of model.NormalizeAgentFrameworkName applies.
	Framework string
	// Secret names the secret this credential is stored under
	// (secrets.GeminiAPIKeySecret and friends), so a caller reporting a
	// refusal can say which field to paste a new value into without
	// knowing anything about frameworks.
	Secret string
	// Detail is one sentence of evidence: on success, that the vendor
	// accepted the credential and what it listed back, which is what
	// makes this different from "the secret exists". Errors carry their
	// own sentence and leave this empty.
	Detail string
}

// SecretFor maps a framework name onto the secret its credential is
// stored under -- the same three well-known names cmd/grain's own
// agentCredential resolves before every dispatch. The bool is false for
// anything that is not one of grain's frameworks, which is a mistake
// about grain rather than an answer about a credential and is reported
// as one.
func SecretFor(framework string) (string, bool) {
	// Normalized first, so the legacy "gemini" spelling resolves to the
	// key agy really authenticates with, exactly as pkg/ui's own
	// agent-key handlers do.
	switch model.NormalizeAgentFrameworkName(framework) {
	case model.AgentFrameworkAntigravity:
		return secrets.GeminiAPIKeySecret, true
	case model.AgentFrameworkClaude:
		return secrets.ClaudeOAuthTokenSecret, true
	case model.AgentFrameworkCodex:
		return secrets.OpenAIAPIKeySecret, true
	default:
		return "", false
	}
}

// Checker makes the call. The zero value is the one a deployment uses:
// the vendors' real hosts, over a client with DefaultTimeout.
type Checker struct {
	// HTTPClient, when set, is used instead of a fresh client with
	// DefaultTimeout. Set by tests; a deployment leaves it nil.
	HTTPClient *http.Client
	// The three base URLs, empty meaning the Default* constants above.
	// Tests point them at an httptest.Server; nothing in a deployment
	// sets them, since checking a credential against a host that did not
	// issue it answers a different question than the one asked.
	GeminiBaseURL    string
	AnthropicBaseURL string
	OpenAIBaseURL    string
}

// Check authenticates as credential and lists what the vendor will show
// it, returning what came back.
//
// The split between the two returns is the same one model.
// CredentialChecker draws, and the same one pkg/ui's handlers rely on:
// an error here is *an answer* about this deployment's credential -- it
// was refused, or the vendor could not be reached -- and Result still
// carries Framework and Secret so a caller can name the field to fix. A
// framework grain does not have is the one thing that is not an answer,
// and it is told apart by SecretFor's own bool before any call is made.
//
// Nothing is written anywhere, by design: an operator is expected to
// press this repeatedly while pasting a key in, and a check that cost a
// model call or left anything behind would be one they learn not to
// press.
func (c Checker) Check(ctx context.Context, framework, credential string) (Result, error) {
	name := model.NormalizeAgentFrameworkName(framework)
	secret, ok := SecretFor(name)
	if !ok {
		return Result{Framework: name}, fmt.Errorf("no agent framework named %q", framework)
	}
	res := Result{Framework: name, Secret: secret}
	credential = strings.TrimSpace(credential)
	if credential == "" {
		// Worth its own sentence rather than a refusal from the vendor:
		// there is nothing to send, and the remedy is the field beside
		// the button rather than anything at the far end.
		return res, fmt.Errorf(
			"no credential is set for %s: paste one into the field beside this (it is stored as the %q secret)",
			name, secret)
	}
	var detail string
	var err error
	switch name {
	case model.AgentFrameworkAntigravity:
		detail, err = c.checkGemini(ctx, credential, secret)
	case model.AgentFrameworkClaude:
		detail, err = c.checkAnthropic(ctx, credential, secret)
	case model.AgentFrameworkCodex:
		detail, err = c.checkOpenAI(ctx, credential, secret)
	}
	if err != nil {
		return res, err
	}
	res.Detail = detail
	return res, nil
}

// checkGemini lists the models the key can see. `key=` in the query
// string is how this API takes an API key and the only way it takes one;
// it is a GET to Google over TLS, and the value never reaches a grain
// log, since nothing here logs the request.
func (c Checker) checkGemini(ctx context.Context, key, secret string) (string, error) {
	req, err := c.request(ctx, baseOr(c.GeminiBaseURL, DefaultGeminiBaseURL)+"/v1beta/models")
	if err != nil {
		return "", err
	}
	AuthorizeGemini(req, key)
	body, err := c.do(req, "Google", secret, "gemini-api-key")
	if err != nil {
		return "", err
	}
	var listing struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &listing); err != nil {
		return "", fmt.Errorf("Google accepted the key held in %q but answered with something this is not able to read: %w",
			secret, err)
	}
	names := make([]string, 0, len(listing.Models))
	for _, m := range listing.Models {
		names = append(names, strings.TrimPrefix(m.Name, "models/"))
	}
	return fmt.Sprintf("Google accepted the key held in %q and listed %s.",
		secret, describeModels(names)), nil
}

// checkAnthropic lists the models the token can see.
func (c Checker) checkAnthropic(ctx context.Context, token, secret string) (string, error) {
	req, err := c.request(ctx, baseOr(c.AnthropicBaseURL, DefaultAnthropicBaseURL)+"/v1/models")
	if err != nil {
		return "", err
	}
	req.Header.Set("anthropic-version", AnthropicVersion)
	AuthorizeAnthropic(req, token)
	body, err := c.do(req, "Anthropic", secret, "claude-oauth-token")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Anthropic accepted the token held in %q and listed %s.",
		secret, describeModels(dataIDs(body))), nil
}

// checkOpenAI lists the models the key can see.
func (c Checker) checkOpenAI(ctx context.Context, key, secret string) (string, error) {
	req, err := c.request(ctx, baseOr(c.OpenAIBaseURL, DefaultOpenAIBaseURL)+"/v1/models")
	if err != nil {
		return "", err
	}
	AuthorizeOpenAI(req, key)
	body, err := c.do(req, "OpenAI", secret, "openai-api-key")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("OpenAI accepted the key held in %q and listed %s.",
		secret, describeModels(dataIDs(body))), nil
}

// AuthorizeAnthropic presents the credential the way the credential
// itself asks to be presented: a Claude Code OAuth access token as a
// bearer, with the beta header that API pairs with one, and anything
// else as an ordinary x-api-key.
//
// Both are accepted because the field this checks is filled by hand.
// `claude setup-token` prints an OAuth token, which is what grain
// documents and what CLAUDE_CODE_OAUTH_TOKEN means -- but an operator
// with a plain Anthropic API key can paste that instead and the CLI will
// run on it, so a check that only understood one of them would call a
// working deployment broken.
//
// Exported because it is the whole of "how this deployment presents its
// Claude credential", and the check is no longer the only thing that
// needs it: pkg/modelproxy authenticates every request it forwards to
// Anthropic with the same credential, and a proxy that chose the header
// differently from the pane an operator tests that credential on would
// be the two halves disagreeing about one secret.
func AuthorizeAnthropic(req *http.Request, token string) {
	if strings.HasPrefix(token, oauthTokenPrefix) {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("anthropic-beta", anthropicOAuthBeta)
		return
	}
	req.Header.Set("x-api-key", token)
}

// AuthorizeOpenAI presents an OpenAI API key the one way that API takes
// one: an Authorization bearer, exactly as the codex CLI and the OpenAI
// SDKs send it.
//
// Exported for the same reason AuthorizeAnthropic is -- pkg/modelproxy
// forwards with this deployment's OpenAI key and must present it the way
// the pane that tested that key did.
func AuthorizeOpenAI(req *http.Request, key string) {
	req.Header.Set("Authorization", "Bearer "+key)
}

// AuthorizeGemini presents a Gemini API key the way the Generative
// Language API takes one: as the `key=` query parameter, which is what
// google-genai and every curl example send and the only form that API
// has always accepted.
//
// A query parameter rather than a header is a real difference and not a
// stylistic one -- a credential in a URL is a credential in anything
// that logs URLs -- so the two callers of this both hold to the same
// rule: nothing here logs the request (this package makes one GET to
// Google and reads the body), and pkg/modelproxy's audit entries record
// a path with the query deliberately stripped.
func AuthorizeGemini(req *http.Request, key string) {
	q := req.URL.Query()
	q.Set("key", key)
	req.URL.RawQuery = q.Encode()
}

func (c Checker) request(ctx context.Context, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// do makes the request and turns everything that is not a 2xx into the
// sentence this check exists to produce: who refused, what they said,
// and which secret holds the value to replace.
//
// vendor is the name of the far end as an operator thinks of it, and
// flag is the -<name>-file flag that seeds the same credential from
// disk, since a deployment seeded that way is one where replacing the
// secret alone may not be the whole remedy.
func (c Checker) do(req *http.Request, vendor, secret, flag string) ([]byte, error) {
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		// Not a verdict on the credential and says so: a deployment
		// behind a proxy that blocks the vendor would otherwise read
		// this as "the key is bad" and rotate a perfectly good one.
		//
		// The *url.Error client.Do returns is unwrapped first: it renders
		// the whole request URL, and AuthorizeGemini puts the key in that
		// URL's query string. The host is all this sentence needs.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("could not reach %s at %s to test the credential in %q: %w",
			vendor, req.URL.Host, secret, err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s refused the credential held in %q (HTTP %d%s). "+
			"Paste a current value into the field beside this, or replace whatever -%s-file names on this host.",
			vendor, secret, resp.StatusCode, APIMessage(body), flag)
	}
	if readErr != nil {
		return nil, fmt.Errorf("%s accepted the credential held in %q but the answer could not be read: %w",
			vendor, secret, readErr)
	}
	return body, nil
}

// APIMessage digs the vendor's own sentence out of an error body, so a
// refusal reads as "OAuth access token is invalid" rather than as a bare
// status code. All three of these APIs answer with an "error" object;
// two spell the sentence "message" inside it and Google spells it the
// same way, so one shape covers them. Anything else -- an HTML error
// page from a proxy, an empty body -- contributes nothing rather than
// being pasted raw into a UI.
//
// Exported because a refusal of the model listing agent/claude reads is
// the same refusal, from the same API, worth the same sentence.
func APIMessage(body []byte) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ""
	}
	if envelope.Error.Message == "" {
		return ""
	}
	return ": " + strings.TrimSpace(envelope.Error.Message)
}

// dataIDs reads the {"data":[{"id":...}]} listing both Anthropic and
// OpenAI answer with. A body that will not parse yields no names rather
// than an error: the credential was accepted, which is the question that
// was asked, and describeModels has an honest answer for an empty list.
func dataIDs(body []byte) []string {
	var listing struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &listing); err != nil {
		return nil
	}
	ids := make([]string, 0, len(listing.Data))
	for _, m := range listing.Data {
		ids = append(ids, m.ID)
	}
	return ids
}

// describeModels is the evidence half of a successful check: a count,
// and the first couple of names to show it is a real listing of real
// models rather than a 200 from something that was not asked the
// question. Truncated because a Gemini listing runs to dozens of entries
// and this is one line in a pane.
func describeModels(names []string) string {
	if len(names) == 0 {
		// A 200 with nothing in it is still an accepted credential, and
		// the sentence should not pretend otherwise in either direction.
		return "no models (the credential was accepted, but this account can see nothing)"
	}
	shown := names
	if len(shown) > 3 {
		shown = shown[:3]
	}
	suffix := ""
	if len(names) > len(shown) {
		suffix = ", ..."
	}
	return fmt.Sprintf("%d model(s): %s%s", len(names), strings.Join(shown, ", "), suffix)
}

func baseOr(configured, fallback string) string {
	if configured == "" {
		return fallback
	}
	return strings.TrimSuffix(configured, "/")
}
