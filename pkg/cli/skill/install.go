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

package skill

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// EmbeddedMeta is the machine-readable comment payload embedded at the top of
// an installed SKILL.md file (#XC-30.3).
type EmbeddedMeta struct {
	Version          string `json:"version"`
	Context          string `json:"context"`
	DeploymentSHA256 string `json:"deploymentSha256"`
}

var commentPattern = regexp.MustCompile(`(?m)^<!-- grain-skill: (\{[^\n]*\}) -->\s*$`)

// ParseMetadata extracts EmbeddedMeta from rendered SKILL.md bytes.
func ParseMetadata(content []byte) (EmbeddedMeta, bool) {
	m := commentPattern.FindSubmatch(content)
	if m == nil {
		return EmbeddedMeta{}, false
	}
	var meta EmbeddedMeta
	if err := json.Unmarshal(m[1], &meta); err != nil {
		return EmbeddedMeta{}, false
	}
	return meta, meta.Version != "" && meta.DeploymentSHA256 != ""
}

// WriteFileAtomically writes data to path via a sibling temp file, preserving
// existing file permissions or defaulting to 0644 when creating a new file.
func WriteFileAtomically(path string, data []byte) error {
	return WriteFileWithMode(path, data, 0o644)
}

// WriteFileWithMode writes data atomically to path, preserving the existing
// file mode if path already exists and otherwise applying defaultPerm.
func WriteFileWithMode(path string, data []byte, defaultPerm os.FileMode) error {
	perm := defaultPerm
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}
	dirPerm := os.FileMode(0o755)
	if perm&0o077 == 0 {
		dirPerm = 0o700
	}
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
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
	if err := tmp.Chmod(perm); err != nil {
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

// EnsureNoSymlinksUnderRoot verifies that targetPath resides strictly inside
// root and that no path component beneath root (including targetPath itself, if
// it exists) is a symbolic link. This prevents untrusted repository checkouts
// from tricking `grain skill install --project [--mcp]` into reading user-level
// MCP configs (e.g. .mcp.json -> ~/.claude.json) into the repository worktree
// or writing files outside the project root via directory symlinks.
func EnsureNoSymlinksUnderRoot(root, targetPath string) error {
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolving project root %s: %w", root, err)
	}
	cleanTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return fmt.Errorf("resolving target path %s: %w", targetPath, err)
	}
	rel, err := filepath.Rel(cleanRoot, cleanTarget)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("refusing project target %s outside project root %s", cleanTarget, cleanRoot)
	}
	cur := cleanRoot
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return fmt.Errorf("inspecting %s: %w", cur, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to follow symlink %s inside project directory %s", cur, cleanRoot)
		}
	}
	return nil
}

// SanitizeContextFlag strips control characters and backticks from a context
// name rendered into SKILL.md inline code spans.
func SanitizeContextFlag(name string) string {
	return cleanInline(name)
}

func cleanInline(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ", "`", "'").Replace(s)
	return strings.TrimSpace(s)
}

// ScrubServerURL strips any embedded userinfo credentials from rawURL.
func ScrubServerURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	u, err := url.Parse(trimmed)
	if err != nil || u.User == nil {
		return strings.TrimRight(trimmed, "/")
	}
	u.User = nil
	return strings.TrimRight(u.String(), "/")
}

// RegisterMCPServer registers `grain mcp [--context <ctx>]` in the target
// agent's JSON or Codex TOML MCP configuration file (#XC-31.3).
func RegisterMCPServer(isCodex bool, configPath, grainBin, contextName string) error {
	args := []string{"mcp"}
	if c := strings.TrimSpace(contextName); c != "" {
		args = []string{"mcp", "--context", c}
	}
	if isCodex || strings.HasSuffix(configPath, ".toml") {
		return registerCodexTOML(configPath, grainBin, args)
	}
	return registerJSONMCP(configPath, grainBin, args)
}

func registerJSONMCP(configPath, grainBin string, args []string) error {
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
	return WriteFileWithMode(configPath, append(encoded, '\n'), 0o600)
}

var (
	codexGrainTableHeader = regexp.MustCompile(`(?m)^\s*\[mcp_servers\.(?:grain|"grain")\]\s*(?:#.*)?$`)
	anyTOMLTableHeader    = regexp.MustCompile(`(?m)^\s*\[\[?[^\]\r\n]+\]\]?\s*(?:#.*)?$`)
)

func registerCodexTOML(configPath, grainBin string, args []string) error {
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
	return WriteFileWithMode(configPath, []byte(updated), 0o600)
}

// FormatDeploymentSection renders the live deployment metadata section of
// SKILL.md and returns its SHA-256 hex digest (#XC-30.2, #XC-30.3).
func FormatDeploymentSection(ctxLabel, baseURL string, who ui.AuthWhoamiResponse, cfg ui.SkillDeploymentConfig, templates []ui.Template) (section, digest string) {
	repos := reachableRepos(who, cfg)
	designPresent := detectDesignWorkflow(cfg, templates)

	var b strings.Builder
	b.WriteString("## 5. Active Deployment Metadata\n\n")
	fmt.Fprintf(&b, "- **Context:** `%s`\n", cleanInline(ctxLabel))
	fmt.Fprintf(&b, "- **Server:** `%s`\n", cleanInline(ScrubServerURL(baseURL)))
	if envName := cleanInline(cfg.EnvironmentName); envName != "" {
		fmt.Fprintf(&b, "- **Deployment name:** %s\n", envName)
	}
	fmt.Fprintf(&b, "- **Principal:** `%s`\n", cleanInline(who.Principal))
	if owner := cleanInline(who.Owner); owner != "" {
		fmt.Fprintf(&b, "- **Owner (`on behalf of`):** `%s`\n", owner)
	}
	if scopeSet := cleanInline(who.ScopeSet); scopeSet != "" {
		fmt.Fprintf(&b, "- **Scope set:** `%s`\n", scopeSet)
	}
	if len(who.ResolvedScopes) > 0 {
		scopes := make([]string, 0, len(who.ResolvedScopes))
		for _, sc := range who.ResolvedScopes {
			if c := cleanInline(sc); c != "" {
				scopes = append(scopes, c)
			}
		}
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
			cr := cleanInline(r)
			if cfg.DefaultTarget != "" && strings.EqualFold(r, cfg.DefaultTarget) {
				fmt.Fprintf(&b, "- `%s` (default target)\n", cr)
			} else {
				fmt.Fprintf(&b, "- `%s`\n", cr)
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
			id := cleanInline(t.ID)
			name := cleanInline(t.Name)
			line := fmt.Sprintf("- `%s`", id)
			if name != "" && name != id {
				line += " (" + name + ")"
			}
			if repo := cleanInline(t.Repo); repo != "" {
				line += " [repo: `" + repo + "`]"
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
			desc := fmt.Sprintf("- `%s` (`%s`", cleanInline(f.Name), cleanInline(f.Type))
			if f.Multiple {
				desc += ", repeatable"
			}
			desc += ")"
			if len(f.Options) > 0 {
				opts := make([]string, 0, len(f.Options))
				for _, opt := range f.Options {
					opts = append(opts, cleanInline(opt))
				}
				desc += ": `" + strings.Join(opts, "`, `") + "`"
			}
			b.WriteString(desc + "\n")
		}
	}

	rendered := b.String()
	sum := sha256.Sum256([]byte(rendered))
	return rendered, hex.EncodeToString(sum[:])
}

func reachableRepos(who ui.AuthWhoamiResponse, cfg ui.SkillDeploymentConfig) []string {
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
