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
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	switch args[0] {
	case "install":
		return cmdSkillInstall(ctx, c, out, sel, args[1:])
	case "status":
		return cmdSkillStatus(ctx, c, out, sel, args[1:])
	default:
		fmt.Fprint(os.Stderr, skillUsage)
		return usageErrorf("grain skill: unknown subcommand %q: use install or status", args[0])
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
	metaJSON, err := json.Marshal(skill.EmbeddedMeta{
		Version:          ver,
		Context:          ctxKey,
		DeploymentSHA256: digest,
	})
	if err != nil {
		return err
	}

	rendered, err := skill.Render(skill.Data{
		MetadataJSON:      string(metaJSON),
		ContextFlag:       skill.SanitizeContextFlag(sel.name),
		DeploymentSection: strings.TrimRight(section, "\n"),
	})
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
		projRoot := projectRootForSkillTarget(*t)
		if projRoot != "" {
			if err := skill.EnsureNoSymlinksUnderRoot(projRoot, t.SkillPath); err != nil {
				return err
			}
		}
		if *withMCP || t.MCPConfigPath != "" {
			if t.MCPConfigPath == "" {
				mcpPath, err := mcpConfigPathForTarget(t.Agent, t.Project, filepath.Dir(t.SkillPath))
				if err != nil {
					return err
				}
				t.MCPConfigPath = mcpPath
			}
			if projRoot != "" {
				if err := skill.EnsureNoSymlinksUnderRoot(projRoot, t.MCPConfigPath); err != nil {
					return err
				}
			}
		}
		if err := skill.WriteFileAtomically(t.SkillPath, []byte(rendered)); err != nil {
			return err
		}
		if *withMCP || t.MCPConfigPath != "" {
			if err := skill.RegisterMCPServer(t.Agent == skillAgentCodex, t.MCPConfigPath, grainBin, sel.name); err != nil {
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

	res := struct {
		Context          string        `json:"context"`
		Server           string        `json:"server"`
		Version          string        `json:"version"`
		DeploymentDigest string        `json:"deploymentDigest"`
		Targets          []skillTarget `json:"targets"`
	}{
		Context:          ctxKey,
		Server:           skill.ScrubServerURL(c.BaseURL),
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

var rawExternalTokenPattern = regexp.MustCompile(`\bgma_[0-9a-fA-F]{16,}\b`)

func assertSkillContainsNoSecrets(ctx context.Context, rendered string, c *ui.HTTPClient) error {
	if rawExternalTokenPattern.MatchString(rendered) {
		return fmt.Errorf("refusing to write SKILL.md: rendered content matched a gma_ token pattern")
	}
	if strings.Contains(rendered, "Authorization: Bearer ") {
		return fmt.Errorf("refusing to write SKILL.md: rendered content contained a bearer header")
	}
	if u, err := url.Parse(strings.TrimSpace(c.BaseURL)); err == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok && pw != "" && strings.Contains(rendered, pw) {
			return fmt.Errorf("refusing to write SKILL.md: rendered content contained URL credentials")
		}
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

func resolveSkillTargets(jetski, claude, codex, gemini bool, dir string, project, withMCP bool, existing *skillContextState) ([]skillTarget, error) {
	chosen := 0
	var kind skillAgentKind
	for _, opt := range []struct {
		set  bool
		kind skillAgentKind
	}{
		{jetski, skillAgentJetski},
		{claude, skillAgentClaude},
		{codex, skillAgentCodex},
		{gemini, skillAgentGemini},
		{dir != "", skillAgentDir},
	} {
		if opt.set {
			chosen++
			kind = opt.kind
		}
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
	for cur := cwd; ; {
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

func projectRootForSkillTarget(t skillTarget) string {
	if !t.Project {
		return ""
	}
	if t.Agent == skillAgentDir {
		return filepath.Dir(t.SkillPath)
	}
	// <root>/.{agents,claude,gemini}/skills/grain/SKILL.md
	return filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(t.SkillPath))))
}

func buildDeploymentSection(ctx context.Context, c *ui.HTTPClient, sel selectedContext) (section, digest string, err error) {
	who, err := c.AuthWhoami(ctx)
	if err != nil {
		return "", "", expiredForContext(err, sel.name)
	}
	if !who.Authenticated {
		safeURL := skill.ScrubServerURL(c.BaseURL)
		fix := "grain login " + safeURL
		return "", "", &cliError{
			exit: exitAuth,
			code: "unauthenticated",
			fix:  fix,
			err:  fmt.Errorf("not signed in to %s (it accepts %s): run %s", safeURL, strings.Join(who.AcceptedAuth, ", "), fix),
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
	sec, dig := skill.FormatDeploymentSection(skillContextKey(sel.name), c.BaseURL, who, cfg, templates)
	return sec, dig, nil
}

type skillFileReport struct {
	Path             string   `json:"path"`
	Status           string   `json:"status"`
	Reasons          []string `json:"reasons,omitempty"`
	InstalledVersion string   `json:"installedVersion,omitempty"`
	InstalledDigest  string   `json:"installedDigest,omitempty"`
	MCPConfigPath    string   `json:"mcpConfigPath,omitempty"`
	MCPConfigPresent bool     `json:"mcpConfigPresent,omitempty"`
}

type skillStatusReport struct {
	Context          string            `json:"context"`
	Server           string            `json:"server"`
	Status           string            `json:"status"`
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
		Server:           skill.ScrubServerURL(c.BaseURL),
		CurrentVersion:   curVer,
		DeploymentDigest: liveDigest,
		Files:            []skillFileReport{},
	}

	if len(candidates) == 0 {
		report.Status = "missing"
	} else {
		overall := "installed"
		var allReasons []string
		allMissing := true
		for _, t := range candidates {
			fr := inspectSkillFile(t, curVer, liveDigest)
			report.Files = append(report.Files, fr)
			if fr.Status != "missing" {
				allMissing = false
			}
			for _, r := range fr.Reasons {
				if !slices.Contains(allReasons, r) {
					allReasons = append(allReasons, r)
				}
			}
			if fr.Status == "stale" {
				overall = "stale"
			} else if fr.Status == "missing" && overall == "installed" {
				overall = "missing"
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
			cand := skillTarget{Agent: kind, Project: proj, SkillPath: p}
			if root := projectRootForSkillTarget(cand); root != "" && skill.EnsureNoSymlinksUnderRoot(root, p) != nil {
				continue
			}
			raw, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			if meta, ok := skill.ParseMetadata(raw); ok && meta.Context != ctxKey {
				continue
			}
			if !slices.ContainsFunc(found, func(t skillTarget) bool { return t.SkillPath == p }) {
				found = append(found, cand)
			}
		}
	}
	return found
}

func inspectSkillFile(t skillTarget, curVer, expectedDigest string) skillFileReport {
	fr := skillFileReport{Path: t.SkillPath, MCPConfigPath: t.MCPConfigPath}
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
	meta, ok := skill.ParseMetadata(raw)
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

func warnIfCommandSkillStale(rawCmd string, sel selectedContext) {
	switch canonicalCommand(rawCmd) {
	case "skill", "mcp", "context", "login", "logout":
		return
	}
	warnIfSkillStale(os.Stderr, sel)
}

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
		meta, ok := skill.ParseMetadata(raw)
		if !ok || meta.Version != curVer || (rec.CachedDeploymentDigest != "" && meta.DeploymentSHA256 != rec.CachedDeploymentDigest) {
			fmt.Fprintf(w, "note: grain skill for context %s is stale; run 'grain skill install'\n", ctxKey)
			return
		}
	}
}
