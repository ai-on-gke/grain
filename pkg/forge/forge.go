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

// Package forge is what is true about a branch, and whether it may land.
//
// It is one of the three pieces docs/three-pieces.md names, and the rule
// that document states is the whole of this package's contract: tasks
// point at the forge, the forge points at sandboxes, and nothing points
// up. So there is no model.Task here, no model.Observation, no
// model.Store and no import of pkg/model at all -- forge_deps_test.go
// fails the build if one appears.
//
// That is not tidiness. What lived here was the read half of
// orchestrator's SyncPullRequests, and it was tangled with the write
// half in one function: syncEntry read a pull request's health and, in
// the same two hundred lines, wrote task comments, filed repair tasks
// and escalated to a person. Untangling them is what lets this half be
// tested without a store, replaced for a forge that is not GitHub, and
// -- eventually -- run somewhere else entirely
// (docs/leaving-the-github-api.md).
//
// # What the forge answers, and what it does not
//
// Three questions about a branch, and no more:
//
//	Observe   what CI and the base say about it, right now
//	Mergeable clean, conflicted, behind          (still in orchestrator)
//	Merge     land it, or say why not           (still in orchestrator)
//
// Only the first has moved so far. The queue's *other* half -- which
// branch is next, and what to do when one is red -- is deliberately not
// here and never will be: queueOrder is keyed by task ID,
// requeueForRepair files a task, escalateToUser writes a comment. A
// forge that filed repair tasks would be pointing up, which is the one
// thing the direction forbids.
//
// # Its own vocabulary
//
// Health and Ref are this package's own rather than model.PrHealth and
// model.PullRequestRef, and the values are deliberately identical
// strings so that the conversion at the boundary is a string conversion
// and not a switch somebody has to keep in step. That duplication is
// the price of the direction rule, and it is a small and fixed price:
// two types that have not changed shape in the life of this repository.
//
// # Grain CI is an input, not a read
//
// GrainCI is what grain's own CI (pkg/ci) made of the same commit. It
// arrives as an argument rather than being read here, because reading it
// means reading a task's own record (Observation.CI, and the grain-ci
// capability on the task's grants) -- which is exactly the upward
// pointer this package exists not to have. orchestrator's
// grainCIVerdictOf does that read and hands the answer down.
package forge

import "fmt"

// Ref names one pull request on one repository: this package's own
// spelling of model.PullRequestRef, for the reason the package comment
// gives.
//
// It is a comparable struct because the sightings maps below use it as a
// key, which is the same thing model.PullRequestRef was relied on for.
type Ref struct {
	Owner  string
	Repo   string
	Number int
}

// String is "owner/name#123" -- model.PullRequestRef.String's own
// rendering, kept identical because it is what every log line and error
// message in this package already said and what a person reading a
// journal already recognises.
func (r Ref) String() string { return fmt.Sprintf("%s/%s#%d", r.Owner, r.Repo, r.Number) }

// Health is how a pull request stands: this package's own spelling of
// model.PrHealth, with the same seven values as the same strings, so the
// conversion at the boundary is `model.PrHealth(h)` and nothing has to
// be kept in step by hand.
//
// docs/data-model.md's PrHealth enum is the reference for what each one
// means; HealthFrom is where they are decided.
type Health string

const (
	Unknown    Health = "unknown"
	Clean      Health = "clean"
	Pending    Health = "pending"
	Conflicted Health = "conflicted"
	Failing    Health = "failing"
	Merged     Health = "merged"
	Closed     Health = "closed"
)
