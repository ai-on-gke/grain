package repodecl

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/ai-on-gke/grain/pkg/model"
)

// `.grain/triggers/<name>.yml` (grain/task-1712): run a template on every
// merge, or every pull request, that touches a set of paths. model's
// path_trigger.go has what one is; this is the YAML surface and the
// checks a file has to pass before it is anything at all.

// rawTrigger is one trigger file as written.
type rawTrigger struct {
	On          string   `yaml:"on"`
	Branches    []string `yaml:"branches"`
	Paths       []string `yaml:"paths"`
	Ignore      []string `yaml:"ignore"`
	Template    string   `yaml:"template"`
	Per         string   `yaml:"per"`
	Gate        *bool    `yaml:"gate"`
	Human       string   `yaml:"human"`
	AutoApprove bool     `yaml:"auto-approve"`
	Sensitive   bool     `yaml:"sensitive"`
	Approvers   []string `yaml:"approvers"`
}

// loadTriggers reads and checks every file in `.grain/triggers`.
// templates is the stems this tree declares, which is what a trigger's
// template is resolved against first.
func loadTriggers(tree fs.FS, repo model.RepoRef, templates map[string]bool) ([]model.PathTrigger, error) {
	files, err := readDir(tree, model.TriggerDeclarationDir)
	if err != nil {
		return nil, err
	}
	var out []model.PathTrigger
	var errs []error
	for _, f := range files {
		trigger, err := parseTrigger(f, repo, templates)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, trigger)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

// parseTrigger turns one file into a model.PathTrigger, refusing
// anything that would be a trigger nobody meant: no paths, a glob that
// does not parse, a template that is not there, or a gate on an event
// that has no merge to hold.
//
// The template is a stem of this repository's own `.grain/templates`
// when one by that name exists. Otherwise it has to be shaped like a
// deployment template's id (deploymentTemplateID) -- anything else is a
// stem somebody misspelt, and is an error here rather than an id the
// firing would fail to find. Whether an id names a template this
// deployment actually has is checked when it is synced
// (orchestrator.checkTriggerTemplates), since a tree cannot say.
func parseTrigger(f file, repo model.RepoRef, templates map[string]bool) (model.PathTrigger, error) {
	var raw rawTrigger
	if err := decodeInto(f, &raw); err != nil {
		return model.PathTrigger{}, err
	}
	t := model.PathTrigger{
		Name:        f.stem,
		Path:        f.path,
		On:          model.TriggerEvent(strings.TrimSpace(raw.On)),
		AutoApprove: raw.AutoApprove,
	}
	var errs []error
	switch t.On {
	case model.TriggerOnMerge, model.TriggerOnPullRequest:
	case "":
		errs = append(errs, fmt.Errorf("%s: a trigger needs `on: merge` or `on: pull_request`", f.path))
	default:
		errs = append(errs, fmt.Errorf("%s: on is \"merge\" or \"pull_request\", got %q", f.path, raw.On))
	}

	for _, b := range raw.Branches {
		if err := model.ValidateBranchPattern(b); err != nil {
			errs = append(errs, fmt.Errorf("%s: branches: %w", f.path, err))
			continue
		}
		t.Branches = append(t.Branches, strings.TrimSpace(b))
	}
	if len(raw.Paths) == 0 {
		errs = append(errs, fmt.Errorf("%s: a trigger needs `paths:` -- the files and folders whose changes fire it", f.path))
	}
	for _, p := range raw.Paths {
		if err := model.ValidatePathPattern(p); err != nil {
			errs = append(errs, fmt.Errorf("%s: paths: %w", f.path, err))
			continue
		}
		t.Paths = append(t.Paths, strings.TrimSpace(p))
	}
	for _, p := range raw.Ignore {
		if err := model.ValidatePathPattern(p); err != nil {
			errs = append(errs, fmt.Errorf("%s: ignore: %w", f.path, err))
			continue
		}
		t.Ignore = append(t.Ignore, strings.TrimSpace(p))
	}

	template := strings.TrimSpace(raw.Template)
	if raw.Sensitive && template == "" {
		// A sensitive path is reviewed by grain's own security review
		// unless the trigger names something else.
		template = model.SecurityReviewTemplateID
	}
	switch {
	case raw.Sensitive && template == model.TemplateNone:
		t.Template = model.TemplateNone
	case template == "":
		errs = append(errs, fmt.Errorf("%s: a trigger needs a `template:` to file", f.path))
	case templates[template]:
		t.Template = model.RepoDeclaredID(model.DeclaredTemplate, repo, template)
	case !deploymentTemplateID(template):
		errs = append(errs, fmt.Errorf("%s: no template %q -- a trigger files a template this "+
			"repository declares, named by file (%s/%s.yml), or a deployment template by its id",
			f.path, template, model.TemplateDeclarationDir, template))
	default:
		t.Template = template
	}

	switch per := model.TriggerPer(strings.TrimSpace(raw.Per)); per {
	case "":
		t.Per = model.TriggerPerChange
	case model.TriggerPerChange, model.TriggerPerFolder:
		t.Per = per
	default:
		errs = append(errs, fmt.Errorf("%s: per is \"change\" or \"folder\", got %q", f.path, raw.Per))
	}

	human := model.TriggerHuman(strings.TrimSpace(raw.Human))
	switch human {
	case "", model.TriggerHumanRequired, model.TriggerHumanOnFindings, model.TriggerHumanNo:
	default:
		errs = append(errs, fmt.Errorf("%s: human is \"required\", \"on-findings\" or \"no\", got %q",
			f.path, raw.Human))
	}
	if raw.Gate != nil && t.On != model.TriggerOnPullRequest {
		errs = append(errs, fmt.Errorf("%s: gate holds a pull request's merge, so it goes with "+
			"`on: pull_request` only", f.path))
	}
	gate := raw.Gate != nil && *raw.Gate
	if human != "" && !gate {
		errs = append(errs, fmt.Errorf("%s: human says what releases a gate, so it goes with "+
			"`gate: true` only", f.path))
	}
	if gate {
		t.Gate = true
		t.Human = human
		if t.Human == "" {
			t.Human = model.TriggerHumanOnFindings
		}
	}
	errs = append(errs, parseSensitive(f, raw, &t)...)
	if len(errs) > 0 {
		return model.PathTrigger{}, errors.Join(errs...)
	}
	return t, nil
}

// parseSensitive checks what `sensitive: true` adds to a gating trigger
// (model's sensitive_path.go): that it gates at all, that it leaves at
// least one kind of review standing, and that its approvers are shaped
// like approvers. Whether they name anybody this deployment knows is the
// sync's to check (orchestrator.checkTriggerApprovers).
func parseSensitive(f file, raw rawTrigger, t *model.PathTrigger) []error {
	var errs []error
	if !raw.Sensitive {
		if len(raw.Approvers) > 0 {
			errs = append(errs, fmt.Errorf("%s: approvers say who releases a sensitive path, so they go "+
				"with `sensitive: true` only", f.path))
		}
		if strings.TrimSpace(raw.Template) == model.TemplateNone {
			errs = append(errs, fmt.Errorf("%s: `template: none` files nothing, which only a sensitive "+
				"trigger waiting for a human may do", f.path))
		}
		return errs
	}
	t.Sensitive = true
	if !t.Gate {
		errs = append(errs, fmt.Errorf("%s: a sensitive trigger holds the change it matches, so it "+
			"needs `on: pull_request` and `gate: true`", f.path))
	}
	if t.Template == model.TemplateNone && t.Gate && t.Human != model.TriggerHumanRequired {
		errs = append(errs, fmt.Errorf("%s: `template: none` with `human: %s` would let a sensitive path "+
			"through with no review at all -- name a template, or say `human: required`", f.path, t.Human))
	}
	seen := map[string]bool{}
	for _, a := range raw.Approvers {
		a = strings.TrimSpace(a)
		switch {
		case a == "":
			errs = append(errs, fmt.Errorf("%s: approvers: an empty approver names nobody", f.path))
		case strings.HasPrefix(a, model.ApproverTeamPrefix) && strings.TrimSpace(strings.TrimPrefix(a, model.ApproverTeamPrefix)) == "":
			errs = append(errs, fmt.Errorf("%s: approvers: %q names no team", f.path, a))
		case strings.ContainsAny(a, ", ="):
			errs = append(errs, fmt.Errorf("%s: approvers: %q is not a user or `team:<name>`", f.path, a))
		case strings.Contains(a, ":") && !strings.HasPrefix(a, model.ApproverTeamPrefix) && !strings.HasPrefix(a, "user:"):
			errs = append(errs, fmt.Errorf("%s: approvers: %q is not a user or `team:<name>`", f.path, a))
		case !seen[strings.ToLower(a)]:
			seen[strings.ToLower(a)] = true
			t.Approvers = append(t.Approvers, a)
		}
	}
	return errs
}

// deploymentTemplateID reports whether name is shaped like an id grain
// gives a template of its own: `template-<n>`, or a prefixed one such as
// `system:...`.
func deploymentTemplateID(name string) bool {
	if strings.Contains(name, ":") {
		return true
	}
	n, ok := strings.CutPrefix(name, "template-")
	if !ok || n == "" {
		return false
	}
	for _, r := range n {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
