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

package checks

import (
	"context"
	"testing"
)

func TestCheckTestHygiene(t *testing.T) {
	ws := &InMemoryWorkspace{
		HeadFiles: map[string]string{
			"foo_test.go": `package foo

import (
	"testing"
	"time"
)

func TestFoo(t *testing.T) {
	time.Sleep(1 * time.Second) // qa:allow-sleep
	time.Sleep(2 * time.Second)

	for {
		time.Sleep(1 * time.Second)
	}

	for true {
		break
	}
}
`,
		},
	}

	pd := ParsedDiff{
		Files: []DiffFile{
			{
				NewPath: "foo_test.go",
				AddedLines: []NumberedLine{
					{Line: 9, Text: "time.Sleep(1 * time.Second) // qa:allow-sleep"},
					{Line: 10, Text: "time.Sleep(2 * time.Second)"},
					{Line: 12, Text: "for {"},
					{Line: 16, Text: "for true {"},
				},
			},
		},
	}

	findings, err := CheckTestHygiene(context.Background(), ws, pd)
	if err != nil {
		t.Fatalf("CheckTestHygiene: %v", err)
	}

	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(findings))
	}

	// Expect time.Sleep without qa:allow-sleep at line 10
	if findings[0].Line != 10 {
		t.Errorf("expected finding on line 10, got %d", findings[0].Line)
	}

	// Expect for {} loop without break at line 12
	if findings[1].Line != 12 {
		t.Errorf("expected finding on line 12, got %d", findings[1].Line)
	}
}
