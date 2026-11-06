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

// Package plcseams defines the pluggable extension seams for PLC Studio and
// the PLC Chat Gateway: request identity verification, delegated user
// credentials, group resolution, chat webhook verification/delivery, and
// external spec fetching.
package plcseams

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Standard HTTP headers used across PLC Studio, the PLC Chat Gateway, the
// Grain Registry, and Grain daemons.
const (
	HeaderOnBehalfOf        = "X-Grain-On-Behalf-Of"
	HeaderSubjectToken      = "X-Grain-Subject-Token"
	HeaderRegistryAssertion = "X-Grain-Registry-Assertion"
	HeaderClient            = "X-Grain-Client"
	HeaderChatSignature     = "X-PLC-Chat-Signature"
)

// Common errors returned by seam implementations.
var (
	ErrUnauthenticated  = errors.New("plcseams: unauthenticated request")
	ErrInvalidSignature = errors.New("plcseams: invalid chat webhook signature")
	ErrSpecNotFound     = errors.New("plcseams: spec not found")
)

// Principal represents the verified caller identity extracted from an inbound request.
type Principal struct {
	Email        string   `json:"email"`
	Subject      string   `json:"subject,omitempty"`
	Groups       []string `json:"groups,omitempty"`
	SubjectToken string   `json:"-"`
}

type principalContextKey struct{}

// WithPrincipal attaches a verified Principal to ctx.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}

// PrincipalFromContext retrieves a verified Principal from ctx, if present.
func PrincipalFromContext(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalContextKey{}).(*Principal)
	return p, ok && p != nil
}

// IdentityVerifier authenticates an inbound HTTP request and returns its Principal.
type IdentityVerifier interface {
	VerifyRequest(ctx context.Context, r *http.Request) (*Principal, error)
}

// DelegationCredentials mints HTTP headers (such as X-Grain-On-Behalf-Of and
// X-Grain-Subject-Token) when PLC Studio or the Chat Gateway calls a Grain
// daemon or registry on behalf of a verified user.
type DelegationCredentials interface {
	HeadersForUser(ctx context.Context, userEmail string) (http.Header, error)
}

// GroupResolver resolves group memberships for an authenticated user email.
type GroupResolver interface {
	ResolveGroups(ctx context.Context, userEmail string) ([]string, error)
}

// ChatInvocation represents a verified inbound command from a chat webhook.
type ChatInvocation struct {
	UserEmail      string   `json:"user_email"`
	SpaceID        string   `json:"space_id"`
	ThreadID       string   `json:"thread_id,omitempty"`
	Command        string   `json:"command"`
	Args           []string `json:"args,omitempty"`
	RawText        string   `json:"raw_text"`
	TargetInstance string   `json:"target_instance,omitempty"`
	ExternalRef    string   `json:"external_ref,omitempty"`
}

// ChatMessage represents an outbound notification posted to a chat space/thread.
type ChatMessage struct {
	SpaceID     string `json:"space_id"`
	ThreadID    string `json:"thread_id,omitempty"`
	Text        string `json:"text"`
	ExternalRef string `json:"external_ref,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	EventType   string `json:"event_type,omitempty"`
}

// ChatAdapter verifies inbound chat webhooks and delivers outbound notifications.
type ChatAdapter interface {
	VerifyAndParse(ctx context.Context, r *http.Request) (*ChatInvocation, error)
	PostMessage(ctx context.Context, msg ChatMessage) error
}

// SpecFetcher fetches external specification or playbook documents by reference.
type SpecFetcher interface {
	FetchSpec(ctx context.Context, ref string) ([]byte, error)
}

var (
	mu                    sync.RWMutex
	identityVerifier      IdentityVerifier      = &HeaderIdentityVerifier{}
	delegationCredentials DelegationCredentials = &HeaderDelegationCredentials{}
	groupResolver         GroupResolver         = &StaticGroupResolver{}
	chatAdapter           ChatAdapter           = NewHMACChatAdapter("")
	specFetcher           SpecFetcher           = NewMemorySpecFetcher(nil)
)

// RegisterIdentityVerifier overrides the process-wide IdentityVerifier seam.
func RegisterIdentityVerifier(v IdentityVerifier) {
	if v == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	identityVerifier = v
}

// CurrentIdentityVerifier returns the active IdentityVerifier seam.
func CurrentIdentityVerifier() IdentityVerifier {
	mu.RLock()
	defer mu.RUnlock()
	return identityVerifier
}

// RegisterDelegationCredentials overrides the process-wide DelegationCredentials seam.
func RegisterDelegationCredentials(d DelegationCredentials) {
	if d == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	delegationCredentials = d
}

// CurrentDelegationCredentials returns the active DelegationCredentials seam.
func CurrentDelegationCredentials() DelegationCredentials {
	mu.RLock()
	defer mu.RUnlock()
	return delegationCredentials
}

// RegisterGroupResolver overrides the process-wide GroupResolver seam.
func RegisterGroupResolver(g GroupResolver) {
	if g == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	groupResolver = g
}

// CurrentGroupResolver returns the active GroupResolver seam.
func CurrentGroupResolver() GroupResolver {
	mu.RLock()
	defer mu.RUnlock()
	return groupResolver
}

// RegisterChatAdapter overrides the process-wide ChatAdapter seam.
func RegisterChatAdapter(c ChatAdapter) {
	if c == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	chatAdapter = c
}

// CurrentChatAdapter returns the active ChatAdapter seam.
func CurrentChatAdapter() ChatAdapter {
	mu.RLock()
	defer mu.RUnlock()
	return chatAdapter
}

// RegisterSpecFetcher overrides the process-wide SpecFetcher seam.
func RegisterSpecFetcher(s SpecFetcher) {
	if s == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	specFetcher = s
}

// CurrentSpecFetcher returns the active SpecFetcher seam.
func CurrentSpecFetcher() SpecFetcher {
	mu.RLock()
	defer mu.RUnlock()
	return specFetcher
}

// ResetForTest restores all seams to their default implementations.
func ResetForTest() {
	mu.Lock()
	defer mu.Unlock()
	identityVerifier = &HeaderIdentityVerifier{}
	delegationCredentials = &HeaderDelegationCredentials{}
	groupResolver = &StaticGroupResolver{}
	chatAdapter = NewHMACChatAdapter("")
	specFetcher = NewMemorySpecFetcher(nil)
}

// HeaderIdentityVerifier is the default OSS IdentityVerifier. It extracts the
// caller email from X-Grain-On-Behalf-Of (or X-Forwarded-Email) and optional
// subject token from X-Grain-Subject-Token.
type HeaderIdentityVerifier struct {
	DefaultEmail string
}

// VerifyRequest extracts the caller Principal from standard proxy headers.
func (v *HeaderIdentityVerifier) VerifyRequest(ctx context.Context, r *http.Request) (*Principal, error) {
	email := strings.TrimSpace(r.Header.Get(HeaderOnBehalfOf))
	if email == "" {
		email = strings.TrimSpace(r.Header.Get("X-Forwarded-Email"))
	}
	if email == "" {
		email = strings.TrimSpace(v.DefaultEmail)
	}
	if email == "" {
		return nil, ErrUnauthenticated
	}
	token := strings.TrimSpace(r.Header.Get(HeaderSubjectToken))
	groups, _ := CurrentGroupResolver().ResolveGroups(ctx, email)
	return &Principal{
		Email:        email,
		Subject:      email,
		Groups:       groups,
		SubjectToken: token,
	}, nil
}

// HeaderDelegationCredentials is the default OSS DelegationCredentials implementation.
type HeaderDelegationCredentials struct {
	StaticSubjectToken string
}

// HeadersForUser constructs X-Grain-On-Behalf-Of and X-Grain-Subject-Token headers.
func (d *HeaderDelegationCredentials) HeadersForUser(ctx context.Context, userEmail string) (http.Header, error) {
	email := strings.TrimSpace(userEmail)
	token := strings.TrimSpace(d.StaticSubjectToken)
	if p, ok := PrincipalFromContext(ctx); ok {
		if email == "" {
			email = p.Email
		}
		if token == "" {
			token = p.SubjectToken
		}
	}
	if email == "" {
		return nil, fmt.Errorf("plcseams: userEmail is required for delegated headers")
	}
	h := make(http.Header)
	h.Set(HeaderOnBehalfOf, email)
	if token != "" {
		h.Set(HeaderSubjectToken, token)
	}
	return h, nil
}

// StaticGroupResolver resolves user group memberships from an in-memory map.
type StaticGroupResolver struct {
	mu      sync.RWMutex
	Members map[string][]string
}

// ResolveGroups returns a copy of the configured groups for userEmail.
func (g *StaticGroupResolver) ResolveGroups(_ context.Context, userEmail string) ([]string, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.Members == nil {
		return nil, nil
	}
	groups := g.Members[strings.ToLower(strings.TrimSpace(userEmail))]
	out := make([]string, len(groups))
	copy(out, groups)
	return out, nil
}

// HMACChatAdapter is the default OSS ChatAdapter. When Secret is non-empty,
// VerifyAndParse validates the hex-encoded HMAC-SHA256 in X-PLC-Chat-Signature.
// Outbound messages are recorded in Outbox for inspection or forwarded if Sink is set.
type HMACChatAdapter struct {
	Secret string
	Sink   func(ctx context.Context, msg ChatMessage) error

	mu     sync.Mutex
	outbox []ChatMessage
}

// NewHMACChatAdapter creates an HMACChatAdapter with the given optional shared secret.
func NewHMACChatAdapter(secret string) *HMACChatAdapter {
	return &HMACChatAdapter{Secret: secret}
}

// SignPayload computes the hex-encoded HMAC-SHA256 signature for body using secret.
func SignPayload(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyAndParse verifies the request signature (if Secret is configured) and
// parses a JSON ChatInvocation payload, splitting RawText into Command and Args
// when Command is not explicitly populated.
func (a *HMACChatAdapter) VerifyAndParse(_ context.Context, r *http.Request) (*ChatInvocation, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read chat request body: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	if a.Secret != "" {
		sig := strings.TrimSpace(r.Header.Get(HeaderChatSignature))
		expected := SignPayload(a.Secret, body)
		if !hmac.Equal([]byte(sig), []byte(expected)) {
			return nil, ErrInvalidSignature
		}
	}

	var inv ChatInvocation
	if err := json.Unmarshal(body, &inv); err != nil {
		return nil, fmt.Errorf("decode chat invocation: %w", err)
	}
	inv.UserEmail = strings.TrimSpace(inv.UserEmail)
	if inv.UserEmail == "" {
		return nil, ErrUnauthenticated
	}
	if inv.Command == "" && inv.RawText != "" {
		ParseChatCommandText(inv.RawText, &inv)
	}
	return &inv, nil
}

// ParseChatCommandText parses a slash command such as "/grain status bug:12345"
// or "status bug:12345" into inv.Command and inv.Args.
func ParseChatCommandText(raw string, inv *ChatInvocation) {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) == 0 {
		return
	}
	first := strings.TrimPrefix(strings.ToLower(fields[0]), "/")
	if first == "grain" || first == "plc" {
		fields = fields[1:]
		if len(fields) == 0 {
			return
		}
		first = strings.TrimPrefix(strings.ToLower(fields[0]), "/")
	}
	inv.Command = first
	inv.Args = fields[1:]
}

// PostMessage records the outbound chat message and invokes Sink if set.
func (a *HMACChatAdapter) PostMessage(ctx context.Context, msg ChatMessage) error {
	a.mu.Lock()
	a.outbox = append(a.outbox, msg)
	sink := a.Sink
	a.mu.Unlock()
	if sink != nil {
		return sink(ctx, msg)
	}
	return nil
}

// Outbox returns a copy of all outbound chat messages recorded by this adapter.
func (a *HMACChatAdapter) Outbox() []ChatMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]ChatMessage, len(a.outbox))
	copy(out, a.outbox)
	return out
}

// MemorySpecFetcher is an in-memory SpecFetcher implementation.
type MemorySpecFetcher struct {
	mu    sync.RWMutex
	specs map[string][]byte
}

// NewMemorySpecFetcher creates a MemorySpecFetcher seeded with initial specs.
func NewMemorySpecFetcher(initial map[string][]byte) *MemorySpecFetcher {
	m := &MemorySpecFetcher{
		specs: make(map[string][]byte, len(initial)),
	}
	for k, v := range initial {
		cp := make([]byte, len(v))
		copy(cp, v)
		m.specs[k] = cp
	}
	return m
}

// Put stores a spec document at ref.
func (m *MemorySpecFetcher) Put(ref string, content []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(content))
	copy(cp, content)
	m.specs[ref] = cp
}

// FetchSpec returns the spec bytes stored for ref, or ErrSpecNotFound.
func (m *MemorySpecFetcher) FetchSpec(_ context.Context, ref string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.specs[ref]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSpecNotFound, ref)
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp, nil
}
