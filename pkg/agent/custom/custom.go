// Package custom implements the versioned command protocol documented in
// docs/custom-agent-backends.md.
package custom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/procgroup"
)

// ProtocolVersion is the version of the request/response protocol this
// package speaks. A response carrying any other version is a backend
// failure.
const ProtocolVersion = 2

// Command is a process a backend is told to start: the mcp_server it
// connects to as an MCP client. Env holds NAME=value additions to the
// backend's own environment.
type Command struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Env     []string `json:"env,omitempty"`
}

// Request is the one JSON object Run writes to the backend's stdin.
type Request struct {
	ProtocolVersion  int     `json:"protocol_version"`
	Operation        string  `json:"operation"`
	Prompt           string  `json:"prompt"`
	WorkingDirectory string  `json:"working_directory,omitempty"`
	Model            string  `json:"model,omitempty"`
	Effort           string  `json:"effort,omitempty"`
	ModelProxyURL    string  `json:"model_proxy_url,omitempty"`
	ModelProxyToken  string  `json:"model_proxy_token,omitempty"`
	MaxTurns         int     `json:"max_turns,omitempty"`
	Deadline         string  `json:"deadline,omitempty"`
	MCPServer        Command `json:"mcp_server"`
	// MCPServers are the MCP servers this run's capability grants expose
	// (agent.RunConfig.CapabilityMCPServers), each a Streamable HTTP URL
	// on grain's own host, to be connected to beside mcp_server.
	MCPServers []HTTPServer `json:"mcp_servers,omitempty"`
}

// HTTPServer is one capability grant's MCP server as a backend connects
// to it: its name, grain's proxy URL for it, and the tools it reported.
type HTTPServer struct {
	Name  string   `json:"name"`
	Type  string   `json:"type"`
	URL   string   `json:"url"`
	Tools []string `json:"tools"`
}

// Response is the one JSON object a backend writes to its stdout: a
// Result, an Error, or an Error alongside a partial Result.
type Response struct {
	ProtocolVersion int            `json:"protocol_version"`
	Result          *Result        `json:"result,omitempty"`
	Error           *ResponseError `json:"error,omitempty"`
}

// Result is what a backend reports a run produced.
type Result struct {
	FinalText  string     `json:"final_text"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	Transcript string     `json:"transcript,omitempty"`
}

// ToolCall is one MCP tool call a backend's agent made, under its bare
// tool name, in the order it was made.
type ToolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Text      string         `json:"text,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
}

func (r *Result) agentResult() *agent.Result {
	if r == nil {
		return nil
	}
	calls := make([]agent.ToolCall, len(r.ToolCalls))
	for i, call := range r.ToolCalls {
		calls[i] = agent.ToolCall{Name: call.Name, Arguments: call.Arguments, Text: call.Text, IsError: call.IsError}
	}
	return &agent.Result{FinalText: r.FinalText, ToolCalls: calls, Transcript: r.Transcript}
}

// ResponseError is a backend's account of a failed run. Kind is empty
// or "usage_limit"; ResumeAt (RFC 3339) and RetryAfter (a positive Go
// duration) only mean something beside the latter, and usageLimit says
// what happens when they are unreadable or both set.
type ResponseError struct {
	Message    string `json:"message"`
	Kind       string `json:"kind,omitempty"`
	ResumeAt   string `json:"resume_at,omitempty"`
	RetryAfter string `json:"retry_after,omitempty"`
	// Scope and Model are a usage_limit error's reach: "account" for a
	// refusal of the whole key, anything else (normally omitted) for one
	// model's quota, and the model refused where the backend knows it
	// (agent.UsageLimitError.Scope, .Model).
	Scope string `json:"scope,omitempty"`
	Model string `json:"model,omitempty"`
}

// Config is how a deployment configures one custom backend. Command is
// resolved against StateRepoDir when it is relative; Model and Effort are
// passed through uninterpreted.
type Config struct {
	Command      string
	StateRepoDir string
	Model        string
	Effort       string
	// ModelProxy resolves the model proxy a run reaches and the token it
	// authenticates with. release, which may be nil, is called once the
	// run is over, whatever way it ends, so the token is revoked rather
	// than left honoured by the proxy until its lease runs out.
	ModelProxy func(context.Context, agent.RunConfig) (url, token string, release func(), err error)
	// MCPServer builds the grain mcpserver invocation scoped to one run.
	// It is required.
	MCPServer func(context.Context, agent.RunConfig) (Command, error)
}

// Framework is an agent.Framework that drives a custom backend command,
// or the agent CLI a declared mapping names (mapping.go).
type Framework struct {
	config  Config
	mapping *Mapping
}

// New validates config and returns a Framework for it. It fails unless
// the command resolves to an executable file.
func New(config Config) (*Framework, error) {
	if strings.TrimSpace(config.Command) == "" {
		return nil, errors.New("custom agent backend: command is required")
	}
	if config.MCPServer == nil {
		return nil, errors.New("custom agent backend: MCP server builder is required")
	}
	if !filepath.IsAbs(config.Command) {
		if config.StateRepoDir == "" {
			return nil, errors.New("custom agent backend: state repository directory is required for a relative command")
		}
		config.Command = filepath.Join(config.StateRepoDir, filepath.Clean(config.Command))
	}
	if IsMappingPath(config.Command) {
		mapping, err := LoadMapping(config.Command, config.StateRepoDir, config.Model, config.Effort)
		if err != nil {
			return nil, err
		}
		return &Framework{config: config, mapping: mapping}, nil
	}
	info, err := os.Stat(config.Command)
	if err != nil {
		return nil, fmt.Errorf("custom agent backend: command %q: %w", config.Command, err)
	}
	if info.IsDir() || info.Mode()&0111 == 0 {
		return nil, fmt.Errorf("custom agent backend: command %q is not executable", config.Command)
	}
	return &Framework{config: config}, nil
}

// Run implements agent.Framework: it starts the command once, writes a
// Request to its stdin and reads a Response from its stdout. A partial
// Result is returned beside an error whenever the backend reported one.
func (f *Framework) Run(ctx context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	var modelProxyURL, modelProxyToken string
	if f.config.ModelProxy != nil {
		var err error
		var release func()
		modelProxyURL, modelProxyToken, release, err = f.config.ModelProxy(ctx, cfg)
		if release != nil {
			defer release()
		}
		if err != nil {
			return nil, fmt.Errorf("custom agent backend: resolving model proxy: %w", err)
		}
	}
	mcpServer, err := f.config.MCPServer(ctx, cfg)
	if err != nil {
		return nil, err
	}
	mcpServer.Env = withMCPServerEnv(mcpServer.Env)
	request := Request{
		ProtocolVersion: ProtocolVersion, Operation: "run", Prompt: cfg.Prompt,
		WorkingDirectory: cfg.SandboxRoot, Model: f.config.Model, Effort: f.config.Effort,
		ModelProxyURL: modelProxyURL, ModelProxyToken: modelProxyToken, MaxTurns: cfg.MaxTurns, MCPServer: mcpServer,
	}
	for _, s := range cfg.CapabilityMCPServers {
		request.MCPServers = append(request.MCPServers, HTTPServer{Name: s.Name, Type: "http", URL: s.URL, Tools: append([]string{}, s.Tools...)})
	}
	deadline := cfg.Deadline
	if deadline.IsZero() {
		deadline, _ = ctx.Deadline()
	}
	if !deadline.IsZero() {
		request.Deadline = deadline.UTC().Format(time.RFC3339)
	}
	if f.mapping != nil {
		return f.runMapped(ctx, cfg, request)
	}
	input, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("custom agent backend: encoding request: %w", err)
	}
	home, err := os.MkdirTemp("", "grain-custom-home-*")
	if err != nil {
		return nil, fmt.Errorf("custom agent backend: creating home directory: %w", err)
	}
	defer os.RemoveAll(home)
	cmd := exec.CommandContext(ctx, f.config.Command)
	procgroup.Prepare(cmd)
	cmd.Env = commandEnv(os.Environ(), home)
	cmd.Stdin = bytes.NewReader(input)
	if cfg.SandboxRoot != "" {
		cmd.Dir = cfg.SandboxRoot
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// The same line the built-in frameworks' own CLIs get
	// (agent.LocalHost.Run). A backend this deployment did not write is
	// the one an operator can say least about from anywhere else, and
	// the request -- proxy token included -- travels on stdin, which
	// agent.CommandLine deliberately does not render.
	log.Printf("agent: running: %s", agent.CommandLine(agent.Command{Path: cmd.Path, Dir: cmd.Dir}))
	runErr := cmd.Run()

	var response Response
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if runErr != nil {
			return nil, fmt.Errorf("custom agent backend: command failed: %w: %s", runErr, detail)
		}
		return nil, fmt.Errorf("custom agent backend: decoding response: %w", err)
	}
	result := response.Result.agentResult()
	if response.ProtocolVersion != ProtocolVersion {
		return result, fmt.Errorf("custom agent backend: response protocol_version is %d, want %d", response.ProtocolVersion, ProtocolVersion)
	}
	if response.Error != nil {
		message := strings.TrimSpace(response.Error.Message)
		if response.Error.Kind == "usage_limit" {
			return result, usageLimit(message, response.Error)
		}
		return result, fmt.Errorf("custom agent backend: %s", message)
	}
	if runErr != nil {
		return result, fmt.Errorf("custom agent backend: command failed: %w: %s", runErr, strings.TrimSpace(stderr.String()))
	}
	if response.Result == nil {
		return nil, errors.New("custom agent backend: response has neither result nor error")
	}
	return result, nil
}

// usageLimit turns a backend's usage_limit error into the
// agent.UsageLimitError the orchestrator pauses on. An unreadable
// resume_at or retry_after does not demote the error to an ordinary
// failure -- the backend still says its quota is gone, and pausing it
// for the default window beats retrying it at once -- but the value it
// sent is named in the message, so a run that paused for the default
// window says why it did not pause for the backend's own. When both are
// set, resume_at wins, as it does in UsageLimitError.ResumeAt.
func usageLimit(message string, e *ResponseError) *agent.UsageLimitError {
	limit := &agent.UsageLimitError{Framework: "custom", Message: agent.TrimLimitMessage(message)}
	var notes []string
	if e.ResumeAt != "" {
		at, err := time.Parse(time.RFC3339, e.ResumeAt)
		if err != nil {
			notes = append(notes, fmt.Sprintf("backend sent an unreadable resume_at %q", agent.TrimLimitMessage(e.ResumeAt)))
		} else {
			limit.ResetAt = at
		}
	}
	if e.RetryAfter != "" {
		after, err := time.ParseDuration(e.RetryAfter)
		if err != nil || after <= 0 {
			notes = append(notes, fmt.Sprintf("backend sent an unreadable retry_after %q", agent.TrimLimitMessage(e.RetryAfter)))
		} else {
			limit.RetryAfter = after
		}
	}
	if strings.EqualFold(strings.TrimSpace(e.Scope), string(agent.LimitScopeAccount)) {
		limit.Scope = agent.LimitScopeAccount
	}
	limit.Model = strings.ToLower(strings.TrimSpace(e.Model))
	if strings.TrimSpace(e.Scope) == "" {
		limit.Classify(message)
	}
	if len(notes) > 0 {
		note := "(" + strings.Join(notes, "; ") + ")"
		if limit.Message == "" {
			limit.Message = note
		} else {
			limit.Message += " " + note
		}
	}
	return limit
}

// inheritedEnv names the variables a backend inherits from the daemon.
// Everything else is withheld: the daemon's environment can carry
// provider credentials (GEMINI_API_KEY, ANTHROPIC_API_KEY,
// GOOGLE_APPLICATION_CREDENTIALS, ...) and its own secrets, and a
// backend reaches a model through model_proxy_url and model_proxy_token
// instead. What is left is what an ordinary process needs to find its
// tools, its locale and the system's CA bundle. Proxy variables are left
// off on purpose: they can carry credentials in their URLs.
//
// HOME is not on the list: commandEnv sets it to a directory of the
// run's own (see there). Neither are the Docker client's settings, which
// are the grain mcpserver's to need rather than the backend's, and reach
// it through mcp_server.env (mcpServerEnv).
var inheritedEnv = map[string]bool{
	"PATH": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"LANG": true, "LANGUAGE": true, "TZ": true, "TMPDIR": true, "TERM": true,
	"XDG_RUNTIME_DIR": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
}

// fallbackPath is a backend's PATH when the daemon has none of its own,
// the value login(1) and cron fall back to -- the same one mcp's
// run_command and the host CI shell use.
const fallbackPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// commandEnv filters environ down to inheritedEnv and the LC_* locale
// variables, with HOME set to home.
//
// home is a directory made for this run alone and removed when it ends.
// It is not the daemon's own HOME, whose dotfiles are exactly where
// gcloud's application-default credentials, ~/.git-credentials and the
// built-in agents' auth files live -- and which an SDK or CLI a backend
// wraps would pick up without being asked. Nor is it the sandbox, where
// a backend's caches and config would land in the working tree the run
// commits from (and which a remote run does not have locally at all).
// It is set first, so nothing later in environ can put another back.
func commandEnv(environ []string, home string) []string {
	env := []string{"HOME=" + home}
	havePath := false
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if name == "PATH" {
			if value == "" {
				continue
			}
			havePath = true
		}
		if inheritedEnv[name] || strings.HasPrefix(name, "LC_") {
			env = append(env, kv)
		}
	}
	if !havePath {
		env = append(env, "PATH="+fallbackPath)
	}
	return env
}

// mcpServerEnv names the daemon variables the grain mcpserver a backend
// starts needs and the backend's own environment does not carry: the
// Docker client's settings, with which it reaches a container or granule
// sandbox through `docker exec`, and the daemon's HOME, where that client
// finds ~/.docker when DOCKER_CONFIG does not say otherwise. And
// GRAIN_LOCAL_API_TOKEN_FILE (ui.LocalAPITokenFileEnv), the path of the
// daemon's local API token: it is the only credential that lets the
// mcpserver call the daemon back over loopback, and without it the server
// cannot even read its own task and exits (grain/task-2380). It names a
// file the backend, running as the daemon's own account, could read
// anyway. None of them is a key. They travel in mcp_server.env, which the
// protocol already defines as additions to the environment the server is
// started with.
var mcpServerEnv = []string{
	"HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG",
	"DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY", "GRAIN_LOCAL_API_TOKEN_FILE",
}

// withMCPServerEnv adds mcpServerEnv's variables this process has to
// env, without overriding a name env already sets.
func withMCPServerEnv(env []string) []string {
	named := map[string]bool{}
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		named[name] = true
	}
	out := append([]string(nil), env...)
	for _, name := range mcpServerEnv {
		if value, ok := os.LookupEnv(name); ok && !named[name] {
			out = append(out, name+"="+value)
		}
	}
	return out
}
