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

package protocol

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Default control-plane timeouts (§4.0.2).
const (
	DefaultListTimeout    = 10 * time.Second
	DefaultMintTimeout    = 60 * time.Second
	DefaultReleaseTimeout = 30 * time.Second
)

// Caller is the part of a Client a Server needs; tests substitute their own.
type Caller interface {
	ListTools(ctx context.Context) ([]Tool, error)
	CallTool(ctx context.Context, name string, arguments map[string]any) ([]byte, error)
	Close() error
}

type clientCaller struct{ *Client }

func (c clientCaller) CallTool(ctx context.Context, name string, arguments map[string]any) ([]byte, error) {
	return c.Client.CallTool(ctx, name, arguments)
}

// Server is an attached capability server: its capability_id, the methods
// its tools/list advertised, and the session to call them over.
type Server struct {
	ID      string
	Methods map[string]*MethodInfo
	// ConfigSchema is the configure tool's config schema (§4.5.1), nil
	// for a server that advertises no configure tool.
	ConfigSchema map[string]any
	// ControlTools are the names of the tools its tools/list advertised
	// that are neither a method's nor configure: tools of its own Grain
	// may call by name (CallTool), such as host-script's test_script.
	ControlTools []string
	// Identity is the Grain the server is attached to, filled into the
	// identity fields of its configuration (§4.5.7, WithIdentity) on
	// every configure.
	Identity Identity
	// Managed keeps the values of the configure fields Grain manages
	// (WithManagedSecrets); nil manages none.
	Managed ManagedSecrets

	ListTimeout, MintTimeout, ReleaseTimeout time.Duration

	caller Caller
	locks  sync.Map // attempt_id -> *sync.Mutex

	// dataPlane is where the server's attempt toolsets are served
	// (ServerConfig.DataPlane), or dataPlaneErr why there is nowhere.
	dataPlane    DataPlane
	dataPlaneErr error
	// configuredToken is the DataPlaneTokenConfig the configuration last
	// applied gave the server, if any.
	configuredToken string
}

// Attach resolves a configured server's secrets, connects to it,
// discovers its methods and, for a server advertising configure, sends it
// its configuration and re-lists its tools (§3.1.2, §4.0, §4.1, §4.5). A
// disabled entry, a missing secret, a discovery error or a configure that
// fails refuses the attach; a ConfigureError says the last of these and
// carries the schema, so Settings can still draw the form to fix it.
func Attach(ctx context.Context, id string, cfg ServerConfig, secrets SecretLookup, opts DialOptions) (*Server, error) {
	if err := cfg.Validate(id); err != nil {
		return nil, err
	}
	if cfg.Disabled {
		return nil, fmt.Errorf("capability server %s is disabled", id)
	}
	resolved, err := cfg.Resolve(secrets)
	if err != nil {
		return nil, fmt.Errorf("capability server %s: %v", id, err)
	}
	dialCtx, cancel := context.WithTimeout(ctx, DefaultListTimeout)
	defer cancel()
	c, err := Dial(dialCtx, resolved, opts)
	if err != nil {
		return nil, fmt.Errorf("capability server %s: %w", id, err)
	}
	s, err := attachCaller(ctx, id, cfg, secrets, clientCaller{c}, opts.Identity, opts.Managed)
	if err != nil {
		return nil, err
	}
	s.SetDataPlane(s.dataPlaneOf(resolved))
	return s, nil
}

// dataPlaneOf is resolved's data plane (ServerConfig.DataPlane), behind
// the DataPlaneTokenConfig the configuration applied gave a stdio server,
// when it gave one, rather than its environment's.
func (s *Server) dataPlaneOf(resolved ServerConfig) (DataPlane, error) {
	dp, err := resolved.DataPlane()
	if err == nil && resolved.Stdio() && s.configuredToken != "" {
		dp.Headers["Authorization"] = "Bearer " + s.configuredToken
	}
	return dp, err
}

// AttachCaller is Attach after the dial: discovery over an initialized
// caller, then the entry's config sent to configure (ApplyConfig). The
// caller is closed when it fails.
func AttachCaller(ctx context.Context, id string, cfg ServerConfig, secrets SecretLookup, caller Caller) (*Server, error) {
	return attachCaller(ctx, id, cfg, secrets, caller, Identity{}, nil)
}

// AttachCallerAs is AttachCaller for a Grain whose identity is id
// (Server.Identity).
func AttachCallerAs(ctx context.Context, id string, cfg ServerConfig, secrets SecretLookup, caller Caller, identity Identity) (*Server, error) {
	return attachCaller(ctx, id, cfg, secrets, caller, identity, nil)
}

// AttachCallerManaged is AttachCallerAs whose managed configure fields
// (WithManagedSecrets) are kept in managed.
func AttachCallerManaged(ctx context.Context, id string, cfg ServerConfig, secrets SecretLookup, caller Caller, identity Identity, managed ManagedSecrets) (*Server, error) {
	return attachCaller(ctx, id, cfg, secrets, caller, identity, managed)
}

func attachCaller(ctx context.Context, id string, cfg ServerConfig, secrets SecretLookup, caller Caller, identity Identity, managed ManagedSecrets) (*Server, error) {
	s, err := NewServer(ctx, id, caller)
	if err != nil {
		caller.Close()
		return nil, err
	}
	s.Identity = identity
	s.Managed = managed
	if err := s.ApplyConfig(ctx, cfg.Config, secrets); err != nil {
		caller.Close()
		return nil, err
	}
	return s, nil
}

// NewServer discovers the methods behind an already-connected caller.
func NewServer(ctx context.Context, id string, caller Caller) (*Server, error) {
	s := &Server{
		ID:             id,
		ListTimeout:    DefaultListTimeout,
		MintTimeout:    DefaultMintTimeout,
		ReleaseTimeout: DefaultReleaseTimeout,
		caller:         caller,
	}
	if err := s.Refresh(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Refresh re-issues tools/list (§4.0.4). On a discovery error the previous
// method set is kept and the error returned.
func (s *Server) Refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.ListTimeout)
	defer cancel()
	tools, err := s.caller.ListTools(ctx)
	if err != nil {
		return fmt.Errorf("capability server %s: %w", s.ID, err)
	}
	methods, err := Discover(tools)
	if err != nil {
		return fmt.Errorf("capability server %s: %w", s.ID, err)
	}
	schema, err := DiscoverConfigure(tools)
	if err != nil {
		return fmt.Errorf("capability server %s: %w", s.ID, err)
	}
	var control []string
	for _, t := range tools {
		if t.Name != ConfigureTool && !strings.HasPrefix(t.Name, mintPrefix) && !strings.HasPrefix(t.Name, releasePrefix) {
			control = append(control, t.Name)
		}
	}
	s.Methods, s.ConfigSchema, s.ControlTools = methods, schema, control
	return nil
}

// HasControlTool reports whether the server advertised the control-plane
// tool name.
func (s *Server) HasControlTool(name string) bool {
	return slices.Contains(s.ControlTools, name)
}

// DataPlaneToken is the bearer token the server's data plane takes: the
// DataPlaneTokenConfig its configuration gave it, or the one its data
// plane's own Authorization header carries. A daemon-only control tool
// that is authenticated by that token (ObserveTool) is called with it.
func (s *Server) DataPlaneToken() string {
	if s.configuredToken != "" {
		return s.configuredToken
	}
	if s.dataPlaneErr == nil && s.dataPlane.Headers != nil {
		if a := s.dataPlane.Headers["Authorization"]; strings.HasPrefix(a, "Bearer ") {
			return strings.TrimPrefix(a, "Bearer ")
		}
	}
	return ""
}

// CallTool calls one of the server's control-plane tools (ControlTools)
// and returns its result's text. A method's mint_ and release_ tools and
// configure are not called this way: Mint, Release and ApplyConfig are.
func (s *Server) CallTool(ctx context.Context, name string, arguments map[string]any, timeout time.Duration) ([]byte, error) {
	if !s.HasControlTool(name) {
		return nil, fmt.Errorf("capability server %s advertises no tool %s", s.ID, name)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return s.caller.CallTool(ctx, name, arguments)
}

// ConfigureError is a server refused for its configuration (§4.5.5): the
// stored config does not validate against the configure schema, a secret
// it references is missing, or configure itself failed. Schema is the
// schema it was checked against.
type ConfigureError struct {
	ID     string
	Schema map[string]any
	Err    error
}

func (e *ConfigureError) Error() string {
	return fmt.Sprintf("capability server %s: configure: %v", e.ID, e.Err)
}

func (e *ConfigureError) Unwrap() error { return e.Err }

// ApplyConfig sends config, its secrets resolved through lookup, to the
// server's configure tool, then re-issues tools/list, since a server's
// methods and their args schemas may depend on its configuration
// (§4.5.4). A server that advertises no configure tool takes no config:
// an empty one is nothing to do, and a non-empty one is refused rather
// than silently ignored.
func (s *Server) ApplyConfig(ctx context.Context, config map[string]any, lookup SecretLookup) error {
	if s.ConfigSchema == nil {
		if len(config) > 0 {
			return &ConfigureError{ID: s.ID, Err: fmt.Errorf("config is set, but the server advertises no %s tool to take it", ConfigureTool)}
		}
		return nil
	}
	schema := s.ConfigSchema
	config = WithIdentity(schema, config, s.Identity)
	config, err := WithManagedSecrets(s.ID, schema, config, s.Managed)
	if err != nil {
		return &ConfigureError{ID: s.ID, Schema: schema, Err: err}
	}
	resolved, err := ResolveConfig(schema, config, lookup)
	if err != nil {
		return &ConfigureError{ID: s.ID, Schema: schema, Err: err}
	}
	if err := s.Configure(ctx, resolved); err != nil {
		return &ConfigureError{ID: s.ID, Schema: schema, Err: err}
	}
	s.configuredToken = ""
	if slices.Contains(SecretFields(schema), DataPlaneTokenConfig) {
		s.configuredToken, _ = resolved[DataPlaneTokenConfig].(string)
	}
	if err := s.Refresh(ctx); err != nil {
		return err
	}
	if s.ConfigSchema == nil {
		// A server must keep advertising configure: Grain calls it
		// again on every reconnect.
		return &ConfigureError{ID: s.ID, Schema: schema, Err: fmt.Errorf("the server stopped advertising %s once configured", ConfigureTool)}
	}
	return nil
}

// ServiceProcess is the caller's own (Client.ServiceProcess) when it has
// one: what a grant's ports are checked against (§5.3.2). A caller with
// none -- a test double -- answers 0 and "".
func (s *Server) ServiceProcess() (pid int, serverURL string) {
	if sp, ok := s.caller.(interface{ ServiceProcess() (int, string) }); ok {
		return sp.ServiceProcess()
	}
	return 0, ""
}

// Ended is the caller's own (Client.Ended) when it has one. A caller
// with none -- a test double -- never ends.
func (s *Server) Ended() bool {
	if e, ok := s.caller.(interface{ Ended() bool }); ok {
		return e.Ended()
	}
	return false
}

// SetDataPlane records where the server serves its attempt toolsets, or
// why it serves them nowhere Grain can reach. Attach sets it from the
// server's entry; tests set their own.
func (s *Server) SetDataPlane(dp DataPlane, err error) {
	s.dataPlane, s.dataPlaneErr = dp, err
	if err == nil && dp.Origin == "" {
		s.dataPlaneErr = fmt.Errorf("capability server %s serves no data plane", s.ID)
	}
}

// DataPlane is where the server's /runs/<attempt_id>/mcp are served
// (§5.2.2). It holds resolved secrets: never log it.
func (s *Server) DataPlane() (DataPlane, error) {
	if s.dataPlaneErr != nil {
		return DataPlane{}, s.dataPlaneErr
	}
	if s.dataPlane.Origin == "" {
		return DataPlane{}, fmt.Errorf("capability server %s serves no data plane", s.ID)
	}
	return s.dataPlane, nil
}

// Close ends the server's session.
func (s *Server) Close() error { return s.caller.Close() }

// Resolve fills in a reference's method by the shorthand rules of §2.1.4
// and checks that the server advertises it.
func (s *Server) Resolve(r Ref) (Ref, error) {
	if r.CapabilityID != s.ID {
		return Ref{}, fmt.Errorf("capability %s is not served by %s", r, s.ID)
	}
	return resolveMethod(r, s.Methods)
}

func (s *Server) lock(attemptID string) func() {
	m, _ := s.locks.LoadOrStore(attemptID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Mint calls mint_<method> for one grant (§4.3) and validates the response
// (ParseMintResponse). Calls for one attempt_id are serialized (§4.0.3).
// Any error returned is a transient failure (§4.2.4); refused and
// resource_exhausted come back as a response.
func (s *Server) Mint(ctx context.Context, method, attemptID string, deadline time.Time, args map[string]any) (*MintResponse, error) {
	if !ValidAttemptID(attemptID) {
		return nil, fmt.Errorf("attempt id %q does not match %s", attemptID, attemptIDPattern)
	}
	if args == nil {
		args = map[string]any{}
	}
	defer s.lock(attemptID)()
	ctx, cancel := context.WithTimeout(ctx, s.MintTimeout)
	defer cancel()
	raw, err := s.caller.CallTool(ctx, mintPrefix+method, map[string]any{
		"attempt_id": attemptID,
		"deadline":   deadline.UTC().Format(time.RFC3339),
		"args":       args,
	})
	if err != nil {
		return nil, err
	}
	return ParseMintResponse(raw, attemptID)
}

// Release calls release_<method> (§4.4) and returns its reply, audit
// included (§4.4.4); a stateless method's release is a no-op and returns
// a reply with nothing in it.
func (s *Server) Release(ctx context.Context, method, attemptID string) (*ReleaseResponse, error) {
	m := s.Methods[method]
	if m != nil && !m.Releasable {
		return &ReleaseResponse{Status: StatusOK}, nil
	}
	defer s.lock(attemptID)()
	ctx, cancel := context.WithTimeout(ctx, s.ReleaseTimeout)
	defer cancel()
	raw, err := s.caller.CallTool(ctx, releasePrefix+method, map[string]any{"attempt_id": attemptID})
	if err != nil {
		return nil, err
	}
	return ParseReleaseResponse(raw, attemptID)
}
