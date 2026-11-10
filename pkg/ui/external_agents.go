// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ui

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// maxMCPRequestBytes bounds the size of a single Streamable HTTP MCP JSON-RPC
// request body accepted at POST /mcp.
const maxMCPRequestBytes = 4 << 20

// ExternalAgentTokenView is the metadata of an external agent credential
// returned by GET /api/external-agents/tokens. The raw secret token is never
// stored and never returned here.
type ExternalAgentTokenView struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Principal      string   `json:"principal"`
	TokenPrefix    string   `json:"tokenPrefix"`
	ScopeSet       string   `json:"scopeSet,omitempty"`
	Scopes         []string `json:"scopes,omitempty"`
	ResolvedScopes []string `json:"resolvedScopes"`
	Repos          []string `json:"repos,omitempty"`
	RateLimit      int      `json:"rateLimit"`
	MaxConcurrent  int      `json:"maxConcurrent"`
	CreatedBy      string   `json:"createdBy,omitempty"`
	// Owner is the person the credential acts on behalf of
	// (model.ExternalAgentToken.Owner), "" for none.
	Owner      string     `json:"owner,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	RotatedAt  *time.Time `json:"rotatedAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	Active     bool       `json:"active"`
}

// CreateExternalAgentTokenRequest is the request body of POST /api/external-agents/tokens.
type CreateExternalAgentTokenRequest struct {
	Name          string     `json:"name"`
	ScopeSet      string     `json:"scopeSet,omitempty"`
	Scopes        []string   `json:"scopes,omitempty"`
	Repos         []string   `json:"repos,omitempty"`
	ExpiresAt     *time.Time `json:"expiresAt,omitempty"`
	RateLimit     int        `json:"rateLimit,omitempty"`
	MaxConcurrent int        `json:"maxConcurrent,omitempty"`
	// Owner is the person the credential acts on behalf of
	// (model.ExternalAgentToken.Owner). Left out, it is the verified
	// person issuing it, and nobody for an issuer no verifier established
	// -- the access code never makes the deployment's own actor an owner
	// (externalAgentTokenOwner says who may name another).
	Owner string `json:"owner,omitempty"`
	// SelfService marks a credential a person mints for their own
	// workstation (`grain login --issue-token`): its Owner is the
	// verified caller and nobody else, whatever Owner says -- and nobody
	// at all when the caller only presented the access code.
	SelfService bool `json:"selfService,omitempty"`
}

// IssuedExternalAgentToken is returned once when a credential is created or
// rotated. Token holds the raw secret bearer token (gma_...) and is never
// shown again.
type IssuedExternalAgentToken struct {
	ExternalAgentTokenView
	Token string `json:"token"`
}

// RevokeAllExternalAgentTokensResponse is the response of POST /api/external-agents/tokens/revoke-all.
type RevokeAllExternalAgentTokensResponse struct {
	Revoked int `json:"revoked"`
}

func externalAgentTokenViewOf(t model.ExternalAgentToken, now time.Time) ExternalAgentTokenView {
	_, set, _, _ := model.ResolveExternalAgentGrants(t.ScopeSet, t.Scopes, t.Repos)
	// A scope held on named repositories only is listed once per
	// repository (tasks:write@acme/widgets), so the view never reads as
	// a grant on every repository.
	var resolved []string
	for _, s := range set.Scopes() {
		repos := set.Repos(s)
		if len(repos) == 0 {
			resolved = append(resolved, string(s))
			continue
		}
		for _, r := range repos {
			resolved = append(resolved, string(s)+"@"+r)
		}
	}
	if resolved == nil {
		resolved = []string{}
	}
	return ExternalAgentTokenView{
		ID:             t.ID,
		Name:           t.Name,
		Principal:      t.PrincipalID(),
		TokenPrefix:    t.Prefix,
		ScopeSet:       t.ScopeSet,
		Scopes:         t.Scopes,
		ResolvedScopes: resolved,
		Repos:          t.Repos,
		RateLimit:      t.EffectiveRateLimit(),
		MaxConcurrent:  t.EffectiveMaxConcurrent(),
		CreatedBy:      t.CreatedBy,
		Owner:          t.Owner,
		CreatedAt:      t.CreatedAt,
		LastUsedAt:     t.LastUsedAt,
		ExpiresAt:      t.ExpiresAt,
		RevokedAt:      t.RevokedAt,
		Active:         t.Active(now),
	}
}

// ListExternalAgentTokens lists all external agent credentials (active, expired, and revoked).
func (c *Client) ListExternalAgentTokens(ctx context.Context) ([]ExternalAgentTokenView, error) {
	rows, err := c.Store.ListExternalAgentTokens(ctx)
	if err != nil {
		return nil, err
	}
	now := c.now()
	out := make([]ExternalAgentTokenView, len(rows))
	for i, r := range rows {
		out[i] = externalAgentTokenViewOf(r, now)
	}
	return out, nil
}

func (c *Client) canIssuePersonExternalAgentToken(_ context.Context, actor model.Principal) bool {
	return actor.Kind == model.PrincipalHuman
}

// externalAgentTokenOwner is the owner a credential req asks to issue is
// recorded with (design/spec/external-cli/external-cli.spec.md#XC-14),
// and the source that owner is recorded with
// (model.ExternalAgentToken.OwnerSource). The owner is who the
// credential's calls are attributed to and whose owner-trust policy its
// filings are measured against, so naming one is held to who is asking:
//
//   - an agent -- an external agent credential, or a task acting through
//     its grants -- may only pass on the owner it acts for itself, with
//     the source it was recorded with, so a credential cannot be minted
//     on behalf of somebody more trusted;
//   - a verified person minting for themselves (SelfService) owns it,
//     and may name nobody else;
//   - anybody else -- a signed-in person or the operator issuing one for
//     somebody -- may name its owner. Left out, it defaults to the
//     verified person issuing it, and to nobody when no verifier
//     established who that is.
//
// The shared access code, break-glass loopback and the in-process CLI
// establish nobody in particular (operator-attribution spec §6.3), so
// they never become an owner by default -- a credential they mint acts
// on behalf of nobody, self-service ones included -- and an owner they
// name is recorded with their own, unverified, source rather than as a
// verified on-behalf-of.
func (c *Client) externalAgentTokenOwner(ctx context.Context, req CreateExternalAgentTokenRequest) (string, model.AuthSource, error) {
	asked := strings.TrimSpace(req.Owner)
	for _, r := range asked {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", "", validationErrorf("owner %q may not contain whitespace or control characters", asked)
		}
	}
	if p, ok := PrincipalFromContext(ctx); ok && p.Kind == model.PrincipalAgent {
		if req.SelfService {
			return "", "", forbiddenErrorf("a self-service credential is minted by a signed-in person")
		}
		own, src := "", model.AuthSource("")
		if tok := externalTokenFromContext(ctx); tok != nil {
			own, src = strings.TrimSpace(tok.Owner), tok.OwnerSource
		}
		if asked != "" && !strings.EqualFold(asked, own) {
			return "", "", forbiddenErrorf("an agent cannot issue a credential on behalf of %q", asked)
		}
		if own == "" {
			src = ""
		}
		return own, src, nil
	}
	// caller is the verified person issuing the credential, if any; src
	// is the source its owner is recorded with -- the verifier that
	// established caller, or the unverified source (the access code,
	// loopback, in-process) of an issuer nobody verified.
	issuer := c.requestActor(ctx).Actor
	caller, src := "", issuer.Source
	if issuer.Verified() {
		caller = requestPerson(ctx)
	}
	if caller == "" && (src == "" || issuer.Verified()) {
		src = model.SourceInProcess
	}
	if req.SelfService {
		if caller == "" {
			// The access code may still mint a workstation credential
			// (`grain login --issue-token` is what login points it at
			// instead of storing the code), but it is nobody's.
			if asked != "" {
				return "", "", forbiddenErrorf("a self-service credential minted without a verified sign-in acts for nobody, not %q", asked)
			}
			return "", "", nil
		}
		if asked != "" && !strings.EqualFold(asked, caller) {
			return "", "", forbiddenErrorf("a self-service credential belongs to %s, not %q", caller, asked)
		}
		return caller, src, nil
	}
	if asked != "" {
		return asked, src, nil
	}
	if caller == "" {
		return "", "", nil
	}
	return caller, src, nil
}

// selfServiceLimits is what a person minting a credential for their own
// workstation (`grain login --issue-token`, external-cli.spec.md#XC-9)
// may ask for without capabilities:admin: no dangerous scope and no
// requires_person scope set -- those are an operator's to issue -- and
// only repositories the deployment already works in.
func (c *Client) selfServiceLimits(ctx context.Context, name string, repos []string, needsPerson bool) error {
	if needsPerson && requestPerson(ctx) != "" {
		return forbiddenErrorf("self-service credential %q asks for a dangerous scope or a requires_person scope set: "+
			"an operator issues those (Settings -> Agents)", name)
	}
	for _, r := range repos {
		ref, err := model.ParseRepo(r)
		if err != nil {
			return validationErrorf("%v", err)
		}
		if err := c.refuseOffRepoAllowlist(ctx, ref, "a self-service credential cannot be limited to it"); err != nil {
			return err
		}
	}
	return nil
}

// CreateExternalAgentToken issues a new scoped external agent credential.
// Issuing a credential that includes any dangerous scope or a requires_person
// scope set requires a human principal.
func (c *Client) CreateExternalAgentToken(ctx context.Context, req CreateExternalAgentTokenRequest) (IssuedExternalAgentToken, error) {
	name, err := model.ValidateExternalAgentName(req.Name)
	if err != nil {
		return IssuedExternalAgentToken{}, validationErrorf("%v", err)
	}
	scopeSet, scopes, repos, _, _, needsPerson, err := model.NormalizeExternalAgentSpec(req.ScopeSet, req.Scopes, req.Repos)
	if err != nil {
		return IssuedExternalAgentToken{}, validationErrorf("%v", err)
	}
	actor := c.requestActor(ctx).Actor
	if needsPerson && isDelegatedRequest(ctx) {
		return IssuedExternalAgentToken{}, forbiddenCodeErrorf(
			"requires_person",
			"external agent credential %q includes a dangerous scope or a requires_person scope set: only a person acting directly may issue it",
			name,
		)
	}
	if needsPerson && !c.canIssuePersonExternalAgentToken(ctx, actor) {
		return IssuedExternalAgentToken{}, forbiddenErrorf(
			"external agent credential %q includes a dangerous scope or a requires_person scope set: only a person may issue it",
			name,
		)
	}
	owner, ownerSource, err := c.externalAgentTokenOwner(ctx, req)
	if err != nil {
		return IssuedExternalAgentToken{}, err
	}
	if req.SelfService {
		if err := c.selfServiceLimits(ctx, name, repos, needsPerson); err != nil {
			return IssuedExternalAgentToken{}, err
		}
	}
	if req.RateLimit < 0 {
		return IssuedExternalAgentToken{}, validationErrorf("rateLimit must be non-negative")
	}
	if req.MaxConcurrent < 0 {
		return IssuedExternalAgentToken{}, validationErrorf("maxConcurrent must be non-negative")
	}
	// The name is the agent's identity (external:<name>) in the journal,
	// in task conversations and in who may lift a pause it set, so two
	// credentials may not share one -- a revoked one included, until it
	// is deleted.
	existing, err := c.Store.ListExternalAgentTokens(ctx)
	if err != nil {
		return IssuedExternalAgentToken{}, err
	}
	for _, t := range existing {
		if strings.EqualFold(t.Name, name) {
			return IssuedExternalAgentToken{}, validationErrorf("an external agent credential named %q already exists", name)
		}
	}
	now := c.now()
	var expiresAt *time.Time
	if req.ExpiresAt != nil {
		exp := req.ExpiresAt.UTC()
		if !exp.After(now) {
			return IssuedExternalAgentToken{}, validationErrorf("expiresAt must be in the future")
		}
		expiresAt = &exp
	}
	rawToken, hash, prefix, err := model.GenerateExternalAgentToken()
	if err != nil {
		return IssuedExternalAgentToken{}, err
	}
	createdBy := actor.ID
	if actor.Kind != "" && actor.ID != "" {
		createdBy = string(actor.Kind) + ":" + actor.ID
	}
	rec, err := c.Store.CreateExternalAgentToken(ctx, model.ExternalAgentToken{
		Name:          name,
		Prefix:        prefix,
		TokenHash:     hash,
		ScopeSet:      scopeSet,
		Scopes:        scopes,
		Repos:         repos,
		RateLimit:     req.RateLimit,
		MaxConcurrent: req.MaxConcurrent,
		CreatedBy:     createdBy,
		Owner:         owner,
		OwnerSource:   ownerSource,
		CreatedAt:     now,
		ExpiresAt:     expiresAt,
	})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") || strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return IssuedExternalAgentToken{}, validationErrorf("an external agent credential named %q already exists", name)
		}
		return IssuedExternalAgentToken{}, err
	}
	return IssuedExternalAgentToken{
		ExternalAgentTokenView: externalAgentTokenViewOf(rec, now),
		Token:                  rawToken,
	}, nil
}

// RotateExternalAgentToken replaces the secret bearer token of an existing
// non-revoked credential and returns the new raw token once.
func (c *Client) RotateExternalAgentToken(ctx context.Context, id string) (IssuedExternalAgentToken, error) {
	id = strings.TrimSpace(id)
	existing, err := c.Store.GetExternalAgentToken(ctx, id)
	if err != nil {
		return IssuedExternalAgentToken{}, err
	}
	if existing == nil {
		return IssuedExternalAgentToken{}, noExternalAgentToken(id)
	}
	if existing.RevokedAt != nil {
		return IssuedExternalAgentToken{}, validationErrorf("external agent credential %s (%s) is revoked and cannot be rotated", id, existing.Name)
	}
	_, _, needsPerson, err := model.ResolveExternalAgentGrants(existing.ScopeSet, existing.Scopes, existing.Repos)
	if err != nil {
		return IssuedExternalAgentToken{}, validationErrorf("%v", err)
	}
	actor := c.requestActor(ctx).Actor
	if needsPerson && isDelegatedRequest(ctx) {
		return IssuedExternalAgentToken{}, forbiddenCodeErrorf(
			"requires_person",
			"external agent credential %q includes a dangerous scope or a requires_person scope set: only a person acting directly may rotate it",
			existing.Name,
		)
	}
	if needsPerson && !c.canIssuePersonExternalAgentToken(ctx, actor) {
		return IssuedExternalAgentToken{}, forbiddenErrorf(
			"external agent credential %q includes a dangerous scope or a requires_person scope set: only a person may rotate it",
			existing.Name,
		)
	}
	if p, ok := PrincipalFromContext(ctx); ok && p.Kind == model.PrincipalAgent {
		own := ""
		if tok := externalTokenFromContext(ctx); tok != nil {
			own = strings.TrimSpace(tok.Owner)
		}
		if existingOwner := strings.TrimSpace(existing.Owner); !strings.EqualFold(existingOwner, own) {
			return IssuedExternalAgentToken{}, forbiddenErrorf(
				"an agent cannot rotate a credential owned by %q",
				existingOwner,
			)
		}
	}
	rawToken, hash, prefix, err := model.GenerateExternalAgentToken()
	if err != nil {
		return IssuedExternalAgentToken{}, err
	}
	now := c.now()
	updated, err := c.Store.RotateExternalAgentToken(ctx, id, hash, prefix)
	if err != nil {
		return IssuedExternalAgentToken{}, err
	}
	if updated == nil {
		return IssuedExternalAgentToken{}, noExternalAgentToken(id)
	}
	view := externalAgentTokenViewOf(*updated, now)
	view.RotatedAt = &now
	return IssuedExternalAgentToken{
		ExternalAgentTokenView: view,
		Token:                  rawToken,
	}, nil
}

// noExternalAgentToken is the 404 for a credential id that names none,
// rather than NotFoundError's own "no task <id>".
func noExternalAgentToken(id string) error {
	return &NotFoundError{ID: id, message: "no external agent credential " + id}
}

// RevokeExternalAgentToken marks an external agent credential revoked immediately.
func (c *Client) RevokeExternalAgentToken(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if err := c.Store.RevokeExternalAgentToken(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "not found") {
			return noExternalAgentToken(id)
		}
		return err
	}
	noteConfigWrite(ctx)
	return nil
}

// DeleteExternalAgentToken deletes an external agent credential record.
func (c *Client) DeleteExternalAgentToken(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if err := c.Store.DeleteExternalAgentToken(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "not found") {
			return noExternalAgentToken(id)
		}
		return err
	}
	noteConfigWrite(ctx)
	return nil
}

// RevokeAllExternalAgentTokens is the kill switch: it revokes every active
// external agent credential immediately and returns the number revoked.
func (c *Client) RevokeAllExternalAgentTokens(ctx context.Context) (int, error) {
	n, err := c.Store.RevokeAllExternalAgentTokens(ctx)
	if err != nil {
		return 0, err
	}
	noteConfigWrite(ctx)
	return int(n), nil
}

// externalAgentLimiter enforces per-credential token-bucket rate limits and
// in-flight concurrency caps.
type externalAgentLimiter struct {
	mu      sync.Mutex
	buckets map[string]*externalTokenBucket
}

type externalTokenBucket struct {
	tokens     float64
	lastRefill time.Time
	inFlight   int
}

func newExternalAgentLimiter() *externalAgentLimiter {
	return &externalAgentLimiter{
		buckets: make(map[string]*externalTokenBucket),
	}
}

// acquire takes one request's slot and rate token for tok. A refusal
// carries how long the caller should wait before asking again
// (Retry-After): a full slot frees when a request finishes, but an empty
// bucket only refills at rateLimit tokens a minute, so at 3/min a caller
// told to wait one second would be refused again for twenty.
func (l *externalAgentLimiter) acquire(tok *model.ExternalAgentToken, now time.Time) (func(), time.Duration, error) {
	if l == nil || tok == nil {
		return func() {}, 0, nil
	}
	ratePerMin := tok.EffectiveRateLimit()
	maxConc := tok.EffectiveMaxConcurrent()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[tok.ID]
	if !ok {
		b = &externalTokenBucket{
			tokens:     float64(ratePerMin),
			lastRefill: now,
		}
		l.buckets[tok.ID] = b
	} else {
		elapsed := now.Sub(b.lastRefill).Minutes()
		if elapsed > 0 {
			b.tokens += elapsed * float64(ratePerMin)
			if b.tokens > float64(ratePerMin) {
				b.tokens = float64(ratePerMin)
			}
			b.lastRefill = now
		}
	}
	if b.inFlight >= maxConc {
		return nil, time.Second, fmt.Errorf("external agent %q exceeded max concurrent requests (%d)", tok.Name, maxConc)
	}
	if b.tokens < 1.0 {
		wait := time.Duration((1.0 - b.tokens) / float64(ratePerMin) * float64(time.Minute))
		return nil, wait, fmt.Errorf("external agent %q exceeded rate limit (%d requests/minute)", tok.Name, ratePerMin)
	}
	b.tokens -= 1.0
	b.inFlight++
	var once sync.Once
	release := func() {
		once.Do(func() {
			l.mu.Lock()
			if cur, exists := l.buckets[tok.ID]; exists && cur.inFlight > 0 {
				cur.inFlight--
			}
			l.mu.Unlock()
		})
	}
	return release, 0, nil
}

func (l *externalAgentLimiter) reset(id string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	delete(l.buckets, id)
	l.mu.Unlock()
}

func (l *externalAgentLimiter) resetAll() {
	if l == nil {
		return
	}
	l.mu.Lock()
	clear(l.buckets)
	l.mu.Unlock()
}

type externalTokenContextKey struct{}

func withExternalToken(ctx context.Context, tok *model.ExternalAgentToken) context.Context {
	return context.WithValue(ctx, externalTokenContextKey{}, tok)
}

func externalTokenFromContext(ctx context.Context) *model.ExternalAgentToken {
	if ctx == nil {
		return nil
	}
	tok, _ := ctx.Value(externalTokenContextKey{}).(*model.ExternalAgentToken)
	return tok
}

func extractExternalBearerToken(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) >= 7 && strings.EqualFold(auth[:7], "Bearer ") {
		raw := strings.TrimSpace(auth[7:])
		if model.IsExternalAgentToken(raw) {
			return raw
		}
	}
	return ""
}

func presentsExternalAgentToken(r *http.Request) bool {
	return extractExternalBearerToken(r) != ""
}

// authenticateExternalAgent validates an external agent bearer token on r,
// checks that the external agent API is enabled in Settings, enforces expiry,
// revocation, rate limits, and concurrency caps, and returns the credential
// along with its concurrency release callback.
func (s *Server) authenticateExternalAgent(w http.ResponseWriter, r *http.Request) (*model.ExternalAgentToken, func(), bool) {
	if (r.URL.Path == "/mcp" || r.URL.Path == "/api/mcp") && r.ContentLength > maxMCPRequestBytes {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("MCP request body exceeds maximum size of %d bytes", maxMCPRequestBytes))
		return nil, nil, false
	}
	if s.tasks == nil || s.tasks.Store == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("external agent API requires a configured task store"))
		return nil, nil, false
	}
	cfg, err := s.tasks.Store.GetConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return nil, nil, false
	}
	if cfg == nil || !cfg.ExternalAgentAPI {
		writeError(w, http.StatusForbidden, errors.New("external agent API is disabled in Settings (externalAgentApi: false)"))
		return nil, nil, false
	}
	raw := extractExternalBearerToken(r)
	if raw == "" {
		writeError(w, http.StatusUnauthorized, errors.New("missing or invalid external agent bearer token"))
		return nil, nil, false
	}
	tok, err := s.tasks.Store.GetExternalAgentTokenByHash(r.Context(), model.HashExternalAgentToken(raw))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return nil, nil, false
	}
	if tok == nil {
		writeError(w, http.StatusUnauthorized, errors.New("invalid external agent bearer token"))
		return nil, nil, false
	}
	now := s.tasks.now()
	if tok.RevokedAt != nil {
		writeError(w, http.StatusUnauthorized, errors.New("external agent bearer token has been revoked"))
		return nil, nil, false
	}
	if tok.ExpiresAt != nil && !now.Before(*tok.ExpiresAt) {
		writeTokenExpired(w, *tok.ExpiresAt)
		return nil, nil, false
	}
	if status, err := s.externalTokenOwnerCap(r.Context(), tok); err != nil {
		writeError(w, status, err)
		return nil, nil, false
	}
	release, wait, err := s.externalLimiter.acquire(tok, now)
	if err != nil {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
		writeError(w, http.StatusTooManyRequests, err)
		return nil, nil, false
	}
	_ = s.tasks.Store.TouchExternalAgentTokenUsed(r.Context(), tok.ID, now)
	return tok, release, true
}

// tokenWriter is who an external agent token write was made by, for its
// log line: the label and source RequestActor gives, as config writes
// log theirs (recordConfigWrite). A credential is not configuration, so
// it has no config_audit row; this line is its audit trail
// (docs/test-runs/2026-11-03-operator-attribution.md F6).
func tokenWriter(r *http.Request) string {
	who := RequestActor(r.Context())
	return PrincipalLabel(who.Actor, who.OnBehalfOf) + " (" + sourceName(who.Actor.Source) + ")"
}

func (s *Server) handleListExternalAgentTokens(w http.ResponseWriter, r *http.Request) {
	list, err := parseListQuery(r, listPageLarge)
	if err != nil {
		writeClientError(w, err)
		return
	}
	tokens, err := s.tasks.ListExternalAgentTokens(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	tokens = filterList(tokens, func(t ExternalAgentTokenView) bool {
		return list.matches(t.ID, t.Name, t.Owner, t.ScopeSet)
	})
	writeList(w, tokens, list)
}

func (s *Server) handleCreateExternalAgentToken(w http.ResponseWriter, r *http.Request) {
	var req CreateExternalAgentTokenRequest
	if !readJSON(w, r, &req) {
		return
	}
	issued, err := s.tasks.CreateExternalAgentToken(r.Context(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	log.Printf("grain external-agent: %s issued credential id=%s name=%q owner=%q scopeSet=%q scopes=%v repos=%v",
		tokenWriter(r), issued.ID, issued.Name, issued.Owner, issued.ScopeSet, issued.Scopes, issued.Repos)
	writeJSON(w, http.StatusCreated, issued)
}

func (s *Server) handleRotateExternalAgentToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	issued, err := s.tasks.RotateExternalAgentToken(r.Context(), id)
	if err != nil {
		writeClientError(w, err)
		return
	}
	s.externalLimiter.reset(strings.TrimSpace(id))
	log.Printf("grain external-agent: %s rotated credential id=%s name=%q", tokenWriter(r), issued.ID, issued.Name)
	writeJSON(w, http.StatusOK, issued)
}

func (s *Server) handleRevokeExternalAgentToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.tasks.RevokeExternalAgentToken(r.Context(), id); err != nil {
		writeClientError(w, err)
		return
	}
	s.externalLimiter.reset(strings.TrimSpace(id))
	log.Printf("grain external-agent: %s revoked credential id=%s", tokenWriter(r), id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteExternalAgentToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.tasks.DeleteExternalAgentToken(r.Context(), id); err != nil {
		writeClientError(w, err)
		return
	}
	s.externalLimiter.reset(strings.TrimSpace(id))
	log.Printf("grain external-agent: %s deleted credential id=%s", tokenWriter(r), id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRevokeAllExternalAgentTokens(w http.ResponseWriter, r *http.Request) {
	n, err := s.tasks.RevokeAllExternalAgentTokens(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	s.externalLimiter.resetAll()
	log.Printf("grain external-agent: %s's kill switch revoked %d active credential(s)", tokenWriter(r), n)
	writeJSON(w, http.StatusOK, RevokeAllExternalAgentTokensResponse{Revoked: n})
}

// ExternalAgentAPITools returns the MCP tools of every agent API route that tok's
// resolved scopes reach and that does not depend on a calling task context.
func ExternalAgentAPITools(s *Server, tok *model.ExternalAgentToken) ([]mcp.Tool, error) {
	return externalAgentAPITools(s, tok, nil)
}

func externalAgentAPITools(s *Server, tok *model.ExternalAgentToken, role *accessRole) ([]mcp.Tool, error) {
	if tok == nil {
		return nil, nil
	}
	_, set, _, err := model.ResolveExternalAgentGrants(tok.ScopeSet, tok.Scopes, tok.Repos)
	if err != nil {
		return nil, err
	}
	if set.Empty() {
		return nil, nil
	}
	var tools []mcp.Tool
	for _, route := range AgentAPIRoutes() {
		if _, blocked := isNoTaskContextRoute(route.Pattern); blocked {
			continue
		}
		if !set.Has(route.Scope) && !tasksAdminAllowsTaskPatch(set, route.Pattern) && !capabilitiesDetachAllowsRoute(set, route.Pattern) {
			continue
		}
		if !roleAllowsMCPRoute(role, route) {
			continue
		}
		tools = append(tools, route.externalTool(s, tok))
	}
	return tools, nil
}

// handleMCP serves the Streamable HTTP MCP JSON-RPC 2.0 endpoint at POST /mcp.
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxMCPRequestBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("MCP request body exceeds maximum size of %d bytes", maxMCPRequestBytes))
			return
		}
		writeError(w, http.StatusBadRequest, fmt.Errorf("reading MCP request body: %w", err))
		return
	}
	var probe struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id,omitempty"`
		Method  string          `json:"method"`
		Params  struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	validRPC := len(bytes.TrimSpace(body)) > 0 && json.Unmarshal(body, &probe) == nil && strings.TrimSpace(probe.Method) != ""

	tok := externalTokenFromContext(r.Context())
	if tok == nil {
		if !validRPC && s.presentsAccess(r) {
			writeError(w, http.StatusBadRequest, errors.New("invalid JSON-RPC 2.0 request"))
			return
		}
		var release func()
		var ok bool
		tok, release, ok = s.authenticateExternalAgent(w, r)
		if !ok {
			return
		}
		defer release()
	}
	if !validRPC {
		writeError(w, http.StatusBadRequest, errors.New("invalid JSON-RPC 2.0 request"))
		return
	}

	principal := "external:" + tok.Name
	switch probe.Method {
	case "initialize", "tools/list":
		log.Printf("grain mcp: {agent, %s} %s", principal, probe.Method)
	case "tools/call":
		log.Printf("grain mcp: {agent, %s} tools/call name=%q", principal, probe.Params.Name)
	case "ping":
		log.Printf("grain mcp: {agent, %s} ping", principal)
		resp, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      probe.ID,
			"result":  map[string]any{},
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resp)
		return
	}

	var listRole *accessRole
	if probe.Method == "tools/list" {
		listRole = accessRoleFromContext(r.Context())
	}
	tools, err := externalAgentAPITools(s, tok, listRole)
	if err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	reg := mcp.NewRegistry()
	for _, t := range tools {
		reg.Register(t)
	}
	ctx := withExternalToken(r.Context(), tok)
	resp := reg.Handle(ctx, body)
	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resp)
}
