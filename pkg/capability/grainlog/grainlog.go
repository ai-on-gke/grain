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

// Package grainlog is a GRANT model.CapabilityProvider: a task holding
// it gets one read-only tool, read_grain_log, returning a bounded,
// host-side-redacted slice of this deployment's own systemd journal --
// the same source the UI's System pane serves (pkg/systemlog).
//
// It exists for a failure that actually happened. grain files a check
// task after every self-upgrade (orchestrator.reconcileUpgradeCheck),
// and the first one this deployment ran was asked whether anything in
// the daemon's log was new since the upgrade. The run could not read the
// log -- nothing it held opened it -- so it answered out of the state
// repository dump instead, which is evidence *about* the log rather than
// the log, and reported clean. A panic a reconciler recovers from, or an
// error repeating every tick without stopping the deployment, leaves no
// trace in that dump. A check that reports clean because it could not
// look is worse than one that reports it could not look.
//
// # Why this is its own capability and not part of self-debug
//
// pkg/capability/selfdebug grants "read the source this host runs". This
// grants "read everything anything on this host has ever logged", which
// is a categorically larger thing: the journal carries whatever the
// daemon and every agent wrote to it, including material quoted into
// error messages, other tasks' text, and credentials nobody meant to
// print. Folding it into self-debug would have widened every existing
// self-debug task silently. So it is a separate name a human attaches
// separately, and a task can hold either without the other. The upgrade
// check holds both; other self-debug tasks keep only source.
//
// # What a run granted this can do with what it reads
//
// It leaves the host, twice over: into a model's context, and into a
// sandbox whose outbound network is not restricted -- verified, not
// assumed. Network isolation is not the containment story here, and
// nothing in this package pretends otherwise. What contains this is the
// three things below, in this order of importance:
//
//   - Redaction on the host, in this process, before the answer is
//     built (Redactor). The sandbox is the wrong place for it: material
//     redacted there has already arrived.
//   - A fixed surface. The units readable are an allowlist (Sources),
//     every bound is a typed parameter, and no caller-supplied string
//     ever reaches journalctl's own argument surface
//     (systemlog.Query.args). There is no shell anywhere near this.
//   - An audit line per call, naming the task, the parameters and how
//     much came back, so what each run read is recoverable afterwards
//     from the very journal it read (Config.Log).
//
// # Truncation is loud
//
// Every cap here reports itself. A cap that silently drops the rest
// reproduces exactly the failure this capability exists to fix: a run
// that saw the first two hundred lines of a window holding thousands,
// and could not tell that from a window holding two hundred, would
// report clean for the same reason the first check did. So a truncated
// answer says so, at the top, in a line an agent cannot read as
// ordinary output -- and an empty answer says outright that it is an
// answer.
//
// How it reaches a run is `grain mcpserver -grant grain-log`, passed by
// a Framework only for a task holding this grant (agent.RunConfig.Grants,
// agent.GrantArgs), alongside -grain-secrets-dir, which is where that
// process reads the values to redact.
package grainlog

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/systemlog"
)

// CapabilityName is the grant name ui.OfferedCapabilities lists as
// "grain log".
const CapabilityName = "grain-log"

// Provider is the grain-log capability. Every method but Spec is
// model.BaseCapability's default: there is no credential to withhold, no
// lease to mint, nothing to place and nothing to revoke -- what it
// grants is one tool.
type Provider struct {
	model.BaseCapability
}

// ProviderConfig holds configuration for grainlog Provider.
type ProviderConfig struct{}

// New returns a grain-log Provider, ready to register.
func New() *Provider { return &Provider{} }

// NewWithConfig returns a grain-log Provider with config.
func NewWithConfig(_ ProviderConfig) *Provider { return &Provider{} }

func (p *Provider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:      CapabilityName,
		Label:     "Grain log",
		Source:    model.GrantByLabel,
		Provision: model.ProvisionGrant,
		Description: "Let this task read a bounded slice of grain's own systemd journal on this " +
			"host -- the daemon's log and the kernel's -- with known credentials redacted before " +
			"it leaves. Separate from self-debug on purpose: the journal carries whatever anything " +
			"here has ever logged, which is far more than grain's source code.",
	}
}

// maxLinesCap is the most lines one call can ever return, whatever it
// asked for. It bounds a tool result rather than a file: the answer goes
// straight into a model's context, and a run that spends its whole
// context on one journal read has stopped being able to do the thing it
// read the journal for.
const maxLinesCap = 1000

// defaultMaxLines is what a call that names no cap gets.
const defaultMaxLines = 200

// windowCap is how many entries one call reads out of journalctl before
// filtering, and is the bound that makes `contains` safe to apply here
// rather than in journalctl. It is deliberately several times
// maxLinesCap: a substring match over a busy window should have a real
// window to match in. A window with more entries than this is answered
// with its most recent windowCap and a truncation marker saying so.
const windowCap = 5000

// maxWindow is the longest span between since and until one call may
// ask for. A week is far more than the question this exists for needs
// ("anything since this upgrade", minutes to hours) and still covers an
// upgrade whose restart left a deployment down over a weekend.
const maxWindow = 7 * 24 * time.Hour

// Source is one journal this capability may read, by the short name a
// run names it with. The set of them is an allowlist and the whole of
// what is reachable: there is no parameter through which a run can name
// a unit that is not here.
type Source struct {
	// Name is what a run passes as `source`.
	Name string
	// What is the one-line description of it in the tool's own schema.
	What string
	// Read performs the read. A field rather than a systemlog value so a
	// test can stand in for journalctl without a systemd on the runner.
	Read func(ctx context.Context, q systemlog.Query) ([]string, error)
}

// DefaultSources is the allowlist a real deployment serves: the daemon's
// own unit, and the kernel log underneath it.
//
// grain-daemon.service is scripts/setup.sh's own unit name, the same
// string pkg/cli/daemon.go wires the UI's System pane to. dmesg is
// here because the failures the daemon's own journal cannot describe are
// the ones where the process that would have written them is the one
// that stopped -- an agent CLI killed by the OOM killer during the very
// run being checked.
//
// grain-config-sync.service, which the System pane also serves, is
// deliberately absent: it is the rollout loop, not this deployment's
// behaviour, and adding a unit here is a decision about what leaves the
// host rather than a line of plumbing. It is one entry when somebody
// decides that.
func DefaultSources() []Source {
	daemon := systemlog.Journalctl{Unit: "grain-daemon.service"}
	return []Source{
		{
			Name: "daemon",
			What: "the grain daemon's own journal (grain-daemon.service) -- what this deployment logged",
			Read: daemon.Read,
		},
		{
			Name: "dmesg",
			What: "the kernel log of the machine underneath it -- OOM kills, disk and network errors",
			Read: systemlog.Dmesg{}.Read,
		},
	}
}

// Config is what cmd/grain hands Tools: who is reading, what may be
// read, and what has to be cut out of it first.
type Config struct {
	// TaskID is the task whose run this server serves, for the audit
	// line. "" still serves the tool -- a `grain mcpserver` run by hand,
	// a test -- and the audit line says the task is unknown rather than
	// pretending to one.
	TaskID string
	// Redactor is applied to every line before it is returned. Nil is not
	// "nothing to redact": it is "this process could not read what it
	// would have redacted", and every call is refused, because serving a
	// journal under a weaker guarantee than the one this capability was
	// granted under is the one failure mode nobody could see from the
	// outside. A host with an empty secret store passes a Redactor over
	// no known values instead (NewRedactor(nil)), which is a different
	// thing and says so.
	Redactor *Redactor
	// Sources is the allowlist; nil means DefaultSources.
	Sources []Source
	// Log writes the audit line. Nil means log.Printf, which in a forked
	// mcpserver is stderr, which the daemon's own subprocess plumbing
	// carries into the very journal being read.
	Log func(format string, args ...any)
	// Now is the clock the window is bounded against, overridable for a
	// test. Nil means time.Now.
	Now func() time.Time
}

// noRedactor is what every call is answered with when the process could
// not build the redaction list. A sentence about this deployment rather
// than an argument error, in the shape selfdebug.noSourceDir already
// takes: nothing the run does differently will make this call work.
const noRedactor = "This run cannot read the journal: the process serving this tool could not read " +
	"the credentials it would have to redact out of what it returned (no -grain-secrets-dir, or a " +
	"secrets file this host cannot open), and returning an unredacted journal is not something it " +
	"will do instead. Nothing you can do from here will change it; say so plainly if it matters to " +
	"what you were asked to do."

// Tools returns the tools a grain-log grant adds to a run: read_grain_log
// and nothing else. It always returns the tool, even where nothing can
// be served, so that a run's roster is a property of the grant it holds
// rather than of one deployment's configuration -- the rule
// selfdebug.SourceTools already follows, and for the same reason: an
// absent tool reads to an agent as a capability it was never given,
// while a refusing one says what is actually wrong.
func Tools(cfg Config) []mcp.Tool {
	if cfg.Sources == nil {
		cfg.Sources = DefaultSources()
	}
	if cfg.Log == nil {
		cfg.Log = log.Printf
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return []mcp.Tool{readLogTool(cfg)}
}

func sourceNames(sources []Source) []string {
	names := make([]string, 0, len(sources))
	for _, s := range sources {
		names = append(names, s.Name)
	}
	return names
}

func inputSchema(sources []Source) map[string]any {
	var what strings.Builder
	for _, s := range sources {
		fmt.Fprintf(&what, "%q is %s. ", s.Name, s.What)
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"source": map[string]any{
				"type": "string",
				"enum": sourceNames(sources),
				"description": "which log to read. " + strings.TrimSpace(what.String()) +
					" These are the only logs readable here.",
			},
			"since": map[string]any{
				"type": "string",
				"description": "start of the window, as an RFC3339 timestamp " +
					"(\"2026-09-07T10:56:13Z\"). Omit for \"as far back as max_lines reaches\". " +
					"The window may span at most " + maxWindow.String() + ".",
			},
			"until": map[string]any{
				"type":        "string",
				"description": "end of the window, as an RFC3339 timestamp. Omit for now.",
			},
			"min_priority": map[string]any{
				"type": "string",
				"enum": systemlog.Priorities,
				"description": "least severe priority to return: \"err\" gives errors and " +
					"everything worse, \"warning\" adds warnings, and so on. Omit for every priority. " +
					"Narrowing this is the first thing to reach for when a window is too big to read.",
			},
			"contains": map[string]any{
				"type": "string",
				"description": "return only entries containing this exact text, matched " +
					"case-insensitively as a plain substring -- not a regular expression, and not a " +
					"journalctl argument. Omit for every entry in the window.",
			},
			"max_lines": map[string]any{
				"type": "integer",
				"description": fmt.Sprintf(
					"most entries to return, most recent last (default %d, maximum %d). If more "+
						"matched than this, the answer says so at the top rather than quietly "+
						"stopping.", defaultMaxLines, maxLinesCap),
			},
		},
		"required": []string{"source"},
	}
}

func readLogTool(cfg Config) mcp.Tool {
	return mcp.Tool{
		Name: "read_grain_log",
		Description: "Read a bounded slice of grain's own systemd journal on the host this " +
			"deployment runs on -- the daemon's log, or the kernel's. Read-only, and needs no " +
			"permission. Credentials this host holds, and anything shaped like a token, are " +
			"redacted on the host before the answer is returned; every call is logged. Use it to " +
			"answer a question about what this deployment actually did -- a panic, an error " +
			"repeating every tick, a reconciler failing quietly -- rather than inferring it from " +
			"somewhere the log is not.",
		InputSchema: inputSchema(cfg.Sources),
		Handler:     func(ctx context.Context, args map[string]any) mcp.Result { return readLog(ctx, cfg, args) },
	}
}

// request is one call's parameters, after parsing and bounding.
type request struct {
	source   Source
	query    systemlog.Query
	contains string
	maxLines int
}

func readLog(ctx context.Context, cfg Config, args map[string]any) mcp.Result {
	req, err := parseRequest(cfg, args)
	if err != nil {
		return mcp.Result{Text: err.Error(), IsError: true}
	}
	// After parsing, so that the audit line records what a refused call
	// asked for too: "this run tried to read the journal and was told
	// no" is as much a thing an operator wants in the trail as a read
	// that happened.
	if cfg.Redactor == nil {
		audit(cfg, req, 0, 0, "refused: no redactor")
		return mcp.Result{Text: noRedactor, IsError: true}
	}

	raw, err := req.source.Read(ctx, req.query)
	if err != nil {
		audit(cfg, req, 0, 0, "failed: "+err.Error())
		return mcp.Result{
			Text:    fmt.Sprintf("Reading the %s log failed: %v", req.source.Name, err),
			IsError: true,
		}
	}
	windowFull := len(raw) >= windowCap

	matched := raw
	if req.contains != "" {
		matched = filter(raw, req.contains)
	}
	shown := matched
	dropped := 0
	if len(shown) > req.maxLines {
		dropped = len(shown) - req.maxLines
		shown = shown[dropped:]
	}
	shown, redacted := cfg.Redactor.Redact(shown)

	audit(cfg, req, len(shown), redacted, "ok")
	return mcp.Result{Text: answer(req, shown, len(matched), dropped, redacted, windowFull)}
}

// filter is the `contains` match: a plain, case-insensitive substring
// test, applied here rather than passed to journalctl. journalctl's own
// -g takes a regular expression, and handing an agent's string to a
// regex engine on the host is both an argument surface and a way to
// spend a host's CPU; a substring is the whole of what the question
// needs.
func filter(lines []string, contains string) []string {
	needle := strings.ToLower(contains)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.Contains(strings.ToLower(line), needle) {
			out = append(out, line)
		}
	}
	return out
}

// answer assembles what the run reads: a header saying exactly what was
// read, every truncation said out loud before the lines themselves, and
// -- when nothing matched -- a sentence saying that this is an answer
// rather than a failure.
func answer(req request, shown []string, matched, dropped, redacted int, windowFull bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s log, %s", req.source.Name, describeWindow(req.query))
	if req.query.Priority != "" {
		fmt.Fprintf(&b, ", priority %s and above", req.query.Priority)
	}
	if req.contains != "" {
		fmt.Fprintf(&b, ", containing %q", req.contains)
	}
	b.WriteString(".\n")

	if windowFull {
		fmt.Fprintf(&b, "\n!! TRUNCATED: this window holds at least %d entries and only its most "+
			"recent %d were read, so anything earlier in it was NOT examined and is NOT reported "+
			"below. Narrow the window with since/until, or raise min_priority, and read it again "+
			"before concluding anything about the part you have not seen.\n", windowCap, windowCap)
	}
	if dropped > 0 {
		fmt.Fprintf(&b, "\n!! TRUNCATED: %d entries matched and the %d most recent are below -- "+
			"the earlier %d are NOT shown. Raise max_lines (up to %d), or narrow the window, if "+
			"the beginning of this is what matters.\n", matched, len(shown), dropped, maxLinesCap)
	}

	if len(shown) == 0 {
		b.WriteString("\nNo entries matched. This is an answer, not a failure: the read " +
			"succeeded and this log held nothing matching in that window")
		if windowFull {
			b.WriteString(" -- within the part of it that was read, per the truncation above")
		}
		b.WriteString(".\n")
		return b.String()
	}

	fmt.Fprintf(&b, "\n%d entries, oldest first", len(shown))
	if redacted > 0 {
		fmt.Fprintf(&b, "; %d credential values were redacted on the host before this was returned, "+
			"each marked in place", redacted)
	}
	b.WriteString(":\n\n")
	for _, line := range shown {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func describeWindow(q systemlog.Query) string {
	switch {
	case !q.Since.IsZero() && !q.Until.IsZero():
		return fmt.Sprintf("%s to %s", stamp(q.Since), stamp(q.Until))
	case !q.Since.IsZero():
		return fmt.Sprintf("since %s", stamp(q.Since))
	case !q.Until.IsZero():
		return fmt.Sprintf("up to %s", stamp(q.Until))
	}
	return "most recent entries"
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// audit writes the one line per call this capability owes an operator:
// which task read what, and how much came back. It goes to this
// process's stderr, which the daemon collects into the daemon's own
// journal -- so the trail of what each run read lands in the same log it
// was reading, where the next run to ask can find it.
func audit(cfg Config, req request, lines, redacted int, outcome string) {
	task := cfg.TaskID
	if task == "" {
		task = "(no task)"
	}
	cfg.Log("grain mcpserver: read_grain_log: task %s read %s (%s, priority %q, contains %q, "+
		"max_lines %d): %s, %d lines, %d redacted",
		task, req.source.Name, describeWindow(req.query), req.query.Priority, req.contains,
		req.maxLines, outcome, lines, redacted)
}

func parseRequest(cfg Config, args map[string]any) (request, error) {
	var req request

	name, _ := args["source"].(string)
	found := false
	for _, s := range cfg.Sources {
		if s.Name == name {
			req.source, found = s, true
			break
		}
	}
	if !found {
		return req, fmt.Errorf("source %q is not a log this run can read; the ones it can are: %s",
			name, strings.Join(sourceNames(cfg.Sources), ", "))
	}

	since, err := parseTime(args, "since")
	if err != nil {
		return req, err
	}
	until, err := parseTime(args, "until")
	if err != nil {
		return req, err
	}
	if !since.IsZero() && !until.IsZero() && !until.After(since) {
		return req, fmt.Errorf("until (%s) is not after since (%s), so the window is empty",
			stamp(until), stamp(since))
	}
	// The span is bounded here rather than left to the line caps,
	// because the two bound different things: max_lines caps what comes
	// back, and this caps what journald is asked to walk on a host that
	// has a deployment to run at the same time.
	end := until
	if end.IsZero() {
		end = cfg.Now()
	}
	if !since.IsZero() && end.Sub(since) > maxWindow {
		return req, fmt.Errorf("that window is %s wide, and the most one call may ask for is %s; "+
			"read it in pieces, narrowing since/until", end.Sub(since).Round(time.Minute), maxWindow)
	}

	if priority, ok := args["min_priority"].(string); ok && priority != "" {
		if !containsString(systemlog.Priorities, priority) {
			return req, fmt.Errorf("min_priority %q is not a journald priority; the ones there are: %s",
				priority, strings.Join(systemlog.Priorities, ", "))
		}
		req.query.Priority = priority
	}
	req.contains, _ = args["contains"].(string)

	req.maxLines = defaultMaxLines
	if raw, ok := args["max_lines"]; ok {
		n, ok := asInt(raw)
		if !ok || n <= 0 {
			return req, fmt.Errorf("max_lines must be a positive whole number of entries")
		}
		req.maxLines = min(n, maxLinesCap)
	}

	req.query.Since, req.query.Until = since, until
	// Always windowCap, never the run's own max_lines: journalctl's
	// --lines returns the *last* N of the window, so asking it for
	// max_lines and then filtering here would drop matches that are
	// older than the last max_lines entries and report the window clean.
	// The filtering is this package's, so the window it filters has to be
	// the window, up to a bound this package states out loud when it hits
	// it.
	req.query.Lines = windowCap
	return req, nil
}

func parseTime(args map[string]any, key string) (time.Time, error) {
	raw, ok := args[key].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return time.Time{}, nil
	}
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, fmt.Errorf("%s %q is not an RFC3339 timestamp "+
			"(\"2026-09-07T10:56:13Z\"); this tool takes no other kind of time -- no \"1 hour ago\", "+
			"no journalctl expressions", key, raw)
	}
	return at, nil
}

// asInt accepts both shapes a JSON number arrives in: float64, which is
// what encoding/json gives for every number, and int, which is what a
// caller building the arguments in Go hands over (pkg/mcp's own tests,
// and this package's).
func asInt(raw any) (int, bool) {
	switch v := raw.(type) {
	case float64:
		if v != float64(int(v)) {
			return 0, false
		}
		return int(v), true
	case int:
		return v, true
	}
	return 0, false
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
