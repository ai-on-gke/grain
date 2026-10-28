package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
)

// Asserted here rather than at the one call site that type-asserts for it
// (orchestrator's console.go), for the reason the PullRequestFramework
// assertion above it gives: a Framework that stopped implementing this
// would not fail to compile, it would quietly stop being something an
// interactive task can be dispatched to.
var _ agent.TerminalFramework = (*Framework)(nil)

// TerminalCommand implements agent.TerminalFramework: the same `claude`
// this package already runs, minus the three flags that make it headless.
//
// What stays is everything that decides what the agent can do. `--tools
// ""` still empties claude's native roster, `--mcp-config` still points
// at this same grain binary's own "mcpserver" subcommand bound to this
// run's sandbox, `--strict-mcp-config` and `--allowedTools` still admit
// nothing but the grain-sandbox tools, and the model is still the one the
// deployment asked for. So an interactive session reaches exactly what an
// unattended run of the same task reaches -- which matters more here than
// there, since the CLI is running on the *controller*: without that
// roster a person typing into this session would be editing the daemon's
// own filesystem rather than the sandbox's.
//
// What goes is `-p` (print mode: read the prompt, answer, exit), and with
// it `--output-format stream-json` and `--verbose`, which exist to make
// that mode's output parseable. What is left is claude's own terminal
// interface, which is the thing the person attaching wants to see.
//
// The prompt does not travel here at all: an interactive session's first
// message is typed by whoever opened it. See agent.TerminalCommand.Args.
//
// That "minus the three flags" is a derivation, and derivations are how a
// framework ends up passing a run's flag to a session that has no such
// flag -- pkg/agent/codex was doing exactly that with codex exec's
// --skip-git-repo-check. docs/claude-surface.md is the measurement that
// says this one is not: every long flag above has a row there saying the
// installed claude accepts it in this argv, and the capture's interactive
// section shows the same command line starting claude's own terminal
// interface on a pty. surface_capture_test.go is what keeps the two in
// step.
func (f *Framework) TerminalCommand(ctx context.Context, cfg agent.RunConfig) (*agent.TerminalCommand, error) {
	if f.claudePath == "" {
		return nil, fmt.Errorf("claude: this Framework was built without a claude binary to run interactively")
	}
	if f.grainBinaryPath == "" {
		return nil, fmt.Errorf("claude: grainBinaryPath is required")
	}
	mcpArgs, err := f.mcpServerArgs(ctx, cfg)
	if err != nil {
		return nil, err
	}
	servers, releaseServers, err := agent.CarryCapabilityServers(f.host, cfg.CapabilityMCPServers)
	if err != nil {
		return nil, fmt.Errorf("claude: %w", err)
	}
	configPath, err := writeMCPConfig(ctx, f.host, f.grainBinaryPath, mcpArgs, servers...)
	if err != nil {
		releaseServers()
		return nil, err
	}

	args := []string{}
	if !isUnconfined(f.host) {
		args = append(args,
			"--tools", "",
			"--mcp-config", configPath,
			"--strict-mcp-config",
			"--allowedTools", strings.Join(allowedTools(cfg.CapabilityMCPServers...), ","),
		)
	} else {
		args = append(args,
			"--mcp-config", configPath,
		)
	}
	if f.model != "" {
		args = append(args, "--model", f.model)
	}
	// The stop rule's hook (stophook.go), as a session: somebody is at
	// this terminal, so only a turn that ends on a promise is sent back.
	// No record file -- a console session hands grain no Result to put
	// one on.
	settingsPath, err := writeStopSettings(ctx, f.host, f.grainBinaryPath, true, "")
	if err != nil {
		settingsPath = ""
	} else {
		args = append(args, "--settings", settingsPath)
	}

	env := mcpToolTimeout()
	var credsCleanup func()
	if f.useVertex {
		env = append(env, "CLAUDE_CODE_USE_VERTEX=1")
		if f.oauthToken != nil {
			cred, err := f.oauthToken(ctx)
			if err != nil {
				os.Remove(configPath)
				removeStopSettings(settingsPath)
				releaseServers()
				return nil, fmt.Errorf("claude: reading the Claude Code credential: %w", err)
			}
			if cred != "" {
				vc, err := setupVertexCredentials(ctx, f.host, cred)
				if err != nil {
					os.Remove(configPath)
					removeStopSettings(settingsPath)
					releaseServers()
					return nil, err
				}
				credsCleanup = vc.cleanup
				env = append(env, vc.env...)
			}
		}
	} else if f.oauthToken != nil {
		token, err := f.oauthToken(ctx)
		if err != nil {
			os.Remove(configPath)
			removeStopSettings(settingsPath)
			releaseServers()
			return nil, fmt.Errorf("claude: reading the Claude Code OAuth token: %w", err)
		}
		if token != "" {
			env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+token)
		}
	}

	return &agent.TerminalCommand{
		Path: f.claudePath,
		Args: args,
		Env:  env,
		// Removed once the session ends rather than once this function
		// returns: the config file has to outlive the call, because the
		// process that reads it has not been started yet.
		Cleanup: func() {
			os.Remove(configPath)
			removeStopSettings(settingsPath)
			releaseServers()
			if credsCleanup != nil {
				credsCleanup()
			}
		},
	}, nil
}

type vertexCredentials struct {
	env     []string
	cleanup func()
}

// setupVertexCredentials configures the environment and any temporary
// credentials file required for Claude Code to authenticate to Google Cloud
// Vertex AI with a service account or non-ambient credential.
func setupVertexCredentials(ctx context.Context, host agent.Host, cred string) (*vertexCredentials, error) {
	cred = strings.TrimSpace(cred)
	if cred == "" {
		return &vertexCredentials{}, nil
	}
	var env []string
	var cleanup func()

	// If cred is an existing file path on the host, point directly at it.
	if _, err := os.Stat(cred); err == nil {
		env = append(env, "GOOGLE_APPLICATION_CREDENTIALS="+cred)
		if data, err := os.ReadFile(cred); err == nil {
			var sa struct {
				ProjectID string `json:"project_id"`
			}
			if err := json.Unmarshal(data, &sa); err == nil && sa.ProjectID != "" {
				if os.Getenv("ANTHROPIC_VERTEX_PROJECT_ID") == "" && os.Getenv("GOOGLE_CLOUD_PROJECT") == "" {
					env = append(env, "ANTHROPIC_VERTEX_PROJECT_ID="+sa.ProjectID)
				}
			}
		}
		return &vertexCredentials{env: env}, nil
	}

	var sa struct {
		ProjectID string `json:"project_id"`
	}
	_ = json.Unmarshal([]byte(cred), &sa)

	path, err := host.CreateTemp(ctx, "", "grain-claude-vertex-creds-*.json")
	if err != nil {
		return nil, fmt.Errorf("claude: creating vertex credentials file: %w", err)
	}
	if err := host.WriteFile(ctx, path, []byte(cred), 0o600); err != nil {
		host.RemoveAll(ctx, path)
		return nil, fmt.Errorf("claude: writing vertex credentials file: %w", err)
	}
	cleanup = func() {
		removeCtx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), time.Minute)
		defer cancel()
		host.RemoveAll(removeCtx, path)
	}
	env = append(env, "GOOGLE_APPLICATION_CREDENTIALS="+path)
	if sa.ProjectID != "" && os.Getenv("ANTHROPIC_VERTEX_PROJECT_ID") == "" && os.Getenv("GOOGLE_CLOUD_PROJECT") == "" {
		env = append(env, "ANTHROPIC_VERTEX_PROJECT_ID="+sa.ProjectID)
	}
	return &vertexCredentials{env: env, cleanup: cleanup}, nil
}

// writeMCPConfig writes the --mcp-config file both a headless run and an
// interactive session need, and returns its path. The caller owns
// removing it -- with a defer in Run's case, and with
// agent.TerminalCommand.Cleanup in a session's, where "when this function
// returns" is far too early.
func writeMCPConfig(ctx context.Context, host agent.Host, grainBinaryPath string, mcpArgs []string, servers ...agent.CapabilityMCPServer) (string, error) {
	configJSON, err := mcpConfigJSON(grainBinaryPath, mcpArgs, servers...)
	if err != nil {
		return "", fmt.Errorf("claude: building mcp config: %w", err)
	}
	return writeMCPConfigJSON(ctx, host, configJSON)
}

// writeMCPConfigJSON stages an --mcp-config file holding configJSON, 0600:
// an HTTP one carries its endpoint's bearer token.
func writeMCPConfigJSON(ctx context.Context, host agent.Host, configJSON []byte) (string, error) {
	path, err := host.CreateTemp(ctx, "", "grain-claude-mcp-config-*.json")
	if err != nil {
		return "", fmt.Errorf("claude: creating mcp config file: %w", err)
	}
	if err := host.WriteFile(ctx, path, configJSON, 0o600); err != nil {
		host.RemoveAll(ctx, path)
		return "", fmt.Errorf("claude: writing mcp config file: %w", err)
	}
	return path, nil
}

// removeStopSettings removes a session's --settings file, which, like its
// --mcp-config, is on this host (os.Remove above).
func removeStopSettings(path string) {
	if path != "" {
		os.Remove(path)
	}
}
