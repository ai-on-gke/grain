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

// Package grit is grain's CI engine as a service of its own
// (docs/design/grit-split.md). It knows trees, workflows, jobs, results
// and attestations, and nothing about tasks, pull requests or the merge
// queue.
//
// The service itself arrives over GS-2. What lives here now is the
// import lint (independence_test.go) that keeps grit's packages off
// grain-only ones from the start; the one-job Actions runner that used
// to be this package is pkg/grit/actions.
package grit
