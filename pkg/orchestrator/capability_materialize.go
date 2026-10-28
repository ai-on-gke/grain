package orchestrator

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/capability/portforward"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

// This file materializes a minted pass (capability_pass.go) into the
// run's sandbox, in the order docs/capabilities-spec.md §5 sets, before
// RunDispatch starts anything: every grant's ports forwarded (§5.3), then
// every placement written (§5.1), then the one setup_root and each
// grant's setup run (§5.5); then every mcp_endpoint is proxied and every
// prompt_section rendered (capability_endpoints.go). The same
// materialization is applied again, without minting, to the fresh VM
// recreate_sandbox builds (§4.4.3, §5.5.5).

const (
	// DefaultCapabilitySetupTimeout bounds one setup command (§5.5.3).
	DefaultCapabilitySetupTimeout = 10 * time.Minute
	// capabilitySetupOutputTail is how much of a setup command's output
	// is kept as diagnostics (§5.5.3). It never reaches the prompt.
	capabilitySetupOutputTail = 64 << 10
	// capabilityRootDir is the attempt's root working directory (§1.2):
	// the directory the agent CLI starts in. Today that is the sandbox
	// user's home, where runs clone their repository into work/.
	capabilityRootDir = "/home/debian"
	// capabilityPortsDir holds grain's own guest forwarder and the
	// per-forward tokens it presents -- under /home/debian/.grain, which
	// no placement may write (§5.1.2).
	capabilityPortsDir = "/home/debian/.grain/capability-ports"
	// sandboxUser is the user every sandbox runs its commands as.
	sandboxUser = "debian"
)

// capabilityGrantsNote covers materializePass on the task's row.
const capabilityGrantsNote = "setting up the task's capability grants"

// placementOwner is the user names a placement's owner may take: plain
// account names, so one can never smuggle an option or a group into
// chown.
var placementOwner = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// passMaterialization is one attempt's pass as it has been carried into
// its sandbox: the forwards the relay holds open for it, which outlive a
// recreate_sandbox, and what is needed to apply the pass to a new VM.
// Like the pass it holds leased secrets and is never logged or stored.
type passMaterialization struct {
	pass     *protocol.Pass
	deadline time.Time
	timeout  time.Duration
	now      func() time.Time

	relayHost string
	relayPort int
	forwards  []openForward

	// endpoints are the grants' proxied mcp_endpoints, servers what the
	// agent CLI is configured with for them, and promptSections the
	// pass's attributed prompt blocks (wireEndpoints).
	endpoints      []capabilityEndpoint
	servers        []agent.CapabilityMCPServer
	promptSections string
	// narrator, set before wireEndpoints, is told of every tool call made
	// through the endpoints; nil narrates nothing (a CI job's pass, which
	// has no task row).
	narrator *mcp.CapabilityNarrator
}

type openForward struct {
	sandboxPort int
	forward     *portforward.Forward
}

// CapabilitySetupError is a setup command that failed (§5.5.4): which
// grant's, how, and the tail of what it printed -- the capability
// server's text, not grain's.
type CapabilitySetupError struct {
	Grant     string
	AttemptID string
	Path      string
	ExitCode  int
	TimedOut  bool
	Output    string
}

func (e *CapabilitySetupError) Error() string {
	how := fmt.Sprintf("exited %d", e.ExitCode)
	if e.TimedOut {
		how = "timed out"
	}
	return fmt.Sprintf("the setup command %s of capability %s (%s) %s", e.Path, e.Grant, e.AttemptID, how)
}

// sandboxRunsCommands reports whether a sandbox can have a pass
// materialized into it at all.
func sandboxRunsCommands(sandbox Sandbox) (SandboxCommander, bool) {
	c, ok := sandbox.(SandboxCommander)
	return c, ok
}

// materializePass opens the pass's forwards on the relay and applies the
// pass to sandbox. service identifies each capability server's own
// service, which its ports' listeners must belong to. On error, whatever
// it returns has already been closed.
func materializePass(ctx context.Context, cfg Config, sandbox Sandbox, pass *protocol.Pass, deadline time.Time,
	service func(capabilityID string) (portforward.Service, error)) (*passMaterialization, error) {

	m := &passMaterialization{pass: pass, deadline: deadline, timeout: cfg.capabilitySetupTimeout(), now: cfg.now}
	if err := m.openForwards(ctx, cfg, service); err != nil {
		m.Close()
		return nil, err
	}
	if err := m.apply(ctx, sandbox); err != nil {
		m.Close()
		return nil, err
	}
	return m, nil
}

// openForwards registers each distinct sandbox_port of the pass on the
// relay, checking its host_port has a listener of the granting server's
// service (§5.3.2).
func (m *passMaterialization) openForwards(ctx context.Context, cfg Config, service func(string) (portforward.Service, error)) error {
	seen := map[int]bool{}
	for _, g := range m.pass.Grants {
		for _, port := range g.Response.Ports {
			if seen[port.SandboxPort] {
				continue
			}
			seen[port.SandboxPort] = true
			if cfg.CapabilityPorts == nil || cfg.CapabilityPortsURL == "" {
				return &protocol.RefusedError{Grant: g.Ref.String(), Reason: "this deployment serves no capability port relay, so it cannot forward ports into a sandbox"}
			}
			if m.relayHost == "" {
				host, rport, err := portforward.RelayAddress(cfg.CapabilityPortsURL)
				if err != nil {
					return err
				}
				m.relayHost, m.relayPort = host, rport
			}
			svc, err := service(g.Ref.CapabilityID)
			if err != nil {
				return &protocol.RefusedError{Grant: g.Ref.String(), Reason: err.Error()}
			}
			f, err := cfg.CapabilityPorts.Open(ctx, port.HostPort, svc)
			if err != nil {
				return &protocol.RefusedError{Grant: g.Ref.String(), Reason: err.Error()}
			}
			m.forwards = append(m.forwards, openForward{sandboxPort: port.SandboxPort, forward: f})
		}
	}
	return nil
}

// Close closes every forward of the attempt (§5.3.4). The pass itself is
// released by whoever holds it (CapabilityPasses.Release).
func (m *passMaterialization) Close() {
	if m == nil {
		return
	}
	for _, f := range m.forwards {
		f.forward.Close()
	}
	m.forwards = nil
	m.closeEndpoints()
}

// apply carries the pass into sandbox: the guest end of every forward,
// then every placement, then the setup commands -- at dispatch, and again
// into the fresh VM of a recreate_sandbox (§5.5.5).
func (m *passMaterialization) apply(ctx context.Context, sandbox Sandbox) error {
	commander, ok := sandboxRunsCommands(sandbox)
	if !ok {
		return fmt.Errorf("this sandbox backend (%T) cannot run commands in its sandbox, so it cannot materialize capability grants", sandbox)
	}
	if err := m.startGuestForwarders(ctx, sandbox, commander); err != nil {
		return err
	}
	for _, pl := range m.pass.Placements() {
		if err := placeInSandbox(ctx, sandbox, commander, pl); err != nil {
			return fmt.Errorf("placing %s: %w", pl.Path, err)
		}
	}
	return m.runSetup(ctx, commander)
}

func (m *passMaterialization) startGuestForwarders(ctx context.Context, sandbox Sandbox, commander SandboxCommander) error {
	return m.startForwarders(ctx, sandbox, commander, m.forwards)
}

// startForwarders places grain's guest forwarder and each forward's token
// and starts the forwarder on each forward's sandbox port.
func (m *passMaterialization) startForwarders(ctx context.Context, sandbox Sandbox, commander SandboxCommander, forwards []openForward) error {
	if len(forwards) == 0 {
		return nil
	}
	script := capabilityPortsDir + "/forwarder.py"
	if err := placeInSandbox(ctx, sandbox, commander, protocol.Placement{Path: script, Mode: "0600", Content: portforward.GuestForwarder}); err != nil {
		return fmt.Errorf("placing the capability port forwarder: %w", err)
	}
	for _, f := range forwards {
		tokenFile := fmt.Sprintf("%s/%d.token", capabilityPortsDir, f.sandboxPort)
		if err := placeInSandbox(ctx, sandbox, commander, protocol.Placement{Path: tokenFile, Mode: "0600", Content: f.forward.Token()}); err != nil {
			return fmt.Errorf("placing the token of the forward to sandbox port %d: %w", f.sandboxPort, err)
		}
		_, stderr, code := commander.RunInSandbox(ctx, []string{"python3", script,
			strconv.Itoa(f.sandboxPort), m.relayHost, strconv.Itoa(m.relayPort), tokenFile}, "")
		if code != 0 {
			return fmt.Errorf("forwarding sandbox port %d to host_port %d: the guest forwarder (python3) did not start: %s",
				f.sandboxPort, f.forward.HostPort(), guestCommandDetail(stderr, code))
		}
	}
	return nil
}

// placeScript writes stdin, base64, to $1 atomically: into a temporary
// file beside it that is given its mode and owner first and then renamed
// over the path, creating the parent directories (§5.1.3).
const placeScript = `set -e
umask 022
path=$1 mode=$2 owner=$3
dir=$(dirname "$path")
mkdir -p "$dir"
tmp=$(mktemp "$dir/.grain-placement.XXXXXX")
trap 'rm -f "$tmp"' EXIT
base64 -d >"$tmp"
chmod "$mode" "$tmp"
if [ -n "$owner" ]; then chown "$owner" "$tmp"; fi
mv -f "$tmp" "$path"
trap - EXIT`

// placeInSandbox writes one placement: as the sandbox user when the
// sandbox user is to own it, and as root -- the backend's own root
// transport, or sudo inside the guest -- when someone else is.
func placeInSandbox(ctx context.Context, sandbox Sandbox, commander SandboxCommander, pl protocol.Placement) error {
	data, err := pl.Decoded()
	if err != nil {
		return err
	}
	mode := fmt.Sprintf("%04o", pl.FileMode())
	stdin := base64.StdEncoding.EncodeToString(data)
	owner := pl.Owner
	if owner == "" || owner == sandboxUser {
		_, stderr, code := commander.RunInSandbox(ctx, []string{"sh", "-c", placeScript, "sh", pl.Path, mode, ""}, stdin)
		if code != 0 {
			return errors.New(guestCommandDetail(stderr, code))
		}
		return nil
	}
	if !placementOwner.MatchString(owner) {
		return fmt.Errorf("owner %q is not a user name", owner)
	}
	argv := []string{"sh", "-c", placeScript, "sh", pl.Path, mode, owner}
	var stderr string
	var code int
	if root, ok := sandbox.(SandboxRootCommander); ok {
		_, stderr, code = root.RunAsRootInSandbox(ctx, argv, stdin)
	} else {
		_, stderr, code = commander.RunInSandbox(ctx, append([]string{"sudo", "-n"}, argv...), stdin)
	}
	if code != 0 {
		return errors.New(guestCommandDetail(stderr, code))
	}
	return nil
}

// setupScript runs $1 as §5.5.3 says: in the root working directory $3,
// with nothing of grain's environment (env -i: HOME, the default PATH and
// the user's own names only), stdin closed, under timeout $2 seconds,
// printing the last 64 KiB of its output and exiting with its status
// (124 for a timeout, 126 for a path that is not an executable file).
const setupScript = `path=$1 secs=$2 root=$3 tail=$4
if [ ! -f "$path" ] || [ ! -x "$path" ]; then
  echo "$path is not an executable regular file" >&2
  exit 126
fi
mkdir -p "$root" && cd "$root" || exit 125
out=$(mktemp) || exit 125
env -i HOME=/home/debian USER=debian LOGNAME=debian PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
  timeout -k 10 "$secs" "$path" </dev/null >"$out" 2>&1
rc=$?
tail -c "$tail" "$out"
rm -f "$out"
exit $rc`

// runSetup runs the pass's setup commands one at a time, in order, each
// bounded by the setup timeout and by the grants' deadline.
func (m *passMaterialization) runSetup(ctx context.Context, commander SandboxCommander) error {
	for _, cmd := range m.pass.SetupCommands() {
		timeout := m.timeout
		if left := m.deadline.Sub(m.now()); !m.deadline.IsZero() && left < timeout {
			timeout = left
		}
		if timeout < time.Second {
			return &CapabilitySetupError{Grant: cmd.Grant, AttemptID: cmd.AttemptID, Path: cmd.Path, ExitCode: -1, TimedOut: true,
				Output: "the grants' deadline left no time to run it"}
		}
		secs := strconv.Itoa(int(timeout / time.Second))
		cctx, cancel := context.WithTimeout(ctx, timeout+30*time.Second)
		stdout, stderr, code := commander.RunInSandbox(cctx, []string{"sh", "-c", setupScript, "sh",
			cmd.Path, secs, capabilityRootDir, strconv.Itoa(capabilitySetupOutputTail)}, "")
		cancel()
		output := stdout + stderr
		if len(output) > capabilitySetupOutputTail {
			output = output[len(output)-capabilitySetupOutputTail:]
		}
		if code != 0 {
			return &CapabilitySetupError{Grant: cmd.Grant, AttemptID: cmd.AttemptID, Path: cmd.Path,
				ExitCode: code, TimedOut: code == 124, Output: output}
		}
		// Diagnostics only: the output stays out of the prompt, and its
		// length is all the log carries of it.
		log.Printf("orchestrator: capability %s (%s): setup %s succeeded, %d bytes of output", cmd.Grant, cmd.AttemptID, cmd.Path, len(output))
	}
	return nil
}

// capabilitySetupComment is what a task's conversation is told when its
// attempt was aborted because a grant could not be materialized: the
// failing grant, its exit status and the tail of its output, quoted as
// the capability server's untrusted text (§5.5.4).
func capabilitySetupComment(err error) (body, source string) {
	var setup *CapabilitySetupError
	var refused *protocol.RefusedError
	switch {
	case errors.As(err, &setup):
		how := fmt.Sprintf("exited with status %d", setup.ExitCode)
		if setup.TimedOut {
			how = "timed out"
		}
		output := strings.TrimSpace(setup.Output)
		if output == "" {
			output = "(no output)"
		}
		return fmt.Sprintf("This attempt was aborted before its agent started: the setup command `%s` of the capability `%s` (attempt_id `%s`) %s. "+
			"Its sandbox was torn down and its grants released.\n\n"+
			"The tail of what it printed, verbatim -- untrusted text from the capability server, not grain's own words:\n\n```\n%s\n```",
			setup.Path, setup.Grant, setup.AttemptID, how, strings.ReplaceAll(output, "```", "'''")), "capability:" + model.CapabilityGrantRef(setup.Grant)
	case errors.As(err, &refused):
		return fmt.Sprintf("This attempt was aborted before its agent started: the capability grants could not be materialized into its sandbox (%s). "+
			"Its sandbox was torn down and its grants released.", strings.ReplaceAll(refused.Error(), "```", "'''")), "grain"
	default:
		return fmt.Sprintf("This attempt was aborted before its agent started: its capability grants could not be materialized into its sandbox: %s. "+
			"Its sandbox was torn down and its grants released.", err), "grain"
	}
}

// reportCapabilitySetupFailure posts capabilitySetupComment on the task.
func reportCapabilitySetupFailure(ctx context.Context, store *model.Store, taskID string, err error, at time.Time) {
	if store == nil {
		return
	}
	body, source := capabilitySetupComment(err)
	author := model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}}
	if source != "grain" {
		author.OnBehalfOf = &model.Principal{Kind: model.PrincipalAutomation, ID: source}
	}
	if _, cerr := store.AddComment(context.WithoutCancel(ctx), model.Comment{TaskID: taskID, Author: author, Body: body, CreatedAt: at}); cerr != nil {
		log.Printf("orchestrator: task %s: reporting its capability setup failure: %v", taskID, cerr)
	}
}

// capabilityService identifies the service a capability server's ports
// must belong to, from the server admission minted through.
func (d Deps) capabilityService(capabilityID string) (portforward.Service, error) {
	if d.CapabilityServers == nil {
		return portforward.Service{}, errors.New("this deployment attaches no capability servers")
	}
	srv, err := d.CapabilityServers.Lookup(capabilityID)
	if err != nil {
		return portforward.Service{}, err
	}
	return portforward.ServiceFor(srv.ServiceProcess())
}

func (c Config) capabilitySetupTimeout() time.Duration {
	if c.CapabilitySetupTimeout > 0 {
		return c.CapabilitySetupTimeout
	}
	return DefaultCapabilitySetupTimeout
}
