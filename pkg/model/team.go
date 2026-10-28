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

package model

// DefaultTeamID is the identifier of the built-in default team: the one
// team the API's team shape and an old client's `team` may still name.
// The team table went at schema 24, its settings onto grain_config, and
// the team columns other rows carried at 25 (docs/single-tenant-reset.md,
// 6-ii-b5).
const DefaultTeamID = "default"
