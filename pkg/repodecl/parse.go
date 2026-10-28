package repodecl

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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
		return fmt.Errorf("%s: %w", f.path, err)
	}
	return nil
}

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
	// NothingNeeded is model.Template.NothingNeededAction: "close" (what
	// empty means) or "ack".
	NothingNeeded string              `yaml:"nothing-needed"`
	CustomFields  map[string]rawValue `yaml:"custom-fields"`
	// Capabilities is the repository access every task filed from this
	// template carries, and only that: `repo-read:<owner>/<repo>` and
	// `repo-write:<owner>/<repo>` (parseRepoAccess).
	Capabilities model.CapabilityList `yaml:"capabilities"`
	// Push is the refs beyond their own branch that the runs of tasks
	// filed from this template may update in this repository
	// (model.RefPush, grain/task-2536; parsePush).
	Push []rawPush `yaml:"push"`
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
	reads, writes, grants, err := parseCapabilities(f, repo, raw.Capabilities)
	if err != nil {
		return model.Template{}, raw, err
	}
	if err := checkTeam(f, raw.Team); err != nil {
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
		NothingNeededAction: nothingNeeded,
	}
	if raw.AutoMerge != nil {
		tmpl.AutoMerge = *raw.AutoMerge
	}
	return tmpl, raw, nil
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

// parseCapabilities parses a declared template's or schedule's `capabilities:`.
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
func parseCapabilities(f file, repo model.RepoRef, caps []string) ([]model.RepoRef, []model.WriteTarget, []model.Grant, error) {
	var reads []model.RepoRef
	var writes []model.WriteTarget
	var grants []model.Grant
	var errs []error
	seenGrants := map[string]bool{}

	for _, c := range caps {
		c = strings.TrimSpace(c)
		if c == "" {
			errs = append(errs, fmt.Errorf("%s: capabilities: capability name cannot be empty", f.path))
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
		case model.IsCapabilityRef(c):
			// A capability reference (docs/capabilities-spec.md §2.2):
			// its grammar is checked here; its with: arguments against
			// its server's schema when a task is filed from it, and
			// again at admission.
			if err := model.CheckCapabilityGrantSyntax(c); err != nil {
				errs = append(errs, fmt.Errorf("%s: capabilities: %v", f.path, err))
				continue
			}
			if !seenGrants[c] {
				seenGrants[c] = true
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
			if !seenGrants[c] {
				seenGrants[c] = true
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
		return nil, nil, nil, err
	}
	return reads, writes, grants, nil
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
}

func parseSuite(f file, repo model.RepoRef, templates map[string]bool) (model.Suite, error) {
	var raw rawSuite
	if err := decode(f, model.DeclaredSuite, &raw); err != nil {
		return model.Suite{}, err
	}
	if len(raw.Templates) == 0 {
		return model.Suite{}, fmt.Errorf("%s: a suite needs at least one template, named by file: "+
			"`templates: [nightly]` runs %s/nightly.yml", f.path, model.TemplateDeclarationDir)
	}
	items := make([]model.SuiteItem, 0, len(raw.Templates))
	for _, name := range raw.Templates {
		name = strings.TrimSpace(name)
		if !templates[name] {
			return model.Suite{}, fmt.Errorf("%s: no template %q -- a suite runs the templates this "+
				"repository declares, named by file (%s/%s.yml)", f.path, name, model.TemplateDeclarationDir, name)
		}
		items = append(items, model.SuiteItem{TemplateID: model.RepoDeclaredID(model.DeclaredTemplate, repo, name)})
	}

	if err := checkTeam(f, raw.Team); err != nil {
		return model.Suite{}, err
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
		return model.Suite{}, fmt.Errorf("%s: mode is \"count\" or \"until-clean\", got %q", f.path, mode)
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
		return model.Suite{}, fmt.Errorf("%s: %w", f.path, err)
	}
	return suite, nil
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
	Every                *rawEvery            `yaml:"every"`
	Daily                *rawAt               `yaml:"daily"`
	Weekly               *rawWeekly           `yaml:"weekly"`
	Monthly              *rawDayAt            `yaml:"monthly"`
	Overlap              string               `yaml:"overlap"`
	Review               string               `yaml:"review"`
	Enabled              *bool                `yaml:"enabled"`
	Model                string               `yaml:"model"`
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
		id := model.RepoDeclaredID(model.DeclaredSuite, repo, suite)
		sched.SuiteID = &id
		sched.Title = displayName(raw.Name, f.stem)
	default:
		return model.Schedule{}, fmt.Errorf("%s: a schedule needs a `template:` to fire, a `suite:` to run "+
			"or a `workflow:` to run", f.path)
	}

	recurrence, err := parseRecurrence(f, raw)
	if err != nil {
		return model.Schedule{}, err
	}
	sched.Recurrence = recurrence
	switch overlap := model.OverlapPolicy(strings.TrimSpace(raw.Overlap)); overlap {
	case "", model.OverlapSkip, model.OverlapAllow:
		sched.Overlap = overlap
	default:
		return model.Schedule{}, fmt.Errorf("%s: overlap is \"skip\" or \"allow\", got %q", f.path, string(overlap))
	}
	if workflow != "" {
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
	if sched.WorkflowReview != "" && workflow == "" {
		return model.Schedule{}, fmt.Errorf("%s: `review:` says when the run of a scheduled workflow is "+
			"reviewed, and this schedule runs no `workflow:`", f.path)
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

	reads, writes, grants, err := parseCapabilities(f, repo, raw.Capabilities)
	if err != nil {
		return model.Schedule{}, err
	}
	if len(reads) > 0 || len(writes) > 0 {
		return model.Schedule{}, fmt.Errorf("%s: capabilities: a schedule fires a template, runs a suite or runs a workflow, "+
			"and repository access belongs on the template it fires (repo-read/repo-write)", f.path)
	}
	sched.Grants = grants

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
	if len(found) != 1 {
		return model.Recurrence{}, fmt.Errorf("%s: a schedule needs exactly one cadence -- "+
			"`every: {hours: N}`, `every: {minutes: N}`, `daily: {at: \"09:00\"}`, `weekly: {day: monday, at: \"09:00\"}` or "+
			"`monthly: {day: 1, at: \"09:00\"}` -- and this one has %d", f.path, len(found))
	}
	tz := strings.TrimSpace(raw.TimeZone)
	var r model.Recurrence
	switch found[0] {
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
