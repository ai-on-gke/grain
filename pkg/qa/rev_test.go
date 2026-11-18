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

package qa

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const ExpectedQAEngineDigest = "e65d8952cdc2636ed7fd545078e17aa6a9ffba7ba108ffd61bd3b984c2aaba3d"

func TestQAEngineRevDigest(t *testing.T) {
	got, err := ComputeQAEngineDigest(".")
	if err != nil {
		t.Fatalf("ComputeQAEngineDigest: %v", err)
	}
	if got != ExpectedQAEngineDigest {
		t.Errorf("QA engine behavior revision changed!\ngot:  %s\nwant: %s\n\nIf you intentionally modified pkg/qa/** non-test files, you MUST bump QAEngineRev in pkg/qa/rev.go and update ExpectedQAEngineDigest.", got, ExpectedQAEngineDigest)
	}
}

func TestQAFlowsHaveNoInternalLinks(t *testing.T) {
	badPatterns := []*regexp.Regexp{
		regexp.MustCompile(`\b[g]o/[a-z0-9-]+`),
		regexp.MustCompile(`https?://[^\s"']*\.internal\b`),
	}

	importRe := regexp.MustCompile(`"g` + `o/[a-z0-9/]+"\n`)

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".go") {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			str := string(content)

			// Remove known safe imports
			str = importRe.ReplaceAllString(str, "\n")

			for _, pat := range badPatterns {
				if pat.MatchString(str) {
					t.Errorf("File %s contains forbidden internal pattern %s", path, pat.String())
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir: %v", err)
	}

	for _, bundle := range BuiltInFlowVersions() {
		str := bundle.Summary + " " + bundle.Label + " " + bundle.SourceYAML + " " + bundle.Resolved.ActiveCodingGuidance(nil)
		for _, pat := range badPatterns {
			if pat.MatchString(str) {
				t.Errorf("Flow bundle %s contains forbidden internal pattern %s", bundle.Key, pat.String())
			}
		}
	}

	for _, packID := range []string{"go", "ts-angular", "python", "cpp"} {
		txt := BuiltInReadabilityPackText(packID)
		if strings.TrimSpace(txt) == "" {
			t.Errorf("BuiltInReadabilityPackText(%q) returned empty text", packID)
		}
		for _, pat := range badPatterns {
			if pat.MatchString(txt) {
				t.Errorf("BuiltInReadabilityPackText(%q) contains forbidden internal pattern %s", packID, pat.String())
			}
		}
	}
}
