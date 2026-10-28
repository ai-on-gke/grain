package daemon

// Where each agent CLI runs: on the machine (host) or in the daemon's own
// container (container) -- docs/design/cli-run-location.md -- or, behind
// grain hostd, detached on the machine as a container run would be
// (detached).
//
// For a daemon in the release container, host is grain hostd
// (hostexec.go) and container is a subprocess beside the daemon.
// detached is the image's own CLI with the deployment's login, started
// through hostd's container-equivalent allowlist entry ("claude-detached")
// so that it outlives a daemon upgrade (docs/non-disruptive-upgrade.md,
// "Which substrate carries which CLI"). A
// development daemon on the bare machine (`grain demo`, a local `grain
// daemon`) has no container of its own, so it offers only host: a child
// of the daemon.
//
// The setting is resolved per run, from the live config, so a change in
// Settings -> Models applies to the next run with no restart. Precedence
// is the one -claude-path and a connection's own CLI path already have:
//
//  1. Settings -> Models (model.Config.CLIRun, stored in grain_config);
//  2. the daemon's flag (-claude-run, -codex-run, -agy-run), which the
//     installer writes from GRAIN_CLAUDE_RUN and its neighbours;
//  3. the built-in default: container in a container (host for a CLI an
//     edition registered, RegisterHostdDefault), host on the bare
//     machine.

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/model"
)

// CLIRunResolution is where one framework's CLI runs, and why.
type CLIRunResolution struct {
	Framework string `json:"framework"`
	// Setting is what was asked for (host, container, detached) and Source where
	// that came from: "settings", "flag" or "default".
	Setting string `json:"setting"`
	Source  string `json:"source"`
	// Location is where it runs: host, container or detached.
	Location string `json:"location"`
	// Entry is the grain hostd allowlist entry a run through hostd
	// names: the CLI's own ("claude") for host, its container-equivalent
	// one ("claude-detached") for detached.
	Entry string `json:"entry,omitempty"`
	// HostPath is the binary the run starts: as the daemon's own child,
	// or through grain hostd, the path its allowlist entry names, as
	// hostd reports it.
	HostPath string `json:"hostPath,omitempty"`
	// Reason says why Location is what it is, for the log and the UI.
	Reason string `json:"reason"`
	// Err is set when the run cannot be made at all: a host run through
	// a hostd that cannot take it.
	Err string `json:"error,omitempty"`
	// Via is how the run starts its CLI: ViaDaemon (the daemon's own
	// child) or ViaHostd (hostexec.go).
	Via string `json:"via"`
	// Locations are the settings this deployment accepts for the
	// framework, which is what Settings offers.
	Locations []string `json:"locations"`
	// Warning is set when the setting could not be honoured as written:
	// a stored "host" where there is no host executor, or a stored
	// "container" on a daemon that has no container of its own.
	Warning string `json:"warning,omitempty"`
	// HostUnavailable is, for a daemon whose host executor is grain
	// hostd, why a host run of this CLI would fail now whatever the
	// setting: hostd does not answer, or its allowlist offers no entry
	// for the CLI. Empty when hostd offers it. Settings shows it beside
	// the select, and a run set to host fails with it (Err).
	HostUnavailable string `json:"hostUnavailable,omitempty"`
	// UpgradeInterrupts is set, for a daemon in a container, when a run
	// of this CLI as resolved would not survive a daemon upgrade: its CLI
	// is a child of the daemon, so the restart finishes the run as
	// interrupted and queues its task again. It says so, and why there is
	// no location that would carry it, for Settings -> Models and the
	// upgrade pane (docs/non-disruptive-upgrade.md, "Which substrate
	// carries which CLI"). Empty for a run grain hostd or a run host
	// carries across the restart.
	UpgradeInterrupts string `json:"upgradeInterrupts,omitempty"`
}

// Summary is r as one line: "claude: container (the daemon's own, …) -- …".
func (r CLIRunResolution) Summary() string {
	name := cliBinaryName(r.Framework)
	if r.Location == model.CLIRunContainer {
		return fmt.Sprintf("%s: container (the daemon's own, %s) -- %s", name, orNone(r.HostPath), r.Reason)
	}
	if r.Location == model.CLIRunDetached {
		return fmt.Sprintf("%s: detached (%s, through grain hostd, entry %q) -- %s", name, orNone(r.HostPath), r.Entry, r.Reason)
	}
	if r.Via == ViaHostd {
		return fmt.Sprintf("%s: host (%s, through grain hostd, entry %q) -- %s", name, orNone(r.HostPath), name, r.Reason)
	}
	return fmt.Sprintf("%s: host (%s) -- %s", name, orNone(r.HostPath), r.Reason)
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// cliBinaryName is the CLI a framework runs.
func cliBinaryName(framework string) string {
	switch framework {
	case model.AgentFrameworkAntigravity:
		return "agy"
	case model.AgentFrameworkCustom:
		return "custom"
	}
	return framework
}

// cliRunAuto is the run location releases before v2.3.0 also took. A
// -<cli>-run=auto an older unit still passes is accepted, said once at
// start (CheckCLIRunFlags), and read as unset.
const cliRunAuto = "auto"

// cliRunFlag is the daemon flag's value for framework, "" for none.
func (c Config) cliRunFlag(framework string) string {
	if v := c.rawCLIRunFlag(framework); v != cliRunAuto {
		return v
	}
	return ""
}

// defaultCLIRun is framework's built-in run location under c: container
// when the daemon itself runs in a container -- except where an edition
// registered another for a daemon whose host executor is grain hostd
// (RegisterHostdDefault) -- and host on the bare machine, the only
// location a development daemon has.
func (c Config) defaultCLIRun(framework string) string {
	exec := c.hostExecutor()
	if !exec.containerised() {
		return model.CLIRunHost
	}
	if v := hostdDefaults[framework]; v != "" && exec.Kind == HostExecutorHostd {
		return v
	}
	return model.CLIRunContainer
}

// hostdDefaults are the built-in run locations editions registered for a
// daemon in a container with grain hostd beside it.
var hostdDefaults = map[string]string{}

// RegisterHostdDefault makes location framework's built-in run location
// on a daemon in the release container whose host executor is grain
// hostd. It is for an edition's init: the Google build puts agy on the
// host, where the user's corp identity is. Anywhere else -- the bare
// machine, Kubernetes, a container with no hostd -- the default is
// unchanged.
func RegisterHostdDefault(framework, location string) {
	hostdDefaults[model.NormalizeAgentFrameworkName(framework)] = location
}

// detachedCaveats are what editions registered to be said beside
// "Set it to detached" (RegisterDetachedCaveat).
var detachedCaveats = map[string]string{}

// RegisterDetachedCaveat adds caveat to what Settings -> Models and the
// upgrade pane say after offering detached as the cure for framework's
// runs being lost on an upgrade. It is for an edition's init: the Google
// build's installer keeps claude in the container because a Cloudtop's
// exec policy may refuse it on the bare machine, which detached is.
func RegisterDetachedCaveat(framework, caveat string) {
	detachedCaveats[model.NormalizeAgentFrameworkName(framework)] = caveat
}

// cliRunLocations are the settings c accepts for framework.
func (c Config) cliRunLocations(framework string) []string {
	exec := c.hostExecutor()
	if !exec.containerised() {
		return []string{model.CLIRunHost}
	}
	if ok, _ := exec.runsOnHost(framework); !ok {
		return []string{model.CLIRunContainer}
	}
	if ok, _ := exec.runsDetached(framework); ok {
		return []string{model.CLIRunHost, model.CLIRunContainer, model.CLIRunDetached}
	}
	return []string{model.CLIRunHost, model.CLIRunContainer}
}

// detachedFrameworks are the CLIs that have a detached location: those
// whose runs a daemon can adopt from hostd's spool (agent.ResultParser,
// and a framework that leaves a detached attempt alone) and whose MCP
// server outlives the daemon with them. codex is not one yet
// (docs/non-disruptive-upgrade.md, "Which substrate carries which CLI"),
// though setup.sh writes its entry and agent/codex now reads an adopted
// run: its grain tools are an HTTP endpoint this daemon serves
// (mcp.StartCommandHTTP), which a detached codex would lose at the
// restart. noDetached says so.
var detachedFrameworks = map[string]bool{model.AgentFrameworkClaude: true}

// noDetached is why a CLI detachedFrameworks leaves out has no detached
// location.
var noDetached = map[string]string{
	model.AgentFrameworkCodex: "codex has no detached location: its grain tools are an MCP endpoint the daemon itself serves, " +
		"so a codex left running across an upgrade would lose them at the restart",
}

// detachedEntry is framework's container-equivalent allowlist entry:
// "claude-detached", beside the operator's own "claude", whose meaning
// (a CLI on the host's PATH, with the host user's login) it leaves alone.
func detachedEntry(framework string) string {
	return cliBinaryName(framework) + "-detached"
}

// runsDetached reports whether framework can be set to detached under e,
// and says why not.
func (e HostExecutor) runsDetached(framework string) (bool, string) {
	switch {
	case e.Kind != HostExecutorHostd:
		if e.Kind == HostExecutorLocal {
			return false, "detached runs the image's CLI through grain hostd, and this daemon runs on the bare machine (" + e.Reason + ")"
		}
		return false, "detached runs the image's CLI through grain hostd, and " + e.Reason
	case !detachedFrameworks[framework]:
		if why := noDetached[framework]; why != "" {
			return false, why
		}
		return false, cliBinaryName(framework) + " has no detached location: a daemon cannot yet adopt its runs across an upgrade"
	}
	return true, ""
}

// CLIRunSetting is framework's run location, and where it came from, by
// the precedence this file's header gives.
func (c Config) CLIRunSetting(framework string) (location, source string) {
	switch {
	case c.CLIRun[framework].Location != "":
		return c.CLIRun[framework].Location, "settings"
	case c.cliRunFlag(framework) != "":
		return c.cliRunFlag(framework), "flag"
	}
	return c.defaultCLIRun(framework), "default"
}

// cliHostPath is the binary a host run of framework starts, "" when
// there is none.
func (c Config) cliHostPath(framework string) string {
	path := ""
	switch framework {
	case model.AgentFrameworkClaude:
		path = c.ClaudePath
	case model.AgentFrameworkCodex:
		path = c.CodexPath
	case model.AgentFrameworkAntigravity:
		path = c.AgyPath
	}
	if path != "" || framework == model.AgentFrameworkCustom {
		// A custom command is each connection's own, never one binary.
		return path
	}
	if resolved, err := exec.LookPath(cliBinaryName(framework)); err == nil {
		return resolved
	}
	return ""
}

// ResolveCLIRun is where framework's CLI runs under c.
func (c Config) ResolveCLIRun(ctx context.Context, framework string) CLIRunResolution {
	framework = model.NormalizeAgentFrameworkName(framework)
	location, source := c.CLIRunSetting(framework)
	r := CLIRunResolution{Framework: framework, Setting: location, Source: source, HostPath: c.cliHostPath(framework)}
	from := map[string]string{"settings": "set in Settings -> Models", "flag": "set by -" + cliRunFlagName(framework),
		"default": "the default"}[source]
	r.Locations = c.cliRunLocations(framework)
	if exec := c.hostExecutor(); exec.containerised() {
		c.resolveContainerised(ctx, &r, exec, location, from)
		r.UpgradeInterrupts = c.upgradeInterrupts(r, exec)
		return r
	}
	// The bare machine: a development daemon, whose only location is a
	// child of its own. A container setting -- one stored by a release
	// that had a per-run docker launcher, or a flag an older unit passes
	// -- runs here, with a warning, rather than failing every run, and so
	// does a detached one, which needs a container's hostd.
	r.Location, r.Via = model.CLIRunHost, ViaDaemon
	r.Reason = "host " + from
	if location == model.CLIRunContainer || location == model.CLIRunDetached {
		why := "a daemon on the bare machine has no container of its own to run it in"
		r.Reason = location + " " + from + ", but " + why
		r.Warning = "set to " + location + ", but " + why + ". It runs on the host until this is set to host or default"
		warnStoredContainerOnce(framework, from)
	}
	return r
}

// resolveContainerised resolves location for a daemon that itself runs
// in a container (hostexec.go): container is a subprocess in that
// container, and host is grain hostd when there is one. A host setting where there is none -- a stored one
// from an earlier install, since the API and the flags refuse it -- runs
// in the container, with a warning logged once and shown in Settings,
// rather than failing every run.
func (c Config) resolveContainerised(ctx context.Context, r *CLIRunResolution, exec HostExecutor, location, from string) {
	r.Location, r.Via = model.CLIRunContainer, ViaDaemon
	// offered is the binary hostd's allowlist entry runs on the host:
	// a host run's HostPath, since the one this container resolves is
	// not what runs.
	offered := ""
	if exec.Kind == HostExecutorHostd {
		if ok, why := exec.runsOnHost(r.Framework); !ok {
			r.HostUnavailable = why
		} else {
			offered, r.HostUnavailable = hostdOffersCLI(ctx, exec.Socket, cliBinaryName(r.Framework))
		}
	}
	switch location {
	case model.CLIRunDetached:
		c.resolveDetached(ctx, r, exec, from)
	case model.CLIRunHost:
		ok, why := exec.runsOnHost(r.Framework)
		if ok {
			r.Location, r.Via, r.HostPath, r.Entry = model.CLIRunHost, ViaHostd, offered, cliBinaryName(r.Framework)
			r.Reason = "host " + from + ": through grain hostd at " + exec.Socket + ", as its allowlist entry \"" + cliBinaryName(r.Framework) + "\""
			// Allowed only for an entry hostd offers: otherwise the run
			// fails, saying which of the two is missing.
			r.Err = r.HostUnavailable
			return
		}
		r.Reason = "host " + from + ", but " + why
		r.Warning = "set to host, but " + why + ". It runs in the container until this is set to container or default"
		warnStoredHostOnce(r.Framework, from, why)
	default:
		r.Reason = "container " + from + ": a subprocess in the daemon's own container"
	}
}

// resolveDetached resolves a detached setting: the CLI's
// container-equivalent entry on hostd, when hostd answers, is compatible
// and offers it. Otherwise the run is a container run, as it was before
// detached existed, and Settings says why -- a run is never failed for
// it, since container is exactly what detached stands in for.
func (c Config) resolveDetached(ctx context.Context, r *CLIRunResolution, exec HostExecutor, from string) {
	why := ""
	path := ""
	entry := detachedEntry(r.Framework)
	if ok, no := exec.runsDetached(r.Framework); !ok {
		why = no
	} else {
		path, why = hostdOffersCLI(ctx, exec.Socket, entry)
	}
	if why != "" {
		r.Reason = "detached " + from + ", but " + why
		r.Warning = "set to detached, but " + why + ". It runs in the container, and an upgrade interrupts it, until grain hostd offers the \"" +
			entry + "\" entry (re-run the installer to add it) or this is set to container"
		warnDetachedOnce(r.Framework, from, why)
		return
	}
	r.Location, r.Via, r.HostPath, r.Entry = model.CLIRunDetached, ViaHostd, path, entry
	r.Reason = "detached " + from + ": the image's CLI with the deployment's login, through grain hostd at " + exec.Socket +
		" as its allowlist entry \"" + entry + "\", so it outlives a daemon upgrade"
}

// upgradeInterrupts is CLIRunResolution.UpgradeInterrupts for r under
// exec: "" for a run the next daemon adopts -- detached in grain hostd,
// agy on the host through hostd (which starts it detached), claude in a
// run host -- and otherwise the sentence saying the run is lost, with
// what would carry it, or why nothing can yet.
func (c Config) upgradeInterrupts(r CLIRunResolution, exec HostExecutor) string {
	switch {
	case r.Location == model.CLIRunDetached:
		return ""
	case r.Via == ViaHostd && r.Framework == model.AgentFrameworkAntigravity:
		return ""
	case r.Framework == model.AgentFrameworkClaude && c.RunHost != nil:
		return ""
	}
	where := "in the daemon's own container"
	if r.Via == ViaHostd {
		where = "on the host with its MCP server in the daemon"
	}
	out := fmt.Sprintf("A daemon upgrade interrupts %s runs: %s runs %s, so the restart finishes each run in flight as interrupted and queues its task again.",
		cliBinaryName(r.Framework), cliBinaryName(r.Framework), where)
	if ok, why := exec.runsDetached(r.Framework); ok {
		out += " Set it to detached to carry its runs across an upgrade."
		if caveat := detachedCaveats[r.Framework]; caveat != "" {
			out += " " + caveat
		}
	} else {
		out += " Nothing carries them yet: " + why + "."
	}
	return out
}

// detachedWarned is the (framework, reason) pairs this daemon has already
// logged a detached setting falling back to the container for: once each,
// not once a run, and again when the reason changes.
var detachedWarned sync.Map

func warnDetachedOnce(framework, from, why string) {
	if _, seen := detachedWarned.LoadOrStore(framework+"\x00"+why, true); seen {
		return
	}
	log.Printf("grain daemon: agent CLI %s is set to detached (%s), but %s; running it in the daemon's own container instead, "+
		"where a daemon upgrade interrupts it", cliBinaryName(framework), from, why)
}

func cliRunFlagName(framework string) string {
	return cliBinaryName(framework) + "-run"
}

// upgradeInterruptNotes is orchestrator.Config.UpgradeInterrupts: by
// framework, the UpgradeInterrupts of every CLI whose run location under
// c, with the run locations store holds now, loses its runs on a
// restart -- the same notes the upgrade pane shows.
func (c Config) upgradeInterruptNotes(ctx context.Context, store *model.Store) map[string]string {
	if mc, err := store.GetConfig(ctx); err != nil {
		log.Printf("grain daemon: reading the run locations for the upgrade check: %v", err)
	} else if mc != nil {
		c.CLIRun = mc.CLIRun
	}
	out := map[string]string{}
	for _, r := range c.ResolveCLIRuns(ctx) {
		if r.UpgradeInterrupts != "" {
			out[r.Framework] = r.UpgradeInterrupts
		}
	}
	return out
}

// ResolveCLIRuns is every framework's resolution, in Settings order.
func (c Config) ResolveCLIRuns(ctx context.Context) []CLIRunResolution {
	var out []CLIRunResolution
	for _, fw := range model.CLIRunFrameworks {
		out = append(out, c.ResolveCLIRun(ctx, fw))
	}
	return out
}

// CheckCLIRunFlags refuses a -claude-run, -codex-run or -agy-run that is
// not a location, and a host one where this deployment has no host
// executor. A -<cli>-run=auto from an older unit is accepted and logged:
// it is read as unset.
func CheckCLIRunFlags(cfg Config) error {
	exec := cfg.hostExecutor()
	for _, fw := range []string{model.AgentFrameworkClaude, model.AgentFrameworkCodex, model.AgentFrameworkAntigravity} {
		if cfg.rawCLIRunFlag(fw) == cliRunAuto {
			log.Printf("grain daemon: -%s=auto is no longer a run location and is ignored, leaving %s to the default "+
				"(re-run the installer, or drop GRAIN_%s_RUN=auto, to clear this)", cliRunFlagName(fw), cliBinaryName(fw), strings.ToUpper(cliBinaryName(fw)))
			continue
		}
		v := cfg.cliRunFlag(fw)
		if !model.ValidCLIRunLocation(v) {
			return fmt.Errorf("-%s=%q: want host, container or detached", cliRunFlagName(fw), v)
		}
		if v == model.CLIRunDetached {
			if ok, why := exec.runsDetached(fw); !ok {
				return fmt.Errorf("-%s=detached (the installer writes it from GRAIN_%s_RUN): %s. Drop it, or set it to container",
					cliRunFlagName(fw), strings.ToUpper(cliBinaryName(fw)), why)
			}
		}
		if v == model.CLIRunHost && !exec.HostAvailable() {
			return fmt.Errorf("-%s=host (the installer writes it from GRAIN_%s_RUN): %s. Drop it, or set it to container",
				cliRunFlagName(fw), strings.ToUpper(cliBinaryName(fw)), exec.Reason)
		}
	}
	return nil
}

// rawCLIRunFlag is the daemon flag's value for framework as given, auto
// included.
func (c Config) rawCLIRunFlag(framework string) string {
	switch framework {
	case model.AgentFrameworkClaude:
		return c.ClaudeRun
	case model.AgentFrameworkCodex:
		return c.CodexRun
	case model.AgentFrameworkAntigravity:
		return c.AgyRun
	}
	return ""
}

// logCLIRuns writes where each CLI will run to the daemon's log.
func logCLIRuns(ctx context.Context, cfg Config) {
	exec := cfg.hostExecutor()
	log.Printf("grain daemon: host executor: %s -- %s", exec.Kind, exec.Reason)
	for _, r := range cfg.ResolveCLIRuns(ctx) {
		line := "grain daemon: agent CLI " + r.Summary()
		if r.Err != "" {
			line += " (cannot run: " + r.Err + ")"
		}
		log.Print(line)
	}
}

// storedContainerWarned is the CLIs whose container setting this
// development daemon has already logged as run on the host: once each,
// not once a run.
var storedContainerWarned sync.Map

// warnStoredContainerOnce logs, once per framework per process, that a
// container setting is being run on the host instead.
func warnStoredContainerOnce(framework, from string) {
	if _, seen := storedContainerWarned.LoadOrStore(framework, true); seen {
		return
	}
	log.Printf("grain daemon: agent CLI %s is set to container (%s), but a daemon on the bare machine has no container "+
		"of its own; running it on the host instead. Choose host or default in Settings -> Models to clear this", cliBinaryName(framework), from)
}
