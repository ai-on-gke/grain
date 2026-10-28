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
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/agent/claude"
	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/procgroup"
)

// A mapping is the second way to configure a custom backend, ported from
// Silo v2's agentcli/custom (Silo's cli-mapping.md §2.4): rather than an
// executable that speaks the JSON protocol, the backend's command names a
// YAML file declaring how an existing agent CLI is given each of a run's
// settings. Grain refuses any setting the mapping does not declare, so a
// run never starts with a setting the CLI was silently not given.
// docs/custom-agent-backends.md ("Declared CLI mappings") is the public
// interface.

// Staged file names, in the directory the mapping's placeholders name.
const (
	mappingPromptFile = "prompt.md"
	mappingJSONConfig = "mcp.json"
	mappingTOMLConfig = "mcp.toml"
)

// Mapping is a declared mapping of a run's settings onto an agent CLI.
// Its YAML field names are the same as a Silofile's agent.custom block,
// with binary beside them, so a Silo v2 mapping carries over unchanged
// apart from the settings grain does not have (interactive mode and
// remote_permissions all), which are refused.
type Mapping struct {
	// Binary is the CLI, absolute or relative to the state repository.
	Binary      string             `yaml:"binary"`
	Argv        MappingArgv        `yaml:"argv"`
	MCPConfig   MappingMCPConfig   `yaml:"mcp_config"`
	Permissions MappingPermissions `yaml:"permissions"`
	// Events is how the CLI's stdout is read: claude-stream-json, or
	// none for plain text that becomes the run's final text.
	Events string `yaml:"events"`
	// Model, Effort and MaxTurns are the arguments that pass the
	// backend's model, its effort and the run's turn limit, with
	// {model}, {effort} and {max_turns} substituted.
	Model    []string `yaml:"model"`
	Effort   []string `yaml:"effort"`
	MaxTurns []string `yaml:"max_turns"`
	// ModelProxy names the environment variables the model proxy's URL
	// and token are delivered in.
	ModelProxy *MappingModelProxy `yaml:"model_proxy"`
}

// MappingArgv is the CLI's arguments per mode. Grain runs custom backends
// unattended only, so Interactive exists to be refused.
type MappingArgv struct {
	Headless    []string `yaml:"headless"`
	Interactive []string `yaml:"interactive"`
}

// MappingMCPConfig says how the CLI is given its MCP config: the file's
// format, and the arguments ({file}) or environment variable naming it.
type MappingMCPConfig struct {
	Format string   `yaml:"format"`
	Arg    []string `yaml:"arg"`
	Env    string   `yaml:"env"`
}

// MappingPermissions are the arguments that switch the CLI's built-in
// tools off (None). A grain run reaches its sandbox only through grain's
// MCP server, and a custom CLI runs on the node host, so every run is
// none; All exists to be refused.
type MappingPermissions struct {
	None *[]string `yaml:"none"`
	All  *[]string `yaml:"all"`
}

// MappingModelProxy names where the model proxy's URL and token go.
type MappingModelProxy struct {
	URLEnv   string `yaml:"url_env"`
	TokenEnv string `yaml:"token_env"`
}

// IsMappingPath reports whether a backend command names a mapping file
// rather than a protocol executable.
func IsMappingPath(command string) bool {
	switch strings.ToLower(filepath.Ext(command)) {
	case ".yaml", ".yml":
		return true
	}
	return false
}

// LoadMapping reads and checks the mapping at path for a backend
// configured with model and effort. stateRepoDir resolves a relative
// binary.
func LoadMapping(path, stateRepoDir, model, effort string) (*Mapping, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("custom agent backend: mapping %q: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var m Mapping
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("custom agent backend: mapping %q: %w", path, err)
	}
	if err := m.check(model, effort); err != nil {
		return nil, fmt.Errorf("custom agent backend: mapping %q: %w", path, err)
	}
	if !filepath.IsAbs(m.Binary) {
		if stateRepoDir == "" {
			return nil, fmt.Errorf("custom agent backend: mapping %q: a relative binary needs the state repository directory", path)
		}
		m.Binary = filepath.Join(stateRepoDir, filepath.Clean(m.Binary))
	}
	info, err := os.Stat(m.Binary)
	if err != nil {
		return nil, fmt.Errorf("custom agent backend: mapping %q: binary: %w", path, err)
	}
	if info.IsDir() || info.Mode()&0111 == 0 {
		return nil, fmt.Errorf("custom agent backend: mapping %q: binary %q is not executable", path, m.Binary)
	}
	return &m, nil
}

// check refuses a mapping that leaves a setting every run has, or one
// this backend is configured with, undeclared -- and the settings grain
// never asks for.
func (m *Mapping) check(model, effort string) error {
	if strings.TrimSpace(m.Binary) == "" {
		return errors.New("binary is required")
	}
	if len(m.Argv.Interactive) > 0 {
		return errors.New("argv.interactive is refused: custom backends run unattended only; declare argv.headless")
	}
	if !mentions(m.Argv.Headless, "{prompt}") {
		return errors.New("argv.headless must pass the prompt: name the staged prompt file with {prompt}")
	}
	switch m.MCPConfig.Format {
	case "claude-json", "codex-toml":
	case "":
		return errors.New("mcp_config.format is required: claude-json or codex-toml")
	default:
		return fmt.Errorf("mcp_config.format %q is not supported: use claude-json or codex-toml", m.MCPConfig.Format)
	}
	if !mentions(m.MCPConfig.Arg, "{file}") && m.MCPConfig.Env == "" {
		return errors.New("mcp_config must pass the config: name the staged file with {file} in mcp_config.arg, or set mcp_config.env")
	}
	if m.Permissions.None == nil {
		return errors.New("permissions.none is required: the arguments that switch the CLI's built-in tools off (an empty list if it has none)")
	}
	if m.Permissions.All != nil {
		return errors.New("permissions.all is refused: every grain run is none, since the CLI runs on the node host and reaches the sandbox through grain's MCP server")
	}
	switch m.Events {
	case "claude-stream-json", "none":
	case "":
		return errors.New("events is required: claude-stream-json, or none")
	default:
		return fmt.Errorf("events %q is not supported: use claude-stream-json or none", m.Events)
	}
	if model != "" && !mentions(m.Model, "{model}") {
		return fmt.Errorf("the backend is configured with model %q but the mapping declares no model argument with {model}", model)
	}
	if effort != "" && !mentions(m.Effort, "{effort}") {
		return fmt.Errorf("the backend is configured with effort %q but the mapping declares no effort argument with {effort}", effort)
	}
	if len(m.MaxTurns) > 0 && !mentions(m.MaxTurns, "{max_turns}") {
		return errors.New("max_turns must name {max_turns}")
	}
	if p := m.ModelProxy; p != nil && (p.URLEnv == "" || p.TokenEnv == "") {
		return errors.New("model_proxy needs both url_env and token_env")
	}
	return nil
}

func mentions(args []string, placeholder string) bool {
	for _, a := range args {
		if strings.Contains(a, placeholder) {
			return true
		}
	}
	return false
}

// mappedRun is one run's staged files, command line and environment.
type mappedRun struct {
	files map[string][]byte
	argv  []string
	env   map[string]string
}

// plan builds a run's staged files, argv and extra environment, with
// staged files in dir.
func (m *Mapping) plan(dir string, req Request) (mappedRun, error) {
	run := mappedRun{files: map[string][]byte{mappingPromptFile: []byte(req.Prompt)}, env: map[string]string{}}
	var file string
	switch m.MCPConfig.Format {
	case "claude-json":
		file = mappingJSONConfig
		server := map[string]any{"command": req.MCPServer.Command, "args": nonNil(req.MCPServer.Args)}
		if env := envMap(req.MCPServer.Env); len(env) > 0 {
			server["env"] = env
		}
		all := map[string]any{mcp.ToolNamespace: server}
		for _, s := range req.MCPServers {
			all[s.Name] = map[string]any{"type": "http", "url": s.URL}
		}
		data, err := json.Marshal(map[string]any{"mcpServers": all})
		if err != nil {
			return run, err
		}
		run.files[file] = data
	case "codex-toml":
		file = mappingTOMLConfig
		run.files[file] = codexTOML(req.MCPServer, req.MCPServers...)
	}
	sub := strings.NewReplacer(
		"{file}", filepath.Join(dir, file),
		"{prompt}", filepath.Join(dir, mappingPromptFile),
		"{model}", req.Model,
		"{effort}", req.Effort,
		"{max_turns}", strconv.Itoa(req.MaxTurns),
	)
	add := func(args []string) {
		for _, a := range args {
			run.argv = append(run.argv, sub.Replace(a))
		}
	}
	run.argv = []string{m.Binary}
	add(m.Argv.Headless)
	add(m.MCPConfig.Arg)
	if m.MCPConfig.Env != "" {
		run.env[m.MCPConfig.Env] = filepath.Join(dir, file)
	}
	if req.Model != "" {
		add(m.Model)
	}
	if req.Effort != "" {
		add(m.Effort)
	}
	if req.MaxTurns > 0 {
		add(m.MaxTurns)
	}
	add(*m.Permissions.None)
	if p := m.ModelProxy; p != nil && req.ModelProxyURL != "" {
		run.env[p.URLEnv] = req.ModelProxyURL
		run.env[p.TokenEnv] = req.ModelProxyToken
	}
	return run, nil
}

// codexTOML is the [mcp_servers] table Codex reads, for grain's stdio
// server.
//
// Each capability MCP server follows as a url table of its own.
func codexTOML(server Command, servers ...HTTPServer) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "[mcp_servers.%s]\ncommand = %s\nargs = [", tomlString(mcp.ToolNamespace), tomlString(server.Command))
	for i, a := range server.Args {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(tomlString(a))
	}
	b.WriteString("]\nrequired = true\n")
	if env := envMap(server.Env); len(env) > 0 {
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("env = { ")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%s = %s", tomlString(k), tomlString(env[k]))
		}
		b.WriteString(" }\n")
	}
	for _, s := range servers {
		fmt.Fprintf(&b, "[mcp_servers.%s]\nurl = %s\nrequired = true\n", tomlString(s.Name), tomlString(s.URL))
	}
	return []byte(b.String())
}

// tomlString renders a Go string as a TOML basic string, which is
// spelled exactly as a JSON string.
func tomlString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// runMapped runs one request through the mapping: it stages the prompt
// and MCP config, starts the CLI with the declared arguments and reads
// its stdout as Events says.
func (f *Framework) runMapped(ctx context.Context, cfg agent.RunConfig, request Request) (*agent.Result, error) {
	home, err := os.MkdirTemp("", "grain-custom-home-*")
	if err != nil {
		return nil, fmt.Errorf("custom agent backend: creating home directory: %w", err)
	}
	defer os.RemoveAll(home)
	// Staged beside HOME, not in the sandbox, whose working tree a run
	// commits from: the MCP config names the mcpserver's arguments.
	dir := filepath.Join(home, ".grain-mapping")
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, fmt.Errorf("custom agent backend: staging directory: %w", err)
	}
	run, err := f.mapping.plan(dir, request)
	if err != nil {
		return nil, fmt.Errorf("custom agent backend: building command: %w", err)
	}
	for name, data := range run.files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return nil, fmt.Errorf("custom agent backend: staging %s: %w", name, err)
		}
	}
	cmd := exec.CommandContext(ctx, run.argv[0], run.argv[1:]...)
	procgroup.Prepare(cmd)
	env := commandEnv(os.Environ(), home)
	keys := make([]string, 0, len(run.env))
	for k := range run.env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+run.env[k])
	}
	cmd.Env = env
	cmd.Dir = home
	if cfg.SandboxRoot != "" {
		cmd.Dir = cfg.SandboxRoot
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	log.Printf("agent: running: %s", agent.CommandLine(agent.Command{Path: cmd.Path, Args: cmd.Args[1:], Dir: cmd.Dir}))
	runErr := cmd.Run()

	var result *agent.Result
	switch f.mapping.Events {
	case "claude-stream-json":
		parsed, err := claude.ParseStreamJSON(stdout.String())
		if err != nil {
			partial := &agent.Result{Transcript: claude.PartialTranscript(stdout.String())}
			if runErr != nil {
				return partial, fmt.Errorf("custom agent backend: command failed: %w: %s", runErr, strings.TrimSpace(stderr.String()))
			}
			return partial, fmt.Errorf("custom agent backend: reading claude-stream-json output: %w", err)
		}
		result = parsed
	default:
		text := strings.TrimSpace(stdout.String())
		result = &agent.Result{FinalText: text, Transcript: text}
	}
	if runErr != nil {
		return result, fmt.Errorf("custom agent backend: command failed: %w: %s", runErr, strings.TrimSpace(stderr.String()))
	}
	return result, nil
}
