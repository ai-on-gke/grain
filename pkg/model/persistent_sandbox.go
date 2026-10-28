package model

import (
	"fmt"
	"strings"
	"time"
)

// PersistentSandbox is a long-lived staging environment or service grain
// keeps running: a repository checked out at a ref inside a managed
// sandbox, one command started in that checkout, and an optional route to
// a port the command listens on.
//
// It is StandingSandbox's sibling rather than an extension of it. A
// standing sandbox boots an image somebody built elsewhere and serves it
// with no route in but an exec; a persistent sandbox is built from a
// repository grain already works in, on whatever backend the deployment
// runs its tasks on, and is reached from outside -- on a port of the grain
// host (HostPort) or under the grain UI (UISubpath, "/staging/<name>/").
//
// Defined in grain/config (tables/persistent_sandbox.json) and supervised
// by the orchestrator's PersistentSandboxSupervisor, which starts every
// enabled one when the daemon starts and restarts the command whenever it
// exits or its sandbox dies.
type PersistentSandbox struct {
	// Name identifies the sandbox, and is the <name> of its UI sub-path.
	Name string `json:"name"`
	// Repo is the repository checked out into the sandbox (owner/name).
	Repo string `json:"repo"`
	// Ref is the branch, tag or commit checked out. Empty is the
	// repository's default branch.
	Ref string `json:"ref,omitempty"`
	// AutoUpdate fetches Ref again on every restart, so a restart picks
	// up what has landed since. Off keeps the checkout the sandbox was
	// built with until it is rebuilt.
	AutoUpdate bool `json:"auto_update,omitempty"`
	// Command is the shell command run in the checkout. It is expected
	// not to return; when it does, it is restarted.
	Command string `json:"command"`
	// Dir is Command's working directory, relative to the checkout.
	Dir string `json:"dir,omitempty"`
	// Env is literal environment for Command.
	Env map[string]string `json:"env,omitempty"`
	// Capabilities are attached the way a task's are (model.Capability
	// names), for the credentials the service needs.
	Capabilities []string `json:"capabilities,omitempty"`
	// CPUs, MemoryMB and DiskGB size the sandbox (0 for the default).
	CPUs     int `json:"cpus,omitempty"`
	MemoryMB int `json:"memory_mb,omitempty"`
	DiskGB   int `json:"disk_gb,omitempty"`
	// NetworkAccess is how much of the internet the sandbox may reach.
	NetworkAccess NetworkAccess `json:"network_access,omitempty"`
	// SandboxPort is the port Command listens on inside the sandbox. It
	// is what HostPort and UISubpath forward to; zero exposes nothing.
	SandboxPort int `json:"sandbox_port,omitempty"`
	// HostPort, when set, is a port on the grain host whose connections
	// are forwarded to SandboxPort. By convention it is in 4280-4289
	// (ports.PersistentSandboxMin-Max, docs/ports.md); the staging grain
	// is on 4280.
	HostPort int `json:"host_port,omitempty"`
	// UISubpath, when set, mounts SandboxPort's HTTP service under the
	// grain UI at this path -- "/staging/<name>" by convention, and the
	// only shape accepted (PersistentSandboxSubpath).
	UISubpath string `json:"ui_subpath,omitempty"`
	// StripPrefix removes UISubpath from the request path before it is
	// forwarded, so a service that thinks it is served at "/" works
	// unmodified. Off forwards the path as the browser sent it.
	StripPrefix bool `json:"strip_prefix,omitempty"`
	// Enabled is whether grain keeps the sandbox running.
	Enabled bool `json:"enabled"`
	// Refresh is when grain restarts the command by itself to move it to
	// a newer commit of Ref (ParsePersistentRefresh): "manual" (empty, the
	// default) never does; "on_push" does once Ref's head has moved and
	// stayed put for a debounce; "interval:<duration>" checks on that
	// cadence and restarts only if the head moved; "after_green" waits for
	// grain CI's base-branch verdict on the new head and moves to exactly
	// that commit only once it passed. Every policy but manual needs
	// AutoUpdate -- a restart that does not fetch could not move anywhere
	// -- and validation rejects one without it rather than implying it.
	// after_green also needs Ref to name a branch, the one whose grain CI
	// verdicts it reads.
	Refresh string `json:"refresh,omitempty"`
	// RollbackOnCrash, with Refresh "after_green", puts the sandbox back
	// on the commit it ran before a refresh when the refreshed commit's
	// command exits PersistentRollbackCrashes times in a row without
	// staying up, and does not refresh onto that commit again.
	RollbackOnCrash bool `json:"rollback_on_crash,omitempty"`
}

// Refresh policies (PersistentSandbox.Refresh).
const (
	PersistentRefreshManual     = "manual"
	PersistentRefreshOnPush     = "on_push"
	PersistentRefreshInterval   = "interval"
	PersistentRefreshAfterGreen = "after_green"
)

// PersistentRollbackCrashes is how many rapid exits in a row of a freshly
// refreshed commit's command RollbackOnCrash takes as that commit being
// broken.
const PersistentRollbackCrashes = 3

// PersistentRefreshMinInterval is the shortest "interval:" accepted: the
// head is not read more often than this anyway.
const PersistentRefreshMinInterval = time.Minute

// ParsePersistentRefresh splits a Refresh value into its policy and, for
// "interval:<duration>", the interval. Empty is manual.
func ParsePersistentRefresh(refresh string) (policy string, every time.Duration, err error) {
	refresh = strings.TrimSpace(refresh)
	switch refresh {
	case "", PersistentRefreshManual:
		return PersistentRefreshManual, 0, nil
	case PersistentRefreshOnPush, PersistentRefreshAfterGreen:
		return refresh, 0, nil
	}
	if rest, ok := strings.CutPrefix(refresh, PersistentRefreshInterval+":"); ok {
		d, err := time.ParseDuration(strings.TrimSpace(rest))
		if err != nil {
			return "", 0, fmt.Errorf("refresh %q: %w", refresh, err)
		}
		if d < PersistentRefreshMinInterval {
			return "", 0, fmt.Errorf("refresh %q: the interval must be at least %s", refresh, PersistentRefreshMinInterval)
		}
		return PersistentRefreshInterval, d, nil
	}
	return "", 0, fmt.Errorf("refresh %q is not manual, on_push, interval:<duration> or after_green", refresh)
}

// PersistentSandboxSubpathPrefix is the part of the grain UI every
// persistent sandbox's sub-path lives under.
const PersistentSandboxSubpathPrefix = "/staging/"

// PersistentSandboxSubpath is the UI sub-path a persistent sandbox called
// name is served under.
func PersistentSandboxSubpath(name string) string {
	return PersistentSandboxSubpathPrefix + name
}

// ValidatePersistentSandbox checks one definition on its own; collisions
// between definitions are ValidatePersistentSandboxes'.
func ValidatePersistentSandbox(sb PersistentSandbox) error {
	if err := ValidateStandingSandboxName(sb.Name); err != nil {
		return fmt.Errorf("persistent sandbox: %w", err)
	}
	repo := strings.TrimSpace(sb.Repo)
	if owner, name, ok := strings.Cut(repo, "/"); !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return fmt.Errorf("persistent sandbox %q: repo %q is not owner/name", sb.Name, sb.Repo)
	}
	if strings.TrimSpace(sb.Command) == "" {
		return fmt.Errorf("persistent sandbox %q needs a command to run", sb.Name)
	}
	if strings.HasPrefix(sb.Ref, "-") || strings.ContainsAny(sb.Ref, " \t\n") {
		return fmt.Errorf("persistent sandbox %q: ref %q is not a git ref", sb.Name, sb.Ref)
	}
	if strings.HasPrefix(sb.Dir, "/") || strings.Contains("/"+sb.Dir+"/", "/../") {
		return fmt.Errorf("persistent sandbox %q: dir %q must stay inside the checkout", sb.Name, sb.Dir)
	}
	for _, port := range []struct {
		name  string
		value int
	}{{"sandbox_port", sb.SandboxPort}, {"host_port", sb.HostPort}} {
		if port.value < 0 || port.value > 65535 {
			return fmt.Errorf("persistent sandbox %q: %s %d is not a port", sb.Name, port.name, port.value)
		}
	}
	if (sb.HostPort != 0 || sb.UISubpath != "") && sb.SandboxPort == 0 {
		return fmt.Errorf("persistent sandbox %q exposes host_port or ui_subpath but names no sandbox_port to forward to", sb.Name)
	}
	if sb.UISubpath != "" && strings.TrimSuffix(sb.UISubpath, "/") != PersistentSandboxSubpath(sb.Name) {
		return fmt.Errorf("persistent sandbox %q: ui_subpath must be %q, not %q", sb.Name, PersistentSandboxSubpath(sb.Name), sb.UISubpath)
	}
	policy, _, err := ParsePersistentRefresh(sb.Refresh)
	if err != nil {
		return fmt.Errorf("persistent sandbox %q: %w", sb.Name, err)
	}
	if policy != PersistentRefreshManual && !sb.AutoUpdate {
		return fmt.Errorf("persistent sandbox %q: refresh %q needs auto_update, since a restart without it fetches nothing", sb.Name, sb.Refresh)
	}
	if policy == PersistentRefreshAfterGreen && strings.TrimSpace(sb.Ref) == "" {
		return fmt.Errorf("persistent sandbox %q: refresh after_green needs a ref naming the branch whose grain CI verdicts it follows", sb.Name)
	}
	if sb.RollbackOnCrash && policy != PersistentRefreshAfterGreen {
		return fmt.Errorf("persistent sandbox %q: rollback_on_crash needs refresh after_green, which is what knows a commit to roll back to", sb.Name)
	}
	return nil
}

// ValidatePersistentSandboxes checks every definition and the one thing
// that is only wrong across them: two sandboxes asking for one host port,
// or a host port grain itself is listening on (reserved).
func ValidatePersistentSandboxes(list []PersistentSandbox, reserved []int) error {
	ports := make(map[int]string, len(list))
	for _, port := range reserved {
		if port != 0 {
			ports[port] = "grain itself"
		}
	}
	names := make(map[string]bool, len(list))
	for _, sb := range list {
		if err := ValidatePersistentSandbox(sb); err != nil {
			return err
		}
		if names[sb.Name] {
			return fmt.Errorf("persistent sandbox %q is defined twice", sb.Name)
		}
		names[sb.Name] = true
		if sb.HostPort == 0 || !sb.Enabled {
			continue
		}
		if owner, taken := ports[sb.HostPort]; taken {
			return fmt.Errorf("persistent sandbox %q: host_port %d is already used by %s", sb.Name, sb.HostPort, owner)
		}
		ports[sb.HostPort] = fmt.Sprintf("persistent sandbox %q", sb.Name)
	}
	return nil
}

// PersistentSandboxEvent is one thing that happened to a persistent
// sandbox: its command exiting, its sandbox failing, an operator action.
type PersistentSandboxEvent struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
}

// PersistentSandboxStatus is what the UI is told about one persistent
// sandbox. It carries the definition's public half -- its env is left
// out, since an operator may well have put something there that should
// not be on a page.
type PersistentSandboxStatus struct {
	Name      string    `json:"name"`
	Repo      string    `json:"repo"`
	Ref       string    `json:"ref,omitempty"`
	Command   string    `json:"command"`
	State     string    `json:"state"`
	Enabled   bool      `json:"enabled"`
	Sandbox   string    `json:"sandbox"`
	StartedAt time.Time `json:"startedAt,omitzero"`
	Restarts  int       `json:"restarts"`
	LastError string    `json:"lastError,omitempty"`
	// PackageCache says whether the sandbox it holds now fetches its
	// packages through this deployment's package cache, or why not.
	// Empty before a sandbox has been built.
	PackageCache string                   `json:"packageCache,omitempty"`
	NextAttempt  time.Time                `json:"nextAttempt,omitzero"`
	SandboxPort  int                      `json:"sandboxPort,omitempty"`
	HostPort     int                      `json:"hostPort,omitempty"`
	UISubpath    string                   `json:"uiSubpath,omitempty"`
	CPUs         int                      `json:"cpus,omitempty"`
	MemoryMB     int                      `json:"memoryMB,omitempty"`
	DiskGB       int                      `json:"diskGB,omitempty"`
	Events       []PersistentSandboxEvent `json:"events,omitempty"`
	// Refresh is the definition's refresh policy, "manual" when unset.
	Refresh string `json:"refresh,omitempty"`
	// Commit is the commit the command is running, as checked out.
	Commit string `json:"commit,omitempty"`
	// LastRefresh is the most recent refresh the supervisor made by
	// itself, nil until there has been one.
	LastRefresh *PersistentSandboxRefresh `json:"lastRefresh,omitempty"`
}

// PersistentSandboxRefresh is one restart the refresh policy made: from
// which commit, to which, and why.
type PersistentSandboxRefresh struct {
	At     time.Time `json:"at"`
	From   string    `json:"from,omitempty"`
	To     string    `json:"to"`
	Reason string    `json:"reason"`
}
