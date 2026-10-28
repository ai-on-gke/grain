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

package ci

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// The three answers a job's `capabilities:` key can give
// (requirements.go's jobCapabilities): absent is everything the
// deployment provides, `[]` is nothing, and a list is exactly that list.
func TestCapabilitiesAbsentEmptyAndListed(t *testing.T) {
	grants := map[string][]string{"acme/widgets": {"gcp-key", "gemini-key", "package-publish"}}
	for _, tc := range []struct {
		name, decl string
		want       []string
	}{
		{"absent", "", []string{"gcp-key", "gemini-key", "package-publish"}},
		{"empty", "    capabilities: []\n", nil},
		{"listed", "    capabilities: [gcp-key]\n", []string{"gcp-key"}},
		{"empty with an optional one", "    capabilities: []\n    optional-capabilities: [gemini-key]\n", []string{"gemini-key"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job, given := runOptional(t, "on: push\njobs:\n  a:\n"+tc.decl+"    steps:\n      - run: true\n", grants)
			if job.Conclusion != ConclusionSuccess || job.Error != "" {
				t.Fatalf("the job concluded %q (%q)", job.Conclusion, job.Error)
			}
			if len(given) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(given, tc.want) {
				t.Errorf("the job was given %v, want %v", given, tc.want)
			}
		})
	}
}

// What the parser keeps: only a present, empty list is NoCapabilities.
// A null value (`capabilities:` with nothing after it) reads as absent.
func TestParseTellsEmptyCapabilitiesFromAbsent(t *testing.T) {
	for _, tc := range []struct {
		decl string
		none bool
		caps []string
	}{
		{"", false, nil},
		{"    capabilities:\n", false, nil},
		{"    capabilities: []\n", true, nil},
		{"    capabilities: gcp-key\n", false, []string{"gcp-key"}},
		{"    capabilities: [gcp-key]\n", false, []string{"gcp-key"}},
	} {
		w, err := Parse(".grain/workflows/test.yml", []byte("on: push\njobs:\n  a:\n"+tc.decl+"    steps:\n      - run: true\n"))
		if err != nil {
			t.Fatalf("%q: %v", tc.decl, err)
		}
		job := w.Jobs[0]
		if job.NoCapabilities != tc.none || !reflect.DeepEqual(job.Capabilities, tc.caps) {
			t.Errorf("%q parsed as NoCapabilities=%v Capabilities=%v, want %v %v", tc.decl, job.NoCapabilities, job.Capabilities, tc.none, tc.caps)
		}
	}
}

// Repository access a job declares beside nothing else is still kept,
// and `[]` does not let one the deployment's list names leak in.
func TestEmptyCapabilitiesLeavesRepositoryAccessAlone(t *testing.T) {
	provided := []string{"gcp-key", "repo-read:acme/other"}
	if got := jobCapabilities(Job{NoCapabilities: true}, provided, nil); len(got) != 0 {
		t.Errorf("capabilities: [] was given %v, want nothing", got)
	}
	if got := jobCapabilities(Job{}, provided, nil); !reflect.DeepEqual(got, []string{"gcp-key"}) {
		t.Errorf("an absent key was given %v, want the deployment's set without repository access", got)
	}
}

// This repository's own suite: every job in tests.yml says it needs no
// deployment capability, so none of them mints a gcp-key or gemini-key
// (the 2026-10-23 IAM quota storm), and gcp-smoke is given its key --
// as the gcp server's reference, which needs no place in the
// repository's CI capabilities, rather than the legacy in-process gcp-key.
func TestSelfWorkflowsAskOnlyForWhatTheyUse(t *testing.T) {
	workflows, err := LoadNativeDir(os.DirFS(selfRepoRoot))
	if err != nil {
		t.Fatal(err)
	}
	provided := []string{"gemini-key", "gcp-key", "package-publish"}
	var testsJobs int
	var smoke bool
	for _, w := range workflows {
		for _, job := range w.Jobs {
			got := jobCapabilities(job, provided, nil)
			switch {
			case strings.HasSuffix(w.Path, "/tests.yml"):
				testsJobs++
				if !job.NoCapabilities || len(got) != 0 {
					t.Errorf("tests.yml job %s resolves to %v (NoCapabilities=%v), want capabilities: [] and nothing given", job.ID, got, job.NoCapabilities)
				}
			case strings.HasSuffix(w.Path, "/gcp-smoke.yml"):
				smoke = true
				want := []string{"https://grain.dev/capabilities/gcp#gcp-key"}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("gcp-smoke job %s resolves to %v, want %v", job.ID, got, want)
				}
			}
		}
	}
	if testsJobs == 0 || !smoke {
		t.Fatalf("found %d tests.yml jobs and gcp-smoke=%v; the loader did not read this repository's workflows", testsJobs, smoke)
	}
}
