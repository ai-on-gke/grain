package repodecl_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

const plannerTemplate = `
title: Plan {{group}}
body: Plan it.
`

func TestTriggerParsesEveryField(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/design-planner.yml": plannerTemplate,
		".grain/triggers/design.yml": `
on: merge
branches: [main, "rc/v*"]
paths: [docs/design/**]
ignore: ["**/plan.yml"]
template: design-planner
per: folder
auto-approve: true
`,
		".grain/triggers/sensitive.yml": `
on: pull_request
paths: [config/secrets]
template: template-12
gate: true
human: required
`,
	})
	want := []model.PathTrigger{
		{
			Name: "design", Path: ".grain/triggers/design.yml", On: model.TriggerOnMerge,
			Branches: []string{"main", "rc/v*"}, Paths: []string{"docs/design/**"}, Ignore: []string{"**/plan.yml"},
			Template: model.RepoDeclaredID(model.DeclaredTemplate, repo, "design-planner"),
			Per:      model.TriggerPerFolder, AutoApprove: true,
		},
		{
			Name: "sensitive", Path: ".grain/triggers/sensitive.yml", On: model.TriggerOnPullRequest,
			Paths: []string{"config/secrets"}, Template: "template-12", Per: model.TriggerPerChange,
			Gate: true, Human: model.TriggerHumanRequired,
		},
	}
	if !reflect.DeepEqual(declared.Triggers, want) {
		t.Fatalf("triggers =\n%+v\nwant\n%+v", declared.Triggers, want)
	}
}

func TestAGateDefaultsToOnFindings(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/templates/check.yml": plannerTemplate,
		".grain/triggers/gate.yml":   "on: pull_request\npaths: [x]\ntemplate: check\ngate: true\n",
	})
	if got := declared.Triggers[0].Human; got != model.TriggerHumanOnFindings {
		t.Fatalf("human = %q", got)
	}
}

func TestTriggerRefusals(t *testing.T) {
	cases := map[string]struct{ file, want string }{
		"no event":           {"paths: [x]\ntemplate: check\n", "needs `on: merge` or `on: pull_request`"},
		"unknown event":      {"on: push\npaths: [x]\ntemplate: check\n", `on is "merge" or "pull_request"`},
		"no paths":           {"on: merge\ntemplate: check\n", "a trigger needs `paths:`"},
		"bad glob":           {"on: merge\npaths: [\"a/[b\"]\ntemplate: check\n", "paths:"},
		"bad ignore":         {"on: merge\npaths: [a]\nignore: [\"a/**b\"]\ntemplate: check\n", "ignore:"},
		"bad branch":         {"on: merge\nbranches: [\"rc/[\"]\npaths: [a]\ntemplate: check\n", "branches:"},
		"no template":        {"on: merge\npaths: [a]\n", "needs a `template:`"},
		"missing stem":       {"on: merge\npaths: [a]\ntemplate: nope\n", `no template "nope"`},
		"gate on merge":      {"on: merge\npaths: [a]\ntemplate: check\ngate: true\n", "`on: pull_request` only"},
		"human, no gate":     {"on: pull_request\npaths: [a]\ntemplate: check\nhuman: no\n", "`gate: true` only"},
		"unknown human":      {"on: pull_request\npaths: [a]\ntemplate: check\ngate: true\nhuman: maybe\n", `human is "required"`},
		"unknown per":        {"on: merge\npaths: [a]\ntemplate: check\nper: file\n", `per is "change" or "folder"`},
		"unknown key":        {"on: merge\npaths: [a]\ntemplate: check\nreads: [x/y]\n", "reads"},
		"gate false, mrg":    {"on: merge\npaths: [a]\ntemplate: check\ngate: false\n", "`on: pull_request` only"},
		"sensitive, no gate": {"on: pull_request\npaths: [a]\nsensitive: true\n", "needs `on: pull_request` and `gate: true`"},
		"none, human no": {"on: pull_request\npaths: [a]\ntemplate: none\ngate: true\nhuman: no\nsensitive: true\n",
			"`template: none` with `human: no`"},
		"none, on-findings": {"on: pull_request\npaths: [a]\ntemplate: none\ngate: true\nsensitive: true\n",
			"`template: none` with `human: on-findings`"},
		"none, not sensitive": {"on: pull_request\npaths: [a]\ntemplate: none\ngate: true\nhuman: required\n",
			"only a sensitive trigger"},
		"approvers, not sensitive": {"on: pull_request\npaths: [a]\ntemplate: check\ngate: true\napprovers: [alice]\n",
			"`sensitive: true` only"},
		"empty team": {"on: pull_request\npaths: [a]\ngate: true\nsensitive: true\napprovers: [\"team:\"]\n", "names no team"},
		"odd approver": {"on: pull_request\npaths: [a]\ngate: true\nsensitive: true\napprovers: [\"group:x\"]\n",
			"is not a user or `team:<name>`"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			msg := loadErr(t, map[string]string{
				".grain/templates/check.yml":  plannerTemplate,
				".grain/triggers/trigger.yml": c.file,
			})
			if !strings.Contains(msg, c.want) || !strings.Contains(msg, ".grain/triggers/trigger.yml") {
				t.Errorf("error = %q, want it to name the file and say %q", msg, c.want)
			}
		})
	}
}

func TestASensitiveTriggerDefaultsToTheSecurityReviewAndKeepsItsApprovers(t *testing.T) {
	declared := load(t, map[string]string{
		".grain/triggers/payments.yml": "on: pull_request\npaths: [payments]\ngate: true\nhuman: required\n" +
			"sensitive: true\napprovers: [\"team:payments\", alice, alice]\n",
		".grain/triggers/keys.yml": "on: pull_request\npaths: [keys]\ngate: true\nhuman: required\n" +
			"sensitive: true\ntemplate: none\n",
	})
	byName := map[string]model.PathTrigger{}
	for _, tr := range declared.Triggers {
		byName[tr.Name] = tr
	}
	payments := byName["payments"]
	if !payments.Sensitive || payments.Template != model.SecurityReviewTemplateID ||
		!reflect.DeepEqual(payments.Approvers, []string{"team:payments", "alice"}) {
		t.Errorf("payments = %+v", payments)
	}
	if keys := byName["keys"]; keys.Template != model.TemplateNone || len(keys.Approvers) != 0 {
		t.Errorf("keys = %+v", keys)
	}
}
