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

package hostd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// AllowlistTemplateVersion is the schema version of AllowlistTemplate.
const AllowlistTemplateVersion = 1

// InstallTemplateEndpoint and query constants for fetching the controller's
// agentd allowlist template from /hostd/install.
const (
	InstallTemplateEndpoint = "/hostd/install"
	InstallTemplateQuery    = "template"
	InstallTemplateAgentd   = "agentd"
	// AgentdTemplateEnv carries an inline JSON AllowlistTemplate from the
	// hosted shell installer (/hostd/install --agentd) into `grain-agentd install`.
	AgentdTemplateEnv = "GRAIN_AGENTD_TEMPLATE"
)

const (
	maxTemplateCLIs  = 64
	maxTemplateBytes = 1 << 20
)

// AllowlistTemplate is the controller-served allowlist template for
// `grain-agentd install` and `grain-agentd allow`. CLI knowledge -- which
// agent CLIs exist, candidate binary paths, and environment policies -- is
// built daemon-side from the CLIs the deployment knows and served as data, so
// adding or changing a CLI never needs a new host binary.
type AllowlistTemplate struct {
	Version int                `json:"version"`
	Server  string             `json:"server,omitempty"`
	CLIs    []CLITemplateEntry `json:"clis"`
}

// CLITemplateEntry describes one agent CLI entry the controller knows how to
// run on a host.
type CLITemplateEntry struct {
	// Name is the allowlist clis.<name> key ("agy", "claude", "codex").
	Name string `json:"name"`
	// Aliases are alternate CLI or framework spellings accepted by
	// `grain-agentd allow <name>` (for example "antigravity" for "agy").
	Aliases []string `json:"aliases,omitempty"`
	// Candidates are where the host looks for this CLI's executable binary, in
	// order: a clean absolute path, a "~/"-prefixed path under the user's home,
	// or a bare binary name looked up on $PATH.
	Candidates []string `json:"candidates"`
	// Env is the closed env: list for hosts without FeatureEnvOpen, and the
	// source of any exact denied variable names (such as agy's HOME) kept
	// alongside "*" when the target host advertises FeatureEnvOpen.
	Env []string `json:"env,omitempty"`
	// PassEnv are host environment variable names forwarded into this CLI's
	// processes (for example HTTP_PROXY, HTTPS_PROXY, NO_PROXY).
	PassEnv []string `json:"pass_env,omitempty"`
}

// ResolvedCLI pairs a CLITemplateEntry with the executable path found on a host.
type ResolvedCLI struct {
	Entry CLITemplateEntry
	Path  string
}

var (
	cliTemplateMu    sync.RWMutex
	extraCandidates  = map[string][]string{}
	extraCLITemplate []CLITemplateEntry
)

// RegisterCLICandidates prepends candidate binary paths for CLI name on the
// controller when building AllowlistTemplate (for an edition whose machines
// install a CLI somewhere outside $PATH and ~/.local/bin). Calling the
// returned function removes the registration.
func RegisterCLICandidates(name string, paths ...string) (unregister func()) {
	name = strings.TrimSpace(name)
	if !validName(name) {
		panic(fmt.Sprintf("hostd: RegisterCLICandidates(%q): invalid CLI name", name))
	}
	cleaned := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if err := validateCandidate(p); err != nil {
			panic(fmt.Sprintf("hostd: RegisterCLICandidates(%q, %q): %v", name, p, err))
		}
		cleaned = append(cleaned, p)
	}
	cliTemplateMu.Lock()
	prev := slices.Clone(extraCandidates[name])
	extraCandidates[name] = append(slices.Clone(prev), cleaned...)
	cliTemplateMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			cliTemplateMu.Lock()
			defer cliTemplateMu.Unlock()
			if len(prev) == 0 {
				delete(extraCandidates, name)
			} else {
				extraCandidates[name] = slices.Clone(prev)
			}
		})
	}
}

// RegisteredCLICandidates returns any extra candidate paths registered for name.
func RegisteredCLICandidates(name string) []string {
	cliTemplateMu.RLock()
	defer cliTemplateMu.RUnlock()
	return slices.Clone(extraCandidates[strings.TrimSpace(name)])
}

// RegisterCLITemplate adds or overrides a CLITemplateEntry on the controller's
// allowlist template, returning an unregister function for tests.
func RegisterCLITemplate(entry CLITemplateEntry) (unregister func()) {
	if err := entry.Validate(); err != nil {
		panic(fmt.Sprintf("hostd: RegisterCLITemplate(%q): %v", entry.Name, err))
	}
	cloned := cloneCLITemplateEntry(entry)
	cliTemplateMu.Lock()
	prev := slices.Clone(extraCLITemplate)
	replaced := false
	for i := range extraCLITemplate {
		if extraCLITemplate[i].Name == cloned.Name {
			extraCLITemplate[i] = cloned
			replaced = true
			break
		}
	}
	if !replaced {
		extraCLITemplate = append(extraCLITemplate, cloned)
	}
	cliTemplateMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			cliTemplateMu.Lock()
			defer cliTemplateMu.Unlock()
			extraCLITemplate = slices.Clone(prev)
		})
	}
}

// RegisteredCLITemplates returns a copy of all edition-registered CLI template entries.
func RegisteredCLITemplates() []CLITemplateEntry {
	cliTemplateMu.RLock()
	defer cliTemplateMu.RUnlock()
	out := make([]CLITemplateEntry, len(extraCLITemplate))
	for i, e := range extraCLITemplate {
		out[i] = cloneCLITemplateEntry(e)
	}
	return out
}

func cloneCLITemplateEntry(e CLITemplateEntry) CLITemplateEntry {
	return CLITemplateEntry{
		Name:       e.Name,
		Aliases:    slices.Clone(e.Aliases),
		Candidates: slices.Clone(e.Candidates),
		Env:        slices.Clone(e.Env),
		PassEnv:    slices.Clone(e.PassEnv),
	}
}

// Validate checks that e carries safe CLI names, candidate paths, and env patterns.
func (e CLITemplateEntry) Validate() error {
	if !validName(e.Name) {
		return fmt.Errorf("cli template entry %q: invalid CLI name", e.Name)
	}
	for _, a := range e.Aliases {
		if !validName(a) {
			return fmt.Errorf("cli template %q: invalid alias %q", e.Name, a)
		}
	}
	if len(e.Candidates) == 0 {
		return fmt.Errorf("cli template %q: candidates list is empty", e.Name)
	}
	for _, c := range e.Candidates {
		if err := validateCandidate(c); err != nil {
			return fmt.Errorf("cli template %q: candidate %q: %w", e.Name, c, err)
		}
	}
	for _, n := range e.Env {
		if !validEnvPattern(n) {
			return fmt.Errorf("cli template %q: env %q is not a valid variable name, NAME_* pattern or *", e.Name, n)
		}
	}
	for _, n := range e.PassEnv {
		if !validEnvName(n) {
			return fmt.Errorf("cli template %q: pass_env %q is not a valid variable name", e.Name, n)
		}
	}
	return nil
}

// Validate checks that t is a well-formed, non-empty AllowlistTemplate with
// unique CLI names and valid candidate paths and env policies.
func (t AllowlistTemplate) Validate() error {
	if t.Version < 0 {
		return fmt.Errorf("allowlist template: negative version %d", t.Version)
	}
	if len(t.CLIs) == 0 {
		return errors.New("allowlist template: clis list is empty")
	}
	if len(t.CLIs) > maxTemplateCLIs {
		return fmt.Errorf("allowlist template: too many clis (%d > %d)", len(t.CLIs), maxTemplateCLIs)
	}
	if strings.ContainsAny(t.Server, "\r\n\x00") {
		return errors.New("allowlist template: invalid server URL")
	}
	seen := make(map[string]bool, len(t.CLIs))
	for _, e := range t.CLIs {
		if err := e.Validate(); err != nil {
			return err
		}
		key := strings.ToLower(e.Name)
		if seen[key] {
			return fmt.Errorf("allowlist template: duplicate CLI %q", e.Name)
		}
		seen[key] = true
	}
	return nil
}

// Names returns the canonical CLI names in t, in order.
func (t AllowlistTemplate) Names() []string {
	out := make([]string, len(t.CLIs))
	for i, e := range t.CLIs {
		out[i] = e.Name
	}
	return out
}

// Lookup finds the entry matching name (by canonical Name first, then Aliases,
// case-insensitively).
func (t AllowlistTemplate) Lookup(name string) (CLITemplateEntry, bool) {
	want := strings.TrimSpace(name)
	if want == "" {
		return CLITemplateEntry{}, false
	}
	for _, e := range t.CLIs {
		if strings.EqualFold(e.Name, want) {
			return cloneCLITemplateEntry(e), true
		}
	}
	for _, e := range t.CLIs {
		for _, a := range e.Aliases {
			if strings.EqualFold(a, want) {
				return cloneCLITemplateEntry(e), true
			}
		}
	}
	return CLITemplateEntry{}, false
}

func validateCandidate(c string) error {
	if c == "" || strings.ContainsAny(c, "\r\n\t\x00") {
		return errors.New("empty or contains control characters")
	}
	if rest, ok := strings.CutPrefix(c, "~/"); ok {
		if rest == "" || filepath.Clean("/"+rest) != "/"+rest {
			return errors.New("must be a clean ~/-relative file path")
		}
		return nil
	}
	if strings.HasPrefix(c, "~") {
		return errors.New("home-relative path must start with ~/")
	}
	if filepath.IsAbs(c) {
		if c == "/" || filepath.Clean(c) != c {
			return errors.New("must be a clean absolute file path")
		}
		return nil
	}
	if strings.ContainsRune(c, '/') || c == "." || c == ".." {
		return errors.New("relative candidate must be a bare binary name for $PATH lookup")
	}
	for _, r := range c {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == '+') {
			return fmt.Errorf("invalid character %q in binary name", r)
		}
	}
	return nil
}

// ResolveCandidate returns the first candidate in candidates that resolves to
// an executable regular file on this host, or "" when none exists.
func ResolveCandidate(candidates []string, home string, lookPath func(string) (string, error), isExec func(string) bool) string {
	if isExec == nil {
		return ""
	}
	for _, raw := range candidates {
		cand := strings.TrimSpace(raw)
		if cand == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(cand, "~/"); ok {
			if home == "" || rest == "" {
				continue
			}
			full := filepath.Join(home, rest)
			if isExec(full) {
				return full
			}
			continue
		}
		if filepath.IsAbs(cand) {
			clean := filepath.Clean(cand)
			if isExec(clean) {
				return clean
			}
			continue
		}
		if !strings.ContainsRune(cand, '/') && lookPath != nil {
			if p, err := lookPath(cand); err == nil {
				if abs, err := filepath.Abs(p); err == nil && isExec(abs) {
					return abs
				}
			}
		}
	}
	return ""
}

// ResolveAll returns every CLI in t whose binary exists on this machine, in
// template order.
func (t AllowlistTemplate) ResolveAll(home string, lookPath func(string) (string, error), isExec func(string) bool) []ResolvedCLI {
	var out []ResolvedCLI
	for _, e := range t.CLIs {
		if p := ResolveCandidate(e.Candidates, home, lookPath, isExec); p != "" {
			out = append(out, ResolvedCLI{Entry: cloneCLITemplateEntry(e), Path: p})
		}
	}
	return out
}

// RenderCLIEntry renders one clis: allowlist entry for entry at binPath
// confined to stage, emitting only keys and syntax peer advertises
// (condensing env to "*" and shortening paths under home to "~/" only when
// peer advertises FeatureEnvOpen).
func RenderCLIEntry(entry CLITemplateEntry, binPath, home, stage string, peer Hello) string {
	if !peer.Has(FeatureEnvOpen) && home != "" {
		binPath = expandHomeWith(home, binPath)
		stage = expandHomeWith(home, stage)
	}
	closedEnv := make([]string, 0, len(entry.Env))
	for _, v := range entry.Env {
		if v == "*" && !peer.Has(FeatureEnvOpen) {
			continue
		}
		closedEnv = append(closedEnv, v)
	}
	var roots []string
	if stage != "" {
		roots = []string{stage}
	}
	return CLIEntryYAML(entry.Name, binPath, home, roots, closedEnv, entry.PassEnv, peer)
}

// RenderAgentdAllowlist renders a complete agents-only allowlist from resolved
// CLIs, targeting a host agent that advertises peer, and validates that the
// resulting YAML parses cleanly with no capabilities.
func RenderAgentdAllowlist(program, server, home, stage string, resolved []ResolvedCLI, peer Hello) (string, error) {
	if len(resolved) == 0 {
		return "", errors.New("no resolved agent CLIs to write")
	}
	if program == "" {
		program = "grain-agentd"
	}
	var b strings.Builder
	if server != "" {
		fmt.Fprintf(&b, "# %s's allowlist: the agent CLIs the grain daemon at\n# %s may start on this machine, as you. %s re-reads it on\n# every request. %s runs agent CLIs only: capabilities: stays empty.\n", program, server, program, program)
	} else {
		fmt.Fprintf(&b, "# %s's allowlist: the agent CLIs the grain daemon may start on\n# this machine, as you. %s re-reads it on every request.\n# %s runs agent CLIs only: capabilities: stays empty.\n", program, program, program)
	}
	b.WriteString("clis:\n")
	for _, r := range resolved {
		if err := r.Entry.Validate(); err != nil {
			return "", err
		}
		b.WriteString(RenderCLIEntry(r.Entry, r.Path, home, stage, peer))
	}
	b.WriteString("capabilities: {}\n")
	rendered := b.String()
	if err := validateAgentsOnlyYAML([]byte(rendered), program); err != nil {
		return "", err
	}
	return rendered, nil
}

// UpsertAgentdAllowlistCLI adds or updates resolved in existing allowlist YAML
// (or creates a new allowlist when existing is empty), preserving existing
// comments, entries and formatting, and writing only keys peer advertises.
func UpsertAgentdAllowlistCLI(existing []byte, program, server, home, stage string, resolved ResolvedCLI, peer Hello) (updated []byte, snippet string, unchanged bool, err error) {
	if err := resolved.Entry.Validate(); err != nil {
		return nil, "", false, err
	}
	if program == "" {
		program = "grain-agentd"
	}
	snippet = RenderCLIEntry(resolved.Entry, resolved.Path, home, stage, peer)
	if len(bytes.TrimSpace(existing)) == 0 {
		text, err := RenderAgentdAllowlist(program, server, home, stage, []ResolvedCLI{resolved}, peer)
		if err != nil {
			return nil, "", false, err
		}
		return []byte(text), snippet, false, nil
	}

	orig, err := ParseAllowlist(existing)
	if err != nil {
		return nil, "", false, err
	}
	if len(orig.Capabilities) > 0 {
		names := slices.Sorted(maps.Keys(orig.Capabilities))
		return nil, "", false, fmt.Errorf("allowlist names capabilities (%s): %s runs agent CLIs only", strings.Join(names, ", "), program)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(existing, &doc); err != nil {
		return nil, "", false, fmt.Errorf("parsing allowlist YAML: %w", err)
	}
	var patchDoc yaml.Node
	if err := yaml.Unmarshal([]byte("clis:\n"+snippet), &patchDoc); err != nil {
		return nil, "", false, fmt.Errorf("parsing rendered CLI snippet: %w", err)
	}
	if len(patchDoc.Content) == 0 || len(patchDoc.Content[0].Content) < 2 || len(patchDoc.Content[0].Content[1].Content) < 2 {
		return nil, "", false, errors.New("internal error: rendered CLI snippet produced no YAML entry")
	}
	patchCLIs := patchDoc.Content[0].Content[1]
	newKey, newVal := patchCLIs.Content[0], patchCLIs.Content[1]

	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		text, err := RenderAgentdAllowlist(program, server, home, stage, []ResolvedCLI{resolved}, peer)
		if err != nil {
			return nil, "", false, err
		}
		return []byte(text), snippet, false, nil
	}
	root := doc.Content[0]

	var clisVal *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Kind == yaml.ScalarNode && root.Content[i].Value == "clis" {
			clisVal = root.Content[i+1]
			break
		}
	}
	if clisVal == nil {
		clisKey := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "clis"}
		clisVal = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{newKey, newVal}}
		root.Content = append([]*yaml.Node{clisKey, clisVal}, root.Content...)
	} else {
		if clisVal.Kind != yaml.MappingNode {
			clisVal.Kind = yaml.MappingNode
			clisVal.Tag = "!!map"
			clisVal.Value = ""
			clisVal.Content = nil
		}
		clisVal.Style = 0
		replaced := false
		for j := 0; j+1 < len(clisVal.Content); j += 2 {
			if clisVal.Content[j].Kind == yaml.ScalarNode && clisVal.Content[j].Value == resolved.Entry.Name {
				if yamlNodesEqual(clisVal.Content[j+1], newVal) {
					return existing, snippet, true, nil
				}
				clisVal.Content[j+1] = newVal
				replaced = true
				break
			}
		}
		if !replaced {
			clisVal.Content = append(clisVal.Content, newKey, newVal)
		}
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		_ = enc.Close()
		return nil, "", false, fmt.Errorf("encoding allowlist YAML: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, "", false, fmt.Errorf("encoding allowlist YAML: %w", err)
	}
	out := buf.Bytes()
	if err := validateAgentsOnlyYAML(out, program); err != nil {
		return nil, "", false, err
	}
	return out, snippet, bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace(out)), nil
}

func yamlNodesEqual(a, b *yaml.Node) bool {
	if a == nil || b == nil {
		return a == b
	}
	ab, errA := yaml.Marshal(a)
	bb, errB := yaml.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ab, bb)
}

func validateAgentsOnlyYAML(data []byte, program string) error {
	a, err := ParseAllowlist(data)
	if err != nil {
		return err
	}
	if len(a.Capabilities) > 0 {
		names := slices.Sorted(maps.Keys(a.Capabilities))
		return fmt.Errorf("allowlist names capabilities (%s): %s runs agent CLIs only", strings.Join(names, ", "), program)
	}
	if len(a.CLIs) == 0 {
		return errors.New("allowlist offers no agent CLIs")
	}
	return nil
}

// InstallTemplateURL builds the GET URL on serverURL that serves AllowlistTemplate.
func InstallTemplateURL(serverURL string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if base == "" {
		return "", errors.New("server URL is required")
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("invalid server URL %q", serverURL)
	}
	u.Path = strings.TrimRight(u.Path, "/") + InstallTemplateEndpoint
	q := u.Query()
	q.Set(InstallTemplateQuery, InstallTemplateAgentd)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// FetchTemplateOptions configures FetchAllowlistTemplate.
type FetchTemplateOptions struct {
	ServerURL  string
	HTTPClient *http.Client
	IDToken    func(context.Context) (string, error)
	Timeout    time.Duration
}

// FetchAllowlistTemplate fetches and validates the controller's agentd
// allowlist template from /hostd/install?template=agentd.
func FetchAllowlistTemplate(ctx context.Context, opts FetchTemplateOptions) (AllowlistTemplate, error) {
	endpoint, err := InstallTemplateURL(opts.ServerURL)
	if err != nil {
		return AllowlistTemplate{}, err
	}
	httpClient := opts.HTTPClient
	idToken := opts.IDToken
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return AllowlistTemplate{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "grain-agentd")
	if idToken != nil {
		tok, err := idToken(reqCtx)
		if err != nil {
			return AllowlistTemplate{}, fmt.Errorf("obtaining identity token: %w", err)
		}
		if tok = strings.TrimSpace(strings.TrimPrefix(tok, "Bearer ")); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return AllowlistTemplate{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTemplateBytes))
	if err != nil {
		return AllowlistTemplate{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return AllowlistTemplate{}, fmt.Errorf("GET %s returned HTTP %d", endpoint, resp.StatusCode)
	}
	return ParseAllowlistTemplate(body)
}

// ParseAllowlistTemplate decodes and validates a JSON AllowlistTemplate.
func ParseAllowlistTemplate(data []byte) (AllowlistTemplate, error) {
	var tmpl AllowlistTemplate
	if err := json.Unmarshal(data, &tmpl); err != nil {
		return AllowlistTemplate{}, fmt.Errorf("decoding allowlist template: %w", err)
	}
	if err := tmpl.Validate(); err != nil {
		return AllowlistTemplate{}, err
	}
	return tmpl, nil
}
