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
	"context"
	"reflect"
	"strings"
	"testing"
)

// The job grain/task-2016 exists for, in miniature: a publisher that
// requires `package-publish` and signs with `image-signing` when it has
// it (requirements.go's "Optional capabilities").
const optionalCapabilityWorkflow = "on: push\njobs:\n" +
	"  publish:\n    cache: false\n" +
	"    capabilities: [package-publish]\n" +
	"    optional-capabilities: [image-signing]\n" +
	"    steps:\n      - run: true\n"

// runOptional runs workflow for acme/widgets against a deployment that
// grants each repository what grants names for it, and returns the one
// job's record and the capabilities its sandbox was handed.
func runOptional(t *testing.T, workflow string, grants map[string][]string) (JobRun, []string) {
	t.Helper()
	backend := &fakeSandboxes{}
	service := newTestService(t, backend, map[string]string{".grain/workflows/test.yml": workflow})
	service.Capabilities = func(_ context.Context, repo string) ([]string, error) {
		return grants[repo], nil
	}
	run, err := service.Start(context.Background(), Request{Repo: "acme/widgets", Ref: "main", Event: EventPush})
	if err != nil {
		t.Fatal(err)
	}
	got := waitForRun(t, service, run.ID)
	if len(got.Jobs) != 1 {
		t.Fatalf("run reported jobs %+v", got.Jobs)
	}
	if len(backend.acquired) != 1 {
		t.Fatalf("job %+v acquired %d sandboxes, want 1 -- an optional capability must never keep a job from running",
			got.Jobs[0], len(backend.acquired))
	}
	return got.Jobs[0], backend.acquired[0].Capabilities
}

// Absent: the deployment does not provide it, and the job runs anyway
// with only what it required -- which is the whole point, and the
// opposite of what `capabilities:` does with the same name.
func TestAnAbsentOptionalCapabilityIsNotPlacedAndDoesNotRefuseTheJob(t *testing.T) {
	job, given := runOptional(t, optionalCapabilityWorkflow, map[string][]string{
		"acme/widgets": {"package-publish"},
	})
	if job.Conclusion != ConclusionSuccess || job.Error != "" {
		t.Fatalf("the job concluded %q (%q), want it to run without the optional capability", job.Conclusion, job.Error)
	}
	if !reflect.DeepEqual(given, []string{"package-publish"}) {
		t.Errorf("the job was given %v, want only its requirement", given)
	}

	// The same with nothing required at all: still runs, still given
	// nothing -- declaring an optional capability is a declaration, and
	// does not fall back to the deployment's whole set.
	job, given = runOptional(t, "on: push\njobs:\n  sign:\n    optional-capabilities: image-signing\n    steps:\n      - run: true\n",
		map[string][]string{"acme/widgets": {"gemini-key"}})
	if job.Conclusion != ConclusionSuccess {
		t.Fatalf("a job with only an absent optional capability concluded %q (%q)", job.Conclusion, job.Error)
	}
	if len(given) != 0 {
		t.Errorf("a job that declared only an absent optional capability was given %v, want nothing", given)
	}
}

// Provided and granted: it is placed, beside the requirement, and
// nothing else the repository is granted comes with it.
func TestAProvidedOptionalCapabilityIsPlaced(t *testing.T) {
	job, given := runOptional(t, optionalCapabilityWorkflow, map[string][]string{
		"acme/widgets": {"gemini-key", "image-signing", "package-publish"},
	})
	if job.Conclusion != ConclusionSuccess {
		t.Fatalf("the job concluded %q (%q)", job.Conclusion, job.Error)
	}
	if !reflect.DeepEqual(given, []string{"package-publish", "image-signing"}) {
		t.Errorf("the job was given %v, want its requirement and its optional capability and nothing else", given)
	}
}

// Provided but not granted: the deployment holds the capability and
// grants it to another repository, not this one. Nothing is placed and
// the job is not refused -- the grant is still the deployment's alone,
// and an optional declaration is no more a way round it than a required
// one is.
func TestAnOptionalCapabilityGrantedOnlyToAnotherRepositoryIsNotPlaced(t *testing.T) {
	job, given := runOptional(t, optionalCapabilityWorkflow, map[string][]string{
		"acme/widgets": {"package-publish"},
		"acme/other":   {"image-signing", "package-publish"},
	})
	if job.Conclusion != ConclusionSuccess || job.Error != "" {
		t.Fatalf("the job concluded %q (%q), want it to run without the optional capability", job.Conclusion, job.Error)
	}
	if !reflect.DeepEqual(given, []string{"package-publish"}) {
		t.Errorf("the job was given %v, want only its requirement", given)
	}
}

// A name nobody has heard of parses, runs and is placed nowhere: an
// unknown optional capability is the same case as an ungranted one.
func TestAnUnknownOptionalCapabilityIsIgnored(t *testing.T) {
	w, err := Parse(".grain/workflows/test.yml", []byte("on: push\njobs:\n  a:\n    optional-capabilities: [never-heard-of-it]\n    steps:\n      - run: true\n"))
	if err != nil {
		t.Fatalf("an unknown optional capability failed the parse: %v", err)
	}
	if got := w.Jobs[0].OptionalCapabilities; !reflect.DeepEqual(got, []string{"never-heard-of-it"}) {
		t.Errorf("optional capabilities parsed as %v", got)
	}
	if got := w.Jobs[0].RequiredCapabilities(); len(got) != 0 {
		t.Errorf("an optional capability parsed as a requirement: %v", got)
	}

	job, given := runOptional(t,
		"on: push\njobs:\n  a:\n    capabilities: [package-publish]\n    optional-capabilities: [never-heard-of-it]\n    steps:\n      - run: true\n",
		map[string][]string{"acme/widgets": {"package-publish"}})
	if job.Conclusion != ConclusionSuccess || job.Error != "" {
		t.Fatalf("the job concluded %q (%q), want an unknown optional name ignored", job.Conclusion, job.Error)
	}
	if !reflect.DeepEqual(given, []string{"package-publish"}) {
		t.Errorf("the job was given %v, want only its requirement", given)
	}
}

// What the parser refuses: a name that is both required and optional,
// repository access (which is met by declaring it, so cannot be
// optional), and an empty name, as for `capabilities:`.
func TestOptionalCapabilitiesTheParserRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		yaml string
		want string
	}{
		"in both lists": {
			yaml: "on: push\njobs:\n  a:\n    capabilities: [image-signing]\n    optional-capabilities: [image-signing]\n    steps:\n      - run: true\n",
			want: `"image-signing" is also under capabilities`,
		},
		"repository access": {
			yaml: "on: push\njobs:\n  a:\n    optional-capabilities: [\"repo-write:acme/widgets\"]\n    steps:\n      - run: true\n",
			want: "name it under capabilities",
		},
		"empty": {
			yaml: "on: push\njobs:\n  a:\n    optional-capabilities: ['']\n    steps:\n      - run: true\n",
			want: "an empty name",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(".grain/workflows/test.yml", []byte(tc.yaml))
			if err == nil {
				t.Fatalf("parsed")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to say %q", err, tc.want)
			}
		})
	}
}
