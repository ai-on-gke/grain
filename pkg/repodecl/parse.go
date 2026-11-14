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

package repodecl

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/declschema"
	"github.com/ai-on-gke/grain/pkg/model"
)

// This file is the YAML surface of the format and nothing else -- the
// structs the decoder fills, and the conversion from each of them into
// the model type the rest of grain works with. pkg/ci's yaml.go keeps
// the same split for the same reason: the file is shaped for whoever
// writes it, hyphens and all, and the type is shaped for its readers.
//
// What these structs no longer hold is the other half of the format: the
// keys a repository may *not* write. Those live in pkg/declschema
// alongside the ones it may, one list for both contexts, and are read
// from there at parse time (refuse below) -- so a field added to
// model.Template has to say in that one list which contexts accept it,
// and cannot land in grain's settings and the settings export while
// `.grain` silently has no way to ask for it (grain/task-339).
//
// Every decoder still runs with KnownFields set, so a key that is
// neither accepted here nor refused there is an error naming the key,
// and the refusals still come first: `grants: [gcp-key]` is answered
// with the reason it is refused, not with "field not found". A key this
// package accepts and the schema does not is a build that fails
// TestTheRawStructsAreTheSchemasRepoKeys, which is what keeps the two
// halves the same format rather than two that resemble each other.

// decode parses one file's YAML into v: the keys a repository may not
// set refused first, with the reason the schema gives, and then the
// typed decode with unknown keys refused.
func decode(f file, kind model.DeclarationKind, v any) error {
	if err := refuse(f, kind); err != nil {
		return err
	}
	return decodeInto(f, v)
}

// decodeInto is the typed half on its own, for the one file that keeps
// its refusals in its own raw structs (lifecycle.go).
func decodeInto(f file, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(f.text))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		err = fmt.Errorf("%s: %w", f.path, err)
		// A file that is well-formed YAML and still does not decode has
		// a key this build does not know, or a known key in a shape it
		// does not know -- which is what a file written for a newer
		// grain looks like to an older one (SchemaError).
		var doc yaml.Node
		if yaml.Unmarshal(f.text, &doc) == nil {
			return &SchemaError{Path: f.path, Err: err}
		}
		return err
	}
	return nil
}

// SchemaError is a declaration file this build cannot decode although it
// is well-formed YAML: an unknown key, or a known key in a shape the
// decoder has no case for (`overlap: {skip: true}` read by a build that
// only knows `overlap: skip`). Its message is the decoder's own, unchanged.
//
// It is told apart from every other error because it is the one a
// deployment meets when a repository's `.grain` is newer than the daemon
// reading it, and LoadForwardCompatible holds back only that file rather
// than refusing the whole set (docs/declarations.md, "Declarations newer
// than the daemon").
type SchemaError struct {
	Path string
	Err  error
}

func (e *SchemaError) Error() string { return e.Err.Error() }
func (e *SchemaError) Unwrap() error { return e.Err }

// refuse is the whole of what a repository may not say about itself,
// checked against declschema rather than against a list kept here.
//
// One sentence per reason rather than one per key: a file that sets
// `reads` and `grants` has made one mistake, and hearing the rule twice
// does not help. A file that has made two different mistakes hears both.
func refuse(f file, kind model.DeclarationKind) error {
	keys, err := topLevelKeys(f)
	if err != nil {
		return err
	}
	var errs []error
	for _, group := range declschema.Group(declschema.RepoRefusals(kind), keys) {
		errs = append(errs, fmt.Errorf("%s: %s: %s", f.path, group, group.Why.Reason()))
	}
	return errors.Join(errs...)
}

// topLevelKeys is the keys one file actually sets, read before anything
// is decoded into a type.
//
// A file that is empty, or that is not a mapping at all, has no keys to
// refuse and no error to give here: the typed decode is what says what
// is wrong with it, in the words it already used.
func topLevelKeys(f file) ([]string, error) {
	var doc yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(f.text))
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %w", f.path, err)
	}
	node := &doc
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil, nil
		}
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return nil, nil
	}
	var keys []string
	for i := 0; i+1 < len(node.Content); i += 2 {
		keys = append(keys, node.Content[i].Value)
	}
	return keys, nil
}

// --- templates ------------------------------------------------------------

type rawTemplate struct {
	Name      string     `yaml:"name"`
	Title     string     `yaml:"title"`
	Body      string     `yaml:"body"`
	Prompt    string     `yaml:"prompt"`
	Fields    []rawField `yaml:"fields"`
	Base      string     `yaml:"base"`
	Team      string     `yaml:"team"`
	AutoMerge *bool      `yaml:"auto-merge"`
	Network   string     `yaml:"network"`
	Model     string     `yaml:"model"`
	// Interactive is model.Template.Interactive: every task filed from
	// this template is a live chat.
	Interactive bool `yaml:"interactive"`
	// KeepRunning is model.Template.KeepRunning: every task filed from
	// this template immediately restarts when its run exits.
	KeepRunning bool `yaml:"keep-running"`
	// Admission is model.Template.Admission: "standard", "bypass" or "durable"
	// (empty for unset).
	Admission string `yaml:"admission"`
	// Hosting is model.Template.Hosting: "unattended" or "console"
	// (empty for unset).
	Hosting string `yaml:"hosting"`
	// Skills is model.Template.Skills: optional per-task skill discovery
	// override for tasks filed from this template.
	Skills *rawSkills `yaml:"skills"`
	// Priority is model.Template.Priority: optional default priority for
	// tasks filed from this template.
	Priority *int `yaml:"priority"`
	// Sandbox is model.Template.Sandbox: the sandbox shape every task
	// filed from this template asks for, under a workflow job's own
	// `sandbox:` keys (grain/task-4591).
	Sandbox *rawSandbox `yaml:"sandbox"`
	// NothingNeeded is model.Template.NothingNeededAction: "close" (what
	// empty means) or "ack".
	NothingNeeded string              `yaml:"nothing-needed"`
	CustomFields  map[string]rawValue `yaml:"custom-fields"`
	// Capabilities is the repository access every task filed from this
	// template carries, and only that: `repo-read:<owner>/<repo>` and
	// `repo-write:<owner>/<repo>` (parseRepoAccess).
	Capabilities model.CapabilityList `yaml:"capabilities"`
	// Grants is the unified spelling of Capabilities
	// (design/design/unified-grants.design.md §3): the same entries, held
	// to the same rule, read as one list with it (declaredGrants).
	Grants model.CapabilityList `yaml:"grants"`
	// Push is the refs beyond their own branch that the runs of tasks
	// filed from this template may update in this repository
	// (model.RefPush, grain/task-2536; parsePush).
	Push []rawPush `yaml:"push"`
}

// rawSandbox is a template's `sandbox:` block -- the same three keys, with
// the same meaning, as a `.grain/workflows` job's own `sandbox:`
// (pkg/workflows):
//
//	sandbox:
//	  cpus: 4
//	  memory-mb: 16384
//	  disk-gb: 100
//
// Each is optional, and pointers so that a key written as 0 is told
// apart from one left out: 0 is refused, because a dimension nobody
// wants to change is one to leave out.
type rawSandbox struct {
	CPUs     *int
	MemoryMB *int
	DiskGB   *int
}

// rawSandboxKeys is what the block may say. Anything else is refused by
// name, which -- being a decode error -- is a SchemaError: a block with
// a key a newer grain added is held back by an older daemon rather than
// refusing the whole repository (docs/declarations.md).
var rawSandboxKeys = []string{"cpus", "memory-mb", "disk-gb"}

func (s *rawSandbox) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: sandbox must be a mapping with cpus, memory-mb and/or disk-gb", node.Line)
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if key := node.Content[i].Value; !slices.Contains(rawSandboxKeys, key) {
			return fmt.Errorf("line %d: sandbox: field %s not found -- sandbox accepts %s",
				node.Content[i].Line, key, strings.Join(rawSandboxKeys, ", "))
		}
	}
	var m struct {
		CPUs     *int `yaml:"cpus"`
		MemoryMB *int `yaml:"memory-mb"`
		DiskGB   *int `yaml:"disk-gb"`
	}
	if err := node.Decode(&m); err != nil {
		return err
	}
	*s = rawSandbox{CPUs: m.CPUs, MemoryMB: m.MemoryMB, DiskGB: m.DiskGB}
	return nil
}

// parseSandbox is a template's `sandbox:` as model.Template.Sandbox:
// every key it writes a positive integer, no larger than the ceiling a
// task's own sandbox shape is held to (model.ValidateTemplateSandbox).
func parseSandbox(f file, raw *rawSandbox) (model.SandboxShape, error) {
	if raw == nil {
		return model.SandboxShape{}, nil
	}
	var out model.SandboxShape
	var problems []string
	take := func(name string, v *int, into *int) {
		if v == nil {
			return
		}
		if *v <= 0 {
			problems = append(problems, fmt.Sprintf("%s must be a positive integer, got %d", name, *v))
			return
		}
		*into = *v
	}
	take("cpus", raw.CPUs, &out.CPUs)
	take("memory-mb", raw.MemoryMB, &out.MemoryMB)
	take("disk-gb", raw.DiskGB, &out.DiskGB)
	if len(problems) > 0 {
		return model.SandboxShape{}, fmt.Errorf("%s: sandbox: %s", f.path, strings.Join(problems, "; "))
	}
	if err := model.ValidateTemplateSandbox(out); err != nil {
		return model.SandboxShape{}, fmt.Errorf("%s: %w", f.path, err)
	}
	return out, nil
}

// rawPush is one entry of a template's `push:` list: either a ref
// pattern on its own, or a mapping naming one with what else it allows.
//
//	push:
//	  - refs/heads/main
//	  - ref: refs/heads/candidate
//	    force: true
type rawPush struct {
	Ref    string
	Force  bool
	Delete bool
}

// rawPushKeys is what the mapping form may say. Anything else is refused
// by name, as an unknown top-level key is: a misspelt `forse: true`
// would otherwise be a force-push somebody thinks they declared.
var rawPushKeys = []string{"ref", "force", "delete"}

func (p *rawPush) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		p.Ref = node.Value
		return nil
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			if key := node.Content[i].Value; !slices.Contains(rawPushKeys, key) {
				return fmt.Errorf("line %d: push: field %s not found -- an entry says %s",
					node.Content[i].Line, key, strings.Join(rawPushKeys, ", "))
			}
		}
		var m struct {
			Ref    string `yaml:"ref"`
			Force  bool   `yaml:"force"`
			Delete bool   `yaml:"delete"`
		}
		if err := node.Decode(&m); err != nil {
			return err
		}
		*p = rawPush{Ref: m.Ref, Force: m.Force, Delete: m.Delete}
		return nil
	default:
		return fmt.Errorf("line %d: push: an entry is a ref, or a mapping with ref:, force: and delete:", node.Line)
	}
}

// rawField is one input a template asks for when somebody instantiates
// it (model.TemplateField): a named hole its title and body leave as
// `{{name}}`, and either a default to fill it with or a set of options
// to pick from.
//
// A declaration carries these for the reason declschema gives: a field
// says nothing about what the work may touch, and an unattended firing
// fills every one of them with its default
// (model.Template.FieldValues), so a declared template with fields is
// still a template a schedule fires on its cadence.
type rawField struct {
	Name     string   `yaml:"name"`
	Label    string   `yaml:"label"`
	Options  []string `yaml:"options"`
	Default  string   `yaml:"default"`
	Required bool     `yaml:"required"`
}

func parseTemplate(f file, repo model.RepoRef) (model.Template, model.RefPushSet, error) {
	tmpl, raw, err := parseTemplateRaw(f, repo)
	if err != nil {
		return model.Template{}, nil, err
	}
	push, err := parsePush(f, raw.Push)
	if err != nil {
		return model.Template{}, nil, err
	}
	return tmpl, push, nil
}

func parseTemplateRaw(f file, repo model.RepoRef) (model.Template, rawTemplate, error) {
	var raw rawTemplate
	if err := decode(f, model.DeclaredTemplate, &raw); err != nil {
		return model.Template{}, raw, err
	}
	body := raw.Body
	if strings.TrimSpace(raw.Prompt) != "" {
		if strings.TrimSpace(body) != "" && strings.TrimSpace(body) != strings.TrimSpace(raw.Prompt) {
			return model.Template{}, raw, fmt.Errorf("%s: specify body or prompt, not both", f.path)
		}
		body = raw.Prompt
	}
	title := strings.TrimSpace(raw.Title)
	if title == "" && f.stem != model.DefaultReviewStem {
		return model.Template{}, raw, fmt.Errorf("%s: a template needs a title -- it is what the task it files is called", f.path)
	}
	network, err := parseNetwork(f, raw.Network)
	if err != nil {
		return model.Template{}, raw, err
	}
	nothingNeeded, err := model.ParseNothingNeededAction(strings.TrimSpace(raw.NothingNeeded))
	if err != nil {
		return model.Template{}, raw, fmt.Errorf("%s: nothing-needed is %q or %q, got %q",
			f.path, model.NothingNeededClose, model.NothingNeededAck, strings.TrimSpace(raw.NothingNeeded))
	}
	values, err := parseCustomFields(f, raw.CustomFields)
	if err != nil {
		return model.Template{}, raw, err
	}
	fields, err := parseFields(f, raw.Fields)
	if err != nil {
		return model.Template{}, raw, err
	}
	admission, err := model.ParseOptionalAdmission(raw.Admission)
	if err != nil {
		return model.Template{}, raw, fmt.Errorf("%s: %w", f.path, err)
	}
	hosting, err := model.ParseOptionalHosting(raw.Hosting)
	if err != nil {
		return model.Template{}, raw, fmt.Errorf("%s: %w", f.path, err)
	}
	skills, err := parseRawSkills(f.path, raw.Skills)
	if err != nil {
		return model.Template{}, raw, err
	}
	reads, writes, grants, capAdm, capHost, err := parseCapabilities(f, repo, declaredGrants(raw.Capabilities, raw.Grants))
	if err != nil {
		return model.Template{}, raw, err
	}
	admission = model.MaxAdmission(admission, capAdm)
	hosting = model.MaxHosting(hosting, capHost)
	if err := checkTeam(f, raw.Team); err != nil {
		return model.Template{}, raw, err
	}
	var priority *int
	if raw.Priority != nil {
		if *raw.Priority < 0 {
			return model.Template{}, raw, fmt.Errorf("%s: priority must be a positive integer, got %d", f.path, *raw.Priority)
		}
		if *raw.Priority > 0 {
			p := *raw.Priority
			priority = &p
		}
	}
	sandbox, err := parseSandbox(f, raw.Sandbox)
	if err != nil {
		return model.Template{}, raw, err
	}
	target := repo
	tmpl := model.Template{
		ID:            model.RepoDeclaredID(model.DeclaredTemplate, repo, f.stem),
		Name:          displayName(raw.Name, f.stem),
		Title:         title,
		Body:          body,
		Fields:        fields,
		Target:        &target,
		Base:          strings.TrimSpace(raw.Base),
		NetworkAccess: network,
		Model:         strings.TrimSpace(raw.Model),
		CustomFields:  values,
		Reads:         reads,
		Writes:        writes,
		Grants:        grants,

		Interactive:         raw.Interactive,
		KeepRunning:         raw.KeepRunning,
		Admission:           admission,
		Hosting:             hosting,
		Skills:              skills,
		Priority:            priority,
		Sandbox:             sandbox,
		NothingNeededAction: nothingNeeded,
	}
	if raw.AutoMerge != nil {
		tmpl.AutoMerge = *raw.AutoMerge
	}
	return tmpl, raw, nil
}

// TemplatePush is the push set the template file at filePath declares, as
// the declaration sync would read it: nil for a template with no `push:`
// (or an empty file), and an error for one whose `push:` does not parse.
// Nothing else in the file is checked. It is what the security hold
// compares a pull request's base and head with, so a change to any
// template's push set waits for a person (orchestrator's
// guardTemplatePush, trustboundary.TemplatePush).
func TemplatePush(filePath string, text []byte) (model.RefPushSet, error) {
	var raw struct {
		Push []rawPush `yaml:"push"`
	}
	if err := yaml.NewDecoder(bytes.NewReader(text)).Decode(&raw); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", filePath, err)
	}
	return parsePush(file{path: filePath, text: text}, raw.Push)
}

// parsePush is a declared template's `push:` (grain/task-2536): the
// refs, in this repository only, that the runs of tasks filed from it may
// update beyond their own task branch.
//
// Each entry is an explicit ref pattern (model.ValidateRefPushPattern),
// never an implicit "everything", and force-moves and deletions are
// allowed only where an entry says so. The repository is not written
// anywhere because there is no choice to make: a declaration's push set
// applies to the repository it is declared in and nowhere else, which is
// what keeps it inside the authority that merging into that repository's
// default branch already is. The filing team's own git credential is
// still what every push goes out with, so GitHub refuses what that
// token cannot do.
func parsePush(f file, raw []rawPush) (model.RefPushSet, error) {
	var out model.RefPushSet
	var errs []error
	seen := map[string]int{}
	for _, r := range raw {
		pattern := strings.TrimSpace(r.Ref)
		if err := model.ValidateRefPushPattern(pattern); err != nil {
			errs = append(errs, fmt.Errorf("%s: push: %w", f.path, err))
			continue
		}
		if i, ok := seen[pattern]; ok {
			out[i].Force = out[i].Force || r.Force
			out[i].Delete = out[i].Delete || r.Delete
			continue
		}
		seen[pattern] = len(out)
		out = append(out, model.RefPush{Pattern: pattern, Force: r.Force, Delete: r.Delete})
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

// declaredGrants is a declaration's `capabilities:` and `grants:` read as
// one list: `grants:` is the unified spelling of the same key
// (design/design/unified-grants.design.md §3), so an entry both name, in
// either spelling (model.CanonicalGrantKey), is held once.
func declaredGrants(capabilities, grants []string) []string {
	if len(grants) == 0 {
		return capabilities
	}
	out := slices.Clone(capabilities)
	seen := make(map[string]bool, len(out)+len(grants))
	for _, c := range out {
		seen[model.CanonicalGrantKey(strings.TrimSpace(c))] = true
	}
	for _, g := range grants {
		k := model.CanonicalGrantKey(strings.TrimSpace(g))
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, g)
	}
	return out
}

// parseCapabilities parses a declared template's or schedule's
// `capabilities:` and `grants:` (declaredGrants).
//
// Each `repo-read:<owner>/<repo>` becomes one of the filed task's Reads
// and each `repo-write:<owner>/<repo>` one of its Writes, landing on that
// repository's default branch. That is the allowance and nothing more:
// the git proxy clones and pushes a task's work with its team's own
// GitHub credential (model.Store.TaskGitCredential, with no deployment
// fallback since grain/task-1592), so what GitHub lets that token do is
// the authority, and a repository named nowhere here stays out of the
// task's scope altogether.
//
// Other capability names are recorded as model.Grant entries with
// Via = model.GrantSource(f.path).
//
// grain's configuration repository (model.IsConfigRepo) is refused,
// which is served out of grain's own disk rather than GitHub
// (gitproxy.LocalRepository) and so has no team token in front of it to say no.
// Naming the declaring repository itself in repo-read/repo-write is allowed
// and adds nothing -- a task may already read and push its own target.
//
// Task-target projections (repo#target, and repo#read / repo#write carrying a
// `base`) exist only to spell a task request's repo, base, reads and writes
// fields in an API `grants:` list (model.SplitRequestGrants); CreateTask
// refuses them as capability grants. They are refused here too: a declaration's
// tasks always target the declaring repository (with `base:` naming its branch),
// and extra repository writes always land on that repository's default branch.
// A repository whose default branch already declares one fails Load on its next
// sync, keeping its previously synced declarations live and recording the
// refusal in `repo_declaration_sync.last_error` rather than leaving an
// un-filable template in the store.
func parseCapabilities(f file, repo model.RepoRef, caps []string) ([]model.RepoRef, []model.WriteTarget, []model.Grant, model.Admission, model.Hosting, error) {
	var reads []model.RepoRef
	var writes []model.WriteTarget
	var grants []model.Grant
	var admission model.Admission
	var hosting model.Hosting
	var errs []error
	seenGrants := map[string]bool{}

	for _, c := range caps {
		c = strings.TrimSpace(c)
		if c == "" {
			errs = append(errs, fmt.Errorf("%s: capabilities: capability name cannot be empty", f.path))
			continue
		}
		if adm, ok := model.LegacyAdmissionAlias(c); ok {
			log.Printf("repodecl: %s: capability %q is deprecated; use `admission: %s` instead", f.path, c, adm)
			admission = model.MaxAdmission(admission, adm)
			continue
		}
		if host, ok := model.LegacyHostingAlias(c); ok {
			log.Printf("repodecl: %s: capability %q is deprecated; use `hosting: %s` instead", f.path, c, host)
			hosting = model.MaxHosting(hosting, host)
			continue
		}
		var (
			target model.RepoRef
			write  bool
		)
		switch {
		case strings.HasPrefix(c, model.RepoReadCapabilityPrefix):
			r, ok := model.RepoReadTarget(c)
			if !ok {
				errs = append(errs, fmt.Errorf("%s: capabilities: %q does not name a repository -- "+
					"write %s<owner>/<repo>", f.path, c, model.RepoReadCapabilityPrefix))
				continue
			}
			target = r
		case strings.HasPrefix(c, model.RepoWriteCapabilityPrefix):
			r, ok := model.RepoWriteTarget(c)
			if !ok {
				errs = append(errs, fmt.Errorf("%s: capabilities: %q does not name a repository -- "+
					"write %s<owner>/<repo>", f.path, c, model.RepoWriteCapabilityPrefix))
				continue
			}
			target, write = r, true
		case model.IsRepoAccessGrant(c):
			// The repo server's spelling of the same access
			// (https://grain.dev/capabilities/repo#read / #write
			// {repo}), held to the same rules as the bare names above.
			if r, ok := model.RepoReadTarget(c); ok {
				target = r
			} else {
				target, _ = model.RepoWriteTarget(c)
				write = true
			}
		case model.IsGrainCIGrant(c):
			// grain#ci is the merge-gate grant
			// a repository's CI Mode attaches automatically -- never a hand-held
			// task capability: applyGrainCIGrant strips it on a ci_mode: github
			// repository, and CreateTask refuses it when copied from a template.
			// A .grain template or schedule that names it wants read-only CI
			// access to its own declaring repository.
			if model.IsCapabilityRef(c) {
				if err := model.CheckCapabilityGrantSyntax(c); err != nil {
					errs = append(errs, fmt.Errorf("%s: capabilities: %v", f.path, err))
					continue
				}
			}
			ciRead := model.APIGrantString("ci-read", repo.String())
			ciReadKey := model.CanonicalGrantKey(ciRead)
			if !seenGrants[ciReadKey] {
				seenGrants[ciReadKey] = true
				grants = append(grants, model.Grant{Capability: ciRead, Via: model.GrantSource(f.path)})
			}
			continue
		case model.IsCapabilityRef(c):
			// A capability reference (design/spec/capabilities/capabilities.spec.md §2.2):
			// its grammar is checked here; its with: arguments against
			// its server's schema when a task is filed from it, and
			// again at admission.
			if err := model.CheckCapabilityGrantSyntax(c); err != nil {
				errs = append(errs, fmt.Errorf("%s: capabilities: %v", f.path, err))
				continue
			}
			ref := model.CapabilityGrantRef(c)
			if ref == model.RepoTargetRef {
				errs = append(errs, fmt.Errorf("%s: capabilities: %q: a declaration's tasks always target "+
					"the declaring repository (%s), and its base is `base:`", f.path, c, repo))
				continue
			}
			if ref == model.RepoReadRef || (ref == model.RepoWriteRef && !model.IsGitPushAnyRefGrant(c)) {
				if _, _, hasBase, err := model.RepoGrantArgs(model.CanonicalGrant(c)); err == nil && hasBase {
					if ref == model.RepoReadRef {
						errs = append(errs, fmt.Errorf("%s: capabilities: %q: %s takes only `repo: <owner>/<repo>`, not `base`",
							f.path, c, model.RepoReadRef))
					} else {
						errs = append(errs, fmt.Errorf("%s: capabilities: %q: a declared write to another repository "+
							"always lands on its default branch -- pass only `repo: <owner>/<repo>`, and set this "+
							"repository's own branch with `base:`", f.path, c))
					}
					continue
				}
				errs = append(errs, fmt.Errorf("%s: capabilities: %q does not name a repository -- "+
					"pass repo: <owner>/<repo> (or refs/force for branch push)", f.path, c))
				continue
			}
			key := model.CanonicalGrantKey(c)
			if !seenGrants[key] {
				seenGrants[key] = true
				grants = append(grants, model.Grant{Capability: c, Via: model.GrantSource(f.path)})
			}
			continue
		default:
			// General capability ID syntax check. Colons are allowed for namespace prefixes like gcp-key:<name>.
			if strings.ContainsAny(c, " \t\r\n`'\"") {
				errs = append(errs, fmt.Errorf("%s: capabilities: %q is not a valid capability name", f.path, c))
				continue
			}
			if strings.HasPrefix(c, model.GCPKeyCapabilityPrefix) {
				if _, ok := model.GCPKeyName(c); !ok {
					errs = append(errs, fmt.Errorf("%s: capabilities: %q does not name a GCP key credential -- write %s<name>",
						f.path, c, model.GCPKeyCapabilityPrefix))
					continue
				}
			}
			key := model.CanonicalGrantKey(c)
			if !seenGrants[key] {
				seenGrants[key] = true
				grants = append(grants, model.Grant{
					Capability: c,
					Via:        model.GrantSource(f.path),
				})
			}
			continue
		}
		if model.IsConfigRepo(target) {
			errs = append(errs, fmt.Errorf("%s: capabilities: %q: %s is grain's own configuration, "+
				"served from grain's disk with no team credential in front of it, so no repository's "+
				"file may reach it -- a task that needs it is filed from grain's own settings",
				f.path, c, target))
			continue
		}
		if target.Same(repo) {
			continue
		}
		if write {
			if !slices.ContainsFunc(writes, func(w model.WriteTarget) bool { return w.Repo.Same(target) }) {
				writes = append(writes, model.WriteTarget{Repo: target})
			}
			continue
		}
		if !slices.ContainsFunc(reads, target.Same) {
			reads = append(reads, target)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, nil, nil, "", "", err
	}
	return reads, writes, grants, admission, hosting, nil
}

// checkTeam refuses a template, suite or schedule file whose `team:`
// names any team but the default one. The key is still read, so a file
// written for a multi-team build that names the default team (or none)
// keeps parsing, but a deployment has no other team for its work to
// belong to, and nowhere to record one (schema 25 dropped the team
// columns): a file naming one is refused rather than quietly run as the
// deployment's.
func checkTeam(f file, raw string) error {
	team := strings.TrimSpace(raw)
	if team == "" || team == model.DefaultTeamID {
		return nil
	}
	return fmt.Errorf("%s: team %q does not exist: this deployment has no teams, so `team:` may "+
		"only name %q or be left out", f.path, team, model.DefaultTeamID)
}

// --- suites ---------------------------------------------------------------

type rawSuite struct {
	Name                 string   `yaml:"name"`
	Templates            []string `yaml:"templates"`
	Mode                 string   `yaml:"mode"`
	Count                int      `yaml:"count"`
	MaxPasses            int      `yaml:"max-passes"`
	Team                 string   `yaml:"team"`
	RequireApproval      *bool    `yaml:"require-approval"`
	AutoMerge            *bool    `yaml:"auto-merge"`
	AutoApproveProposals *bool    `yaml:"auto-approve-proposals"`
	// Qualification makes the suite a release gate the
	// release-qualification reconciler runs (model.SuiteQualification).
	Qualification *rawQualification `yaml:"qualification"`
}

type rawQualification struct {
	Check     string     `yaml:"check"`
	Candidate string     `yaml:"candidate"`
	Stage1    *rawStage1 `yaml:"stage1"`
	Upstream  string     `yaml:"upstream"`
}

type rawStage1 struct {
	Workflow string `yaml:"workflow"`
	Status   string `yaml:"status"`
}

// parseQualification is a suite's `qualification:` stanza, nil for none.
// An empty stanza (`qualification: {}`) is a release gate with every
// default.
func parseQualification(f file, raw *rawQualification) (*model.SuiteQualification, error) {
	if raw == nil {
		return nil, nil
	}
	q := &model.SuiteQualification{
		Check:     strings.TrimSpace(raw.Check),
		Candidate: model.QualificationCandidate(strings.TrimSpace(raw.Candidate)),
		Upstream:  strings.TrimSpace(raw.Upstream),
	}
	if raw.Stage1 != nil {
		q.Stage1Workflow, q.Stage1Status = strings.TrimSpace(raw.Stage1.Workflow), strings.TrimSpace(raw.Stage1.Status)
		if q.Stage1Workflow == "" && q.Stage1Status == "" {
			return nil, fmt.Errorf("%s: qualification.stage1 names a `workflow:` or a `status:`", f.path)
		}
	}
	if err := q.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", f.path, err)
	}
	return q, nil
}

func parseSuite(f file, repo model.RepoRef, templates map[string]bool) (model.Suite, *model.SuiteQualification, error) {
	var raw rawSuite
	if err := decode(f, model.DeclaredSuite, &raw); err != nil {
		return model.Suite{}, nil, err
	}
	qualification, err := parseQualification(f, raw.Qualification)
	if err != nil {
		return model.Suite{}, nil, err
	}
	if len(raw.Templates) == 0 {
		return model.Suite{}, nil, fmt.Errorf("%s: a suite needs at least one template, named by file: "+
			"`templates: [nightly]` runs %s/nightly.yml", f.path, model.TemplateDeclarationDir)
	}
	items := make([]model.SuiteItem, 0, len(raw.Templates))
	for _, name := range raw.Templates {
		name = strings.TrimSpace(name)
		if !templates[name] {
			return model.Suite{}, nil, fmt.Errorf("%s: no template %q -- a suite runs the templates this "+
				"repository declares, named by file (%s/%s.yml)", f.path, name, model.TemplateDeclarationDir, name)
		}
		items = append(items, model.SuiteItem{TemplateID: model.RepoDeclaredID(model.DeclaredTemplate, repo, name)})
	}

	if err := checkTeam(f, raw.Team); err != nil {
		return model.Suite{}, nil, err
	}
	suite := model.Suite{
		ID:        model.RepoDeclaredID(model.DeclaredSuite, repo, f.stem),
		Name:      displayName(raw.Name, f.stem),
		Items:     items,
		Count:     raw.Count,
		MaxPasses: raw.MaxPasses,
		AutoMerge: true,
	}
	switch mode := strings.TrimSpace(raw.Mode); mode {
	case "", string(model.SuiteCount):
		suite.Mode = model.SuiteCount
		if suite.Count == 0 {
			suite.Count = 1
		}
	case string(model.SuiteUntilClean), "until-clean":
		suite.Mode = model.SuiteUntilClean
		if suite.MaxPasses == 0 {
			suite.MaxPasses = 5
		}
	default:
		return model.Suite{}, nil, fmt.Errorf("%s: mode is \"count\" or \"until-clean\", got %q", f.path, mode)
	}
	if raw.RequireApproval != nil {
		suite.RequireApproval = *raw.RequireApproval
	}
	if raw.AutoMerge != nil {
		suite.AutoMerge = *raw.AutoMerge
	}
	if raw.AutoApproveProposals != nil {
		suite.AutoApproveProposals = *raw.AutoApproveProposals
	}
	if err := suite.Validate(); err != nil {
		return model.Suite{}, nil, fmt.Errorf("%s: %w", f.path, err)
	}
	return suite, qualification, nil
}

// --- schedules ------------------------------------------------------------

type rawSchedule struct {
	Name                 string               `yaml:"name"`
	Template             string               `yaml:"template"`
	Suite                string               `yaml:"suite"`
	Workflow             string               `yaml:"workflow"`
	Base                 string               `yaml:"base"`
	Team                 string               `yaml:"team"`
	TimeZone             string               `yaml:"timezone"`
	AutoApproveProposals *bool                `yaml:"auto-approve-proposals"`
	Capabilities         model.CapabilityList `yaml:"capabilities"`
	Grants               model.CapabilityList `yaml:"grants"`
	Every                *rawEvery            `yaml:"every"`
	Daily                *rawAt               `yaml:"daily"`
	Weekly               *rawWeekly           `yaml:"weekly"`
	Monthly              *rawDayAt            `yaml:"monthly"`
	Trigger              *rawScheduleTrigger  `yaml:"trigger"`
	Overlap              rawOverlap           `yaml:"overlap"`
	OverlapGroup         string               `yaml:"overlap-group"`
	Review               string               `yaml:"review"`
	GrainCI              *bool                `yaml:"grain-ci"`
	SkipWhen             string               `yaml:"skip-when"`
	Enabled              *bool                `yaml:"enabled"`
	Model                string               `yaml:"model"`
	Admission            string               `yaml:"admission"`
	Hosting              string               `yaml:"hosting"`
}

// rawOverlap is a schedule file's `overlap:`, in either of its forms: the
// plain string (`skip` or `allow`), or the mapping
// `{skip: true, group: <name>}` that also names an overlap group shared
// with the repository's other schedules (model.Schedule.OverlapGroup,
// grain/task-3895).
type rawOverlap struct {
	Scalar   string
	Mapping  bool
	Skip     *bool
	HasGroup bool
	Group    string
}

func (o *rawOverlap) UnmarshalYAML(value *yaml.Node) error {
	const shape = "overlap is \"skip\", \"allow\", or {skip: true, group: <name>}"
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Tag == "!!null" {
			*o = rawOverlap{}
			return nil
		}
		if value.Tag != "!!str" {
			return fmt.Errorf("%s", shape)
		}
		*o = rawOverlap{Scalar: value.Value}
		return nil
	case yaml.MappingNode:
		out := rawOverlap{Mapping: true}
		for i := 0; i+1 < len(value.Content); i += 2 {
			k, v := value.Content[i].Value, value.Content[i+1]
			switch k {
			case "skip":
				if v.Kind != yaml.ScalarNode || v.Tag != "!!bool" {
					return fmt.Errorf("overlap.skip is true or false")
				}
				var b bool
				if err := v.Decode(&b); err != nil {
					return err
				}
				out.Skip = &b
			case "group":
				if v.Kind != yaml.ScalarNode || v.Tag != "!!str" {
					return fmt.Errorf("overlap.group is a name")
				}
				out.HasGroup, out.Group = true, v.Value
			default:
				return fmt.Errorf("overlap: unknown key %q -- the mapping form takes `skip` and `group`", k)
			}
		}
		*o = out
		return nil
	default:
		return fmt.Errorf("%s", shape)
	}
}

// scheduleOverlap resolves a schedule file's `overlap:` and
// `overlap-group:` into the policy and group they say. A group holds
// firings while another schedule in it runs, so it is a skip by
// definition: `overlap: allow` (or `skip: false`) beside one is refused
// rather than one of the two being ignored.
func scheduleOverlap(f file, raw rawOverlap, topGroup string) (model.OverlapPolicy, string, error) {
	var policy model.OverlapPolicy
	var group string
	if raw.Mapping {
		policy = model.OverlapSkip
		if raw.Skip != nil && !*raw.Skip {
			policy = model.OverlapAllow
		}
		if raw.HasGroup {
			if group = strings.TrimSpace(raw.Group); group == "" {
				return "", "", fmt.Errorf("%s: `overlap.group` is empty", f.path)
			}
		}
	} else {
		switch p := model.OverlapPolicy(strings.TrimSpace(raw.Scalar)); p {
		case "", model.OverlapSkip, model.OverlapAllow:
			policy = p
		default:
			return "", "", fmt.Errorf("%s: overlap is \"skip\" or \"allow\", got %q", f.path, string(p))
		}
	}
	if tg := strings.TrimSpace(topGroup); tg != "" {
		if group != "" {
			return "", "", fmt.Errorf("%s: the overlap group is named twice -- write `overlap.group` or "+
				"`overlap-group`, not both", f.path)
		}
		group = tg
	}
	if group == "" {
		return policy, "", nil
	}
	if policy == model.OverlapAllow {
		return "", "", fmt.Errorf("%s: an overlap group holds a firing while another schedule in it is "+
			"running, so it cannot be combined with `overlap: allow` (or `skip: false`)", f.path)
	}
	if err := model.ValidateOverlapGroup(group); err != nil {
		return "", "", fmt.Errorf("%s: %w", f.path, err)
	}
	return model.OverlapSkip, group, nil
}

type rawScheduleTrigger struct {
	GitHubWorkflowRun     *model.GitHubWorkflowRunTrigger `yaml:"github_workflow_run"`
	GitHubWorkflowRunDash *model.GitHubWorkflowRunTrigger `yaml:"github-workflow-run"`
	GitRef                *model.GitRefTrigger            `yaml:"git_ref"`
	GitRefDash            *model.GitRefTrigger            `yaml:"git-ref"`
}

type rawEvery struct {
	Hours   int `yaml:"hours"`
	Minutes int `yaml:"minutes"`
}

type rawAt struct {
	At string `yaml:"at"`
}

type rawWeekly struct {
	Day string `yaml:"day"`
	At  string `yaml:"at"`
}

type rawDayAt struct {
	Day int    `yaml:"day"`
	At  string `yaml:"at"`
}

// parseSchedule reads one schedule file.
//
// A declared schedule fires a declared template, runs a declared suite,
// or runs one of the repository's own workflows (parseWorkflowSchedule),
// and never carries its own inline title and body, which is why `title` and
// `body` are refused here rather than accepted: a schedule is a cadence,
// the work is a template, and a repository that has both in one file
// ends up with two places to edit the same job the moment it wants to
// run it from a suite as well. The template it names is one this same
// repository declares -- naming a template out of grain's settings would
// be a repository helping itself to work somebody else wrote.
//
// workflows is the repository's own `.grain/workflows` (ci.LoadNativeDir),
// and workflowsRead says they could be read at all -- a tree whose
// workflows do not parse is already an error for the whole repository,
// so a schedule naming one is not also told that it does not exist.
func parseSchedule(f file, repo model.RepoRef, templates, suites map[string]bool,
	workflows []ci.Workflow, workflowsRead bool) (model.Schedule, error) {
	var raw rawSchedule
	if err := decode(f, model.DeclaredSchedule, &raw); err != nil {
		return model.Schedule{}, err
	}

	if err := checkTeam(f, raw.Team); err != nil {
		return model.Schedule{}, err
	}
	sched := model.Schedule{
		ID:       model.RepoDeclaredID(model.DeclaredSchedule, repo, f.stem),
		Target:   repo,
		Base:     strings.TrimSpace(raw.Base),
		TimeZone: strings.TrimSpace(raw.TimeZone),
		Enabled:  true,
	}
	template, suite, workflow := strings.TrimSpace(raw.Template), strings.TrimSpace(raw.Suite),
		strings.TrimSpace(raw.Workflow)
	named := 0
	for _, v := range []string{template, suite, workflow} {
		if v != "" {
			named++
		}
	}
	switch {
	case named > 1:
		return model.Schedule{}, fmt.Errorf("%s: a schedule fires a template, runs a suite or runs a "+
			"workflow -- one of `template:`, `suite:` and `workflow:`, not several", f.path)
	case workflow != "":
		if !workflowsRead {
			return model.Schedule{}, fmt.Errorf("%s: cannot check the workflow %q, because this "+
				"repository's %s did not parse", f.path, workflow, ci.WorkflowDir)
		}
		stem, err := scheduledWorkflowStem(f, workflow, workflows)
		if err != nil {
			return model.Schedule{}, err
		}
		id := model.WorkflowReviewTemplateID(stem)
		review := model.SystemTemplate(id, model.RepoRef{})
		if review == nil {
			// Unreachable unless pkg/model and this package have been
			// changed apart -- workflowSchedule's own check.
			return model.Schedule{}, fmt.Errorf("%s: grain has no template to review a scheduled workflow with (%s)",
				f.path, id)
		}
		sched.TemplateID = &id
		sched.Title = strings.TrimSpace(raw.Name)
		if sched.Title == "" {
			sched.Title = model.WorkflowReviewTitle(review.Title, sched.Base)
		}
		// What a workflow's own cron gets (workflowSchedule): the review
		// may file the tasks it finds without a human approving each.
		sched.AutoApproveProposals = true
	case template != "":
		if !templates[template] {
			return model.Schedule{}, fmt.Errorf("%s: no template %q -- a schedule fires a template this "+
				"repository declares, named by file (%s/%s.yml)",
				f.path, template, model.TemplateDeclarationDir, template)
		}
		id := model.RepoDeclaredID(model.DeclaredTemplate, repo, template)
		sched.TemplateID = &id
		sched.Title = displayName(raw.Name, f.stem)
	case suite != "":
		if !suites[suite] {
			return model.Schedule{}, fmt.Errorf("%s: no suite %q -- a schedule runs a suite this "+
				"repository declares, named by file (%s/%s.yml)",
				f.path, suite, model.SuiteDeclarationDir, suite)
		}
		if sched.Base == "" {
			return model.Schedule{}, fmt.Errorf("%s: a schedule that runs a suite needs `base:` -- "+
				"a suite run has no default branch to fall back to", f.path)
		}
		id := model.RepoDeclaredID(model.DeclaredSuite, repo, suite)
		sched.SuiteID = &id
		sched.Title = displayName(raw.Name, f.stem)
	case raw.Trigger != nil && (raw.Trigger.GitHubWorkflowRun != nil || raw.Trigger.GitHubWorkflowRunDash != nil):
		gh := raw.Trigger.GitHubWorkflowRun
		if gh == nil {
			gh = raw.Trigger.GitHubWorkflowRunDash
		}
		stem := gh.WorkflowStem()
		if err := model.ValidateDeclarationStem(stem); err != nil {
			return model.Schedule{}, fmt.Errorf("%s: github_workflow_run workflow %q: %w", f.path, gh.Workflow, err)
		}
		// GitHub's run, not grain CI's: its own prompt
		// (model.GitHubWorkflowReviewTemplateID), which sends the agent to
		// `.github/workflows` rather than `.grain/workflows`.
		id := model.GitHubWorkflowReviewTemplateID(stem)
		review := model.SystemTemplate(id, model.RepoRef{})
		if review == nil {
			return model.Schedule{}, fmt.Errorf("%s: grain has no template to review a workflow with (%s)",
				f.path, id)
		}
		sched.TemplateID = &id
		sched.Title = strings.TrimSpace(raw.Name)
		if sched.Title == "" {
			sched.Title = model.WorkflowReviewTitle(review.Title, sched.Base)
		}
		sched.AutoApproveProposals = true
	default:
		return model.Schedule{}, fmt.Errorf("%s: a schedule needs a `template:` to fire, a `suite:` to run "+
			"or a `workflow:` to run", f.path)
	}

	recurrence, err := parseRecurrence(f, raw)
	if err != nil {
		return model.Schedule{}, err
	}
	sched.Recurrence = recurrence
	overlap, overlapGroup, err := scheduleOverlap(f, raw.Overlap, raw.OverlapGroup)
	if err != nil {
		return model.Schedule{}, err
	}
	sched.Overlap, sched.OverlapGroup = overlap, overlapGroup
	if workflow != "" || (sched.Recurrence.Kind == model.RecurrenceGitHubWorkflowRun && template == "" && suite == "") {
		// A workflow firing is unfinished until its run has ended and its
		// review has been filed, and grain keeps one record of that per
		// schedule (model.ScheduledWorkflowRun) -- a second firing on top
		// of the first would replace the record and leave that run with
		// nobody coming to read it. Which is why a workflow's own cron is
		// always OverlapSkip (workflowSchedule), and why this one is too.
		if sched.Overlap == model.OverlapAllow {
			return model.Schedule{}, fmt.Errorf("%s: a schedule that runs a workflow cannot overlap -- "+
				"grain reviews one run of it at a time, so `overlap:` is \"skip\" or left out", f.path)
		}
		sched.Overlap = model.OverlapSkip
	}
	// When the run gets a review (model.WorkflowReviewPolicy): a setting
	// of a schedule that runs a workflow, and of nothing else -- a
	// template or a suite files its tasks whatever happens.
	sched.WorkflowReview = model.WorkflowReviewPolicy(strings.TrimSpace(raw.Review))
	if err := sched.WorkflowReview.Validate(); err != nil {
		return model.Schedule{}, fmt.Errorf("%s: %w", f.path, err)
	}
	if sched.WorkflowReview != "" && workflow == "" && sched.Recurrence.Kind != model.RecurrenceGitHubWorkflowRun {
		return model.Schedule{}, fmt.Errorf("%s: `review:` says when the run of a scheduled workflow is "+
			"reviewed, and this schedule runs no `workflow:`", f.path)
	}
	// Whether the run is on grain CI even when this repository's
	// ci_mode is github (model.Schedule.GrainCI): a setting of a schedule
	// that runs one of its own `.grain/workflows`, and of nothing else --
	// a GitHub Actions run is GitHub's to start.
	if raw.GrainCI != nil {
		if workflow == "" {
			return model.Schedule{}, fmt.Errorf("%s: `grain-ci:` says the run of a scheduled workflow is on "+
				"grain CI even on a ci_mode: github repository, and this schedule runs no `workflow:`", f.path)
		}
		sched.GrainCI = *raw.GrainCI
	}
	// A condition outside the schedule that holds its firings
	// (model.Schedule.SkipWhen): `skip-when: publishing-held` holds them
	// while its base branch's publishing is held by a release triage task.
	if sched.SkipWhen, err = model.ParseScheduleSkipWhen(raw.SkipWhen); err != nil {
		return model.Schedule{}, fmt.Errorf("%s: skip-when: %w", f.path, err)
	}
	if raw.AutoApproveProposals != nil {
		sched.AutoApproveProposals = *raw.AutoApproveProposals
	}
	if raw.Enabled != nil {
		sched.Enabled = *raw.Enabled
	}
	// The model every firing asks for (model.Schedule.Model), beating
	// the template's own. A suite run files each of its templates as it
	// stands, so a suite schedule has nothing for a model to apply to,
	// and saying one there is refused rather than ignored.
	sched.Model = strings.TrimSpace(raw.Model)
	if sched.Model != "" && sched.SuiteID != nil {
		return model.Schedule{}, fmt.Errorf("%s: a schedule that runs a suite cannot choose a model -- "+
			"set `model:` on the suite's templates instead", f.path)
	}
	schedAdm, err := model.ParseOptionalAdmission(raw.Admission)
	if err != nil {
		return model.Schedule{}, fmt.Errorf("%s: %w", f.path, err)
	}
	schedHost, err := model.ParseOptionalHosting(raw.Hosting)
	if err != nil {
		return model.Schedule{}, fmt.Errorf("%s: %w", f.path, err)
	}

	reads, writes, grants, capAdm, capHost, err := parseCapabilities(f, repo, declaredGrants(raw.Capabilities, raw.Grants))
	if err != nil {
		return model.Schedule{}, err
	}
	if len(reads) > 0 || len(writes) > 0 {
		return model.Schedule{}, fmt.Errorf("%s: capabilities: a schedule fires a template, runs a suite or runs a workflow, "+
			"and repository access belongs on the template it fires (repo-read/repo-write)", f.path)
	}
	sched.Grants = grants
	sched.Admission = model.MaxAdmission(schedAdm, capAdm)
	if sched.Admission != "" && sched.SuiteID != nil {
		return model.Schedule{}, fmt.Errorf("%s: a schedule that runs a suite cannot choose admission -- "+
			"set `admission:` on the suite's templates instead", f.path)
	}
	sched.Hosting = model.MaxHosting(schedHost, capHost)
	if sched.Hosting != "" && sched.SuiteID != nil {
		return model.Schedule{}, fmt.Errorf("%s: a schedule that runs a suite cannot choose hosting -- "+
			"set `hosting:` on the suite's templates instead", f.path)
	}

	return sched, nil
}

// scheduledWorkflowStem resolves a schedule's `workflow:` -- a stem
// ("nightly"), a file name ("nightly.yml") or a path
// (".grain/workflows/nightly.yml") -- to the stem of one of the
// repository's own workflows, which is what the schedule stores
// (model.WorkflowReviewTemplateID) and what its run is narrowed to.
//
// Three refusals, each of them a firing that could never work:
//
//   - no such workflow, in `.grain/workflows` (`.github/workflows` is
//     GitHub's to run, as it is for a cron -- scheduledWorkflows);
//   - a workflow with no `on: schedule` of its own. grain starts these
//     runs as schedule events (ci.EventSchedule), and a workflow that does
//     not answer one has no jobs in that run (ci.Triggers.Matches);
//   - a workflow kept as `.yaml`. The run is narrowed to
//     `.grain/workflows/<stem>.yml` (orchestrator.scheduledWorkflowPath),
//     because a schedule declared here records this file as its
//     provenance rather than the workflow's.
func scheduledWorkflowStem(f file, name string, workflows []ci.Workflow) (string, error) {
	want := strings.TrimPrefix(path.Clean(name), ci.WorkflowDir+"/")
	byFile := path.Ext(want) != ""
	for _, w := range workflows {
		stem := model.DeclarationStem(w.Path)
		if byFile && path.Base(w.Path) != want || !byFile && stem != want {
			continue
		}
		if len(w.On.Schedule) == 0 {
			return "", fmt.Errorf("%s: the workflow %s has no `on: schedule`, so a scheduled run of it "+
				"would have no jobs in it -- give it a `schedule:` trigger (its cron also runs it on the "+
				"default branch)", f.path, w.Path)
		}
		if w.Path != ci.WorkflowDir+"/"+stem+".yml" {
			return "", fmt.Errorf("%s: a schedule runs a workflow kept as %s/<name>.yml, and %s is not "+
				"one -- rename it", f.path, ci.WorkflowDir, w.Path)
		}
		if err := model.ValidateDeclarationStem(stem); err != nil {
			return "", fmt.Errorf("%s: workflow %s: %w", f.path, w.Path, err)
		}
		return stem, nil
	}
	return "", fmt.Errorf("%s: no workflow %q -- a schedule runs a workflow this repository declares, "+
		"named by file (%s/%s.yml)", f.path, name, ci.WorkflowDir, strings.TrimSuffix(want, path.Ext(want)))
}

// parseRecurrence turns whichever one of the four cadence blocks the
// file carries into a model.Recurrence. Exactly one: a file with two is
// a file whose author meant one of them, and guessing which would be a
// job that runs on a cadence nobody wrote down.
func parseRecurrence(f file, raw rawSchedule) (model.Recurrence, error) {
	var found []string
	if raw.Every != nil {
		found = append(found, "every")
	}
	if raw.Daily != nil {
		found = append(found, "daily")
	}
	if raw.Weekly != nil {
		found = append(found, "weekly")
	}
	if raw.Monthly != nil {
		found = append(found, "monthly")
	}
	if raw.Trigger != nil {
		found = append(found, "trigger")
	}
	if len(found) != 1 {
		return model.Recurrence{}, fmt.Errorf("%s: a schedule needs exactly one cadence -- "+
			"`every: {hours: N}`, `every: {minutes: N}`, `daily: {at: \"09:00\"}`, `weekly: {day: monday, at: \"09:00\"}`, "+
			"`monthly: {day: 1, at: \"09:00\"}`, `trigger: {github_workflow_run: ...}` or `trigger: {git_ref: ...}` -- "+
			"and this one has %d", f.path, len(found))
	}
	tz := strings.TrimSpace(raw.TimeZone)
	var r model.Recurrence
	switch found[0] {
	case "trigger":
		gh := raw.Trigger.GitHubWorkflowRun
		if gh == nil {
			gh = raw.Trigger.GitHubWorkflowRunDash
		}
		ref := raw.Trigger.GitRef
		if ref == nil {
			ref = raw.Trigger.GitRefDash
		}
		switch {
		case gh != nil && ref != nil:
			return model.Recurrence{}, fmt.Errorf("%s: `trigger` takes `github_workflow_run` or `git_ref`, not both", f.path)
		case ref != nil:
			cp := model.GitRefTrigger{Repo: strings.TrimSpace(ref.Repo), Ref: strings.TrimSpace(ref.Ref)}
			// grain/config is read off grain's own disk with no credential
			// in front of it, and no repository's file may reach it
			// (parseCapabilities): not even to learn where its refs are.
			if watched, err := model.ParseRepo(cp.Repo); err == nil {
				if model.IsConfigRepo(watched) {
					return model.Recurrence{}, fmt.Errorf("%s: `git_ref` watches %s, which is grain's own "+
						"configuration; no repository's file may reach it -- a schedule that watches it is "+
						"declared in grain's own settings", f.path, watched)
				}
				if model.IsRetiredConfigRepo(watched) {
					return model.Recurrence{}, fmt.Errorf("%s: `git_ref` watches %s: %s", f.path, watched, model.RetiredConfigRepoReason(watched))
				}
			}
			r = model.Recurrence{Kind: model.RecurrenceGitRef, GitRef: &cp, TimeZone: tz}
		case gh != nil:
			cp := *gh
			r = model.Recurrence{Kind: model.RecurrenceGitHubWorkflowRun, GitHubWorkflowRun: &cp, TimeZone: tz}
		default:
			return model.Recurrence{}, fmt.Errorf("%s: `trigger` needs `github_workflow_run` or `git_ref`", f.path)
		}
	case "every":
		switch {
		case raw.Every.Hours != 0 && raw.Every.Minutes != 0:
			return model.Recurrence{}, fmt.Errorf("%s: `every` takes `hours` or `minutes`, not both", f.path)
		case raw.Every.Minutes != 0:
			r = model.Recurrence{Kind: model.RecurrenceEveryNMinutes, EveryNMinutes: raw.Every.Minutes, TimeZone: tz}
		default:
			r = model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: raw.Every.Hours, TimeZone: tz}
		}
	case "daily":
		at, err := parseTimeOfDay(f, raw.Daily.At)
		if err != nil {
			return model.Recurrence{}, err
		}
		r = model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: at, TimeZone: tz}
	case "weekly":
		at, err := parseTimeOfDay(f, raw.Weekly.At)
		if err != nil {
			return model.Recurrence{}, err
		}
		day, err := parseWeekday(f, raw.Weekly.Day)
		if err != nil {
			return model.Recurrence{}, err
		}
		r = model.Recurrence{Kind: model.RecurrenceWeekly, TimeOfDay: at, Weekday: day, TimeZone: tz}
	case "monthly":
		at, err := parseTimeOfDay(f, raw.Monthly.At)
		if err != nil {
			return model.Recurrence{}, err
		}
		r = model.Recurrence{Kind: model.RecurrenceMonthly, TimeOfDay: at, DayOfMonth: raw.Monthly.Day, TimeZone: tz}
	}
	if err := r.Validate(); err != nil {
		return model.Recurrence{}, fmt.Errorf("%s: %w", f.path, err)
	}
	return r, nil
}

// parseTimeOfDay reads "09:00" as minutes since midnight. Whose clock
// that is is the schedule's own `timezone:` when the file sets one, and
// the deployment's otherwise (model.Recurrence.TimeZone).
func parseTimeOfDay(f file, at string) (int, error) {
	at = strings.TrimSpace(at)
	if at == "" {
		return 0, fmt.Errorf("%s: this cadence needs a time of day, as `at: \"09:00\"`", f.path)
	}
	hour, minute, ok := strings.Cut(at, ":")
	if !ok {
		return 0, fmt.Errorf("%s: %q is not a time of day -- write it as \"09:00\"", f.path, at)
	}
	h, herr := strconv.Atoi(strings.TrimSpace(hour))
	m, merr := strconv.Atoi(strings.TrimSpace(minute))
	if herr != nil || merr != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("%s: %q is not a time of day -- write it as \"09:00\"", f.path, at)
	}
	return h*60 + m, nil
}

func parseWeekday(f file, day string) (time.Weekday, error) {
	switch strings.ToLower(strings.TrimSpace(day)) {
	case "sunday", "sun":
		return time.Sunday, nil
	case "monday", "mon":
		return time.Monday, nil
	case "tuesday", "tue", "tues":
		return time.Tuesday, nil
	case "wednesday", "wed":
		return time.Wednesday, nil
	case "thursday", "thu", "thur", "thurs":
		return time.Thursday, nil
	case "friday", "fri":
		return time.Friday, nil
	case "saturday", "sat":
		return time.Saturday, nil
	default:
		return 0, fmt.Errorf("%s: %q is not a day of the week", f.path, day)
	}
}

// --- shared ---------------------------------------------------------------

// rawValue is a custom field's value, written either as one value or as
// a list of them -- the same two shapes a declared field itself can hold
// (model.CustomField.Multiple), so the file reads the way the field is
// described rather than forcing a one-element list everywhere.
type rawValue struct{ values []string }

func (v *rawValue) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var s string
		if err := node.Decode(&s); err != nil {
			return err
		}
		v.values = []string{s}
		return nil
	case yaml.SequenceNode:
		return node.Decode(&v.values)
	default:
		return fmt.Errorf("a custom field's value is one value or a list of them")
	}
}

// parseFields reads the inputs a template asks for, checked as they are
// converted the way ui.templateFields checks the ones typed into the
// form: a hole nothing could ever fill is refused to whoever wrote the
// file rather than discovered by whoever reaches for the template months
// later (model.ValidateTemplateFields).
func parseFields(f file, raw []rawField) ([]model.TemplateField, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]model.TemplateField, 0, len(raw))
	for _, field := range raw {
		out = append(out, model.TemplateField{
			Name:     strings.TrimSpace(field.Name),
			Label:    strings.TrimSpace(field.Label),
			Options:  field.Options,
			Default:  field.Default,
			Required: field.Required,
		})
	}
	if err := model.ValidateTemplateFields(out); err != nil {
		return nil, fmt.Errorf("%s: %w", f.path, err)
	}
	return out, nil
}

func parseCustomFields(f file, raw map[string]rawValue) (map[string][]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string][]string, len(raw))
	for name, value := range raw {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("%s: a custom field needs a name", f.path)
		}
		var values []string
		for _, v := range value.values {
			if v = strings.TrimSpace(v); v != "" {
				values = append(values, v)
			}
		}
		if len(values) > 0 {
			out[name] = values
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// parseNetwork reads the one isolation setting a declaration may carry.
//
// It is here, and the capabilities are not, because this one can only
// ever take something away: the values are "open" -- which is what a
// task in this repository already gets unless the deployment says
// otherwise -- and "none", which is a repository choosing to run its own
// standing job with no route off its sandbox's segment. A file cannot
// use it to reach anything it could not reach already.
func parseNetwork(f file, value string) (model.NetworkAccess, error) {
	switch network := model.NetworkAccess(strings.TrimSpace(value)); network {
	case "", model.NetworkOpen, model.NetworkNone:
		return network, nil
	default:
		return "", fmt.Errorf("%s: network is \"open\" or \"none\", got %q", f.path, string(network))
	}
}

// displayName is the `name:` a file gave, or the file's own stem -- so a
// declaration is never nameless in a list, and a repository that thinks
// the file name says it well enough need not say it twice.
func displayName(name, stem string) string {
	if name = strings.TrimSpace(name); name != "" {
		return name
	}
	return stem
}
