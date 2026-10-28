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
	"fmt"
	"io"
	"io/fs"
)

// Runner is grain CI as everything outside this package asks it: the
// whole of Service's exported method set, and nothing else
// (docs/design/grit-split.md#GS-1).
//
// Callers take a Runner rather than a *Service so that the service can
// move out of the process -- grit.Local in process, grit.Client over its
// /v1 API -- without any caller changing. The only code that names
// *Service is what builds one: pkg/daemon's wiring (BuildCIService) and
// pkg/demo's observability scene, which runs a real engine against a
// fake sandbox backend. TestOnlyBuildersNameTheCIService holds the rest
// of the tree to that.
//
// The answers are Service's own; its doc comments are the contract.
type Runner interface {
	// Start plans and begins a run. A request whose plan matches a run
	// already in flight is answered with that run.
	Start(ctx context.Context, req Request) (Run, error)
	// WorkflowOptions is what a person may ask of a branch's workflows:
	// the jobs and inputs Start would accept.
	WorkflowOptions(ctx context.Context, req Request) ([]WorkflowOption, error)
	// Get returns one run by id, and whether it is still remembered.
	Get(id string) (Run, bool)
	// List returns the runs still held, newest first.
	List() []Run
	// Find returns the runs matching f, asked of everything the store
	// keeps rather than of the page List answers with.
	Find(f RunFilter, limit int) RunPage
	// Cancel ends a run that is still going, reporting whether there was
	// one to end.
	Cancel(id, reason string, by CancelSource) bool
	// MarkFlake marks jobs of run id that passed an uncached recheck
	// (rechecks) as flakes, reporting whether the run was still held.
	MarkFlake(id string, jobs []string, rechecks string) bool
	// CacheStats and ClearCache are the job cache. ok is false where
	// there is no cache to ask.
	CacheStats(ctx context.Context) (CacheStats, bool, error)
	ClearCache(ctx context.Context) (int, bool, error)
	// EvictBaseTransitionFailures drops cached failures recorded against
	// a base commit other than baseCommit, once a base branch has gone
	// from red to green.
	EvictBaseTransitionFailures(ctx context.Context, baseCommit string) (int, bool, error)
	// ClearArtifacts and ArtifactStats are the artifact store as a whole.
	ClearArtifacts(ctx context.Context) (int, int64, bool, error)
	ArtifactStats(ctx context.Context) (ArtifactStats, bool, error)
	// RunArtifacts, OpenArtifact and WithArtifacts are one run's
	// artifacts: the list, one artifact's bytes (the caller closes the
	// reader), and the run with its jobs' artifacts filled in.
	RunArtifacts(ctx context.Context, runID string) ([]Artifact, bool, error)
	OpenArtifact(ctx context.Context, ref ArtifactRef) (io.ReadCloser, Artifact, error)
	WithArtifacts(ctx context.Context, run Run) Run
}

var _ Runner = (*Service)(nil)

// NopRunner is a Runner that holds nothing: no runs, no job cache, no
// artifacts, and a Start that refuses. Every answer is the one Service
// gives where it has nothing behind it -- ok false, nothing found -- so a
// fake that embeds it and overrides the methods it cares about reads, to
// its callers, as a deployment that keeps none of the rest.
type NopRunner struct{}

var _ Runner = NopRunner{}

// errNoRunner is what NopRunner's Start says.
var errNoRunner = errors.New("this CI runner starts no runs")

func (NopRunner) Start(context.Context, Request) (Run, error) { return Run{}, errNoRunner }

func (NopRunner) WorkflowOptions(context.Context, Request) ([]WorkflowOption, error) {
	return nil, nil
}

func (NopRunner) Get(string) (Run, bool)                   { return Run{}, false }
func (NopRunner) List() []Run                              { return nil }
func (NopRunner) Find(RunFilter, int) RunPage              { return RunPage{} }
func (NopRunner) Cancel(string, string, CancelSource) bool { return false }
func (NopRunner) MarkFlake(string, []string, string) bool  { return false }

func (NopRunner) CacheStats(context.Context) (CacheStats, bool, error) {
	return CacheStats{}, false, nil
}

func (NopRunner) ClearCache(context.Context) (int, bool, error) { return 0, false, nil }

func (NopRunner) EvictBaseTransitionFailures(context.Context, string) (int, bool, error) {
	return 0, false, nil
}

func (NopRunner) ClearArtifacts(context.Context) (int, int64, bool, error) {
	return 0, 0, false, nil
}

func (NopRunner) ArtifactStats(context.Context) (ArtifactStats, bool, error) {
	return ArtifactStats{}, false, nil
}

func (NopRunner) RunArtifacts(context.Context, string) ([]Artifact, bool, error) {
	return nil, false, nil
}

// OpenArtifact reports not-found, which is what an artifact a store does
// not keep is.
func (NopRunner) OpenArtifact(_ context.Context, ref ArtifactRef) (io.ReadCloser, Artifact, error) {
	return nil, Artifact{}, fmt.Errorf("this deployment keeps no CI artifacts (%s/%s/%s): %w", ref.Run, ref.Job, ref.Name, fs.ErrNotExist)
}

func (NopRunner) WithArtifacts(_ context.Context, run Run) Run { return run }
