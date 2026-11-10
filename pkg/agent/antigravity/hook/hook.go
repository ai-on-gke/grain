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

// Package hook is the pre-tool hook grain puts in front of every tool call
// an antigravity (agy) run makes: agy forks `<grain binary> agy-tool-hook`
// with the call on stdin, and DecisionFor's reply decides whether it runs.
//
// It is a leaf -- it imports nothing of grain's -- so that every binary a
// run may name as its hook can carry it cheaply: cmd/grain, and
// grain-agentd, which runs the agent CLIs on a machine that is not the
// daemon's and must stay small. pkg/agent/antigravity writes the hook into
// a run's hooks.json and re-exports this package under its old names
// (antigravity.HookDecisionFor, antigravity.RegisterHostToolGrant, ...).
//
// What the hook admits is grain's own published MCP tools (PublishedTools,
// a list kept here so the hook need not link pkg/mcp; antigravity's tests
// hold it to the roster pkg/mcp actually builds), the capability MCP
// servers' tools a run's arguments name, and the host tools of a
// registered HostToolGrant the run holds. A build adds a HostToolGrant
// with RegisterHostToolGrant from init(); the binary answering the hook
// must link the same registrations as the daemon that wrote its arguments.
package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Name is what this run's hook is called in hooks.json. agy merges
// hooks from every source it finds by name, so a name of grain's own
// keeps this one distinct from anything a future agy ships built in.
//
// Exported because it is also how a real agy reports the hook back
// (`agy -p /hooks` prints this name in the first field of the record it
// loaded), and the live test that reads that output should match on this
// constant rather than on a second copy of the string.
const Name = "grain-native-tool-denial"

// Subcommand is the argv[1] a grain binary (cmd/grain, grain-agentd)
// matches to answer the hook (Run). pkg/agent/antigravity writes it into agy's config
// and the binaries match on it, and both read it from here, so the two
// agree.
const Subcommand = "agy-tool-hook"

// Decision answers one PreToolUse payload: it returns the JSON agy
// reads back on stdout, and it is the whole of what cmd/grain's
// "agy-tool-hook" subcommand does.
//
// This hook is the fail-closed half of the boundary. It sees every tool
// call and allows only exact, eagerly registered tools published by
// grain's private MCP server. AGY's native roster can grow independently
// of this repository, so enumerating the native tools to deny would make
// every newly added tool an unreviewed controller capability.
//
// Saying "allow" here would bypass the independent permission layer.
// Grain's tools therefore receive no decision and continue through AGY's
// normal permission check; every other name, and every malformed payload,
// receives a hard denial.
func Decision(payload []byte) []byte {
	return DecisionFor(Options{}, payload)
}

// Options is what one run's hook is told about that run, from the
// arguments hookConfigJSON wrote into its command (ParseArgs). The
// zero value is every run without a host-tool grant, and decides
// exactly what Decision always has.
type Options struct {
	// Grants are the hook-relevant grants this run holds. Only a
	// registered HostToolGrant's Capability means anything, and what it
	// admits is that grant's Tools -- a list in code, never in these
	// options, so no argument can name a host tool into the allowlist.
	Grants []string
	// RecordPath is the JSON-lines file every admitted host-tool call is
	// appended to before it is allowed (HostToolCall). A call that cannot
	// be recorded is denied: host-side calls must never go unrecorded.
	RecordPath string
	// TaskID and RunID are written into each record.
	TaskID, RunID string
	// CapabilityTools are the tools of this run's capability MCP servers
	// (agent.RunConfig.CapabilityMCPServers), each "<server>/<tool>":
	// admitted like grain's own, and nothing else is.
	CapabilityTools []string
}

// The flags a run's hook command carries (antigravity's hookArgs
// writes them) and ParseArgs reads.
const (
	FlagGrant  = "-grant"
	FlagRecord = "-record"
	FlagTask   = "-task"
	FlagRun    = "-run"
	// FlagCapabilityTool names one "<server>/<tool>" a capability
	// grant's MCP server reported.
	FlagCapabilityTool = "-capability-tool"
)

// ParseArgs reads what hookArgs wrote. An argument it does not
// recognize is skipped rather than fatal, because the hook must answer
// every call whatever it was started with (cmd/grain's agyToolHook), and
// returned in ignored for the caller to report; an unrecognized grant is
// kept but admits nothing.
func ParseArgs(args []string) (opts Options, ignored []string) {
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			ignored = append(ignored, args[i])
			break
		}
		switch args[i] {
		case FlagGrant:
			opts.Grants = append(opts.Grants, args[i+1])
		case FlagRecord:
			opts.RecordPath = args[i+1]
		case FlagTask:
			opts.TaskID = args[i+1]
		case FlagRun:
			opts.RunID = args[i+1]
		case FlagCapabilityTool:
			opts.CapabilityTools = append(opts.CapabilityTools, args[i+1])
		default:
			ignored = append(ignored, args[i])
			continue
		}
		i++
	}
	return opts, ignored
}

// Run is the whole of cmd/grain's Subcommand: it reads one
// PreToolUse payload from stdin, writes DecisionFor's reply for the
// options args carry (ParseArgs) to stdout, and says on stderr which
// arguments it did not recognize.
//
// It never fails and writes nothing at all for a call it has no opinion
// about, because agy takes a hook that exits non-zero, or writes
// anything but a decision, as a failed tool call (noOpinion). So a
// payload it cannot read is answered like any other unrecognized call,
// and an argument it does not know is reported rather than fatal.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) {
	payload, err := io.ReadAll(stdin)
	if err != nil {
		payload = nil
	}
	opts, ignored := ParseArgs(args)
	if len(ignored) > 0 {
		fmt.Fprintf(stderr, "grain %s: ignoring unrecognized arguments %q\n", Subcommand, ignored)
	}
	stdout.Write(DecisionFor(opts, payload))
}

// AllowsHostTool returns the grant opts holds that admits the host tool
// name, if there is one.
func (opts Options) AllowsHostTool(name string) (HostToolGrant, bool) {
	for _, g := range HeldHostToolGrants(opts.Grants) {
		if slices.Contains(g.Tools, name) {
			return g, true
		}
	}
	return HostToolGrant{}, false
}

// HostToolCallsFilename is where, inside a run's scratch directory, the
// hook records the host-tool calls it admitted.
const HostToolCallsFilename = "host-tool-calls.jsonl"

// HostToolCall is one admitted host-tool call as the hook recorded it.
type HostToolCall struct {
	At    time.Time      `json:"at"`
	Task  string         `json:"task,omitempty"`
	Run   string         `json:"run,omitempty"`
	Host  string         `json:"host,omitempty"`
	Tool  string         `json:"tool"`
	Query string         `json:"query,omitempty"`
	Args  map[string]any `json:"args,omitempty"`
}

// recordHostToolCall appends call to path as one JSON line.
func recordHostToolCall(path string, call HostToolCall) error {
	if path == "" {
		return fmt.Errorf("no record path")
	}
	line, err := json.Marshal(call)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// hostToolQuery is the part of a host tool's arguments worth a line in
// the daemon log: its query, or the whole of its arguments when it names
// none.
func hostToolQuery(args map[string]any) string {
	for _, key := range []string{"query", "Query", "q"} {
		if q, ok := args[key].(string); ok {
			return q
		}
	}
	if len(args) == 0 {
		return ""
	}
	b, _ := json.Marshal(args)
	return string(b)
}

// DecisionFor is Decision for a run whose hook was started with
// opts: grain's own MCP tools get no opinion, as always; for a run holding
// a registered HostToolGrant, the host tools it names are recorded and
// then get no opinion too; everything else is denied.
func DecisionFor(opts Options, payload []byte) []byte {
	var in struct {
		ToolCall struct {
			Name string         `json:"name"`
			Args map[string]any `json:"args"`
		} `json:"toolCall"`
	}
	if err := json.Unmarshal(payload, &in); err != nil {
		return denyNonGrainTool("unrecognized tool call")
	}
	for _, tool := range PublishedTools {
		if in.ToolCall.Name == agyToolName(tool) {
			return noOpinion
		}
	}
	for _, st := range opts.CapabilityTools {
		server, tool, ok := strings.Cut(st, "/")
		if ok && server != "" && tool != "" && in.ToolCall.Name == capabilityToolName(server, tool) {
			return noOpinion
		}
	}
	if grant, ok := opts.AllowsHostTool(in.ToolCall.Name); ok {
		host, _ := os.Hostname()
		call := HostToolCall{
			At: time.Now().UTC(), Task: opts.TaskID, Run: opts.RunID, Host: host,
			Tool: in.ToolCall.Name, Query: hostToolQuery(in.ToolCall.Args), Args: in.ToolCall.Args,
		}
		if err := recordHostToolCall(opts.RecordPath, call); err != nil {
			return denyReason(in.ToolCall.Name + " is allowed by this task's " + grant.Capability +
				" grant, but grain could not record the call (" + err.Error() + "), and an unrecorded call " +
				"on the machine hosting this session is not one grain will let through.")
		}
		return noOpinion
	}
	switch in.ToolCall.Name {
	case "exit_plan_mode":
		// agy's own plan-mode handoff, not a tool that touches any
		// machine: agy refuses edits until it is called, so denying it
		// traps a run that started in plan mode in a read-only loop. It
		// was 100% of its calls failing before this
		// (design/design/sandbox-first-agent-surface.design.md §5).
		return noOpinion
	case "enter_plan_mode":
		return denyReason("Plan mode is off in headless grain runs, and entering it would stop " +
			"every edit: there is nobody to approve a plan. Carry out the steps directly with " +
			joinNames(sandboxToolNames()) + ".")
	case "schedule":
		return denyReason("schedule's timers do not exist in a headless grain run: nothing wakes " +
			"this session up again. To wait for CI, run `grain ci wait` with " +
			agyToolName("run_command") + " (timeout 600000); to free the sandbox while " +
			"something finishes, call " + agyToolName("resume_later") + ".")
	}
	name := in.ToolCall.Name
	if name == "" {
		name = "unrecognized tool call"
	}
	return denyNonGrainTool(name)
}

func denyNonGrainTool(name string) []byte {
	return denyReason(name + " is not one of grain's sandbox-bound MCP tools and cannot run on the " +
		"machine hosting this session. Use " + joinNames(sandboxToolNames()) +
		" instead, or another " + agyToolName("") + "* tool.")
}

func denyReason(reason string) []byte {
	reply, err := json.Marshal(map[string]any{
		"decision": "deny",
		"reason":   reason,
	})
	if err != nil {
		// json.Marshal cannot fail for this string-only object. Preserve
		// fail-closed behavior if that invariant ever changes.
		return []byte(`{"decision":"deny","reason":"grain could not validate this tool call"}`)
	}
	return reply
}

// noOpinion leaves an exact grain MCP tool to AGY's permission engine:
// nothing at all, written on stdout before the hook exits successfully.
// Every other name and every unreadable payload is denied above.
//
// It used to be `{}`, on the reading that an object carrying no decision
// carries no opinion. Against a real agy 1.1.26 driving a real model that
// is precisely backwards, and it denied every tool call a run made --
// grain's own included, so a run so configured could do nothing at all
// except say why. The whole measured contract, since agy's own guide
// documents only the four decision values and not what it does with
// anything else:
//
//   - no output at all, exit 0: the call proceeds. This is "no opinion",
//     and it is the only spelling of it that is not also something else.
//   - {"decision":"allow"}: proceeds, skipping the permission prompt.
//   - {"decision":"ask"}: proceeds, the prompt being auto-approved by
//     Run's --dangerously-skip-permissions.
//   - {"decision":"deny","reason":r}: "tool call denied by pre-tool hook:
//     r", and the tool does not run. This is the one grain wants.
//   - {} or any object with no "decision": denied, with an empty reason.
//     An absent decision is not an abstention; it is a deny nobody
//     explained.
//   - output that is not JSON: the call fails with "failed to unmarshal
//     result from hook ... via protojson".
//   - a hook command that exits non-zero, or is not there at all: the
//     call fails with "JSON hook ... failed: command failed: exit status
//     N".
//
// The last two are why cmd/grain's subcommand exits 0 whatever happens
// and writes only this or a decision: a hook that errors does not fail
// open, it fails the tool call, so a broken grain binary would take the
// run's tools with it.
var noOpinion []byte

// HostToolGrant is a capability grant that admits a fixed, read-only
// handful of the agy host's own native tools -- tools every other run is
// denied by DecisionFor -- for a run holding it.
//
// The calls it admits run on the host rather than in the sandbox, as
// whatever identity the agy process there holds, so the pre-tool hook
// records each one before letting it through (HostToolCall), and a run
// is told in its preamble what it may call and what it runs as. A
// package outside core registers one from init()
// (RegisterHostToolGrant); core antigravity knows none by name.
type HostToolGrant struct {
	// Capability is the grant a run must hold (agent.RunConfig.Grants),
	// which the hook's own command carries. A capability server method's
	// reference or grant (https://grain.dev/capabilities/corp#research={})
	// is held by a grant of that method whatever its arguments, and by any
	// retired bare name model.CanonicalLegacyGrant rewrites to one; a bare
	// name is held by itself and by what it was retired to
	// (HeldHostToolGrants).
	Capability string
	// Tools is the whole allowlist, spelled as agy names its native
	// tools in a pre-tool hook payload. Nothing a task, a template or a
	// hook argument says can add to it.
	Tools []string
	// Preamble is what toolPreamble adds, after it has finished saying
	// why the built-ins are unavailable, for a run holding the grant:
	// the tools it admits, what they run as, and what to say when they
	// fail. It should start with a space.
	Preamble string
	// HostIdentity is what the admitted calls run as, for the line the
	// run's transcript gets ("as that host's <HostIdentity>").
	HostIdentity string
	// IsAuthFailure reports whether an admitted call's error text reads
	// as the host's credential having lapsed, and AuthHint is the line
	// an operator is shown when one did, naming host. Either may be nil.
	IsAuthFailure func(text string) bool
	AuthHint      func(host string) string
}

var (
	hostToolMu     sync.RWMutex
	hostToolGrants = map[string]HostToolGrant{}
)

// RegisterHostToolGrant makes g's tools admissible to a run holding
// g.Capability until the returned func is called. It panics on a grant
// with no Capability or no Tools, or one already registered.
func RegisterHostToolGrant(g HostToolGrant) (unregister func()) {
	if g.Capability == "" || len(g.Tools) == 0 {
		panic(fmt.Sprintf("hook: RegisterHostToolGrant(%q): a grant needs a capability and tools", g.Capability))
	}
	g.Tools = slices.Clone(g.Tools)
	hostToolMu.Lock()
	defer hostToolMu.Unlock()
	if _, ok := hostToolGrants[g.Capability]; ok {
		panic(fmt.Sprintf("hook: RegisterHostToolGrant(%q): already registered", g.Capability))
	}
	hostToolGrants[g.Capability] = g
	return func() {
		hostToolMu.Lock()
		defer hostToolMu.Unlock()
		delete(hostToolGrants, g.Capability)
	}
}

// RegisteredHostToolGrants is every registered grant, ordered by
// Capability so a run's hook command reads the same way twice.
func RegisteredHostToolGrants() []HostToolGrant {
	hostToolMu.RLock()
	defer hostToolMu.RUnlock()
	out := make([]HostToolGrant, 0, len(hostToolGrants))
	for _, g := range hostToolGrants {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Capability < out[j].Capability })
	return out
}

// HeldHostToolGrants is the registered grants among grants, each
// matched by what it grants rather than by its spelling (hostToolGrantKey).
func HeldHostToolGrants(grants []string) []HostToolGrant {
	keys := make([]string, 0, len(grants))
	for _, grant := range grants {
		keys = append(keys, hostToolGrantKey(grant))
	}
	var held []HostToolGrant
	for _, g := range RegisteredHostToolGrants() {
		if slices.Contains(grants, g.Capability) || slices.Contains(keys, hostToolGrantKey(g.Capability)) {
			held = append(held, g)
		}
	}
	return held
}

// hostToolGrantKey is what grant opens a host-tool grant by: a retired bare
// name as what it became (model.CanonicalLegacyGrant), and a capability
// server method's grant as its reference alone, its arguments dropped. So
// bare corp-research, a recorded corp#research={} and a bare corp#research
// all have one key once corp-research is a registered alias of the method.
func hostToolGrantKey(grant string) string {
	canonical := strings.TrimSpace(canonicalGrant(grant))
	if strings.HasPrefix(canonical, "https://") {
		return grantRef(canonical)
	}
	return canonical
}

// HostToolGrantFor is the registered grant whose allowlist names tool.
func HostToolGrantFor(tool string) (HostToolGrant, bool) {
	for _, g := range RegisteredHostToolGrants() {
		if slices.Contains(g.Tools, tool) {
			return g, true
		}
	}
	return HostToolGrant{}, false
}

// toolNamespace is the MCP server name grain registers its tools under
// (pkg/mcp.ToolNamespace; antigravity's tests hold the two equal).
const toolNamespace = "grain-sandbox"

// agyToolName is how agy names one of grain's eagerly registered MCP
// tools in a hook payload (pkg/mcp.AgyQualifiedToolName).
func agyToolName(tool string) string { return "mcp_" + toolNamespace + "_" + tool }

// capabilityToolName is how agy names a tool of a capability MCP server
// a run was given.
func capabilityToolName(server, tool string) string { return "mcp_" + server + "_" + tool }

// sandboxToolNames is SandboxTools as agy names them.
func sandboxToolNames() []string {
	names := make([]string, len(SandboxTools))
	for i, t := range SandboxTools {
		names[i] = agyToolName(t)
	}
	return names
}

// joinNames renders a tool list as prose: "a, b and c".
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// grantRef is the reference part of a capability grant string, without
// its arguments (pkg/model.CapabilityGrantRef).
func grantRef(s string) string {
	ref, _, _ := strings.Cut(strings.TrimSpace(s), "=")
	return ref
}

var (
	canonicalMu sync.RWMutex
	canonical   func(string) string
)

// SetGrantCanonicalizer installs what HeldHostToolGrants rewrites a
// retired grant name through before matching it (pkg/model's
// CanonicalLegacyGrant, which pkg/agent/antigravity installs from init).
// Without one a grant is matched as spelled: enough for the hook, whose
// arguments carry each held grant's own Capability (antigravity's hookArgs).
func SetGrantCanonicalizer(f func(string) string) {
	canonicalMu.Lock()
	defer canonicalMu.Unlock()
	canonical = f
}

func canonicalGrant(grant string) string {
	canonicalMu.RLock()
	f := canonical
	canonicalMu.RUnlock()
	if f == nil {
		return grant
	}
	return f(grant)
}

// PublishedTools is every tool grain's MCP server can publish to a run, by
// bare name: what the hook admits as grain's own (agyToolName). It is
// pkg/agent.PublishedToolNames written out, so this package need not link
// pkg/mcp; antigravity's TestHookToolListsMatchTheRoster fails, printing
// the list to paste here, when the two drift.
var PublishedTools = []string{
	"run_command", "read_file", "edit_file", "write_file", "ask_question",
	"request_secret", "request_access", "comment_on_issue", "nothing_needed",
	"write_handover", "compact_context", "update_brief", "propose_task", "add_review_comment",
	"pull_request_status", "base_branch_status",
	"reply_to_pull_request_comment", "resolve_review_thread",
	"wait_for_checks", "list_ci_runs", "get_ci_log", "retrigger_ci",
	"query_test_history", "run_ci_job", "get_ci_artifact", "grain_ls",
	"grain_read", "grain_grep", "grain_copy", "open_pull_request",
	"recreate_sandbox", "update_status", "rename_session", "request_human_review",
	"propose_config_change", "wait_for_task", "resume_later",
	"notify_operator", "message_task", "query_merge_queue", "revert_task",
	"revert_merge", "chat", "list_issues", "read_issue", "list_grain_tasks",
	"read_grain_task", "read_grain_task_prompt", "read_grain_task_transcript",
	"read_pipeline_changes", "read_grain_metrics", "read_grain_log",
	"read_deployment_health", "read_deployment_state",
	"query_grain_db",
	"execute_grain_db", "run_host_command",
}

// SandboxTools is the four tools that reach a run's sandbox
// (pkg/mcp.NewSandboxTools), which a denial names as what to use instead.
var SandboxTools = []string{"run_command", "read_file", "edit_file", "write_file"}
