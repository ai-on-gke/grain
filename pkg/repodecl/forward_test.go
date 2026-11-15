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

package repodecl_test

// A tree written for a newer grain than the one reading it
// (grain/task-4392): `overlap:` became a mapping on main while the
// deployed daemon still decoded it as a string, and every other file the
// repository declared was refused with it.

import (
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repodecl"
)

// newerTree has one schedule in a shape this build cannot decode -- a key
// it has never heard of, which is what any newer field looks like to an
// older parser -- beside a template, a suite and a schedule it can.
func newerTree() map[string]string {
	return map[string]string{
		".grain/templates/sync.yml":  "title: Sync\n",
		".grain/templates/train.yml": "title: Train\n",
		".grain/suites/nightly.yml":  "templates: [sync]\n",
		".grain/schedules/sync.yml":  "template: sync\ndaily:\n  at: \"02:30\"\noverlap: {skip: true, group: g}\n",
		".grain/schedules/train.yml": "template: train\ndaily:\n  at: \"03:30\"\nfrom-a-newer-grain: {shape: new}\n",
	}
}

func TestAFileTooNewForThisParserIsHeldBackAndTheRestLoad(t *testing.T) {
	declared, err := repodecl.LoadForwardCompatible(tree(newerTree()), repo)
	if err != nil {
		t.Fatalf("LoadForwardCompatible: %v", err)
	}
	if len(declared.Templates) != 2 || len(declared.Suites) != 1 || len(declared.Schedules) != 1 {
		t.Fatalf("declared %d template(s), %d suite(s), %d schedule(s), want 2, 1, 1",
			len(declared.Templates), len(declared.Suites), len(declared.Schedules))
	}
	if got := declared.Schedules[0].Path; got != ".grain/schedules/sync.yml" {
		t.Errorf("loaded schedule = %s, want sync.yml", got)
	}
	if len(declared.Refused) != 1 {
		t.Fatalf("refused = %+v, want train.yml alone", declared.Refused)
	}
	r := declared.Refused[0]
	if r.Kind != model.DeclaredSchedule || r.Path != ".grain/schedules/train.yml" || r.Stem != "train" {
		t.Errorf("refused = %+v", r)
	}
	if !strings.Contains(r.Message, "line 4") || !strings.Contains(r.Message, "from-a-newer-grain") {
		t.Errorf("message = %q, want the line and the key", r.Message)
	}
}

// A held-back file's message does not repeat its path: everything that
// shows it (the sync's error, `grain health`) writes Path in front of it,
// and an unknown key in a template's `sandbox:` read ".grain/templates/
// big.yml: .grain/templates/big.yml: line 5: ..."
// (docs/test-runs/2026-11-13-schedules-templates.md, F3).
func TestAHeldBackFilesMessageDoesNotRepeatItsPath(t *testing.T) {
	files := map[string]string{
		".grain/templates/big.yml":   "title: Big\nsandbox:\n  disk-gb: 60\n  gpus: 1\n",
		".grain/schedules/train.yml": "template: big\ndaily:\n  at: \"03:30\"\nfrom-a-newer-grain: {shape: new}\n",
	}
	declared, err := repodecl.LoadForwardCompatible(tree(files), repo)
	if err != nil {
		t.Fatalf("LoadForwardCompatible: %v", err)
	}
	if len(declared.Refused) != 2 {
		t.Fatalf("refused = %+v, want both files", declared.Refused)
	}
	for _, r := range declared.Refused {
		if strings.Contains(r.Message, r.Path) {
			t.Errorf("%s: message %q repeats the path", r.Path, r.Message)
		}
		if !strings.Contains(r.Message, "line ") {
			t.Errorf("%s: message %q, want the decoder's own, naming the line", r.Path, r.Message)
		}
	}
}

// `grain declarations check` is strict: CI is where a file too new for the
// code it runs is caught, and it must still fail there.
func TestTheStrictLoadStillRefusesTheWholeSet(t *testing.T) {
	_, err := repodecl.Load(tree(newerTree()), repo)
	var schema *repodecl.SchemaError
	if err == nil || !errors.As(err, &schema) {
		t.Fatalf("Load = %v, want a SchemaError", err)
	}
}

// A held-back template is still a name the files after it may use: the
// deployment has a version of it, and the sync keeps that one.
func TestAHeldBackTemplateStillResolvesForItsSuite(t *testing.T) {
	files := newerTree()
	files[".grain/templates/sync.yml"] = "title: Sync\nfrom-a-newer-grain: true\n"
	declared, err := repodecl.LoadForwardCompatible(tree(files), repo)
	if err != nil {
		t.Fatalf("LoadForwardCompatible: %v", err)
	}
	if len(declared.Suites) != 1 || len(declared.Refused) != 2 {
		t.Fatalf("suites %d, refused %+v", len(declared.Suites), declared.Refused)
	}
}

// Only a newer shape is held back. A file that is not YAML, a key a
// repository may never set, or a value out of range is a mistake in the
// file whatever grain reads it, and refuses the set as it always has.
func TestEveryOtherErrorStillRefusesTheWholeSet(t *testing.T) {
	for name, body := range map[string]string{
		"not yaml":      "template: sync\ndaily: [\n",
		"refused key":   "template: sync\ndaily:\n  at: \"02:30\"\nreads: [acme/other]\n",
		"bad value":     "template: sync\ndaily:\n  at: \"25:00\"\n",
		"no such thing": "template: missing\ndaily:\n  at: \"02:30\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			files := newerTree()
			files[".grain/schedules/train.yml"] = body
			if _, err := repodecl.LoadForwardCompatible(tree(files), repo); err == nil {
				t.Fatal("LoadForwardCompatible accepted it")
			}
		})
	}
}

// `overlap:` in both forms parses on this build, which is the other half
// of the incident: both are what main's release-train schedules write.
func TestOverlapAsAStringAndAsAMappingBothParseForwardCompatibly(t *testing.T) {
	for _, overlap := range []string{"overlap: skip\n", "overlap: {skip: true, group: release-train}\n"} {
		declared, err := repodecl.LoadForwardCompatible(tree(map[string]string{
			".grain/templates/sync.yml": "title: Sync\n",
			".grain/schedules/sync.yml": "template: sync\ndaily:\n  at: \"02:30\"\n" + overlap,
		}), repo)
		if err != nil || len(declared.Refused) != 0 || len(declared.Schedules) != 1 {
			t.Fatalf("%q: err %v, refused %+v", overlap, err, declared.Refused)
		}
		if declared.Schedules[0].Schedule.Overlap != model.OverlapSkip {
			t.Errorf("%q: overlap = %q", overlap, declared.Schedules[0].Schedule.Overlap)
		}
	}
}
