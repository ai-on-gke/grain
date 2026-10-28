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
	"errors"
	"testing"
	"testing/fstest"
)

type definitionSandboxes struct{ acquired int }

func (s *definitionSandboxes) Acquire(context.Context, SandboxRequest) (Sandbox, error) {
	s.acquired++
	return nil, errors.New("a workflow definition failure must not acquire a sandbox")
}

func TestServiceRecordsWorkflowDefinitionErrorsAsFailedRuns(t *testing.T) {
	for name, tree := range map[string]fstest.MapFS{
		"malformed YAML": {
			".grain/workflows/broken.yml": &fstest.MapFile{Data: []byte("on: [push\n")},
		},
		"unsupported matching workflow": {
			".github/workflows/broken.yml": &fstest.MapFile{Data: []byte(`
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: make
  test:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - run: make test ${{ needs.build.outputs.tag }}
`)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			sandboxes := &definitionSandboxes{}
			svc := &Service{
				Source: SourceFunc(func(context.Context, string, string) ([]Workflow, string, error) {
					return LoadDir(tree)
				}),
				Engine: &Engine{Sandboxes: sandboxes},
				NewID:  func() string { return "definition-error" },
			}
			run, err := svc.Start(context.Background(), Request{
				Repo: "o/r", Ref: "grain/task-1", Commit: "abc123", Event: EventPush,
			})
			if err != nil {
				t.Fatalf("starting: %v", err)
			}
			if run.ID == "" || run.Conclusion != ConclusionFailure || run.Error == "" {
				t.Fatalf("recorded run = %+v, want a visible workflow failure", run)
			}
			if run.Commit != "abc123" || run.FinishedAt == nil {
				t.Fatalf("failed run is not tied to its commit and completion: %+v", run)
			}
			if len(run.Jobs) != 0 || sandboxes.acquired != 0 {
				t.Fatalf("definition failure ran work: jobs=%d sandboxes=%d", len(run.Jobs), sandboxes.acquired)
			}
			if stored, ok := svc.Get(run.ID); !ok || stored.Error != run.Error {
				t.Fatalf("failed run was not persisted: %+v, %v", stored, ok)
			}
		})
	}
}

func TestServiceStillReturnsTransientSourceErrorsWithoutCreatingARun(t *testing.T) {
	svc := &Service{
		Source: SourceFunc(func(context.Context, string, string) ([]Workflow, string, error) {
			return nil, "", context.DeadlineExceeded
		}),
		Engine: &Engine{Sandboxes: &definitionSandboxes{}},
	}
	if _, err := svc.Start(context.Background(), Request{
		Repo: "o/r", Ref: "main", Event: EventPush,
	}); err == nil {
		t.Fatal("transient source failure was converted into a deterministic verdict")
	}
	if len(svc.List()) != 0 {
		t.Fatal("transient source failure left a run behind")
	}
}
