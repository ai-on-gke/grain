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
)

// noCache is what an Engine with no Cache uses, so the engine has one
// code path rather than a nil check at each of the two call sites.
type noCache struct{}

func (noCache) Lookup(context.Context, Key) (JobRun, bool, error) { return JobRun{}, false, nil }
func (noCache) Save(context.Context, Key, JobRun) error           { return nil }
