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

// Package intree is grain's in-tree capability servers: the capabilities
// compiled into the grain binary and served, one per process, by
// `grain capability <name>` over stdio (docs/capabilities-spec.md §3.1.1),
// each implementing one of the standard patterns of §6 on top of
// pkg/capability/protocol's Method and CapabilityServer:
//
//   - gcp (§6.1): mint_gcp-key / release_gcp-key, a leased GCP
//     service-account key, through pkg/capability/gcpkey's Provider;
//   - git (§6.3): mint_git-repo / release_git-repo, a per-attempt token
//     on the server's own git smart-HTTP proxy port, with a clone script
//     run as setup_root (root: true) or setup (root: false);
//   - model-proxy (§6.3): mint_model-proxy / release_model-proxy, a
//     per-attempt token on pkg/modelproxy's proxy for one vendor;
//   - config-map (§6.4): mint_config-map, stateless placements of
//     configured config maps and secret maps, each one name holding
//     several files, on the sandbox VM (with the legacy
//     skill CLI subcommand preserved as an alias).
//
// Every server takes its settings through configure (§4.5), its root
// credentials as secret fields, and falls back to its own environment --
// which grain/config binds through $secret:<name> (§3.1.2) -- and its
// arguments for each one configure leaves out. Settings that bind a data-plane listener are arguments only. Leases live in the server's
// memory: RunExpiry releases each at its deadline plus grace whether or
// not release_ ever arrives, and Serve releases every one still held when
// Grain closes the server's stdin, since nothing could expire them once
// the process is gone.
//
// The in-process providers under pkg/capability stay in use until runs
// are moved over to capability servers; these share their code rather
// than duplicating it wherever there is code to share.
package intree

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// ExpiryInterval is how often a server checks its leases for one past its
// deadline plus grace.
const ExpiryInterval = 30 * time.Second

// Env looks up one of the server's environment variables -- os.Getenv in
// `grain capability`, a map in tests.
type Env func(name string) string

// Server is one in-tree capability server, built and ready to serve.
type Server struct {
	Name string
	*protocol.CapabilityServer
	Methods []*protocol.Method
	// Log is where diagnostics go; stderr in `grain capability`, since
	// stdout is the protocol (§3.1.3).
	Log *log.Logger

	closers []func() error
	// ticks run every ExpiryInterval beside lease expiry (onTick).
	ticks []func(ctx context.Context)
	// mu guards Methods once Serve is running, for a server whose
	// configure handler replaces them (configurable).
	mu sync.Mutex
	// addr is the first data-plane listener's address, for tests.
	addr string
	// hostScripts is the host-script server's data plane, for tests.
	hostScripts *hostScripts
}

func newServer(name string, logger *log.Logger, methods ...*protocol.Method) (*Server, error) {
	cs, err := protocol.NewCapabilityServer(name, methods...)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Server{Name: name, CapabilityServer: cs, Methods: methods, Log: logger}, nil
}

// onClose registers f to run when the server is closed, after its leases
// are released.
func (s *Server) onClose(f func() error) { s.closers = append(s.closers, f) }

// onTick registers f to run every ExpiryInterval while the server serves.
func (s *Server) onTick(f func(ctx context.Context)) { s.ticks = append(s.ticks, f) }

// Serve answers the control plane on r/w until r ends or ctx is done,
// expiring leases every ExpiryInterval meanwhile, then releases every
// lease still held and closes the server's data-plane listeners.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	expiry, stop := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(ExpiryInterval)
		defer t.Stop()
		for {
			select {
			case <-expiry.Done():
				return
			case <-t.C:
				for _, m := range s.methods() {
					m.Expire(expiry)
				}
				for _, f := range s.ticks {
					f(expiry)
				}
			}
		}
	}()
	err := s.ServeStdio(ctx, r, w)
	stop()
	if cerr := s.Close(context.WithoutCancel(ctx)); err == nil {
		err = cerr
	}
	return err
}

// Close releases every lease and closes the data-plane listeners.
func (s *Server) Close(ctx context.Context) error {
	var errs []error
	for _, m := range s.methods() {
		if failed := m.ReleaseAll(ctx); len(failed) > 0 {
			errs = append(errs, fmt.Errorf("%s: releasing %s on shutdown failed", m.Name(), strings.Join(failed, ", ")))
		}
	}
	for _, c := range s.closers {
		if err := c(); err != nil {
			errs = append(errs, err)
		}
	}
	s.closers = nil
	return errors.Join(errs...)
}

// methods is a snapshot of Methods.
func (s *Server) methods() []*protocol.Method {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*protocol.Method(nil), s.Methods...)
}

// configurable makes the server advertise configure with schema
// (docs/capabilities-spec.md §4.5): each configure call builds the
// server's methods afresh from the configuration -- which build merges
// over whatever the server's arguments and environment already said, the
// fallback for an entry written before configure existed -- and replaces
// the ones it had. A call repeating the configuration already applied is
// a no-op, as §4.5.4 asks; a different one while any lease is still held
// is refused, since the methods holding those leases would be dropped.
func (s *Server) configurable(schema map[string]any, build func(ctx context.Context, config map[string]any) ([]*protocol.Method, error)) error {
	var applied map[string]any
	configured := false
	return s.HandleConfigure(schema, func(ctx context.Context, config map[string]any) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if configured && reflect.DeepEqual(config, applied) {
			return nil
		}
		for _, m := range s.Methods {
			if n := m.Leases(); n > 0 {
				return fmt.Errorf("%s: %d lease(s) of %s are still held; the new configuration applies once they are released", s.Name, n, m.Name())
			}
		}
		methods, err := build(ctx, config)
		if err != nil {
			return err
		}
		if err := s.SetMethods(methods...); err != nil {
			return err
		}
		s.Methods, applied, configured = methods, config, true
		return nil
	})
}

// Builder makes one in-tree server from its arguments and environment.
type Builder struct {
	Name    string
	Summary string
	Build   func(ctx context.Context, args []string, env Env, logger *log.Logger) (*Server, error)

	// ID is the server's canonical capability_id, CanonicalID(Name),
	// filled in by register for every server that is not Internal: the
	// id grain registers it under on every deployment (EnsureBuiltins).
	ID string
	// Internal marks a server that is not a grant -- one no sandbox ever
	// sees, like host-git -- and so is never registered as a built-in.
	Internal bool
	// NeedsConfig marks a server that does nothing until an operator
	// configures it (gcp's permission sets, host-script's scripts): it is
	// registered enabled, and reported as needing configuring until it
	// has a configuration.
	NeedsConfig bool
	// Host marks a server worth running on the host through grain hostd
	// (run: host) where there is one -- gcp with the host's ambient
	// credentials, host-script -- rather than only in the container.
	Host bool
	// HostOnly marks a server that can only do anything on the host --
	// a workspace server an edition registers that needs a host-only
	// mount and the host's identity: its built-in entry is run: host from the start, naming
	// HostEntry(Name) with HostArgs, rather than the container's
	// `grain capability <name>` (BuiltinEntry).
	HostOnly bool
	// Args are extra args a built-in's container entry passes after
	// `capability <name>`: a data-plane -listen the entry has to name for
	// grain to reach its mcp_endpoint (protocol.ServerConfig.DataPlane).
	Args []string
	// HostArgs are the args a HostOnly built-in's entry passes hostd,
	// which its allowlist entry must accept with extra_args: true.
	HostArgs []string
	// ConfigSchema is the configure schema of a server registered from
	// outside this package (an edition's own), for Settings to
	// offer Configure before the server has ever started
	// (BuiltinConfigSchema): a run: host server whose hostd entry is
	// missing never reports its own.
	ConfigSchema map[string]any
	// PersonGranted names the methods only a person may grant -- the
	// submit method of an edition's workspace server: register marks each with
	// protocol.RequirePersonGrant under ID, so no proposer's own grant
	// ever covers one.
	PersonGranted []string
	// GrantPolicies are the methods whose grants an agent may or may not
	// give depending on their args (protocol.SetGrantPolicy): a workspace
	// method, any agent's to grant for the task's own workspace and a
	// person's for any other. Register sets each under ID.
	GrantPolicies map[string]protocol.GrantPolicy
}

var builders = map[string]Builder{}

// register adds b to the table `grain capability <name>` dispatches from
// and EnsureBuiltins registers from, giving it its canonical id unless it
// is internal: a server added here is a built-in with nothing else to do.
func register(b Builder) {
	if !b.Internal && b.ID == "" {
		b.ID = CanonicalID(b.Name)
	}
	for m, p := range b.GrantPolicies {
		protocol.SetGrantPolicy(b.ID, m, p)
	}
	for _, m := range b.PersonGranted {
		protocol.RequirePersonGrant(b.ID, m)
	}
	builders[b.Name] = b
}

// Register adds b to the table from outside this package: a server
// linked into one build only (an edition's), registered from
// its own init() the way the in-tree ones are.
func Register(b Builder) { register(b) }

// NewServer is newServer for a server built outside this package.
func NewServer(name string, logger *log.Logger, methods ...*protocol.Method) (*Server, error) {
	return newServer(name, logger, methods...)
}

// OnClose registers f to run when the server is closed, after its leases
// are released.
func (s *Server) OnClose(f func() error) { s.onClose(f) }

// Listen binds a loopback data-plane listener serving h until the server
// closes (listen).
func (s *Server) Listen(addr string, h http.Handler) (*net.TCPAddr, error) { return s.listen(addr, h) }

// Configurable is configurable for a server built outside this package.
func (s *Server) Configurable(schema map[string]any, build func(ctx context.Context, config map[string]any) ([]*protocol.Method, error)) error {
	return s.configurable(schema, build)
}

// ListenAddr is listenAddr for a server built outside this package.
func ListenAddr(env Env, addr string) string { return listenAddr(env, addr) }

// NewFlags is newFlags for a server built outside this package.
func NewFlags(name string, logger *log.Logger) *flag.FlagSet {
	return newFlags(name, logOutput(logger))
}

// Lookup is the in-tree server called name.
func Lookup(name string) (Builder, bool) {
	b, ok := builders[name]
	return b, ok
}

// Names lists the in-tree servers, sorted.
func Names() []string {
	names := make([]string, 0, len(builders))
	for n := range builders {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// newFlags is a FlagSet for `grain capability <name>` that reports its
// own errors rather than exiting.
func newFlags(name string, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("grain capability "+name, flag.ContinueOnError)
	if out == nil {
		out = io.Discard
	}
	fs.SetOutput(out)
	return fs
}

func logOutput(l *log.Logger) io.Writer {
	if l == nil {
		return io.Discard
	}
	return l.Writer()
}

// listFlag is a repeatable string flag.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// pathSafe reports whether attempt_id can name a directory: the attempt_id
// grammar admits "." and "..", which no placement path may contain.
func pathSafe(attemptID string) bool {
	return protocol.ValidAttemptID(attemptID) && strings.Trim(attemptID, ".") != ""
}

func refusedResponse(reason string) *protocol.MintResponse {
	return &protocol.MintResponse{Status: protocol.StatusRefused, Reason: reason}
}

// randomToken is 32 random bytes in hex: the per-attempt bearer of a
// data-plane port (§5.3.4).
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// MinTokenBytes is the shortest bearer token a data-plane listener takes
// from its environment (§3.1.3).
const MinTokenBytes = 32

// DataPlaneTokenDescription is the description of a configure schema's
// protocol.DataPlaneTokenConfig field: grain generates the token itself
// (protocol.WithManagedSecrets), so the field is an override only.
var DataPlaneTokenDescription = fmt.Sprintf("Managed by grain: generated when the server is first attached and kept in the encrypted secrets store (Rotate replaces it); "+
	"grain sends it on every proxied tool call to the /runs/<attempt_id>/mcp listener. Set a value of at least %d bytes here only to override it.", MinTokenBytes)

// MissingDataPlaneToken is what a server with no data-plane token says:
// grain generates one wherever it has a secrets store, so this is only
// reached without one, or with an override too short to take.
var MissingDataPlaneToken = fmt.Sprintf("no data-plane token: grain generates one in its secrets store when it attaches the server, "+
	"so check that the secrets store is available, or that an overriding dataPlaneToken is at least %d bytes", MinTokenBytes)

// shellQuote single-quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
