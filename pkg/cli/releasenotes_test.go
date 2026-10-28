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
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/releasenotes"
)

// The JSON an upgrade reads out of a target image: every version newer
// than the one running, newest first, each with its whole notes.
func TestReleaseNotesCmdJSONFromAVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := runReleaseNotes([]string{"--from", "v1.6.0", "--json"}, &out, &errOut); err != nil {
		t.Fatalf("release-notes: %v\n%s", err, errOut.String())
	}
	var report releasenotes.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if len(report.Notes) < 2 {
		t.Fatalf("notes = %d, want at least v1.8.0 and v1.7.0", len(report.Notes))
	}
	for _, n := range report.Notes {
		if releasenotes.Compare(n.Version.Version, "v1.6.0") <= 0 {
			t.Errorf("--from v1.6.0 printed %s", n.Version.Version)
		}
		if !strings.HasPrefix(n.Markdown, "# ") {
			t.Errorf("%s: markdown not carried: %q", n.Version.Version, n.Markdown)
		}
	}
	var found bool
	for _, n := range report.Notes {
		found = found || n.Version.Version == "v1.8.0"
	}
	if !found {
		t.Error("--from v1.6.0 left out v1.8.0")
	}
}

func TestReleaseNotesCmdTextRange(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := runReleaseNotes([]string{"--from", "v1.7.0", "--to", "v1.8.0"}, &out, &errOut); err != nil {
		t.Fatalf("release-notes: %v\n%s", err, errOut.String())
	}
	if !strings.HasPrefix(out.String(), "# v1.8.0\n") {
		t.Errorf("text output does not start with the v1.8.0 notes:\n%.200s", out.String())
	}
	if strings.Contains(out.String(), "# v1.7.0\n") {
		t.Error("--from v1.7.0 printed v1.7.0's own notes")
	}
}

// An empty range is still an answer -- an empty list, not an error -- so
// an upgrade can tell "nothing newer in this build" from "no subcommand".
func TestReleaseNotesCmdEmptyRangeIsAnEmptyList(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := runReleaseNotes([]string{"--from", "v999.0.0", "--json"}, &out, &errOut); err != nil {
		t.Fatalf("release-notes: %v", err)
	}
	if !strings.Contains(out.String(), `"notes": []`) {
		t.Errorf("empty range printed %s", out.String())
	}
}

func TestReleaseNotesCmdRefusesStrayArguments(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := runReleaseNotes([]string{"v1.8.0"}, &out, &errOut); err == nil {
		t.Error("a stray argument was accepted")
	}
}
