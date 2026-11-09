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

// Package playbooks embeds grain's bootstrap playbooks -- markdown runbooks
// served both by the built-in config-map capability server ("bootstrap") and
// by pkg/namespace as src://playbooks. It imports nothing of grain's so any
// package can read them without cycles.
package playbooks

import (
	"embed"
	"io/fs"
)

//go:embed *.md
var embedded embed.FS

// FS is the embedded playbooks, read-only: each one a <name>.md file
// at the root.
var FS fs.FS = embedded
