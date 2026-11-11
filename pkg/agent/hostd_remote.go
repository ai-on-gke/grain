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

package agent

// HostdHost for a grain hostd on another machine: one that dialed in to
// the daemon (grain hostd -server) and shares neither its filesystem nor
// its loopback. Its files are staged there over the session (OpStage),
// and the endpoints grain serves a run -- the run's own MCP server and
// grain's capability proxies -- are carried to that machine's loopback
// (OpListen), each served on grain's side by the handler grain chose.
//
// The run's MCP server stays grain's own process: it is what holds the
// sandbox's access and grain's GitHub credentials, and the run's git
// work through grain's proxy happens in the sandbox it reaches, not on
// the CLI's machine. So the far end is never handed a grain mcpserver
// argv, and has nothing it could choose -github-host, -github-insecure-http
// or -data-dir with (bwsalmon/grain#2383): it reaches an HTTP endpoint
// grain started with arguments grain built.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

// HostdRemote is how a HostdHost reaches a hostd on another machine:
// hostd.Remote, the session it dialed in on.
type HostdRemote interface {
	CarryHTTP(ctx context.Context, h http.Handler) (url string, release func(), err error)
}

// carryTimeout bounds asking hostd for a listener.
const carryTimeout = 30 * time.Second

func (h HostdHost) remote() bool { return h.Remote != nil }

// IsLocal implements the check CarryCapabilityServers falls back on: a
// hostd on another machine is not this one.
func (h HostdHost) IsLocal() bool { return !h.remote() }

// SharesHostLoopback reports whether the CLI reaches grain's loopback:
// a hostd beside the daemon does, one that dialed in from another
// machine does not.
func (h HostdHost) SharesHostLoopback() bool { return !h.remote() }

// GrainBinary is the grain binary on the CLI's machine, for the hooks a
// CLI forks there (HookBinary): hostd's own, for one on another machine.
func (h HostdHost) GrainBinary() string {
	if h.remote() {
		return h.Grain
	}
	return ""
}

// SessionRoot is where a task's CLI home is kept on a hostd's machine,
// as an absolute path there: "sessions" under its staging root. "" for a
// hostd beside the daemon, whose homes are in the daemon's own store.
func (h HostdHost) SessionRoot() string {
	if !h.remote() || h.StageRoot == "" {
		return ""
	}
	return filepath.Join(h.StageRoot, "sessions")
}

// ServesMCPOverHTTP reports that the CLI cannot fork grain's mcpserver
// itself and must be given grain's own endpoint for it (CarryMCPEndpoint).
func (h HostdHost) ServesMCPOverHTTP() bool { return h.remote() }

func (h HostdHost) stage(ctx context.Context, op hostd.StageOp) (hostd.StageReply, error) {
	r, err := h.Client.Stage(ctx, op)
	return r, hostSetup(err)
}

// ErrHostSetup marks, by errors.Is, a run's error that came from grain
// setting up the host its CLI runs on -- staging files on a grain
// hostd's machine, carrying the run's endpoints there -- rather than
// from anything the CLI itself said. The text is the wrapped error's
// own; the mark is only for a reader that has to tell grain's wiring
// from a provider's words (the orchestrator's refusal-burst reading).
var ErrHostSetup = errors.New("grain could not set up the agent CLI's host")

// hostSetupError is err, marked ErrHostSetup.
type hostSetupError struct{ err error }

func (e hostSetupError) Error() string        { return e.err.Error() }
func (e hostSetupError) Unwrap() error        { return e.err }
func (e hostSetupError) Is(target error) bool { return target == ErrHostSetup }

// hostSetup is err marked ErrHostSetup, or nil.
func hostSetup(err error) error {
	if err == nil || errors.Is(err, ErrHostSetup) {
		return err
	}
	return hostSetupError{err}
}

// HostSetupFailure reports whether err came from grain's own setup of
// the CLI's host (ErrHostSetup) or its link to a grain hostd -- none
// connected (hostd.ErrRemoteNotConnected), or one too old to carry
// grain's endpoints (hostd.ErrCarryUnsupported) -- and not from the
// CLI's output: nothing a provider said, however alike a burst of them
// reads.
func HostSetupFailure(err error) bool {
	return errors.Is(err, ErrHostSetup) || errors.Is(err, hostd.ErrRemoteNotConnected) || errors.Is(err, hostd.ErrCarryUnsupported)
}

// MkdirTemp implements Host.
func (h HostdHost) MkdirTemp(ctx context.Context, pattern string) (string, error) {
	if !h.remote() {
		return h.LocalHost.MkdirTemp(ctx, pattern)
	}
	r, err := h.stage(ctx, hostd.StageOp{Op: hostd.StageMkdirTemp, Pattern: pattern})
	return r.Path, err
}

// MkdirAll implements Host.
func (h HostdHost) MkdirAll(ctx context.Context, path string, perm fs.FileMode) error {
	if !h.remote() {
		return h.LocalHost.MkdirAll(ctx, path, perm)
	}
	_, err := h.stage(ctx, hostd.StageOp{Op: hostd.StageMkdirAll, Path: path, Perm: uint32(perm)})
	return err
}

// WriteFile implements Host.
func (h HostdHost) WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
	if !h.remote() {
		return h.LocalHost.WriteFile(ctx, path, data, perm)
	}
	_, err := h.stage(ctx, hostd.StageOp{Op: hostd.StageWriteFile, Path: path, Data: data, Perm: uint32(perm)})
	return err
}

// CreateTemp implements Host.
func (h HostdHost) CreateTemp(ctx context.Context, dir, pattern string) (string, error) {
	if !h.remote() {
		return h.LocalHost.CreateTemp(ctx, dir, pattern)
	}
	r, err := h.stage(ctx, hostd.StageOp{Op: hostd.StageCreateTemp, Dir: dir, Pattern: pattern})
	return r.Path, err
}

// Symlink implements Host.
func (h HostdHost) Symlink(ctx context.Context, target, link string) error {
	if !h.remote() {
		return h.LocalHost.Symlink(ctx, target, link)
	}
	_, err := h.stage(ctx, hostd.StageOp{Op: hostd.StageSymlink, Target: target, Path: link})
	return err
}

// ReadTail implements Host.
func (h HostdHost) ReadTail(ctx context.Context, path string, limit int64) ([]byte, bool, error) {
	if !h.remote() {
		return h.LocalHost.ReadTail(ctx, path, limit)
	}
	r, err := h.stage(ctx, hostd.StageOp{Op: hostd.StageReadTail, Path: path, Limit: limit})
	if err != nil {
		return nil, false, err
	}
	return r.Data, r.Truncated, nil
}

// RemoveAll implements Host. On a dialing-in hostd that is not connected
// right now (for instance, right after -hostd-reattach-timeout gave up with
// ErrHostdLost), cleanup returns at once rather than waiting out Client.dial's
// 30s retry window per file; the next run clears its own per-run files before
// starting and PruneDir sweeps stale homes.
func (h HostdHost) RemoveAll(ctx context.Context, path string) error {
	if !h.remote() {
		return h.LocalHost.RemoveAll(ctx, path)
	}
	_, err := h.stage(hostd.ForDisplay(ctx), hostd.StageOp{Op: hostd.StageRemoveAll, Path: path})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// DirPruner is a Host that prunes a directory of per-task directories
// itself: removing every one last modified more than maxAge ago, then
// all but the keep most recent.
type DirPruner interface {
	PruneDir(ctx context.Context, dir string, maxAge time.Duration, keep int) error
}

// PruneDir implements DirPruner for a hostd on another machine; one
// beside the daemon has nothing to say, and is not a DirPruner in
// effect (it returns at once).
func (h HostdHost) PruneDir(ctx context.Context, dir string, maxAge time.Duration, keep int) error {
	if !h.remote() {
		return nil
	}
	_, err := h.stage(ctx, hostd.StageOp{Op: hostd.StagePrune, Path: dir, Limit: int64(keep), MaxAge: maxAge})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// remoteDir is the working directory a CLI on another machine is
// started in: dir when it is one staged there, and hostd's default
// otherwise. A directory of the daemon's -- the run's sandbox root --
// is not on that machine, and the CLI's work happens in the sandbox
// through grain's MCP server, not in its own working directory.
func (h HostdHost) remoteDir(dir string) string {
	if dir == "" || h.StageRoot == "" {
		return ""
	}
	rel, err := filepath.Rel(h.StageRoot, filepath.Clean(dir))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return dir
}

// CarryCapabilityServers implements CapabilityServerCarrier: for a hostd
// beside the daemon each server is handed on as it is, and for one on
// another machine each is carried to that machine's loopback and served
// there by its Handler, on grain's side.
func (h HostdHost) CarryCapabilityServers(servers []CapabilityMCPServer) ([]CapabilityMCPServer, func(), error) {
	if !h.remote() || len(servers) == 0 {
		return servers, func() {}, nil
	}
	var releases []func()
	releaseAll := func() {
		for _, r := range releases {
			r()
		}
	}
	carried := make([]CapabilityMCPServer, 0, len(servers))
	for _, s := range servers {
		if s.Handler == nil {
			releaseAll()
			return nil, nil, hostSetup(fmt.Errorf("capability MCP server %q is not one grain proxies, so it cannot be carried to the host agent's machine", s.Name))
		}
		base, release, err := h.carry(s.Handler)
		if err != nil {
			releaseAll()
			return nil, nil, hostSetup(fmt.Errorf("carrying capability MCP server %q to the host agent's machine: %w", s.Name, err))
		}
		releases = append(releases, release)
		s.URL, err = rebase(base, s.URL)
		if err != nil {
			releaseAll()
			return nil, nil, hostSetup(err)
		}
		carried = append(carried, s)
	}
	return carried, releaseAll, nil
}

func (h HostdHost) carry(handler http.Handler) (string, func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), carryTimeout)
	defer cancel()
	return h.Remote.CarryHTTP(ctx, handler)
}

// CarryMCPEndpoint implements MCPEndpointCarrier: the run's own MCP
// endpoint, grain's child on grain's loopback, carried to the hostd's
// machine. The bearer token is put on every request on grain's side, so
// the CLI's configuration needs no header to reach it.
func (h HostdHost) CarryMCPEndpoint(endpoint, token string) (string, func(), error) {
	if !h.remote() {
		return endpoint, func() {}, nil
	}
	target, err := url.Parse(endpoint)
	if err != nil {
		return "", nil, hostSetup(fmt.Errorf("carrying the run's MCP endpoint: %w", err))
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&url.URL{Scheme: target.Scheme, Host: target.Host})
			pr.Out.Host = target.Host
			if token != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+token)
			}
		},
		// MCP's streamable HTTP answers a call as an event stream.
		FlushInterval: -1,
	}
	base, release, err := h.carry(proxy)
	if err != nil {
		return "", nil, hostSetup(fmt.Errorf("carrying the run's MCP endpoint to the host agent's machine: %w", err))
	}
	carried, err := rebase(base, endpoint)
	if err != nil {
		release()
		return "", nil, hostSetup(err)
	}
	return carried, release, nil
}

// rebase is orig's path and query under base.
func rebase(base, orig string) (string, error) {
	u, err := url.Parse(orig)
	if err != nil {
		return "", fmt.Errorf("carrying %s: %w", orig, err)
	}
	out := base + u.EscapedPath()
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out, nil
}

// MCPEndpointCarrier is a Host whose CLI cannot reach grain's loopback,
// which carries the run's own MCP endpoint to where it can.
type MCPEndpointCarrier interface {
	CarryMCPEndpoint(endpoint, token string) (carried string, release func(), err error)
}

// CarryMCPEndpoint is endpoint as h's CLI reaches it, and release for
// when the run is done with it: endpoint itself for any host that is not
// an MCPEndpointCarrier.
func CarryMCPEndpoint(h Host, endpoint, token string) (string, func(), error) {
	if c, ok := h.(MCPEndpointCarrier); ok {
		carried, release, err := c.CarryMCPEndpoint(endpoint, token)
		if err != nil {
			return "", nil, err
		}
		if release == nil {
			release = func() {}
		}
		return carried, release, nil
	}
	return endpoint, func() {}, nil
}

// ServesMCPOverHTTP reports whether h's CLI must be given grain's own
// HTTP endpoint for the run's MCP server rather than a command to fork:
// the grain binary there is not this one, and holds none of the sandbox
// access or credentials the server needs.
func ServesMCPOverHTTP(h Host) bool {
	s, ok := h.(interface{ ServesMCPOverHTTP() bool })
	return ok && s.ServesMCPOverHTTP()
}

// HookBinary is the grain binary a hook command h's CLI forks names:
// the one on h's own machine where h says, and fallback -- this
// process's own -- otherwise.
func HookBinary(h Host, fallback string) string {
	if g, ok := h.(interface{ GrainBinary() string }); ok {
		if p := g.GrainBinary(); p != "" {
			return p
		}
	}
	return fallback
}

// SupportsHookPolicy implements SupportsHookPolicy: true when the host
// agent advertised hostd.FeatureHookPolicy.
func (h HostdHost) SupportsHookPolicy() bool { return h.HookPolicy }

// SupportsHookPolicy reports whether the binary answering hooks on h
// evaluates a staged per-run hook policy file (-policy). A host agent says
// so through Hello.Has(hostd.FeatureHookPolicy); LocalHost and a run host
// built from this image fork this build's own grain binary, which always
// does.
func SupportsHookPolicy(h Host) bool {
	if p, ok := h.(interface{ SupportsHookPolicy() bool }); ok {
		return p.SupportsHookPolicy()
	}
	return true
}
