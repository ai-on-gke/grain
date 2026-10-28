// Command fakeagy stands in for the Antigravity CLI (agy) on a fake
// Google-track host, for tests/installer's hostd test. setup.sh's
// allowlist names it as the agy entry's path, so a deployment that runs
// agy "on the host" runs this, through grain hostd, as the installing
// user, outside the daemon's container.
//
// It speaks just enough of agy's interface for pkg/agent/antigravity to
// drive it: the prompt arrives as one `--input-format stream-json` user
// event on stdin, and it answers in agy's stream-json (init, step_update,
// result). Its MCP server is the real one -- the grain-sandbox entry of
// the mcp_config.json grain wrote into its HOME, which forks the host's
// own /usr/local/bin/grain -- and every tool it calls goes through it.
//
// What it saw is written to report=<path> from the prompt, so the test
// can tell where it ran. mode=hang makes it wait, after its tool calls,
// until something kills it: the cancel and deadline cases. mode=steady
// ticks=N prints N numbered ticks, one every half second, before its
// result: the daemon-upgrade case.
//
// With "capability-server <report>" as its arguments it is instead a
// stdio capability server with one method, host-probe, for a `run: host`
// capability entry of the same allowlist.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/capability/protocol"
	"github.com/bwsalmon/grain/pkg/mcp"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("fakeagy 0.0.0 (tests/installer)")
		return
	}
	if len(os.Args) > 2 && os.Args[1] == "capability-server" {
		serveCapability(os.Args[2])
		return
	}
	runAgy()
}

// report is what the test reads back: enough to say this ran on the
// host, as whom, where, and with which environment.
type report struct {
	Args         []string `json:"args"`
	UID          int      `json:"uid"`
	Cwd          string   `json:"cwd"`
	Home         string   `json:"home"`
	EnvNames     []string `json:"envNames"`
	HTTPSProxy   string   `json:"httpsProxy"`
	HostOnlySeen bool     `json:"hostOnlySeen"`
	InContainer  bool     `json:"inContainer"`
	PID          int      `json:"pid"`
	MCPCommand   string   `json:"mcpCommand"`
	MCPArgs      []string `json:"mcpArgs"`
	MCPStderr    string   `json:"mcpStderr"`
	ToolResults  []string `json:"toolResults"`
	ToolErrors   []string `json:"toolErrors"`
}

func runAgy() {
	in := bufio.NewReader(os.Stdin)
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		fail("reading the prompt: " + err.Error())
	}
	var user struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &user); err != nil {
		fail("decoding the prompt event: " + err.Error())
	}
	fields := parseFields(user.Message.Content)

	cwd, _ := os.Getwd()
	r := report{Args: os.Args[1:], UID: os.Getuid(), Cwd: cwd, Home: os.Getenv("HOME"),
		HTTPSProxy: os.Getenv("HTTPS_PROXY"), PID: os.Getpid()}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		r.EnvNames = append(r.EnvNames, name)
	}
	if p := fields["host_only"]; p != "" {
		_, err := os.Stat(p)
		r.HostOnlySeen = err == nil
	}
	_, err = os.Stat("/.dockerenv")
	r.InContainer = err == nil

	emit(map[string]any{"event": "init", "conversation_id": "fake-conversation",
		"init": map[string]any{"cwd": cwd, "tools": []string{}}})

	command, args := mcpServer(r.Home)
	r.MCPCommand, r.MCPArgs = command, args
	step := 0
	if command == "" {
		r.ToolErrors = append(r.ToolErrors, "no "+mcp.ToolNamespace+" server in "+filepath.Join(r.Home, ".gemini", "config", "mcp_config.json"))
	} else {
		step = callTools(command, args, &r)
	}

	if p := fields["report"]; p != "" {
		b, _ := json.Marshal(r)
		_ = os.WriteFile(p+".tmp", b, 0o644)
		_ = os.Rename(p+".tmp", p)
	}
	if fields["mode"] == "hang" {
		// Held until hostd, or the daemon through hostd, kills the group.
		// A child of our own shares it, so the test can check that the
		// whole group went and not just this process.
		child := exec.Command("sleep", "3600")
		_ = child.Start()
		if p := fields["report"]; p != "" {
			_ = os.WriteFile(p+".pids", []byte(fmt.Sprintf("%d %d\n", os.Getpid(), child.Process.Pid)), 0o644)
		}
		_ = child.Wait()
		for {
			time.Sleep(time.Hour)
		}
	}
	if fields["mode"] == "steady" {
		// Prints one numbered tick at a time, for as long as a daemon
		// upgrade takes: the test restarts the daemon in the middle and
		// checks the adopting daemon's transcript has every tick once.
		ticks, _ := strconv.Atoi(fields["ticks"])
		if p := fields["report"]; p != "" {
			_ = os.WriteFile(p+".pid", []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644)
		}
		for i := range ticks {
			step++
			emit(map[string]any{"event": "step_update", "step_update": map[string]any{
				"step_index": step, "state": "DONE", "step_type": "agent_response",
				"text_delta": fmt.Sprintf("steady tick %03d\n", i)}})
			time.Sleep(500 * time.Millisecond)
		}
	}
	step++
	emit(map[string]any{"event": "step_update", "step_update": map[string]any{
		"step_index": step, "state": "DONE", "step_type": "agent_response",
		"text_delta": "fake agy finished on the host"}})
	emit(map[string]any{"event": "result", "conversation_id": "fake-conversation",
		"result": map[string]any{"status": "SUCCESS", "response": "fake agy finished on the host", "num_turns": 1}})
}

// callTools starts grain's MCP server the way agy would and makes two
// calls through it, reporting each as agy's stream-json does.
func callTools(command string, args []string, r *report) int {
	server := exec.Command(command, args...)
	server.Env = os.Environ()
	stdin, _ := server.StdinPipe()
	stdout, _ := server.StdoutPipe()
	var stderr strings.Builder
	server.Stderr = &stderr
	defer func() {
		r.MCPStderr = stderr.String()
		if server.ProcessState != nil {
			r.MCPStderr += "\n(" + server.ProcessState.String() + ")"
		}
	}()
	if err := server.Start(); err != nil {
		r.ToolErrors = append(r.ToolErrors, "starting "+command+": "+err.Error())
		return 0
	}
	client := mcp.NewClient(stdin, stdout, stdin)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer func() {
		cancel()
		_ = client.Close()
		done := make(chan struct{})
		go func() { _ = server.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = server.Process.Kill()
			<-done
		}
	}()
	step := 0
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{"run_command", map[string]any{"command": "cd work && hostname > .hostd-agy-sandbox", "description": "probing the sandbox from a host-run agy"}},
		{"write_file", map[string]any{"file_path": "work/HOSTD_AGY.md", "content": "written by a host-run agy through grain's MCP server\n"}},
	} {
		step++
		emit(map[string]any{"event": "step_update", "step_update": map[string]any{
			"step_index": step, "state": "ACTIVE", "step_type": "tool", "tool_name": call.name,
			"tool_info": map[string]any{"name": call.name, "parameters": call.args}}})
		res, err := client.CallTool(ctx, call.name, call.args)
		state, out := "DONE", ""
		switch {
		case err != nil:
			state, out = "ERROR", err.Error()
			r.ToolErrors = append(r.ToolErrors, call.name+": "+out)
		case res.IsError:
			state, out = "ERROR", res.Text()
			r.ToolErrors = append(r.ToolErrors, call.name+": "+out)
		default:
			out = res.Text()
			r.ToolResults = append(r.ToolResults, call.name+": "+out)
		}
		emit(map[string]any{"event": "step_update", "step_update": map[string]any{
			"step_index": step, "state": state, "step_type": "tool", "tool_name": call.name,
			"tool_info": map[string]any{"name": call.name, "parameters": call.args, "output": out}}})
	}
	return step
}

// mcpServer is the grain-sandbox server from the mcp_config.json grain
// wrote into HOME, the file real agy reads.
func mcpServer(home string) (string, []string) {
	b, err := os.ReadFile(filepath.Join(home, ".gemini", "config", "mcp_config.json"))
	if err != nil {
		return "", nil
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return "", nil
	}
	s := cfg.MCPServers[mcp.ToolNamespace]
	return s.Command, s.Args
}

func emit(event map[string]any) {
	b, _ := json.Marshal(event)
	fmt.Println(string(b))
}

func fail(msg string) {
	emit(map[string]any{"event": "result", "result": map[string]any{"status": "FAILURE", "error": msg}})
	os.Exit(1)
}

// parseFields reads key=value pairs out of the prompt. The first
// occurrence wins, and a trailing full stop is not part of a value: the
// prompt quotes an edited description back in a comment ending in one
// (testagent's parseFields says more).
func parseFields(prompt string) map[string]string {
	out := map[string]string{}
	for _, field := range strings.Fields(prompt) {
		if key, value, ok := strings.Cut(field, "="); ok {
			value = strings.TrimRight(value, ".,")
			if _, seen := out[key]; !seen {
				out[key] = value
			}
		}
	}
	return out
}

// serveCapability is a stdio capability server (docs/capabilities-spec.md)
// with one method, host-probe. It records where it was started in
// reportPath before it serves, so the test can see a `run: host` server
// came up on the host, as the installing user, before any run mints
// from it.
func serveCapability(reportPath string) {
	_, docker := os.Stat("/.dockerenv")
	if f, err := os.OpenFile(reportPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintf(f, "started uid=%d pid=%d in_container=%t\n", os.Getuid(), os.Getpid(), docker == nil)
		f.Close()
	}
	probe, err := protocol.NewMethod(protocol.MethodSpec{
		Name:        "host-probe",
		Description: "Says where this capability server runs.",
		Mint: func(context.Context, string, time.Time, map[string]any) (*protocol.MintResponse, error) {
			return &protocol.MintResponse{Status: protocol.StatusOK,
				PromptSection: fmt.Sprintf("host-probe ran as uid %d.", os.Getuid())}, nil
		},
	})
	if err == nil {
		var srv *protocol.CapabilityServer
		if srv, err = protocol.NewCapabilityServer("fake-host-capability", probe); err == nil {
			err = srv.ServeStdio(context.Background(), os.Stdin, os.Stdout)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
