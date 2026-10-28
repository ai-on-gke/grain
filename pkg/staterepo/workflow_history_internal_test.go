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

package staterepo

// The list itself, from inside the package: workflow_history_test.go
// checks that one particular historical rendering is handled correctly,
// and this checks that every entry in the list is an entry that can be.

import (
	"strings"
	"testing"
)

// Each template has to render into something grain recognises as its
// own, and into something exactly one template matches. Two templates
// matching the same file would mean one is the other with text only on
// the far side of the image, so grain could not say which build wrote a
// file -- and, worse, could read an image out of a file whose text
// around it is not the text it thinks it is.
func TestEveryRecordedTemplateIsRecognisedAndUnambiguous(t *testing.T) {
	t.Parallel()
	const image = "ghcr.io/bwsalmon/grain/grain:sha-abc1234"
	if len(grainWorkflows) != len(earlierWorkflows)+1 {
		t.Fatalf("the template list is not this build's plus the retired ones: %d, %d",
			len(grainWorkflows), len(earlierWorkflows))
	}
	for i, template := range grainWorkflows {
		if !strings.Contains(template, workflowImagePlaceholder) {
			t.Errorf("template %d has no image to substitute", i)
			continue
		}
		body := strings.ReplaceAll(template, workflowImagePlaceholder, image)
		got, ok := RenderedImage([]byte(body))
		if !ok || got != image {
			t.Errorf("template %d renders a file grain does not recognise: %q, %v", i, got, ok)
		}
		var matched []int
		for j, other := range grainWorkflows {
			if _, ok := renderedFrom(body, other); ok {
				matched = append(matched, j)
			}
		}
		if len(matched) != 1 || matched[0] != i {
			t.Errorf("template %d renders a file that matches templates %v", i, matched)
		}
	}
	// Only the first entry is this build's. An earlier rendering that
	// answered true here would be repointed in place rather than brought
	// up to the current wording, and would keep saying whatever the grain
	// that wrote it said about what grain does to this file.
	for i, template := range grainWorkflows {
		body := strings.ReplaceAll(template, workflowImagePlaceholder, image)
		if got := RenderedByThisBuild([]byte(body)); got != (i == 0) {
			t.Errorf("template %d: RenderedByThisBuild = %v", i, got)
		}
	}
}
