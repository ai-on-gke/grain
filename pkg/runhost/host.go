package runhost

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
)

// Run identifies the agent run a Host.Run call belongs to. The
// orchestrator puts it on the context it hands the Framework (WithRun);
// a Host.Run call without one -- a model-catalog probe, a version check
// -- is not a run and runs locally, as it always has.
type Run struct {
	Task string
	Run  string
	// Deadline is the run's own budget. The run host's deadline label is
	// this plus Host.Allowance.
	Deadline time.Time
}

type runKey struct{}

// WithRun returns ctx carrying r.
func WithRun(ctx context.Context, r Run) context.Context {
	return context.WithValue(ctx, runKey{}, r)
}

// RunFrom returns the Run ctx carries, if any.
func RunFrom(ctx context.Context) (Run, bool) {
	r, ok := ctx.Value(runKey{}).(Run)
	return r, ok && r.Run != ""
}

// ErrDetached is Host.Run's error for a run left running in its run host
// because the daemon is stopping (Host.Detach): the next daemon adopts it.
// It is agent.ErrDetached, so a Framework can recognise it without
// importing this package.
var ErrDetached = agent.ErrDetached

// DefaultCursorEvery is how often a tailed run's cursor is persisted at
// most, so the store takes one small write per run per interval. A
// daemon that stops cleanly persists the cursor exactly as it detaches,
// so its successor's transcript has no hole and no duplicate; one that is
// killed outright may leave its successor to re-read up to this much.
const DefaultCursorEvery = 2 * time.Second

// DefaultAllowance is how far past a run's budget its run host's own
// deadline label is set: long enough for a daemon restart and the
// adoption that follows, short enough that a daemon which never comes
// back does not turn a two-hour budget into unbounded spend.
const DefaultAllowance = 30 * time.Minute

// Host is an agent.Host that runs each run's CLI in a run host of its
// own. Staging is local -- the per-run home a Framework writes is a
// directory under StagingRoot, which every run host mounts at the same
// path -- and so is any Host.Run call that is not a run (RunFrom).
type Host struct {
	agent.LocalHost

	Driver     Driver
	Deployment string
	Generation string
	// Image is the run host's image: the daemon's own, so the agent CLIs
	// and the grain binary in it are the build that started the run. A
	// new image therefore applies only to new runs.
	Image string
	// Grain is the grain binary's path inside Image.
	Grain string
	// User is "uid:gid" for the run host, normally the daemon's own.
	User string
	// SpoolRoot holds one spool directory per run host. It is under the
	// data directory, never in the credential directory.
	SpoolRoot string
	// StagingRoot is where MkdirTemp puts per-run homes.
	StagingRoot string
	// Mounts are the other directories every run host is given: the
	// shared credential directory (read-write, as a directory), and, for
	// granule sandboxes, the exec key's directory mcpserver reaches its
	// sandbox with. Nothing here lets a run host reach a local container
	// sandbox, which needs the docker socket, so the daemon refuses that
	// combination (daemon.CheckRunHostBackend). Nor is the data directory
	// ever one of them, so what mcpserver reads from it (the GitHub
	// credential ladder under -data-dir) is absent in a run host.
	Mounts []Mount
	// Allowance is added to a run's budget for its deadline label;
	// DefaultAllowance when zero.
	Allowance time.Duration
	// Poll is how often the spool is read; one second when zero.
	Poll time.Duration
	// OnCursor is told where a run's spool cursor stands, so the daemon
	// can persist it: once as the run host is created (cursor 0, which is
	// also what marks the run as one with a run host), then as it
	// advances -- at most every CursorEvery -- and once more when the
	// tail stops, whether or not it moved, so the time of that last call
	// is when this daemon stopped reading. It may be nil.
	OnCursor func(ctx context.Context, run string, cursor int64)
	// CursorEvery spaces OnCursor's calls; DefaultCursorEvery when zero.
	CursorEvery time.Duration
	// Detach reports whether a cancelled run should be left running for
	// the next daemon rather than destroyed: true when the cancellation
	// is the daemon stopping. Nil means never.
	Detach func(ctx context.Context) bool

	// tails is what Flush detaches.
	tails tails
}

var _ agent.Host = (*Host)(nil)

// SharesHostLoopback reports whether a run host's CLI reaches the
// daemon's own loopback listeners -- a capability grant's MCP proxy
// among them (agent.CarryCapabilityServers). Host says IsLocal, since it
// stages locally, so without this a run pod with a network namespace of
// its own would be handed URLs nothing in it answers. Only a driver that
// says so shares that loopback.
func (h *Host) SharesHostLoopback() bool {
	l, ok := h.Driver.(interface{ SharesHostLoopback() bool })
	return ok && l.SharesHostLoopback()
}

func (h *Host) logf(format string, args ...any) {
	if h.LocalHost.Logf != nil {
		h.LocalHost.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// MkdirTemp implements agent.Host: under StagingRoot, so the run host
// sees the home at the path the Framework wrote into its argv.
func (h *Host) MkdirTemp(_ context.Context, pattern string) (string, error) {
	if err := os.MkdirAll(h.StagingRoot, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(h.StagingRoot, pattern)
}

// CreateTemp implements agent.Host: a file the Framework names in its
// argv -- claude's --mcp-config -- goes under StagingRoot when no
// directory is asked for, for the same reason as MkdirTemp. The daemon's
// os.TempDir is not mounted into a run host, and a CLI there that cannot
// open its --mcp-config refuses to start at all.
func (h *Host) CreateTemp(ctx context.Context, dir, pattern string) (string, error) {
	if dir == "" {
		if err := os.MkdirAll(h.StagingRoot, 0o700); err != nil {
			return "", err
		}
		dir = h.StagingRoot
	}
	return h.LocalHost.CreateTemp(ctx, dir, pattern)
}

// Run implements agent.Host: in a new run host when ctx names a run,
// locally otherwise.
func (h *Host) Run(ctx context.Context, c agent.Command, tee io.Writer) (string, error) {
	r, ok := RunFrom(ctx)
	if !ok {
		return h.LocalHost.Run(ctx, c, tee)
	}
	info, err := h.create(ctx, r, c)
	if err != nil {
		return "", err
	}
	if h.OnCursor != nil {
		h.OnCursor(ctx, r.Run, 0)
	}
	return h.Attach(ctx, info, 0, tee)
}

func (h *Host) create(ctx context.Context, r Run, c agent.Command) (Info, error) {
	for _, root := range []string{h.SpoolRoot, h.StagingRoot} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return Info{}, err
		}
	}
	dir, err := os.MkdirTemp(h.SpoolRoot, "run-"+r.Run+"-")
	if err != nil {
		return Info{}, err
	}
	if err := WriteSpool(dir, Invocation{Path: c.Path, Args: c.Args, Dir: c.Dir}, c.Env, c.Stdin); err != nil {
		os.RemoveAll(dir)
		return Info{}, err
	}
	allowance := h.Allowance
	if allowance == 0 {
		allowance = DefaultAllowance
	}
	labels := Labels{
		Deployment: h.Deployment,
		Task:       r.Task,
		Run:        r.Run,
		Generation: h.Generation,
		Protocol:   Protocol,
		Spool:      dir,
	}
	if !r.Deadline.IsZero() {
		labels.Deadline = r.Deadline.Add(allowance)
	}
	mounts := append([]Mount{{Path: h.SpoolRoot}, {Path: h.StagingRoot}}, h.Mounts...)
	if c.Dir != "" {
		mounts = append(mounts, Mount{Path: c.Dir})
	}
	cmd := []string{h.Grain, "runhost", "exec", "-spool", dir}
	if !labels.Deadline.IsZero() {
		cmd = append(cmd, "-deadline", labels.Deadline.UTC().Format(time.RFC3339))
	}
	spec := Spec{
		Name:    podName(h.Deployment + "-runhost-" + filepath.Base(dir)),
		Image:   h.Image,
		Labels:  labels,
		Mounts:  mounts,
		User:    h.User,
		Command: cmd,
	}
	if err := spec.Validate(isDir); err != nil {
		os.RemoveAll(dir)
		return Info{}, err
	}
	h.logf("runhost: starting run host %s for run %s: %s", spec.Name, r.Run, agent.CommandLine(c))
	if err := h.Driver.Create(ctx, spec); err != nil {
		os.RemoveAll(dir)
		return Info{}, fmt.Errorf("runhost: creating run host for run %s: %w", r.Run, err)
	}
	return Info{Name: spec.Name, Labels: labels, Running: true}, nil
}

// podName makes name one both drivers accept: a docker container name,
// and a Kubernetes pod name (a lowercase DNS subdomain).
func podName(name string) string {
	b := []byte(strings.ToLower(name))
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
			b[i] = '-'
		}
	}
	return strings.Trim(string(b), "-.")
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// livenessEvery is how many spool polls go by between listings that
// check the run host still exists.
const livenessEvery = 10

// Attach tails info's spool from cursor until its CLI exits, writing what
// it reads past cursor to tee, and returns the CLI's whole stdout the way
// agent.Host.Run does. It is Run's second half, and the whole of what an
// adopting daemon does with a run host it did not start: a tail resumed
// from the persisted cursor writes nothing to tee twice and skips
// nothing.
//
// On return the run host is destroyed and its spool removed -- unless ctx
// was cancelled by the daemon stopping (Detach), when both are left for
// the next daemon and ErrDetached is returned.
func (h *Host) Attach(ctx context.Context, info Info, cursor int64, tee io.Writer) (string, error) {
	dir := info.Labels.Spool
	poll := h.Poll
	if poll == 0 {
		poll = time.Second
	}
	every := h.CursorEvery
	if every == 0 {
		every = DefaultCursorEvery
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	flush, untrack := h.tails.track(info.Labels.Run)
	defer untrack()
	persisted, lastPersist := cursor, time.Now()
	// A forced persist is reported even when the cursor has not moved:
	// the time it is written at is when this daemon stopped reading, and
	// the next daemon credits the restart gap from it (Decide). Skipping
	// it would credit a quiet run's whole silence before the stop -- a
	// long tool call -- as restart.
	persist := func(force bool) {
		if h.OnCursor == nil || (!force && (cursor == persisted || time.Since(lastPersist) < every)) {
			return
		}
		h.OnCursor(context.WithoutCancel(ctx), info.Labels.Run, cursor)
		persisted, lastPersist = cursor, time.Now()
	}
	drain := func() error {
		data, next, err := ReadStdout(dir, cursor)
		if err != nil {
			return err
		}
		if len(data) == 0 {
			return nil
		}
		if tee != nil {
			if _, err := tee.Write(data); err != nil {
				return err
			}
		}
		cursor = next
		persist(false)
		return nil
	}
	finish := func() {
		persist(true)
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := h.Driver.Destroy(cctx, info.Name); err != nil {
			h.logf("runhost: destroying run host %s: %v", info.Name, err)
		}
		os.RemoveAll(dir)
	}
	for polls := 0; ; polls++ {
		if err := drain(); err != nil {
			finish()
			return "", fmt.Errorf("runhost: reading the spool of %s: %w", info.Name, err)
		}
		exit, err := ReadExit(dir)
		if err != nil {
			finish()
			return "", err
		}
		if exit != nil {
			if err := drain(); err != nil {
				finish()
				return "", err
			}
			whole, _, err := ReadStdout(dir, 0)
			stderr := ReadStderrTail(dir, 16<<10)
			finish()
			if err != nil {
				return "", err
			}
			if exit.Code != 0 || exit.Error != "" {
				return string(whole), fmt.Errorf("%s (stderr: %s)", exitError(exit), stderr)
			}
			return string(whole), nil
		}
		if polls > 0 && polls%livenessEvery == 0 {
			if gone, err := h.gone(ctx, info.Name); err == nil && gone {
				// One more look: the exit record may have landed between
				// the read above and the listing.
				if exit, _ := ReadExit(dir); exit == nil {
					finish()
					return "", fmt.Errorf("runhost: run host %s is gone and left no exit record", info.Name)
				}
				continue
			}
		}
		select {
		case <-ctx.Done():
			if h.Detach != nil && h.Detach(ctx) {
				persist(true)
				h.logf("runhost: daemon stopping; leaving run host %s (run %s) running at cursor %d", info.Name, info.Labels.Run, cursor)
				return "", ErrDetached
			}
			// What the CLI wrote before it was stopped goes back with the
			// cause, as agent.Host.Run's does for a killed child: a run
			// cut off by its deadline or by its task closing may already
			// have pushed, and its framework reads that from here.
			whole, _, _ := ReadStdout(dir, 0)
			finish()
			return string(whole), context.Cause(ctx)
		case <-flush:
			// An upgrade is about to restart the daemon (Flush): what
			// the spool holds now is read, and the cursor persisted
			// past it, before letting go.
			if err := drain(); err != nil {
				h.logf("runhost: reading the spool of %s before detaching: %v", info.Name, err)
			}
			persist(true)
			h.logf("runhost: upgrade flushing; leaving run host %s (run %s) running at cursor %d", info.Name, info.Labels.Run, cursor)
			return "", ErrDetached
		case <-ticker.C:
		}
	}
}

func exitError(e *Exit) string {
	if e.Error != "" {
		return e.Error
	}
	return fmt.Sprintf("exit status %d", e.Code)
}

// gone reports whether the run host named name is no longer listed.
func (h *Host) gone(ctx context.Context, name string) (bool, error) {
	infos, err := h.Driver.List(ctx, h.Deployment)
	if err != nil {
		return false, err
	}
	for _, i := range infos {
		if i.Name == name {
			return false, nil
		}
	}
	return true, nil
}

// Release destroys the run host info names and removes its spool.
func (h *Host) Release(ctx context.Context, info Info, _ string) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := h.Driver.Destroy(cctx, info.Name); err != nil {
		h.logf("runhost: destroying run host %s: %v", info.Name, err)
	}
	if info.Labels.Spool != "" {
		os.RemoveAll(info.Labels.Spool)
	}
}
