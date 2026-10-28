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
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ai-on-gke/grain/pkg/releasenotes"
	"github.com/ai-on-gke/grain/pkg/version"
)

// releaseNotesCmd is `grain release-notes`: the per-version release
// notes this binary carries (pkg/releasenotes), newest first.
//
// An upgrade asks the image it is about to run for them
// (`release-notes --from <running> --json`, pkg/upgrade), the same way
// it asks for schema-version and sandbox-image, so what the upgrade pane
// shows is what that build itself says it brings. Like those, it
// touches no store and needs no config.
func releaseNotesCmd(args []string) {
	if err := runReleaseNotes(args, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "grain release-notes: "+err.Error())
		os.Exit(2)
	}
}

func runReleaseNotes(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("grain release-notes", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "", "only notes for versions newer than this one (the version being upgraded from)")
	to := fs.String("to", "", "only notes for versions up to and including this one")
	jsonOut := fs.Bool("json", false, "print the notes as JSON, with the version of this binary")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: grain release-notes [--from vA] [--to vB] [--json]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	notes := releasenotes.Between(releasenotes.All(), *from, *to)
	if *jsonOut {
		report := releasenotes.Report{Version: version.Get().Release, Notes: notes}
		if report.Notes == nil {
			report.Notes = []releasenotes.Note{}
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	if len(notes) == 0 {
		fmt.Fprintln(stderr, "no release notes in this build for that range")
		return nil
	}
	for i, n := range notes {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		fmt.Fprint(stdout, strings.TrimRight(n.Markdown, "\n")+"\n")
	}
	return nil
}
