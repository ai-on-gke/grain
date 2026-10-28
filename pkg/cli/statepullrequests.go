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

package cli

// The controller-owned config repository participates in the same task and
// merge-queue lifecycle as an external repository, but none of its repository
// operations belong on GitHub. stateRepoPullRequestClient overlays the subset
// of github.Client used by that lifecycle with local git operations and falls
// through to the embedded client for every ordinary repository.
//
// Keeping the adapter at this boundary lets the mature pull-request state
// machine remain shared: a config change still has a fixes link, can await
// Submit, observes conflicts, and auto-merges in queue order. Its synthetic PR
// number is the task's numeric ID; all durable state already lives on that task
// and its branch, so there is no second PR table to reconcile.
