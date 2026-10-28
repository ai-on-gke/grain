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

package mcp

import (
	"strings"
)

// This file carries the shell-quoting helper the remote sandbox tools
// need. It outlived mcp.SSHRunner, the transport that first brought it
// here: reaching a sandbox guest no longer means running a local `ssh`
// binary against a forwarded port (see DockerExecRunner), but
// sshRunCommandTool still has to build one shell command string for the
// guest to parse, and shellQuote is what makes that safe.

// shellQuote quotes s for a POSIX shell, single-quoting anything outside a
// small safe set of characters -- a literal single quote inside s is
// handled by closing the quote, escaping the quote character itself, then
// reopening it.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.' || r == '/' || r == ':' || r == '@' || r == '=' || r == ',':
		default:
			safe = false
		}
		if !safe {
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
