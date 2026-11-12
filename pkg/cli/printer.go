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

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// unknownCommandError is a command dispatchCLI does not have; Main prints
// the usage for it (the sandbox CLI's, inside a sandbox).
type unknownCommandError struct{ cmd string }

func (e unknownCommandError) Error() string { return fmt.Sprintf("unknown command %q", e.cmd) }

// taskID reads the task id positional argument. It is not parsed as a
// number: ids are decimal today because that is what Store.NewTaskID
// allocates, but Task.ID is an opaque string and nothing here is entitled
// to assume otherwise.
func taskID(s string) (string, error) {
	if s == "" {
		return "", usageErrorf("a task id is required")
	}
	return s, nil
}

// stringList is a repeatable flag.Value -- one -capability per capability
// ID, rather than a single comma-joined flag a capability ID containing
// a comma could never round-trip through.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// printer renders CLI output either as a human-readable table (the
// default) or as JSON (-json) -- the same shape a script or another
// program (the UI included, per this command's own doc comment) would
// want instead of text meant for a terminal.
type printer struct{ json bool }

// note is a line about the listing rather than a line of it -- what was
// left out of a task list and how to ask for it (list.go's hiddenNote).
// It goes to stderr so that a listing stays a listing: `grain list |
// wc -l` counts tasks, and -json emits JSON and nothing else.
func (p *printer) note(text string) {
	if p.json || text == "" {
		return
	}
	fmt.Fprintln(os.Stderr, text)
}

func (p *printer) encode(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
