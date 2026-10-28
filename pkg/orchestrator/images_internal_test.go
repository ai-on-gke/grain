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

package orchestrator

import (
	"strings"
	"testing"
)

// The images sit under the description and above the lineage section,
// whichever of the two was written first, and a newer note's images
// replace an older one's rather than stacking under them.
func TestWithImagesSitsAboveTheLineageAndReplacesItself(t *testing.T) {
	footer := descriptionFooter("t1")
	lineage := lineageStart + "\nFiled by task t0.\n" + lineageEnd
	first := imagesStart(1) + "\nold\n" + imagesEnd
	second := imagesStart(2) + "\nnew\n" + imagesEnd

	images := withImages(withLineage("Change\n\n---\n"+footer, lineage), first)
	lineageFirst := withLineage(withImages("Change\n\n---\n"+footer, first), lineage)
	for _, body := range []string{images, lineageFirst} {
		if i, j := strings.Index(body, first), strings.Index(body, lineageStart); i < 0 || j < 0 || i > j {
			t.Fatalf("body = %q, want the images above the lineage", body)
		}
		if !grainAuthored(body, "t1") {
			t.Fatalf("body = %q, want grain's footer still last", body)
		}
	}

	replaced := withImages(images, second)
	if strings.Contains(replaced, "old") || strings.Count(replaced, imagesEnd) != 1 || !strings.Contains(replaced, "new") {
		t.Fatalf("body = %q, want the newer images in place of the older", replaced)
	}
	if got := imagesSection(replaced); got != second {
		t.Fatalf("imagesSection = %q, want %q", got, second)
	}
	if got := withImages(footer, first); !grainAuthored(got, "t1") || !strings.HasPrefix(got, first) {
		t.Fatalf("on a bare footer = %q", got)
	}
}

func TestUniqueImagePathKeepsTwoShotsApart(t *testing.T) {
	taken := map[string]bool{}
	got := []string{
		uniqueImagePath("contact-sheet.png", taken),
		uniqueImagePath("contact-sheet.png", taken),
		uniqueImagePath("before shot (1).png", taken),
		uniqueImagePath("../..", taken),
	}
	want := []string{"contact-sheet.png", "2-contact-sheet.png", "before-shot-1-.png", "image"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("paths = %q, want %q", got, want)
		}
	}
}
