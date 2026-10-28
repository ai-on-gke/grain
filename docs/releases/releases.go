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

// Package releases carries this directory's per-version release notes
// into the grain binary, so a deployment can show what each version
// brought without reaching bwsalmon/grain (pkg/releasenotes reads them).
//
// It lives here rather than beside its reader because go:embed cannot
// reach out of its own package directory, and the notes have to stay
// where the release-notes task commits them (README.md).
package releases

import "embed"

// Files is every v*.md in this directory -- one per release -- and
// nothing else: README.md describes the directory and is no release.
//
//go:embed v*.md
var Files embed.FS
