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

//go:build race

package orchestrator_test

// raceEnabled reports a -race build. Under the race detector, beside the
// rest of the package's parallel tests and nightly go-long's -shuffle=on
// -count=2, a session turn's wall-clock latency says more about the runner
// than about the code: an in-process turn measured 293ms against a 250ms
// bound, and a warm dispatch 1.73s against a cold one's 1.2-1.5s. The
// latency tests still log what they measure there, but assert only the
// structural signals behind the latency -- the same sandbox reused, how many
// times it was acquired, how many framework.Run calls one run served --
// and leave their wall-clock bounds to non-race builds.
const raceEnabled = true
