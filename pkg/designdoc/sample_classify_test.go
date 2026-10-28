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

package designdoc_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/designdoc"
)

// docs/test-plans/2026-10-05.md §1 step 5: this repository's own sample
// design doc, read from a scratch repository's trailers. A commit naming
// D-2 builds it (done); an edit to D-2's section makes it changed; taking
// D-2 out of the doc makes it removed.
func TestSampleDesignDocClassifiesFromTrailers(t *testing.T) {
	const sample = "docs/design/sample/README.md"
	doc, err := os.ReadFile(filepath.Join("..", "..", sample))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := designdoc.Section(doc, "D-2"); !ok {
		t.Fatalf("%s has no D-2 section", sample)
	}
	f := newFixture(t)
	write := func(content []byte, message string) string {
		t.Helper()
		p := filepath.Join(f.dir, sample)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatal(err)
		}
		f.git("add", "-A")
		f.git("commit", "-q", "-m", message)
		return f.git("rev-parse", "HEAD")
	}
	classOf := func(id string) designdoc.Class {
		t.Helper()
		at := func(rev string) ([]byte, error) {
			cmd := exec.Command("git", "show", rev+":"+sample)
			cmd.Dir = f.dir
			return cmd.Output()
		}
		current, err := at("main")
		if err != nil {
			t.Fatal(err)
		}
		items, err := designdoc.Classify(current, designdoc.History(sample, f.log()), nil, at)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			if it.ID == id {
				return it.Class
			}
		}
		return ""
	}

	rev := write(doc, "Merge the sample design doc")
	if got := classOf("D-2"); got != designdoc.ClassNew {
		t.Errorf("D-2 before any implementing commit = %q, want %q", got, designdoc.ClassNew)
	}
	f.commit("", trailers("Design-Item: "+sample+"#D-2", "Design-Rev: "+rev, "Origin: bwsalmon/grain#3000", "Filed-by: task 1992"))
	if got := classOf("D-2"); got != designdoc.ClassDone {
		t.Errorf("D-2 after its implementing commit = %q, want %q", got, designdoc.ClassDone)
	}

	edited := strings.Replace(string(doc), "A heading with no ID is not an error.", "A heading with no ID is a warning.", 1)
	if edited == string(doc) {
		t.Fatalf("the edit did not apply to %s", sample)
	}
	write([]byte(edited), "Reword D-2")
	if got := classOf("D-2"); got != designdoc.ClassChangedDone {
		t.Errorf("D-2 after its section was edited = %q, want %q", got, designdoc.ClassChangedDone)
	}

	i := strings.Index(edited, "### Check requirement IDs")
	j := strings.Index(edited[i+1:], "\n#")
	removed := edited[:i]
	if j >= 0 {
		removed += edited[i+1+j+1:]
	}
	write([]byte(removed), "Take D-2 out")
	if got := classOf("D-2"); got != designdoc.ClassRemovedDone {
		t.Errorf("D-2 after it left the doc = %q, want %q", got, designdoc.ClassRemovedDone)
	}
}
