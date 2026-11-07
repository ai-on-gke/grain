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

package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/cli/skill"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/version"
)

// skill.go implements `grain skill install|status` and the single-line stderr
// staleness notice (#XC-30), plus `--mcp` registration in each agent's MCP
// configuration file (#XC-31).

const skillUsage = `usage: grain skill <command> [flags]

Install or inspect the Grain CLI skill file (SKILL.md) for workstation coding
agents, populated with live deployment metadata for the active context.

  grain skill install [--jetski | --claude | --codex | --gemini | --dir <path>] [--project] [--mcp] [--json]
  grain skill status [--json]
`

// Injectable hooks for tests.
var (
	skillVersionFunc = func() string { return formatVersion(version.Get()) }
	skillExecutable  = os.Executable
)

type skillAgentKind string

const (
	skillAgentJetski skillAgentKind = "jetski"
	skillAgentClaude skillAgentKind = "claude"
	skillAgentCodex  skillAgentKind = "codex"
	skillAgentGemini skillAgentKind = "gemini"
	skillAgentDir    skillAgentKind = "dir"
)

type skillTarget struct {
	Agent         skillAgentKind `json:"agent"`
	Project       bool           `json:"project,omitempty"`
	SkillPath     string         `json:"skillPath"`
	MCPConfigPath string         `json:"mcpConfigPath,omitempty"`
}

type skillEmbeddedMeta struct {
	Version          string `json:"version"`
	Context          string `json:"context"`
	DeploymentSHA256 string `json:"deploymentSha256"`
}

type skillContextState struct {
	Targets                []skillTarget `json:"targets"`
	CachedDeploymentDigest string        `json:"cachedDeploymentDigest,omitempty"`
}

type skillStateFile struct {
	Contexts map[string]*skillContextState `json:"contexts"`
}

func skillContextKey(name string) string {
	if s := strings.TrimSpace(name); s != "" {
		return s
	}
	return "default"
}

func skillStatePath() (string, error) {
	dir, err := grainConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "skills.json"), nil
}

func loadSkillState() (*skillStateFile, error) {
	st := &skillStateFile{Contexts: map[string]*skillContextState{}}
	path, err := skillStatePath()
	if err != nil {
		return st, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, st); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if st.Contexts == nil {
		st.Contexts = map[string]*skillContextState{}
	}
	return st, nil
}

func saveSkillState(st *skillStateFile) error {
	path, err := skillStatePath()
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(path, append(raw, '\n'))
}

func cmdSkill(ctx context.Context, c *ui.HTTPClient, out *printer, sel selectedContext, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, skillUsage)
		return usageErrorf("usage: grain skill install|status [flags]")
	}
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(os.Stderr, skillUsage)
		return nil
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "install":
		return cmdSkillInstall(ctx, c, out, sel, rest)
	case "status":
		return cmdSkillStatus(ctx, c, out, sel, rest)
	default:
		fmt.Fprint(os.Stderr, skillUsage)
		return usageErrorf("unknown \"grain skill\" subcommand %q: expected install or status", sub)
	}
}

func cmdSkillInstall(ctx context.Context, c *ui.HTTPClient, out *printer, sel selectedContext, args []string) error {
	fs := flag.NewFlagSet("grain skill install", flag.ContinueOnError)
	jetski := fs.Bool("jetski", false, "install for Jetski (~/.gemini/jetski/skills/grain/SKILL.md, or .agents/skills/grain/SKILL.md with -project)")
	claude := fs.Bool("claude", false, "install for Claude Code (~/.claude/skills/grain/SKILL.md, or .claude/skills/grain/SKILL.md with -project)")
	codex := fs.Bool("codex", false, "install for Codex CLI (~/.codex/skills/grain/SKILL.md, or .agents/skills/grain/SKILL.md with -project)")
	gemini := fs.Bool("gemini", false, "install for Gemini CLI (~/.gemini/skills/grain/SKILL.md, or .gemini/skills/grain/SKILL.md with -project)")
	dir := fs.String("dir", "", "write SKILL.md into this directory")
	project := fs.Bool("project", false, "write into the current repository/project skill directory instead of the user home directory")
	withMCP := fs.Bool("mcp", false, "register grain mcp in the target agent's MCP server configuration")
	asJSON := fs.Bool("json", false, "print installation summary as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if err := noArgs(fs, pos); err != nil {
		return err
	}

	st, err := loadSkillState()
	if err != nil {
		return err
	}
	ctxKey := skillContextKey(sel.name)

	targets, err := resolveSkillTargets(*jetski, *claude, *codex, *gemini, strings.TrimSpace(*dir), *project, *withMCP, st.Contexts[ctxKey])
	if err != nil {
		return err
	}

	section, digest, err := buildDeploymentSection(ctx, c, sel)
	if err != nil {
		return err
	}
	ver := skillVersionFunc()
	metaJSON, err := json.Marshal(skillEmbeddedMeta{
		Version:          ver,
		Context:          ctxKey,
		DeploymentSHA256: digest,
	})
	if err != nil {
		return err
	}

	rendered, err := renderSkillDocument(string(metaJSON), sel.name, section)
	if err != nil {
		return err
	}
	if err := assertSkillContainsNoSecrets(ctx, rendered, c); err != nil {
		return err
	}

	var grainBin string
	if *withMCP || slices.ContainsFunc(targets, func(t skillTarget) bool { return t.MCPConfigPath != "" }) {
		grainBin, err = skillExecutable()
		if err != nil || strings.TrimSpace(grainBin) == "" {
			grainBin = "grain"
		}
	}

	for i := range targets {
		t := &targets[i]
		if err := writeSkillFileAtomically(t.SkillPath, []byte(rendered)); err != nil {
			return err
		}
		if *withMCP || t.MCPConfigPath != "" {
			if t.MCPConfigPath == "" {
				mcpPath, err := mcpConfigPathForTarget(t.Agent, t.Project, filepath.Dir(t.SkillPath))
				if err != nil {
					return err
				}
				t.MCPConfigPath = mcpPath
			}
			if err := registerAgentMCPServer(t.Agent, t.MCPConfigPath, grainBin, sel.name); err != nil {
				return err
			}
		}
	}

	rec := st.Contexts[ctxKey]
	if rec == nil {
		rec = &skillContextState{}
		st.Contexts[ctxKey] = rec
	}
	rec.CachedDeploymentDigest = digest
	for _, t := range targets {
		upsertSkillTarget(rec, t)
	}
	if err := saveSkillState(st); err != nil {
		return err
	}

	type installResult struct {
		Context          string        `json:"context"`
		Server           string        `json:"server"`
		Version          string        `json:"version"`
		DeploymentDigest string        `json:"deploymentDigest"`
		Targets          []skillTarget `json:"targets"`
	}
	res := installResult{
		Context:          ctxKey,
		Server:           c.BaseURL,
		Version:          ver,
		DeploymentDigest: digest,
		Targets:          targets,
	}
	if *asJSON || out.json {
		out.encode(res)
		return nil
	}
	for _, t := range targets {
		if t.MCPConfigPath != "" {
			fmt.Printf("installed\t%s\tmcp:%s\n", t.SkillPath, t.MCPConfigPath)
		} else {
			fmt.Printf("installed\t%s\n", t.SkillPath)
		}
	}
	return nil
}

func renderSkillDocument(metaJSON, contextFlag, section string) (string, error) {
	return skill.Render(skill.Data{
		MetadataJSON:      metaJSON,
		ContextFlag:       contextFlag,
		DeploymentSection: strings.TrimRight(section, "\n"),
	})
}

var skillCommentPattern = regexp.MustCompile(`(?m)^<!-- grain-skill: (\{[^\n]*\}) -->\s*$`)

func parseSkillMetadata(content []byte) (skillEmbeddedMeta, bool) {
	m := skillCommentPattern.FindSubmatch(content)
	if m == nil {
		return skillEmbeddedMeta{}, false
	}
	var meta skillEmbeddedMeta
	if err := json.Unmarshal(m[1], &meta); err != nil {
		return skillEmbeddedMeta{}, false
	}
	return meta, meta.Version != "" && meta.DeploymentSHA256 != ""
}

var rawExternalTokenPattern = regexp.MustCompile(`\bgma_[0-9a-fA-F]{16,}\b`)

func assertSkillContainsNoSecrets(ctx context.Context, rendered string, c *ui.HTTPClient) error {
	if rawExternalTokenPattern.MatchString(rendered) {
		return fmt.Errorf("refusing to write SKILL.md: rendered content matched a gma_ token pattern")
	}
	if strings.Contains(rendered, "Authorization: Bearer ") {
		return fmt.Errorf("refusing to write SKILL.md: rendered content contained a bearer header")
	}
	for _, tok := range []string{
		strings.TrimSpace(c.Token),
		strings.TrimSpace(os.Getenv("GRAIN_TOKEN")),
		strings.TrimSpace(os.Getenv("GRAIN_AUTH_TOKEN")),
		strings.TrimSpace(os.Getenv("GRAIN_ID_TOKEN")),
		strings.TrimSpace(os.Getenv("GRAIN_TUNNEL_TOKEN")),
	} {
		if len(tok) >= 8 && strings.Contains(rendered, tok) {
			return fmt.Errorf("refusing to write SKILL.md: rendered content contained an active credential")
		}
	}
	if c.ContextToken != nil {
		if tok, err := c.ContextToken(ctx); err == nil && len(strings.TrimSpace(tok)) >= 8 {
			if strings.Contains(rendered, strings.TrimSpace(tok)) {
				return fmt.Errorf("refusing to write SKILL.md: rendered content contained the context credential")
			}
		}
	}
	return nil
}

func resolveSkillTargets(jetski, claude, codex, gemini bool, dir string, project bool, withMCP bool, existing *skillContextState) ([]skillTarget, error) {
	chosen := 0
	var kind skillAgentKind
	if jetski {
		chosen++
		kind = skillAgentJetski
	}
	if claude {
		chosen++
		kind = skillAgentClaude
	}
	if codex {
		chosen++
		kind = skillAgentCodex
	}
	if gemini {
		chosen++
		kind = skillAgentGemini
	}
	if dir != "" {
		chosen++
		kind = skillAgentDir
	}
	if chosen > 1 {
		return nil, usageErrorf("choose at most one target flag (--jetski, --claude, --codex, --gemini, or --dir)")
	}
	if chosen == 0 {
		if existing != nil && len(existing.Targets) > 0 && !project {
			out := slices.Clone(existing.Targets)
			if withMCP {
				for i := range out {
					if out[i].MCPConfigPath == "" {
						p, err := mcpConfigPathForTarget(out[i].Agent, out[i].Project, filepath.Dir(out[i].SkillPath))
						if err != nil {
							return nil, err
						}
						out[i].MCPConfigPath = p
					}
				}
			}
			return out, nil
		}
		kind = skillAgentJetski
	}

	skillPath, err := skillPathForTarget(kind, project, dir)
	if err != nil {
		return nil, err
	}
	t := skillTarget{Agent: kind, Project: project, SkillPath: skillPath}
	if withMCP {
		mcpPath, err := mcpConfigPathForTarget(kind, project, dir)
		if err != nil {
			return nil, err
		}
		t.MCPConfigPath = mcpPath
	}
	return []skillTarget{t}, nil
}

func resolveProjectRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	home, _ := os.UserHomeDir()
	cur := cwd
	for {
		if home != "" && cur == home {
			break
		}
		if _, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			return cur, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return cwd, nil
}

func skillPathForTarget(kind skillAgentKind, project bool, dir string) (string, error) {
	if kind == skillAgentDir {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", err
		}
		if filepath.Base(abs) == "SKILL.md" {
			return abs, nil
		}
		return filepath.Join(abs, "SKILL.md"), nil
	}
	if project {
		root, err := resolveProjectRoot()
		if err != nil {
			return "", err
		}
		switch kind {
		case skillAgentJetski, skillAgentCodex:
			return filepath.Join(root, ".agents", "skills", "grain", "SKILL.md"), nil
		case skillAgentClaude:
			return filepath.Join(root, ".claude", "skills", "grain", "SKILL.md"), nil
		case skillAgentGemini:
			return filepath.Join(root, ".gemini", "skills", "grain", "SKILL.md"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding home directory: %w", err)
	}
	switch kind {
	case skillAgentJetski:
		return filepath.Join(home, ".gemini", "jetski", "skills", "grain", "SKILL.md"), nil
	case skillAgentClaude:
		return filepath.Join(home, ".claude", "skills", "grain", "SKILL.md"), nil
	case skillAgentCodex:
		return filepath.Join(home, ".codex", "skills", "grain", "SKILL.md"), nil
	case skillAgentGemini:
		return filepath.Join(home, ".gemini", "skills", "grain", "SKILL.md"), nil
	default:
		return "", fmt.Errorf("unknown skill target %q", kind)
	}
}

func mcpConfigPathForTarget(kind skillAgentKind, project bool, dir string) (string, error) {
	if kind == skillAgentDir {
		base := dir
		if filepath.Base(base) == "SKILL.md" {
			base = filepath.Dir(base)
		}
		abs, err := filepath.Abs(base)
		if err != nil {
			return "", err
		}
		return filepath.Join(abs, "mcp_config.json"), nil
	}
	if project {
		root, err := resolveProjectRoot()
		if err != nil {
			return "", err
		}
		switch kind {
		case skillAgentJetski:
			return filepath.Join(root, ".gemini", "config", "mcp_config.json"), nil
		case skillAgentClaude:
			return filepath.Join(root, ".mcp.json"), nil
		case skillAgentCodex:
			return filepath.Join(root, ".codex", "config.toml"), nil
		case skillAgentGemini:
			return filepath.Join(root, ".gemini", "settings.json"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding home directory: %w", err)
	}
	switch kind {
	case skillAgentJetski:
		return filepath.Join(home, ".gemini", "config", "mcp_config.json"), nil
	case skillAgentClaude:
		return filepath.Join(home, ".claude.json"), nil
	case skillAgentCodex:
		return filepath.Join(home, ".codex", "config.toml"), nil
	case skillAgentGemini:
		return filepath.Join(home, ".gemini", "settings.json"), nil
	default:
		return "", fmt.Errorf("unknown MCP target %q", kind)
	}
}

func upsertSkillTarget(rec *skillContextState, next skillTarget) {
	for i := range rec.Targets {
		if rec.Targets[i].SkillPath == next.SkillPath {
			if next.MCPConfigPath == "" {
				next.MCPConfigPath = rec.Targets[i].MCPConfigPath
			}
			rec.Targets[i] = next
			return
		}
	}
	rec.Targets = append(rec.Targets, next)
}

func writeSkillFileAtomically(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".grain-skill-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

func mcpServerArgsForContext(contextName string) []string {
	if c := strings.TrimSpace(contextName); c != "" {
		return []string{"mcp", "--context", c}
	}
	return []string{"mcp"}
}

func registerAgentMCPServer(kind skillAgentKind, configPath, grainBin, contextName string) error {
	args := mcpServerArgsForContext(contextName)
	if kind == skillAgentCodex || strings.HasSuffix(configPath, ".toml") {
		return registerCodexMCPServerTOML(configPath, grainBin, args)
	}
	return registerJSONMCPServer(configPath, grainBin, args)
}

func registerJSONMCPServer(configPath, grainBin string, args []string) error {
	root := map[string]any{}
	if existing, err := os.ReadFile(configPath); err == nil && len(bytes.TrimSpace(existing)) > 0 {
		if err := json.Unmarshal(existing, &root); err != nil {
			return fmt.Errorf("parsing existing MCP config %s: %w", configPath, err)
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading %s: %w", configPath, err)
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok || servers == nil {
		servers = map[string]any{}
	}
	servers["grain"] = map[string]any{
		"command": grainBin,
		"args":    args,
	}
	root["mcpServers"] = servers
	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return writeSkillFileAtomically(configPath, append(encoded, '\n'))
}

var codexGrainTableHeader = regexp.MustCompile(`(?m)^\[mcp_servers\.(?:grain|"grain")\]\s*$`)
var anyTOMLTableHeader = regexp.MustCompile(`(?m)^\[[^\]]+\]\s*$`)

func registerCodexMCPServerTOML(configPath, grainBin string, args []string) error {
	var existing string
	if raw, err := os.ReadFile(configPath); err == nil {
		existing = string(raw)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading %s: %w", configPath, err)
	}

	cmdJSON, _ := json.Marshal(grainBin)
	argItems := make([]string, 0, len(args))
	for _, a := range args {
		b, _ := json.Marshal(a)
		argItems = append(argItems, string(b))
	}
	stanza := fmt.Sprintf("[mcp_servers.grain]\ncommand = %s\nargs = [%s]\n", string(cmdJSON), strings.Join(argItems, ", "))

	var updated string
	if loc := codexGrainTableHeader.FindStringIndex(existing); loc != nil {
		rest := existing[loc[1]:]
		endOffset := len(existing)
		if next := anyTOMLTableHeader.FindStringIndex(rest); next != nil {
			endOffset = loc[1] + next[0]
		}
		prefix := strings.TrimRight(existing[:loc[0]], "\n")
		suffix := strings.TrimLeft(existing[endOffset:], "\n")
		var parts []string
		if prefix != "" {
			parts = append(parts, prefix)
		}
		parts = append(parts, strings.TrimRight(stanza, "\n"))
		if suffix != "" {
			parts = append(parts, strings.TrimRight(suffix, "\n"))
		}
		updated = strings.Join(parts, "\n\n") + "\n"
	} else {
		trimmed := strings.TrimRight(existing, "\n")
		if trimmed == "" {
			updated = stanza
		} else {
			updated = trimmed + "\n\n" + stanza
		}
	}
	return writeSkillFileAtomically(configPath, []byte(updated))
}

func buildDeploymentSection(ctx context.Context, c *ui.HTTPClient, sel selectedContext) (section string, digest string, err error) {
	who, err := c.AuthWhoami(ctx)
	if err != nil {
		return "", "", expiredForContext(err, sel.name)
	}
	if !who.Authenticated {
		fix := "grain login " + c.BaseURL
		return "", "", &cliError{
			exit: exitAuth,
			code: "unauthenticated",
			fix:  fix,
			err:  fmt.Errorf("not signed in to %s (it accepts %s): run %s", c.BaseURL, strings.Join(who.AcceptedAuth, ", "), fix),
		}
	}

	cfg, cfgErr := c.SkillDeploymentConfig(ctx)
	if cfgErr != nil {
		var forbidden *ui.ForbiddenError
		if !errors.As(cfgErr, &forbidden) {
			return "", "", cfgErr
		}
	}

	templates, tmplErr := c.ListTemplates(ctx)
	if tmplErr != nil {
		var forbidden *ui.ForbiddenError
		if !errors.As(tmplErr, &forbidden) {
			return "", "", tmplErr
		}
	}

	repos := reachableSkillRepos(who, cfg)
	designPresent := detectDesignWorkflow(cfg, templates)

	var b strings.Builder
	b.WriteString("## 5. Active Deployment Metadata\n\n")
	ctxLabel := skillContextKey(sel.name)
	fmt.Fprintf(&b, "- **Context:** `%s`\n", ctxLabel)
	fmt.Fprintf(&b, "- **Server:** `%s`\n", strings.TrimRight(c.BaseURL, "/"))
	if cfg.EnvironmentName != "" {
		fmt.Fprintf(&b, "- **Deployment name:** %s\n", cfg.EnvironmentName)
	}
	fmt.Fprintf(&b, "- **Principal:** `%s`\n", who.Principal)
	if who.Owner != "" {
		fmt.Fprintf(&b, "- **Owner (`on behalf of`):** `%s`\n", who.Owner)
	}
	if who.ScopeSet != "" {
		fmt.Fprintf(&b, "- **Scope set:** `%s`\n", who.ScopeSet)
	}
	if len(who.ResolvedScopes) > 0 {
		scopes := slices.Clone(who.ResolvedScopes)
		sort.Strings(scopes)
		fmt.Fprintf(&b, "- **Held scopes:** `%s`\n", strings.Join(scopes, "`, `"))
	} else {
		b.WriteString("- **Held scopes:** full session access for principal\n")
	}
	if designPresent {
		b.WriteString("- **`design/` workflow:** active (pass `--design <design/...>` on `grain create` and `--spec` / `--design` on `grain pr adopt|review`)\n")
	} else {
		b.WriteString("- **`design/` workflow:** optional (pass `--design <design/...>` / `--spec <design/spec/...>` when targeting a repository with a `design/` directory)\n")
	}

	b.WriteString("\n### Reachable Repositories\n\n")
	if len(repos) == 0 {
		b.WriteString("- Unrestricted across deployment repositories\n")
	} else {
		for _, r := range repos {
			if cfg.DefaultTarget != "" && strings.EqualFold(r, cfg.DefaultTarget) {
				fmt.Fprintf(&b, "- `%s` (default target)\n", r)
			} else {
				fmt.Fprintf(&b, "- `%s`\n", r)
			}
		}
	}

	b.WriteString("\n### Available Templates\n\n")
	sortedTemplates := slices.Clone(templates)
	sort.Slice(sortedTemplates, func(i, j int) bool { return sortedTemplates[i].ID < sortedTemplates[j].ID })
	if len(sortedTemplates) == 0 {
		b.WriteString("- None advertised\n")
	} else {
		for _, t := range sortedTemplates {
			line := fmt.Sprintf("- `%s`", t.ID)
			if t.Name != "" && t.Name != t.ID {
				line += " (" + t.Name + ")"
			}
			if t.Repo != "" {
				line += " [repo: `" + t.Repo + "`]"
			}
			b.WriteString(line + "\n")
		}
	}

	b.WriteString("\n### Declared Custom Fields\n\n")
	fields := slices.Clone(cfg.CustomFields)
	sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
	if len(fields) == 0 {
		b.WriteString("- None declared\n")
	} else {
		for _, f := range fields {
			desc := fmt.Sprintf("- `%s` (`%s`", f.Name, f.Type)
			if f.Multiple {
				desc += ", repeatable"
			}
			desc += ")"
			if len(f.Options) > 0 {
				desc += ": `" + strings.Join(f.Options, "`, `") + "`"
			}
			b.WriteString(desc + "\n")
		}
	}

	renderedSection := b.String()
	sum := sha256.Sum256([]byte(renderedSection))
	return renderedSection, hex.EncodeToString(sum[:]), nil
}

func reachableSkillRepos(who ui.AuthWhoamiResponse, cfg ui.SkillDeploymentConfig) []string {
	seen := map[string]string{}
	add := func(r string) {
		r = strings.TrimSpace(r)
		if r == "" {
			return
		}
		key := strings.ToLower(r)
		if _, ok := seen[key]; !ok {
			seen[key] = r
		}
	}
	if len(who.Repos) > 0 {
		for _, r := range who.Repos {
			add(r)
		}
	} else {
		for _, r := range cfg.TargetRepos {
			add(r)
		}
		if cfg.DefaultTarget != "" {
			add(cfg.DefaultTarget)
		}
	}
	out := make([]string, 0, len(seen))
	for _, r := range seen {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

func detectDesignWorkflow(cfg ui.SkillDeploymentConfig, templates []ui.Template) bool {
	if len(cfg.ReposWithWorkflows) > 0 {
		return true
	}
	for _, t := range templates {
		id := strings.ToLower(t.ID)
		name := strings.ToLower(t.Name)
		if strings.Contains(id, "design") || strings.Contains(name, "design") {
			return true
		}
	}
	return false
}

// skillFileReport describes one checked SKILL.md path for `grain skill status`.
type skillFileReport struct {
	Path             string   `json:"path"`
	Status           string   `json:"status"` // "installed", "stale", "missing"
	Reasons          []string `json:"reasons,omitempty"`
	InstalledVersion string   `json:"installedVersion,omitempty"`
	InstalledDigest  string   `json:"installedDigest,omitempty"`
	MCPConfigPath    string   `json:"mcpConfigPath,omitempty"`
	MCPConfigPresent bool     `json:"mcpConfigPresent,omitempty"`
}

type skillStatusReport struct {
	Context          string            `json:"context"`
	Server           string            `json:"server"`
	Status           string            `json:"status"` // "installed", "stale", "missing"
	CurrentVersion   string            `json:"currentVersion"`
	DeploymentDigest string            `json:"deploymentDigest,omitempty"`
	Reasons          []string          `json:"reasons,omitempty"`
	Files            []skillFileReport `json:"files"`
}

func cmdSkillStatus(ctx context.Context, c *ui.HTTPClient, out *printer, sel selectedContext, args []string) error {
	fs := flag.NewFlagSet("grain skill status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print status as JSON")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if err := noArgs(fs, pos); err != nil {
		return err
	}

	st, err := loadSkillState()
	if err != nil {
		return err
	}
	ctxKey := skillContextKey(sel.name)
	rec := st.Contexts[ctxKey]

	_, liveDigest, err := buildDeploymentSection(ctx, c, sel)
	if err != nil {
		return err
	}
	if rec != nil && rec.CachedDeploymentDigest != liveDigest {
		rec.CachedDeploymentDigest = liveDigest
		_ = saveSkillState(st)
	}

	candidates := candidateTargetsForStatus(ctxKey, rec)
	curVer := skillVersionFunc()

	report := skillStatusReport{
		Context:          ctxKey,
		Server:           c.BaseURL,
		CurrentVersion:   curVer,
		DeploymentDigest: liveDigest,
		Files:            []skillFileReport{},
	}

	if len(candidates) == 0 {
		report.Status = "missing"
	} else {
		overall := "installed"
		var allReasons []string
		addReason := func(r string) {
			if !slices.Contains(allReasons, r) {
				allReasons = append(allReasons, r)
			}
		}
		for _, t := range candidates {
			fr := inspectSkillFile(t, curVer, liveDigest)
			report.Files = append(report.Files, fr)
			for _, r := range fr.Reasons {
				addReason(r)
			}
			switch fr.Status {
			case "missing":
				if overall == "installed" {
					overall = "missing"
				}
			case "stale":
				overall = "stale"
			}
		}
		// If some files were missing and none stale, check if all were missing vs partial.
		allMissing := true
		for _, f := range report.Files {
			if f.Status != "missing" {
				allMissing = false
				break
			}
		}
		if allMissing {
			overall = "missing"
		}
		report.Status = overall
		report.Reasons = allReasons
	}

	if *asJSON || out.json {
		out.encode(report)
		return nil
	}
	if len(report.Files) == 0 {
		fmt.Printf("missing\tcontext:%s\n", ctxKey)
		return nil
	}
	for _, f := range report.Files {
		if len(f.Reasons) > 0 {
			fmt.Printf("%s\t%s\t(%s)\n", f.Status, f.Path, strings.Join(f.Reasons, ", "))
		} else {
			fmt.Printf("%s\t%s\n", f.Status, f.Path)
		}
	}
	return nil
}

func candidateTargetsForStatus(ctxKey string, rec *skillContextState) []skillTarget {
	if rec != nil && len(rec.Targets) > 0 {
		return rec.Targets
	}
	var found []skillTarget
	for _, proj := range []bool{false, true} {
		for _, kind := range []skillAgentKind{skillAgentJetski, skillAgentClaude, skillAgentCodex, skillAgentGemini} {
			p, err := skillPathForTarget(kind, proj, "")
			if err != nil {
				continue
			}
			raw, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			meta, ok := parseSkillMetadata(raw)
			if ok && meta.Context != ctxKey {
				continue
			}
			if slices.ContainsFunc(found, func(t skillTarget) bool { return t.SkillPath == p }) {
				continue
			}
			found = append(found, skillTarget{Agent: kind, Project: proj, SkillPath: p})
		}
	}
	return found
}

func inspectSkillFile(t skillTarget, curVer, expectedDigest string) skillFileReport {
	fr := skillFileReport{
		Path:          t.SkillPath,
		MCPConfigPath: t.MCPConfigPath,
	}
	if t.MCPConfigPath != "" {
		if _, err := os.Stat(t.MCPConfigPath); err == nil {
			fr.MCPConfigPresent = true
		}
	}
	raw, err := os.ReadFile(t.SkillPath)
	if errors.Is(err, os.ErrNotExist) {
		fr.Status = "missing"
		return fr
	}
	if err != nil {
		fr.Status = "stale"
		fr.Reasons = []string{"unreadable"}
		return fr
	}
	meta, ok := parseSkillMetadata(raw)
	if !ok {
		fr.Status = "stale"
		fr.Reasons = []string{"missing_metadata"}
		return fr
	}
	fr.InstalledVersion = meta.Version
	fr.InstalledDigest = meta.DeploymentSHA256
	if meta.Version != curVer {
		fr.Reasons = append(fr.Reasons, "version_mismatch")
	}
	if expectedDigest != "" && meta.DeploymentSHA256 != expectedDigest {
		fr.Reasons = append(fr.Reasons, "deployment_digest_mismatch")
	}
	if len(fr.Reasons) > 0 {
		fr.Status = "stale"
		return fr
	}
	fr.Status = "installed"
	return fr
}

// warnIfSkillStale checks the recorded skill files for sel on local disk
// (#XC-30.3) and emits at most one concise single-line notice on w (stderr),
// never touching stdout.
func warnIfSkillStale(w io.Writer, sel selectedContext) {
	st, err := loadSkillState()
	if err != nil {
		return
	}
	ctxKey := skillContextKey(sel.name)
	rec := st.Contexts[ctxKey]
	if rec == nil || len(rec.Targets) == 0 {
		return
	}
	curVer := skillVersionFunc()
	for _, t := range rec.Targets {
		raw, err := os.ReadFile(t.SkillPath)
		if err != nil {
			continue
		}
		meta, ok := parseSkillMetadata(raw)
		if !ok || meta.Version != curVer || (rec.CachedDeploymentDigest != "" && meta.DeploymentSHA256 != rec.CachedDeploymentDigest) {
			fmt.Fprintf(w, "note: grain skill for context %s is stale; run 'grain skill install'\n", ctxKey)
			return
		}
	}
}
