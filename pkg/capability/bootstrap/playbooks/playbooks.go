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

// Package playbooks embeds the bootstrap-playbooks capability's
// markdown runbooks. It is its own package, importing nothing of
// grain's, so that both pkg/capability/bootstrap and pkg/namespace
// (which serves them as src://playbooks) can read them: bootstrap
// already depends on namespace through pkg/mcp, so namespace cannot
// import bootstrap itself.
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
