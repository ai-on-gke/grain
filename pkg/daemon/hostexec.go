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

package daemon

// What "host" can mean for this deployment (docs/design/cli-run-location.md,
// "Deployments with no host executor").
//
// A daemon on the bare machine starts a host CLI as its own child. A
// daemon in the release container has no host of its own: it reaches the
// machine only through grain hostd, when one is configured. A Kubernetes
// daemon has neither -- every CLI runs as a subprocess in the daemon
// pod's container, the way KubernetesMode already withholds reboot,
// restart and shutdown (host.go). Where there is no host executor, "host"
// is refused wherever it is asked for and resolves to "container", so a
// stored value from an earlier install degrades rather than failing every
// run.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/runhost"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/whererun"
)

// Host executor kinds.
const (
	// HostExecutorLocal is a daemon on the bare machine -- a development
	// one: a host run is the daemon's own child, and there is no
	// container run.
	HostExecutorLocal = "local"
	// HostExecutorHostd is a daemon in a container with grain hostd
	// configured: a host run goes through hostd, and a container run is
	// a subprocess in the daemon's own container.
	HostExecutorHostd = "hostd"
	// HostExecutorNone is Kubernetes, or a daemon in a container with no
	// hostd: there is no host to run anything on, and every CLI runs as
	// a subprocess in the daemon's own container.
	HostExecutorNone = "none"
)

// Where a resolved run actually starts its CLI (CLIRunResolution.Via).
const (
	// ViaDaemon is a child of the daemon process, on whatever machine
	// or container the daemon runs in.
	ViaDaemon = "daemon"
	// ViaHostd is through grain hostd (agent.HostdHost).
	ViaHostd = "hostd"
)

// HostExecutor is what a "host" run location means for this deployment.
type HostExecutor struct {
	Kind string `json:"kind"`
	// Reason says why, for the log, the startup refusal and Settings:
	// "this deployment has no host executor (Kubernetes)".
	Reason string `json:"reason"`
	// Socket is hostd's socket, for HostExecutorHostd.
	Socket string `json:"socket,omitempty"`
	// Remote is the session of a grain hostd that dials in to this
	// daemon (hostd -server), for a HostExecutorHostd with no socket of
	// its own to reach -- a daemon on Kubernetes, say. RemoteHostd says
	// so for the UI.
	Remote      *hostd.Remote `json:"-"`
	RemoteHostd bool          `json:"remote,omitempty"`
}

// remoteHostExecutor is HostExecutorHostd through the hostd dialing in
// over r.
func remoteHostExecutor(r *hostd.Remote) HostExecutor {
	return HostExecutor{Kind: HostExecutorHostd, Remote: r, RemoteHostd: true, Reason: remoteHostdReason}
}

// hostdClient is the client that reaches e's hostd: over the session of
// one that dialed in when e has one, and otherwise at its socket.
func (e HostExecutor) hostdClient() hostd.Client {
	c := hostd.Client{Socket: e.Socket, Build: daemonBuild()}
	if e.Remote != nil {
		c.Dial = e.Remote.Dial
	}
	return c
}

// hostdWhere says where e's hostd is, after "grain hostd": "at
// /run/grain-hostd/hostd.sock", or that it dials in.
func (e HostExecutor) hostdWhere() string {
	if e.Remote != nil {
		return "on the machine that dials in to this daemon"
	}
	return "at " + e.Socket
}

// HostdClient is hostdClient, for the daemon's own wiring outside this
// package (pkg/cli's host-git client).
func (e HostExecutor) HostdClient() hostd.Client { return e.hostdClient() }

// remoteHostdReason is HostExecutorHostd's reason with a hostd dialing in.
const remoteHostdReason = "host CLIs and run: host capability servers run through a grain hostd on the operator's machine " +
	"that dials in to this daemon (grain hostd -server, at " + hostd.RemotePath + ")"

// HostAvailable reports whether "host" is a location this deployment
// can run anything at.
func (e HostExecutor) HostAvailable() bool { return e.Kind != HostExecutorNone }

// containerised reports whether the daemon itself runs in a container,
// so that "container" is the daemon's own and needs no image.
func (e HostExecutor) containerised() bool { return e.Kind != HostExecutorLocal }

// kubernetesNoHost is HostExecutorNone's reason on Kubernetes.
const kubernetesNoHost = "this deployment has no host executor (Kubernetes): every agent CLI runs in the daemon pod's own container"

// hostExecutor is c.HostExec when the daemon detected one at start, and
// otherwise what can be said from c alone: Kubernetes has no host, and
// anything else is treated as the bare machine it always was.
func (c Config) hostExecutor() HostExecutor {
	if c.HostExec != nil {
		return *c.HostExec
	}
	if c.HostdRemote != nil {
		return remoteHostExecutor(c.HostdRemote)
	}
	if KubernetesMode(c) {
		return HostExecutor{Kind: HostExecutorNone, Reason: kubernetesNoHost}
	}
	return HostExecutor{Kind: HostExecutorLocal, Reason: "the daemon runs on this machine"}
}

// DetectHostExecutor is cfg's host executor, read once at start. A
// daemon taking a dialing-in hostd (-hostd-token-file) has that one.
// Kubernetes is decided from cfg alone, before anything about the
// process or a hostd socket is looked at. A daemon on the bare machine
// (whererun) runs host CLIs itself. A containerised one has hostd when
// -hostd-socket names its socket, or when hostd's default socket
// directory is mounted into the container -- configured, whether or not
// hostd is up right now -- and otherwise has no host executor.
// socketDirExists is os.Stat's answer unless a test says otherwise.
func DetectHostExecutor(cfg Config, where whererun.Detection, socketDirExists func(string) bool) HostExecutor {
	if cfg.HostdRemote != nil {
		// A hostd that dials in is the host executor wherever the daemon
		// runs, Kubernetes included: it is not this daemon's machine but
		// the operator's, and it reaches the daemon rather than the other
		// way round.
		return remoteHostExecutor(cfg.HostdRemote)
	}
	if KubernetesMode(cfg) {
		return HostExecutor{Kind: HostExecutorNone, Reason: kubernetesNoHost}
	}
	if where.Host {
		return HostExecutor{Kind: HostExecutorLocal, Reason: "the daemon runs on this machine (" + where.Signal + ")"}
	}
	if cfg.HostdSocket != "" {
		return HostExecutor{Kind: HostExecutorHostd, Socket: cfg.HostdSocket,
			Reason: "the daemon runs in a container (" + where.Signal + "), and host CLIs run through grain hostd at " + cfg.HostdSocket}
	}
	if socketDirExists == nil {
		socketDirExists = func(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() }
	}
	if dir := filepath.Dir(hostd.DefaultSocketPath); socketDirExists(dir) {
		return HostExecutor{Kind: HostExecutorHostd, Socket: hostd.DefaultSocketPath,
			Reason: "the daemon runs in a container (" + where.Signal + "), and host CLIs run through grain hostd at " + hostd.DefaultSocketPath}
	}
	return HostExecutor{Kind: HostExecutorNone, Reason: fmt.Sprintf("this deployment has no host executor: the daemon runs in a container (%s) "+
		"with no grain hostd -- installed with GRAIN_HOSTD=0, or no -hostd-socket and nothing mounted at %s -- so every agent CLI "+
		"runs in the daemon's own container and nothing runs on the machine itself; re-run the installer without GRAIN_HOSTD=0 to add it",
		where.Signal, filepath.Dir(hostd.DefaultSocketPath))}
}

// CheckHostExecutorFlags refuses -hostd-socket where there is no host
// for hostd to run on.
func CheckHostExecutorFlags(cfg Config) error {
	if cfg.HostdSocket != "" && KubernetesMode(cfg) {
		return fmt.Errorf("-hostd-socket=%s: %s, so there is no grain hostd to reach; "+
			"a grain hostd on another machine can dial in instead (-hostd-token-file here, grain hostd -server there)", cfg.HostdSocket, kubernetesNoHost)
	}
	if cfg.HostdSocket != "" && cfg.HostdRemote != nil {
		return fmt.Errorf("-hostd-socket=%s and -hostd-token-file: host CLIs go through one grain hostd -- the one at the socket, or one that dials in -- not both", cfg.HostdSocket)
	}
	return nil
}

// CheckCLIRunSettings refuses a setting this deployment cannot honour:
// "host" for any CLI where there is no host executor, and "container" on
// the bare machine, where a development daemon has no container of its
// own. It is what PUT /api/settings/cli-run asks in place of
// model.ValidateCLIRun.
func (c Config) CheckCLIRunSettings(m map[string]model.CLIRunSetting) error {
	if err := model.ValidateCLIRun(m); err != nil {
		return err
	}
	exec := c.hostExecutor()
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		switch m[name].Location {
		case model.CLIRunContainer:
			if !exec.containerised() {
				return fmt.Errorf("run location for %s: container is not available -- this daemon runs on the bare machine "+
					"(%s), so it has no container of its own. Choose host or default", cliBinaryName(name), exec.Reason)
			}
		case model.CLIRunHost:
			if ok, why := exec.runsOnHost(name); !ok {
				return fmt.Errorf("run location for %s: host is not available -- %s. Choose container or default",
					cliBinaryName(name), why)
			}
		case model.CLIRunDetached:
			if ok, why := exec.runsDetached(name); !ok {
				return fmt.Errorf("run location for %s: detached is not available -- %s. Choose container or default",
					cliBinaryName(name), why)
			}
		}
	}
	return nil
}

// runsOnHost reports whether framework can be run at "host" under e, and
// says why not. A custom command is each connection's own, so it has no
// hostd allowlist entry to name.
func (e HostExecutor) runsOnHost(framework string) (bool, string) {
	switch {
	case !e.HostAvailable():
		return false, e.Reason
	case e.Kind == HostExecutorHostd && framework == model.AgentFrameworkCustom:
		return false, "custom commands have no grain hostd allowlist entry, so they run in the daemon's own container"
	}
	return true, ""
}

// HostExecutorStatus is Settings' view of the host executor: the kind,
// why, and -- for hostd -- whether it answers and what it offers.
type HostExecutorStatus struct {
	HostExecutor
	// HostAvailable is HostExecutor.HostAvailable, for the UI.
	HostAvailable bool `json:"hostAvailable"`
	// Hostd is set for HostExecutorHostd.
	Hostd *HostdStatus `json:"hostd,omitempty"`
}

// HostdStatus is one look at hostd.
type HostdStatus struct {
	Reachable bool     `json:"reachable"`
	Error     string   `json:"error,omitempty"`
	Offers    []string `json:"offers,omitempty"`
	// Protocol and Build are what hostd said in hello (protocol 1 for a
	// hostd that predates it).
	Protocol int    `json:"protocol,omitempty"`
	Build    string `json:"build,omitempty"`
	// Updating is set while hostd drains for its own upgrade, and
	// Mismatch names a protocol this daemon cannot talk to: either way
	// no new host run starts, and Error says why.
	Updating bool   `json:"updating,omitempty"`
	Mismatch string `json:"mismatch,omitempty"`
	// Processes is what hostd holds now: each detached run's CLI, which
	// outlives a daemon upgrade and is adopted by the next daemon, and
	// each run: host capability server.
	Processes []HostdProcess `json:"processes,omitempty"`
	// Running is the installer's version of grain hostd runs, Current the
	// version the installer's current link names now and CurrentBuild
	// that version's build (hostd.Hello). Current differs from Running
	// while an upgrade waits on hostd's drain; both are empty for a hostd
	// that was not started through the link.
	Running      string `json:"running,omitempty"`
	Current      string `json:"current,omitempty"`
	CurrentBuild string `json:"currentBuild,omitempty"`
	// Remote is the connection of a hostd that dials in, when that is
	// how this daemon reaches it.
	Remote *hostd.RemoteStatus `json:"remote,omitempty"`
	// Suggest names allowlist entries this edition's defaults would run
	// on the host that hostd's allowlist has none of. Only ever
	// suggested: the allowlist is the operator's, and neither grain nor a
	// re-run of setup.sh writes into one that exists.
	Suggest []string `json:"suggest,omitempty"`
	// Missing are grain's own capabilities entries (hostd.EnsureBuiltins'
	// list for this build) that hostd's allowlist lacks. Unlike Suggest
	// these are grain's to add, and it does, as root on the host, before
	// hostd's next start; each carries the text to add by hand meanwhile.
	Missing []ui.HostdMissingEntry `json:"missing,omitempty"`
}

// hostdMissing are this build's own entries offers lacks.
func hostdMissing(offers []hostd.Offer) []ui.HostdMissingEntry {
	var have []string
	for _, o := range offers {
		if o.Kind == hostd.KindCapability {
			have = append(have, o.Name)
		}
	}
	return ui.MissingHostdBuiltins(have)
}

// OnlyNeeded narrows Hostd.Missing to the entries servers -- the
// deployment's capability servers, grain_config.mcp_servers -- would
// run: grain's own entries that are not a capability server's
// (host-git), and each built-in's that an enabled run: host entry names,
// once a built-in that needs configuring has been. A built-in nobody has
// set up, or has set to run in the container, is not broken for want of
// its host entry: it waits in Settings as needing configuring, and
// System -> Health says nothing about it.
func (st *HostExecutorStatus) OnlyNeeded(servers map[string]protocol.ServerConfig) {
	if st.Hostd == nil || len(st.Hostd.Missing) == 0 {
		return
	}
	ensured, _ := intree.EnsureBuiltins(servers)
	kept := st.Hostd.Missing[:0]
	for _, m := range st.Hostd.Missing {
		if hostdEntryNeeded(m.Name, ensured) {
			kept = append(kept, m)
		}
	}
	st.Hostd.Missing = kept
	if len(kept) == 0 {
		st.Hostd.Missing = nil
	}
}

// hostdEntryNeeded says whether servers would run hostd's entry name.
func hostdEntryNeeded(name string, servers map[string]protocol.ServerConfig) bool {
	var builtin intree.Builtin
	isServer := false
	for _, b := range intree.Builtins() {
		if intree.HostEntry(b.Name) == name {
			builtin, isServer = b, true
		}
	}
	if !isServer {
		return true
	}
	for _, cfg := range servers {
		if cfg.Disabled || !cfg.OnHost() || cfg.Command != name {
			continue
		}
		if cfg.Builtin == builtin.Name && builtin.NeedsConfig && len(cfg.Config) == 0 {
			continue
		}
		return true
	}
	return false
}

// HostdProcess is one row of hostd's listing, for Settings.
type HostdProcess struct {
	ID       string    `json:"id"`
	Run      string    `json:"run,omitempty"`
	Task     string    `json:"task,omitempty"`
	Kind     string    `json:"kind"`
	Name     string    `json:"name"`
	PID      int       `json:"pid"`
	Started  time.Time `json:"started"`
	Detached bool      `json:"detached,omitempty"`
	Attached bool      `json:"attached,omitempty"`
	Exited   bool      `json:"exited,omitempty"`
	// Adopted is a detached run this daemon took over from the one
	// before it: it started before this daemon did, and a daemon is
	// reading it now.
	Adopted bool `json:"adopted,omitempty"`
}

// daemonStarted is when this process started: a detached host run that
// started before it and is attached now is one it adopted.
var daemonStarted = time.Now()

// hostdProcesses is hostd's listing as Settings shows it, or nil when
// hostd does not answer the listing.
func hostdProcesses(ctx context.Context, exec HostExecutor) []HostdProcess {
	procs, err := exec.hostdClient().Processes(ctx)
	if err != nil {
		return nil
	}
	out := make([]HostdProcess, 0, len(procs))
	for _, p := range procs {
		out = append(out, HostdProcess{
			ID: p.ID, Run: p.Run, Task: p.Task, Kind: string(p.Kind), Name: p.Name, PID: p.PID,
			Started: p.Started, Detached: p.Detached, Attached: p.Attached, Exited: p.Exited,
			Adopted: p.Detached && p.Attached && p.Started.Before(daemonStarted),
		})
	}
	return out
}

// hostdUpdating is what Settings and a refused run say while grain hostd
// drains for its own upgrade.
const hostdUpdating = "grain hostd is updating (draining for its own upgrade): new host runs wait until it is back, and runs already going carry on"

// hostdHello fills st's handshake fields from a hello to exec's hostd,
// and returns why no new host run can start there, or "".
func hostdHello(ctx context.Context, exec HostExecutor, st *HostdStatus) string {
	hd := runhost.Hostd{Client: exec.hostdClient()}
	peer, err := hd.Handshake(ctx)
	st.Protocol, st.Build = peer.Protocol, peer.Build
	st.Running, st.Current, st.CurrentBuild = peer.Running, peer.Current, peer.CurrentBuild
	var mismatch *hostd.MismatchError
	switch {
	case errors.As(err, &mismatch):
		st.Mismatch = err.Error()
		return st.Mismatch
	case err != nil:
		return "grain hostd did not answer hello: " + err.Error()
	case peer.Draining:
		st.Updating = true
		return hostdUpdating
	}
	return ""
}

// hostdStatusTimeout bounds Settings' look at hostd.
const hostdStatusTimeout = 2 * time.Second

// HostExecutorStatus is c's host executor, and hostd's state when that
// is what it is. A hostd that is configured but down is still offered:
// a host run fails, saying so, until it is back.
func (c Config) HostExecutorStatus(ctx context.Context) HostExecutorStatus {
	exec := c.hostExecutor()
	out := HostExecutorStatus{HostExecutor: exec, HostAvailable: exec.HostAvailable()}
	if exec.Kind != HostExecutorHostd {
		return out
	}
	hctx, cancel := context.WithTimeout(ctx, hostdStatusTimeout)
	defer cancel()
	offers, err := exec.hostdClient().Offers(hctx)
	out.Hostd = &HostdStatus{Reachable: err == nil}
	if exec.Remote != nil {
		st := exec.Remote.Status()
		out.Hostd.Remote = &st
	}
	if err != nil {
		out.Hostd.Error = err.Error()
		return out
	}
	for _, o := range offers {
		out.Hostd.Offers = append(out.Hostd.Offers, string(o.Kind)+" "+o.Name)
	}
	out.Hostd.Error = hostdHello(hctx, exec, out.Hostd)
	out.Hostd.Processes = hostdProcesses(hctx, exec)
	out.Hostd.Suggest = c.hostdSuggestions(offers)
	out.Hostd.Missing = hostdMissing(offers)
	return out
}

// hostExecutorStatus is c.HostExecutorStatus as Settings and System ->
// Health show it: missing only the entries store's capability servers
// would run (HostExecutorStatus.OnlyNeeded).
func hostExecutorStatus(ctx context.Context, c Config, store *model.Store) HostExecutorStatus {
	st := c.HostExecutorStatus(ctx)
	if st.Hostd == nil || len(st.Hostd.Missing) == 0 || store == nil {
		return st
	}
	stored, err := store.GetConfig(ctx)
	if err != nil || stored == nil {
		return st
	}
	st.OnlyNeeded(stored.MCPServers)
	return st
}

// hostdSuggestions are the cli entries an edition puts on the host by
// default (RegisterHostdDefault) that offers lacks, for a deployment
// that has not moved that CLI off the host itself.
func (c Config) hostdSuggestions(offers []hostd.Offer) []string {
	have := map[string]bool{}
	for _, o := range offers {
		if o.Kind == hostd.KindCLI {
			have[o.Name] = true
		}
	}
	var out []string
	for framework, location := range hostdDefaults {
		if location != model.CLIRunHost {
			continue
		}
		if setting, _ := c.CLIRunSetting(framework); setting != model.CLIRunHost {
			continue
		}
		if name := cliBinaryName(framework); !have[name] {
			out = append(out, "cli "+name)
		}
	}
	sort.Strings(out)
	return out
}

// cliHostdHost is the agent.Host a run resolved to ViaHostd uses: the
// framework's own allowlist entry on this deployment's hostd. It is
// refused, saying why, unless hostd answered and offers that entry
// (CLIRunResolution.Err): a hostd that is down, or an allowlist that does
// not name the CLI, fails the run before anything is staged for it.
func (c Config) cliHostdHost(r CLIRunResolution) (agent.Host, error) {
	if r.Err != "" {
		return nil, fmt.Errorf("%s is set to run on the host (%s), but %s", cliBinaryName(r.Framework), r.Reason, r.Err)
	}
	entry := r.Entry
	if entry == "" {
		entry = cliBinaryName(r.Framework)
	}
	if c.hostdRuns != nil {
		// A run's CLI starts detached, and outlives a daemon upgrade
		// (hostdruns.go).
		h := c.hostdRuns.Host(agent.LocalHost{}, entry)
		h.Path = r.HostPath
		return h, nil
	}
	return agent.HostdHost{Client: c.hostExecutor().hostdClient(), Entry: entry, Path: r.HostPath}, nil
}

// hostdOffersTimeout bounds asking hostd what it offers before a run, so
// a wedged hostd costs a resolution seconds rather than a dispatch.
const hostdOffersTimeout = 3 * time.Second

type hostdOffersCacheKey struct{}

type hostdCacheTarget struct {
	socket string
	remote *hostd.Remote
}

type hostdSocketEntry struct {
	once      sync.Once
	offers    []hostd.Offer
	err       error
	helloOnce sync.Once
	mismatch  string
}

type hostdOffersCache struct {
	mu      sync.Mutex
	sockets map[hostdCacheTarget]*hostdSocketEntry
}

// withHostdOffersCache attaches a per-resolution cache of hostd's Offers
// and Hello responses to ctx so resolving all frameworks in one pass does
// not dial the same socket repeatedly.
func withHostdOffersCache(ctx context.Context) context.Context {
	if ctx.Value(hostdOffersCacheKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, hostdOffersCacheKey{}, &hostdOffersCache{
		sockets: map[hostdCacheTarget]*hostdSocketEntry{},
	})
}

// hostdOffersCLI is the path on the host exec's hostd runs for its cli
// entry named entry, with why "" -- or, when hostd does not answer or
// its allowlist offers no such entry, why not.
func hostdOffersCLI(ctx context.Context, exec HostExecutor, entry string) (path, why string) {
	return hostdOffers(ctx, exec, hostd.KindCLI, entry)
}

// hostdOffers is hostdOffersCLI for an entry of any kind.
func hostdOffers(ctx context.Context, exec HostExecutor, kind hostd.Kind, entry string) (path, why string) {
	if cache, ok := ctx.Value(hostdOffersCacheKey{}).(*hostdOffersCache); ok && cache != nil {
		target := hostdCacheTarget{socket: exec.Socket, remote: exec.Remote}
		cache.mu.Lock()
		se, ok := cache.sockets[target]
		if !ok {
			se = &hostdSocketEntry{}
			cache.sockets[target] = se
		}
		cache.mu.Unlock()
		se.once.Do(func() {
			octx, cancel := context.WithTimeout(ctx, hostdOffersTimeout)
			defer cancel()
			se.offers, se.err = exec.hostdClient().Offers(octx)
		})
		if se.err != nil {
			return "", se.err.Error()
		}
		for _, o := range se.offers {
			if o.Kind == kind && o.Name == entry {
				if kind != hostd.KindCLI {
					return o.Path, ""
				}
				se.helloOnce.Do(func() {
					octx, cancel := context.WithTimeout(ctx, hostdOffersTimeout)
					defer cancel()
					var st HostdStatus
					if why := hostdHello(octx, exec, &st); st.Mismatch != "" {
						se.mismatch = why
					}
				})
				if se.mismatch != "" {
					return o.Path, se.mismatch
				}
				return o.Path, ""
			}
		}
		return "", fmt.Sprintf("grain hostd answers, but its allowlist (%s) offers no %s entry %q", hostd.DefaultAllowlistPath, kind, entry)
	}
	octx, cancel := context.WithTimeout(ctx, hostdOffersTimeout)
	defer cancel()
	offers, err := exec.hostdClient().Offers(octx)
	if err != nil {
		return "", err.Error()
	}
	for _, o := range offers {
		if o.Kind == kind && o.Name == entry {
			if kind != hostd.KindCLI {
				return o.Path, ""
			}
			// An incompatible hostd starts no new host runs, and says
			// which protocols differ. A draining one is left to the
			// start, which re-queues the run rather than failing it.
			var st HostdStatus
			if why := hostdHello(octx, exec, &st); st.Mismatch != "" {
				return o.Path, why
			}
			return o.Path, ""
		}
	}
	return "", fmt.Sprintf("grain hostd answers, but its allowlist (%s) offers no %s entry %q", hostd.DefaultAllowlistPath, kind, entry)
}

// capabilityHostd is the client run: host capability servers start
// through, or nil -- which refuses them -- where the host executor is not
// grain hostd.
func (c Config) capabilityHostd() *hostd.Client {
	exec := c.hostExecutor()
	if exec.Kind != HostExecutorHostd {
		return nil
	}
	client := exec.hostdClient()
	return &client
}

// storedHostWarned is the CLIs whose stored "host" this process has
// already logged as moved to the container: once each, not once a run.
var storedHostWarned sync.Map

// warnStoredHostOnce logs, once per framework per process, that a host
// setting is being run in the container instead.
func warnStoredHostOnce(framework, from, reason string) {
	if _, seen := storedHostWarned.LoadOrStore(framework, true); seen {
		return
	}
	log.Printf("grain daemon: agent CLI %s is set to host (%s), but %s; running it in the daemon's own container instead. "+
		"Choose container or default in Settings -> Models to clear this", cliBinaryName(framework), from, reason)
}
