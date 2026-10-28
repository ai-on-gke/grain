package workflows

import (
	"strings"
	"testing"
)

// The message a workflow author gets for a key the format does not
// define. It is tested on its own because the incident it exists for
// was not a parse bug at all -- the parse was right -- it was three
// hours of a whole deployment's CI erroring in zero seconds while
// "field infrastructure not found in type ci.rawStep" told nobody what
// to do about it (yaml.go's note has the rest).
func TestAnUnknownKeyIsExplainedInTheFormatsOwnWords(t *testing.T) {
	for name, tc := range map[string]struct {
		yaml  string
		wants []string
	}{
		// The 21 Sep 2026 outage was this shape, with `infrastructure:`
		// as the key -- which the format does define now, so the case
		// is written with GitHub's `if:`, another key somebody
		// reasonably expects a step to have and this format has not
		// grown.
		"on a step": {
			yaml: "on: push\njobs:\n  a:\n    steps:\n      - run: true\n        if: failure()\n",
			wants: []string{
				`a step has no "if" field`,
				"a step takes: continue-on-error",
				"working-directory",
				"newer grain than this build",
				"no job in this repository runs until it is gone",
			},
		},
		"on a job": {
			yaml: "on: push\njobs:\n  a:\n    strategy: {}\n    steps:\n      - run: true\n",
			wants: []string{
				`a job has no "strategy" field`,
				"a job takes: artifacts, cache, capabilities",
				"timeout-minutes",
			},
		},
		"on the workflow": {
			yaml: "on: push\npermissions: {}\njobs:\n  a:\n    steps:\n      - run: true\n",
			wants: []string{
				`a workflow has no "permissions" field`,
				"a workflow takes: env, jobs, name, on",
			},
		},
		"on a job's sandbox": {
			yaml: "on: push\njobs:\n  a:\n    sandbox:\n      gpus: 1\n    steps:\n      - run: true\n",
			wants: []string{
				"a job's `sandbox:` has no \"gpus\" field",
				"a job's `sandbox:` takes: cpus, disk-gb, memory-mb",
			},
		},
		"on an artifact": {
			yaml: "on: push\njobs:\n  a:\n    artifacts:\n      - name: logs\n        retention-days: 3\n    steps:\n      - run: true\n",
			wants: []string{
				"an `artifacts:` entry has no \"retention-days\" field",
				"an `artifacts:` entry takes: name, path",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(".grain/workflows/w.yml", []byte(tc.yaml))
			if err == nil {
				t.Fatalf("parsed a key the format does not define")
			}
			got := err.Error()
			for _, want := range tc.wants {
				if !strings.Contains(got, want) {
					t.Errorf("error should say %q: got %v", want, got)
				}
			}
			if strings.Contains(got, "workflows.raw") {
				t.Errorf("error should not name an unexported Go type: got %v", got)
			}
		})
	}
}

// The list of keys is read off the structs, so a field added to one is
// in the message without anybody remembering to write it there. This is
// the property that makes the message worth having at all: the last one
// was wrong about `infrastructure:` the day it was added.
func TestTheKeysAnErrorListsAreTheKeysTheDecoderTakes(t *testing.T) {
	step := yamlFieldNames(rawTypeNouns["workflows.rawStep"].typ)
	for _, want := range []string{"continue-on-error", "env", "infrastructure", "name", "run", "timeout-minutes", "working-directory"} {
		if !contains(step, want) {
			t.Errorf("a step's keys should include %q: got %v", want, step)
		}
	}
	// Sorted, so the message does not reorder itself when a field moves.
	for i := 1; i < len(step); i++ {
		if step[i-1] >= step[i] {
			t.Fatalf("keys should be sorted and distinct: got %v", step)
		}
	}
}

// A TypeError line that is not about an unknown key is the library's to
// word, and grain must not mangle it -- nor add the note, which would
// be false of it.
func TestAValueOfTheWrongShapeKeepsTheLibrarysWording(t *testing.T) {
	_, err := Parse(".grain/workflows/w.yml", []byte("on: push\njobs:\n  a:\n    timeout-minutes: soon\n    steps:\n      - run: true\n"))
	if err == nil {
		t.Fatal("parsed a timeout that is not a number")
	}
	if strings.Contains(err.Error(), unknownFieldNote) {
		t.Errorf("the unknown-key note should not follow an error about a value: got %v", err)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
