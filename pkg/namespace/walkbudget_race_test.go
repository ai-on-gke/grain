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

package namespace

import "time"

// walkBudget is how long one memory:// walk over a couple of thousand
// documents may take in TestMemorySingleDocumentAndLargeWalkAvoidQuadratic
// LayerFetches under the race detector, which slows this code by the 2-20x
// its documentation warns of and runs beside every other parallel test in
// CI's go unit job: 1s there read 1.06s on a loaded runner (PR #758). The
// quadratic behaviour the test guards against is thousands of layer
// fetches -- minutes, not seconds -- and the fetch counts it asserts pin
// that exactly, so this bound only has to catch a walk gone badly wrong.
const walkBudget = 5 * time.Second
