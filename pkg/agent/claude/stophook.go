package claude

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"strings"

	"github.com/bwsalmon/grain/pkg/agent"
)

// StopHookSubcommand is the argv[1] cmd/grain matches to run ServeStopHook:
// Claude Code's own Stop hook, which grain installs in every run's
// settings (stopHookSettings) so that a run which ends its turn while
// promising more work -- or, on a background task, before it has made any
// closing move -- is sent back once to finish (agent.DecideStop).
//
// It is grain forking itself, for the reason the agy hook and mcpserver
// are: the hook must be an executable named in a config written per run,
// and the one a deployment is guaranteed to have is this binary. It runs
// beside the CLI, on the same host, and reads only what Claude Code hands
// it on stdin and the session transcript that names; it asks the daemon
// nothing, holds no credential, and can change nothing but whether this
// one turn ends.
const StopHookSubcommand = "claude-stop-hook"

// stopHookInput is the part of Claude Code's Stop hook payload this hook
// reads. last_assistant_message is only sent by newer builds; an older
// one is read off transcript_path instead.
type stopHookInput struct {
	HookEventName        string `json:"hook_event_name"`
	StopHookActive       bool   `json:"stop_hook_active"`
	TranscriptPath       string `json:"transcript_path"`
	LastAssistantMessage string `json:"last_assistant_message"`
}

// stopHookOutput is the reply that keeps Claude Code going: "block" with
// a reason, which the CLI hands the model as the reason it may not stop.
type stopHookOutput struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// maxHookTranscript bounds how much of a session transcript the hook
// reads: the decision needs the run's tool calls and its last message,
// and a pathological transcript is not worth holding a turn up for.
const maxHookTranscript = 64 << 20

// ServeStopHook answers one Claude Code Stop hook: the payload on stdin,
// the decision on stdout. Its arguments are what stopHookSettings wrote
// for this run -- -interactive for a task somebody is sitting with, and
// -record naming the file each block is appended to for the run's
// transcript and census (readStopBlocks).
//
// It never exits non-zero and writes nothing at all for a stop it allows:
// a hook that fails must cost a run nothing, and an empty stdout with exit
// status 0 is Claude Code's "carry on as you would have".
//
// stop_hook_active is what keeps it to one block per turn: Claude Code
// sets it when the stop it is asking about ends a continuation a Stop
// hook already asked for, and that stop is always allowed, so the rule
// can never loop.
func ServeStopHook(args []string, stdin io.Reader, stdout, stderr io.Writer) {
	fs := flag.NewFlagSet(StopHookSubcommand, flag.ContinueOnError)
	fs.SetOutput(stderr)
	interactive := fs.Bool("interactive", false, "the task is a session somebody is sitting with")
	record := fs.String("record", "", "file to append each block to")
	if err := fs.Parse(args); err != nil {
		return
	}
	var in stopHookInput
	if err := json.NewDecoder(io.LimitReader(stdin, 1<<20)).Decode(&in); err != nil {
		return
	}
	decision, block := stopHookDecision(in, *interactive, readHookTranscript)
	if !block {
		return
	}
	if *record != "" {
		recordStopBlock(*record, agent.StopBlock{Via: agent.StopViaHook, Reason: decision.Reason})
	}
	json.NewEncoder(stdout).Encode(stopHookOutput{Decision: "block", Reason: decision.Reason})
}

// stopHookDecision is the hook's policy apart from its I/O: whether this
// stop is blocked, and with what. transcript reads the session the
// payload names, and is a parameter so the policy is testable without
// one on disk.
func stopHookDecision(in stopHookInput, interactive bool, transcript func(path string) string) (agent.StopDecision, bool) {
	if in.StopHookActive {
		return agent.StopDecision{}, false
	}
	if in.HookEventName != "" && in.HookEventName != "Stop" {
		return agent.StopDecision{}, false
	}
	session := ""
	if in.TranscriptPath != "" {
		session = transcript(in.TranscriptPath)
	}
	// A session transcript's assistant and user lines are the same shape
	// as the stream-json ones parseEvents already reads.
	events := parseEvents(session)
	final := in.LastAssistantMessage
	if final == "" {
		final = lastAssistantText(events)
	}
	d := agent.DecideStop(interactive, final, events.result.ToolCalls)
	return d, d.Block
}

// lastAssistantText is the last text block of a parsed session: the
// message the agent is ending its turn on.
func lastAssistantText(p *parsedEvents) string {
	for i := len(p.items) - 1; i >= 0; i-- {
		if p.items[i].Kind == agent.ItemText {
			return p.items[i].Text
		}
	}
	return ""
}

func readHookTranscript(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, maxHookTranscript))
	return string(data)
}

// recordStopBlock appends b to the run's record file as one JSON line.
// Best-effort: a block that cannot be recorded still blocks. O_APPEND
// without O_CREATE, so the hook only ever writes to the file grain staged.
func recordStopBlock(path string, b agent.StopBlock) {
	line, err := json.Marshal(b)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(line, '\n'))
}

// stopHookSettings is the content of the --settings file that installs
// the hook for one run: grain forking itself as Claude Code's Stop hook,
// with the run's own arguments. A file rather than the inline JSON
// --settings also takes, because hostd admits a path-like argument only
// as a clean absolute path that exists under its roots
// (hostd.checkArg), and a JSON command line is neither.
func stopHookSettings(grainBinaryPath string, interactive bool, recordPath string) (string, error) {
	command := hookQuote(grainBinaryPath) + " " + StopHookSubcommand
	if interactive {
		command += " -interactive"
	}
	if recordPath != "" {
		command += " -record " + hookQuote(recordPath)
	}
	data, err := json.Marshal(map[string]any{
		"hooks": map[string]any{
			"Stop": []any{map[string]any{
				"hooks": []any{map[string]any{
					"type":    "command",
					"command": command,
					"timeout": 30,
				}},
			}},
		},
	})
	return string(data), err
}

// hookQuote quotes s for the shell Claude Code runs a hook command with.
func hookQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeStopSettings stages this run's --settings file on host, 0600, the
// way writeMCPConfigJSON stages its --mcp-config.
func writeStopSettings(ctx context.Context, host agent.Host, grainBinaryPath string, interactive bool, recordPath string) (string, error) {
	settings, err := stopHookSettings(grainBinaryPath, interactive, recordPath)
	if err != nil {
		return "", err
	}
	path, err := host.CreateTemp(ctx, "", "grain-claude-settings-*.json")
	if err != nil {
		return "", err
	}
	if err := host.WriteFile(ctx, path, []byte(settings), 0o600); err != nil {
		host.RemoveAll(ctx, path)
		return "", err
	}
	return path, nil
}

// stageStopRecord creates the empty file this run's hook appends its
// blocks to, on the host the CLI runs on.
func stageStopRecord(ctx context.Context, host agent.Host) (string, error) {
	path, err := host.CreateTemp(ctx, "", "grain-claude-stop-*.jsonl")
	if err != nil {
		return "", err
	}
	if err := host.WriteFile(ctx, path, nil, 0o600); err != nil {
		host.RemoveAll(ctx, path)
		return "", err
	}
	return path, nil
}

// readStopBlocks reads back what the hook recorded for this run.
func readStopBlocks(ctx context.Context, host agent.Host, path string) []agent.StopBlock {
	if path == "" {
		return nil
	}
	data, _, err := host.ReadTail(ctx, path, 1<<20)
	if err != nil {
		return nil
	}
	var blocks []agent.StopBlock
	for _, line := range strings.Split(string(data), "\n") {
		var b agent.StopBlock
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &b) == nil && b.Reason != "" {
			blocks = append(blocks, b)
		}
	}
	return blocks
}

// withStopBlocks puts what the hook recorded on result: the blocks
// themselves, for the census, and a line in the transcript for each.
func withStopBlocks(result *agent.Result, blocks []agent.StopBlock) {
	if result == nil || len(blocks) == 0 {
		return
	}
	result.StopBlocks = append(result.StopBlocks, blocks...)
	for _, b := range blocks {
		result.Transcript = appendNote(result.Transcript, agent.StopNote(b))
	}
}

// EnforcesStopRule implements agent.StopRuleFramework: every run this
// Framework drives carries the Stop hook, so the orchestrator's fallback
// must not apply the rule again.
func (f *Framework) EnforcesStopRule() bool { return true }
