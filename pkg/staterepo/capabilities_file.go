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

package staterepo

// settings/capabilities.yml: the capability catalogue
// (design/design/config-repo-settings.design.md §3), one file standing for what
// is otherwise five places -- grain_config.mcp_servers,
// grain_config.capability_policy, grain_config.default_capabilities,
// api_scope_set, and repo_config.{default_capabilities,ci_capabilities}.
//
// Unlike settings/templates.yml it is not a table: it is a projection
// onto columns of tables that hold other things too. So it is read the
// way every other reader in this package reads a dump -- through
// dumpRowsOf -- by overlaying the rows tables/ holds for those three
// tables with what the file says. The file wins wherever it speaks, and
// a top-level section it leaves out leaves its columns as tables/ has
// them, which is what lets a deployment adopt the file one section at a
// time.
//
// The restrictions themselves (which repositories, templates and kinds
// may hold a capability, which need a person) have no column of their
// own: they are kept in grain_config.capability_policy beside its rules,
// as CapabilityPolicy.Catalogue, so adopting the file needs no
// migration. That field is also what says the deployment has adopted
// it: an export writes settings/capabilities.yml exactly when the
// stored policy carries a catalogue, and removes a stale one otherwise.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

// CapabilitiesFile is the catalogue's name inside SettingsDir.
const CapabilitiesFile = "capabilities.yml"

// CapabilitiesPath is the catalogue's path in the repository.
const CapabilitiesPath = SettingsDir + "/" + CapabilitiesFile

// capabilitiesDoc is settings/capabilities.yml as written. Every
// top-level section is a pointer, so a section left out is told from
// one written empty.
type capabilitiesDoc struct {
	Defaults  *capabilitiesDefaults               `yaml:"defaults,omitempty"`
	Servers   *[]capabilityServerEntry            `yaml:"servers,omitempty"`
	InProcess map[string]model.CatalogueInProcess `yaml:"in_process,omitempty"`
	APIScopes *capabilityAPIScopes                `yaml:"api_scopes,omitempty"`
}

type capabilitiesDefaults struct {
	Task  *catalogueGrants                    `yaml:"task,omitempty"`
	CI    *catalogueGrants                    `yaml:"ci,omitempty"`
	Repos *map[string]capabilitiesRepoDefault `yaml:"repos,omitempty"`
}

type capabilitiesRepoDefault struct {
	Task catalogueGrants `yaml:"task,omitempty"`
	CI   catalogueGrants `yaml:"ci,omitempty"`
}

// capabilityServerEntry is one capability server: its transport, which
// is grain_config.mcp_servers' (protocol.ServerConfig), and its methods
// and restrictions, which are the catalogue's.
type capabilityServerEntry struct {
	ID      string            `yaml:"id"`
	Name    string            `yaml:"name,omitempty"`
	URL     string            `yaml:"url,omitempty"`
	Command string            `yaml:"command,omitempty"`
	Args    []string          `yaml:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty"`
	Run     string            `yaml:"run,omitempty"`
	Enabled *bool             `yaml:"enabled,omitempty"`
	Builtin string            `yaml:"builtin,omitempty"`
	Aliases []string          `yaml:"aliases,omitempty"`
	Notice  string            `yaml:"notice,omitempty"`
	// Secrets are configuration values held by reference
	// ($secret:<name>), merged into Config: never a secret's value.
	Secrets      map[string]string             `yaml:"secrets,omitempty"`
	Config       map[string]any                `yaml:"config,omitempty"`
	Methods      []model.CatalogueMethod       `yaml:"methods,omitempty"`
	Restrictions *model.CapabilityRestrictions `yaml:"restrictions,omitempty"`
}

type capabilityAPIScopes struct {
	Presets   *map[string]capabilityPreset            `yaml:"presets,omitempty"`
	RawScopes map[string]model.CapabilityRestrictions `yaml:"raw_scopes,omitempty"`
}

// capabilityPreset is one api:<name>. One with scopes (or includes) is a
// scope set of this deployment's own -- an api_scope_set row; one with
// neither restricts a set grain ships.
type capabilityPreset struct {
	Description  string                        `yaml:"description,omitempty"`
	Scopes       []string                      `yaml:"scopes,omitempty"`
	Includes     []string                      `yaml:"includes,omitempty"`
	ScopeRepos   []string                      `yaml:"scope_repos,omitempty"`
	Deny         []string                      `yaml:"deny,omitempty"`
	Version      int                           `yaml:"version,omitempty"`
	Deprecated   bool                          `yaml:"deprecated,omitempty"`
	Restrictions *model.CapabilityRestrictions `yaml:"restrictions,omitempty"`
}

func (p capabilityPreset) definesSet() bool { return len(p.Scopes) > 0 || len(p.Includes) > 0 }

func (p capabilityPreset) scopeSet(name string) model.APIScopeSet {
	return model.APIScopeSet{
		Name: name, Description: p.Description, Scopes: nonNil(p.Scopes), Includes: nonNil(p.Includes),
		Repos: nonNil(p.ScopeRepos), Deny: nonNil(p.Deny),
		RequiresPerson: p.Restrictions != nil && p.Restrictions.RequiresPerson,
		Version:        p.Version, Deprecated: p.Deprecated,
	}
}

// catalogueGrants is a list of grants, each a bare name or a
// `<reference>: {with: {...}}` entry, read as grant strings
// (model.EncodeCapabilityGrant's form).
type catalogueGrants []string

func (g *catalogueGrants) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.SequenceNode {
		return fmt.Errorf("line %d: a grant list must be a list", n.Line)
	}
	out := catalogueGrants{}
	for _, item := range n.Content {
		switch item.Kind {
		case yaml.ScalarNode:
			out = append(out, strings.TrimSpace(item.Value))
		case yaml.MappingNode:
			if len(item.Content) != 2 {
				return fmt.Errorf("line %d: a grant with arguments is one `<capability>: {with: ...}` entry", item.Line)
			}
			var body struct {
				With map[string]any `yaml:"with"`
			}
			if err := item.Content[1].Decode(&body); err != nil {
				return fmt.Errorf("line %d: %v", item.Line, err)
			}
			s, err := model.EncodeCapabilityGrant(protocol.GrantSpec{Capability: item.Content[0].Value, With: body.With})
			if err != nil {
				return fmt.Errorf("line %d: %v", item.Line, err)
			}
			out = append(out, s)
		default:
			return fmt.Errorf("line %d: a grant is a name or a `<capability>: {with: ...}` entry", item.Line)
		}
	}
	*g = out
	return nil
}

func (g catalogueGrants) MarshalYAML() (any, error) {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	for _, s := range g {
		spec, err := model.CapabilityGrantSpec(s)
		if !model.IsCapabilityRef(s) || err != nil || len(spec.With) == 0 {
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: s})
			continue
		}
		seq.Style = 0
		var with yaml.Node
		if err := with.Encode(map[string]any{"with": spec.With}); err != nil {
			return nil, err
		}
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: spec.Capability}, &with,
		}})
	}
	return seq, nil
}

// readCapabilitiesDoc parses the file, or reports that there is none.
func readCapabilitiesDoc(dir string) (*capabilitiesDoc, error) {
	data, err := os.ReadFile(filepath.Join(dir, SettingsDir, CapabilitiesFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("staterepo: reading %s: %w", CapabilitiesPath, err)
	}
	return parseCapabilitiesDoc(data)
}

// parseCapabilitiesDoc refuses unknown keys: a misspelt `restrictions`
// that quietly restricted nothing is the worst way this file could fail.
func parseCapabilitiesDoc(data []byte) (*capabilitiesDoc, error) {
	doc := &capabilitiesDoc{}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("staterepo: parsing %s: %w", CapabilitiesPath, err)
	}
	return doc, nil
}

// ValidateCapabilitiesFile checks settings/capabilities.yml's syntax,
// schema and internal consistency from its bytes alone, and returns the
// catalogue it holds.
func ValidateCapabilitiesFile(data []byte) (*model.CapabilityCatalogue, error) {
	doc, err := parseCapabilitiesDoc(data)
	if err != nil {
		return nil, err
	}
	return doc.validate()
}

// validate is the document's consistency: transports that parse,
// secrets held by reference, scope sets that resolve, and everything the
// catalogue checks of itself (model.CapabilityCatalogue.Validate).
func (d *capabilitiesDoc) validate() (*model.CapabilityCatalogue, error) {
	var errs []string
	if d.Servers != nil {
		for i, s := range *d.Servers {
			for _, k := range sortedStringKeys(s.Secrets) {
				if !strings.HasPrefix(s.Secrets[k], model.CatalogueSecretPrefix) {
					errs = append(errs, fmt.Sprintf("servers[%d] (%s): secrets.%s must be a %s<name> reference, never a value",
						i, s.ID, k, model.CatalogueSecretPrefix))
				}
			}
		}
		servers, err := d.mcpServers()
		if err != nil {
			errs = append(errs, err.Error())
		} else if len(servers) > 0 {
			raw, _ := json.Marshal(servers)
			if _, err := protocol.ParseServers(raw); err != nil {
				errs = append(errs, fmt.Sprintf("servers: %v", err))
			}
		}
	}
	if d.APIScopes != nil && d.APIScopes.Presets != nil {
		var sets []model.APIScopeSet
		for _, name := range sortedPresetNames(*d.APIScopes.Presets) {
			if p := (*d.APIScopes.Presets)[name]; p.definesSet() {
				sets = append(sets, p.scopeSet(name))
			}
		}
		if _, err := model.ResolveAPIScopeSets(sets); err != nil {
			errs = append(errs, fmt.Sprintf("api_scopes.presets: %v", err))
		}
	}
	cat := d.catalogue(nil)
	if err := cat.Validate(); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("staterepo: %s: %s", CapabilitiesPath, strings.Join(errs, "; "))
	}
	return cat, nil
}

// mcpServers is the servers section as grain_config.mcp_servers.
func (d *capabilitiesDoc) mcpServers() (map[string]protocol.ServerConfig, error) {
	out := map[string]protocol.ServerConfig{}
	for i, s := range *d.Servers {
		id := strings.TrimSpace(s.ID)
		if id == "" {
			return nil, fmt.Errorf("servers[%d] has no id", i)
		}
		if _, dup := out[id]; dup {
			return nil, fmt.Errorf("servers[%d]: %s is listed twice", i, id)
		}
		cfg := protocol.ServerConfig{
			Command: s.Command, Args: s.Args, Env: s.Env, ServerURL: s.URL, Headers: s.Headers,
			Disabled: s.Enabled != nil && !*s.Enabled, Run: s.Run, Builtin: s.Builtin,
			Aliases: s.Aliases, Notice: s.Notice,
		}
		if len(s.Config) > 0 || len(s.Secrets) > 0 {
			cfg.Config = map[string]any{}
			for k, v := range s.Config {
				cfg.Config[k] = v
			}
			for k, v := range s.Secrets {
				cfg.Config[k] = v
			}
		}
		out[id] = cfg
	}
	return out, nil
}

// catalogue is the document's judgement. prev is the stored catalogue
// whose sections the file leaves out are kept.
func (d *capabilitiesDoc) catalogue(prev *model.CapabilityCatalogue) *model.CapabilityCatalogue {
	c := &model.CapabilityCatalogue{}
	if prev != nil {
		*c = *prev
	}
	if d.Defaults != nil {
		if d.Defaults.Task != nil {
			c.Defaults.Task = *d.Defaults.Task
		}
		if d.Defaults.CI != nil {
			c.Defaults.CI = *d.Defaults.CI
		}
		if d.Defaults.Repos != nil {
			c.Defaults.Repos = map[string]model.CatalogueRepoDefaults{}
			for repo, rd := range *d.Defaults.Repos {
				c.Defaults.Repos[repo] = model.CatalogueRepoDefaults{Task: rd.Task, CI: rd.CI}
			}
		}
	}
	if d.Servers != nil {
		c.Servers = nil
		for _, s := range *d.Servers {
			c.Servers = append(c.Servers, model.CatalogueServer{
				ID: strings.TrimSpace(s.ID), Name: s.Name, Enabled: s.Enabled, Aliases: s.Aliases,
				Methods: s.Methods, Restrictions: s.Restrictions,
			})
		}
	}
	if d.InProcess != nil {
		c.InProcess = d.InProcess
	}
	if d.APIScopes != nil {
		if d.APIScopes.Presets != nil {
			c.APIScopes.Presets = map[string]model.CataloguePreset{}
			for name, p := range *d.APIScopes.Presets {
				if p.Restrictions != nil {
					c.APIScopes.Presets[name] = model.CataloguePreset{Restrictions: p.Restrictions}
				}
			}
		}
		if d.APIScopes.RawScopes != nil {
			c.APIScopes.RawScopes = d.APIScopes.RawScopes
		}
	}
	return c
}

// The tables the catalogue projects onto.
const (
	grainConfigTable = "grain_config"
	repoConfigTable  = "repo_config"
	apiScopeSetTable = "api_scope_set"
)

// overlayCapabilities is dumpRowsOf's last step for the three tables
// settings/capabilities.yml projects onto: rows as tables/ holds them,
// with what the file says put over them.
func overlayCapabilities(dir, table string, rows []map[string]any, present bool) ([]map[string]any, bool, error) {
	if table != grainConfigTable && table != repoConfigTable && table != apiScopeSetTable {
		return rows, present, nil
	}
	doc, err := readCapabilitiesDoc(dir)
	if err != nil || doc == nil {
		return rows, present, err
	}
	switch table {
	case grainConfigTable:
		return overlayGrainConfig(doc, rows, present)
	case repoConfigTable:
		if doc.Defaults == nil || doc.Defaults.Repos == nil {
			return rows, present, nil
		}
		return overlayRepoConfig(*doc.Defaults.Repos, rows), true, nil
	default:
		if doc.APIScopes == nil || doc.APIScopes.Presets == nil {
			return rows, present, nil
		}
		return scopeSetRows(*doc.APIScopes.Presets), true, nil
	}
}

func overlayGrainConfig(doc *capabilitiesDoc, rows []map[string]any, present bool) ([]map[string]any, bool, error) {
	if !present || len(rows) == 0 {
		// A deployment's configuration row is grain's to create; a file
		// cannot conjure one.
		return rows, present, nil
	}
	out := make([]map[string]any, len(rows))
	for i, row := range rows {
		cp := copyRow(row)
		policyText, _ := textOf(cp["capability_policy"])
		policy, err := model.DecodeCapabilityPolicy(policyText)
		if err != nil {
			return nil, false, fmt.Errorf("staterepo: %s: %w", DumpPath(grainConfigTable), err)
		}
		policy.Catalogue = doc.catalogue(policy.Catalogue)
		encoded, err := model.EncodeCapabilityPolicy(policy)
		if err != nil {
			return nil, false, err
		}
		cp["capability_policy"] = encoded
		if doc.Servers != nil {
			servers, err := doc.mcpServers()
			if err != nil {
				return nil, false, fmt.Errorf("staterepo: %s: %w", CapabilitiesPath, err)
			}
			text, err := model.EncodeMCPServers(servers)
			if err != nil {
				return nil, false, err
			}
			cp["mcp_servers"] = text
		}
		if doc.Defaults != nil && doc.Defaults.Task != nil {
			cp["default_capabilities"] = model.JoinCapabilities(*doc.Defaults.Task)
		}
		out[i] = cp
	}
	return out, true, nil
}

func overlayRepoConfig(repos map[string]capabilitiesRepoDefault, rows []map[string]any) []map[string]any {
	want := map[string]capabilitiesRepoDefault{}
	for repo, d := range repos {
		want[strings.ToLower(repo)] = d
	}
	seen := map[string]bool{}
	var out []map[string]any
	for _, row := range rows {
		cp := copyRow(row)
		k := strings.ToLower(fmt.Sprint(row["owner"]) + "/" + fmt.Sprint(row["name"]))
		d := want[k]
		seen[k] = true
		cp["default_capabilities"] = model.JoinCapabilities(d.Task)
		cp["ci_capabilities"] = model.JoinCapabilities(d.CI)
		out = append(out, cp)
	}
	for _, repo := range sortedRepoNames(repos) {
		k := strings.ToLower(repo)
		if seen[k] {
			continue
		}
		seen[k] = true
		owner, name, _ := strings.Cut(repo, "/")
		d := repos[repo]
		out = append(out, map[string]any{
			"owner": owner, "name": name,
			"default_capabilities": model.JoinCapabilities(d.Task),
			"ci_capabilities":      model.JoinCapabilities(d.CI),
		})
	}
	return out
}

func copyRow(row map[string]any) map[string]any {
	cp := make(map[string]any, len(row))
	for k, v := range row {
		cp[k] = v
	}
	return cp
}

func scopeSetRows(presets map[string]capabilityPreset) []map[string]any {
	out := []map[string]any{}
	for _, name := range sortedPresetNames(presets) {
		p := presets[name]
		if !p.definesSet() {
			continue
		}
		set := p.scopeSet(name)
		out = append(out, map[string]any{
			"name": set.Name, "description": set.Description,
			"scopes": jsonList(set.Scopes), "includes": jsonList(set.Includes),
			"repos": jsonList(set.Repos), "deny": jsonList(set.Deny),
			"requires_person": json.Number(boolDigit(set.RequiresPerson)),
			"version":         json.Number(fmt.Sprint(set.Version)),
			"deprecated":      json.Number(boolDigit(set.Deprecated)),
		})
	}
	return out
}

func jsonList(s []string) string {
	data, _ := json.Marshal(nonNil(s))
	return string(data)
}

func jsonListOf(v any) []string {
	s, _ := textOf(v)
	var out []string
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func boolDigit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// intValue is intOf without the question of whether it was one.
func intValue(v any) int64 {
	n, _ := intOf(v)
	return n
}

func sortedPresetNames(m map[string]capabilityPreset) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedRepoNames(m map[string]capabilitiesRepoDefault) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedStringKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// exportCapabilities writes settings/capabilities.yml from the database
// when its configuration carries a catalogue, and removes the file when
// it does not.
func exportCapabilities(ctx context.Context, db querier, dir string) error {
	path := filepath.Join(dir, SettingsDir, CapabilitiesFile)
	data, err := capabilitiesDocumentFor(ctx, db)
	if err != nil {
		return err
	}
	if data == nil {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("staterepo: removing %s: %w", CapabilitiesPath, err)
		}
		return nil
	}
	return writeFileIfChanged(path, data)
}

// capabilitiesDocumentFor renders the file, or nil for a deployment that
// has not adopted one.
func capabilitiesDocumentFor(ctx context.Context, db querier) ([]byte, error) {
	cfgRows, err := declTableRows(ctx, db, grainConfigTable)
	if err != nil || len(cfgRows) == 0 {
		return nil, err
	}
	row := cfgRows[0]
	policyText, _ := textOf(row["capability_policy"])
	policy, err := model.DecodeCapabilityPolicy(policyText)
	if err != nil {
		return nil, err
	}
	cat := policy.Catalogue
	if cat == nil {
		return nil, nil
	}
	serversText, _ := textOf(row["mcp_servers"])
	servers, err := model.DecodeMCPServers(serversText)
	if err != nil {
		return nil, err
	}
	defaultsText, _ := textOf(row["default_capabilities"])

	doc := capabilitiesDoc{}
	task := catalogueGrants(nonNil(model.SplitCapabilities(defaultsText)))
	ci := catalogueGrants(nonNil(cat.Defaults.CI))
	repos := map[string]capabilitiesRepoDefault{}
	repoRows, err := declTableRows(ctx, db, repoConfigTable)
	if err != nil {
		return nil, err
	}
	for _, r := range repoRows {
		dc, _ := textOf(r["default_capabilities"])
		cc, _ := textOf(r["ci_capabilities"])
		d := capabilitiesRepoDefault{Task: model.SplitCapabilities(dc), CI: model.SplitCapabilities(cc)}
		if len(d.Task) == 0 && len(d.CI) == 0 {
			continue
		}
		repos[fmt.Sprint(r["owner"])+"/"+fmt.Sprint(r["name"])] = d
	}
	doc.Defaults = &capabilitiesDefaults{Task: &task, CI: &ci, Repos: &repos}

	byID := map[string]model.CatalogueServer{}
	for _, s := range cat.Servers {
		byID[s.ID] = s
	}
	ids := make([]string, 0, len(servers))
	for id := range servers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	entries := []capabilityServerEntry{}
	for _, id := range ids {
		cfg := servers[id]
		meta := byID[id]
		enabled := !cfg.Disabled
		e := capabilityServerEntry{
			ID: id, Name: meta.Name, URL: cfg.ServerURL, Command: cfg.Command, Args: cfg.Args, Env: cfg.Env,
			Headers: cfg.Headers, Run: cfg.Run, Enabled: &enabled, Builtin: cfg.Builtin, Aliases: cfg.Aliases,
			Notice: cfg.Notice, Methods: meta.Methods, Restrictions: meta.Restrictions,
		}
		for k, v := range cfg.Config {
			if s, ok := v.(string); ok && strings.HasPrefix(s, model.CatalogueSecretPrefix) {
				if e.Secrets == nil {
					e.Secrets = map[string]string{}
				}
				e.Secrets[k] = s
				continue
			}
			if e.Config == nil {
				e.Config = map[string]any{}
			}
			e.Config[k] = v
		}
		entries = append(entries, e)
	}
	doc.Servers = &entries
	doc.InProcess = cat.InProcess
	if doc.InProcess == nil {
		doc.InProcess = map[string]model.CatalogueInProcess{}
	}

	presets := map[string]capabilityPreset{}
	setRows, err := declTableRows(ctx, db, apiScopeSetTable)
	if err != nil {
		return nil, err
	}
	for _, r := range setRows {
		desc, _ := textOf(r["description"])
		p := capabilityPreset{
			Description: desc,
			Scopes:      jsonListOf(r["scopes"]), Includes: jsonListOf(r["includes"]),
			ScopeRepos: jsonListOf(r["repos"]), Deny: jsonListOf(r["deny"]),
			Version: int(intValue(r["version"])), Deprecated: intValue(r["deprecated"]) != 0,
		}
		if intValue(r["requires_person"]) != 0 {
			p.Restrictions = &model.CapabilityRestrictions{RequiresPerson: true}
		}
		presets[fmt.Sprint(r["name"])] = p
	}
	for name, cp := range cat.APIScopes.Presets {
		p := presets[name]
		p.Restrictions = cp.Restrictions
		presets[name] = p
	}
	doc.APIScopes = &capabilityAPIScopes{Presets: &presets, RawScopes: cat.APIScopes.RawScopes}

	var buf bytes.Buffer
	buf.WriteString(capabilitiesHeader)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("staterepo: writing %s: %w", CapabilitiesPath, err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("staterepo: writing %s: %w", CapabilitiesPath, err)
	}
	return buf.Bytes(), nil
}

const capabilitiesHeader = `# grain's capability catalogue (design/design/config-repo-settings.design.md §3):
# every capability server, in-process capability and agent API preset a
# run may hold, who may hold it, and what runs hold by default. It is the
# source of grain_config.mcp_servers, capability_policy and
# default_capabilities, of api_scope_set, and of repo_config's
# default_capabilities and ci_capabilities: where this file speaks, it
# wins over tables/. Secrets are named ($secret:<name>), never written.
# Edit it in a pull request; a person approves every change to it.
`

// checkCapabilities is Check's half of the catalogue: the file parses,
// is consistent with itself, and admits every grant a template or a
// schedule in this dump carries.
func checkCapabilities(dir string) error {
	doc, err := readCapabilitiesDoc(dir)
	if err != nil || doc == nil {
		return err
	}
	cat, err := doc.validate()
	if err != nil {
		return err
	}
	var errs []string
	for _, kind := range []struct{ table, grants, parent string }{
		{"template", "template_grant", "template_id"},
		{"schedule", "schedule_grant", "schedule_id"},
	} {
		owners, _, err := dumpRowsOf(dir, kind.table)
		if err != nil {
			return err
		}
		grants, _, err := dumpRowsOf(dir, kind.grants)
		if err != nil {
			return err
		}
		byID := map[string]map[string]any{}
		for _, r := range owners {
			byID[fmt.Sprint(r["id"])] = r
		}
		for _, g := range grants {
			id := fmt.Sprint(g[kind.parent])
			owner := byID[id]
			capability, _ := textOf(g["capability"])
			gc := model.GrantContext{Kind: model.CatalogueKindTask, Grants: []string{capability}}
			if o, _ := textOf(owner["target_owner"]); o != "" {
				n, _ := textOf(owner["target_name"])
				gc.Repo = &model.RepoRef{Owner: o, Name: n}
			}
			if kind.table == "template" {
				gc.TemplateID = id
				gc.TemplateName, _ = textOf(owner["name"])
			} else if t, ok := textOf(owner["template_id"]); ok {
				gc.TemplateID = t
			}
			for _, r := range cat.Refusals(gc) {
				errs = append(errs, fmt.Sprintf("%s %s grants %s: %s", kind.table, id, r.Grant, r.Reason))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("staterepo: %s: %s", CapabilitiesPath, strings.Join(errs, "; "))
	}
	return nil
}
