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

package model

import (
	"strconv"
	"strings"
	"testing"
)

func TestMeasurePromptEstimatesAndPlacesAPrompt(t *testing.T) {
	t.Parallel()
	small := MeasurePrompt(strings.Repeat("a", 4000))
	if small.Bytes != 4000 || small.EstimatedTokens != 1000 {
		t.Errorf("measured %d bytes as ~%d tokens, want 4000/1000", small.Bytes, small.EstimatedTokens)
	}
	if small.ContextTokens != PromptContextTokens {
		t.Errorf("measurement records window %d, want %d", small.ContextTokens, PromptContextTokens)
	}
	if small.Large() {
		t.Error("a 4 KB prompt reads as large")
	}
	if small.Zero() {
		t.Error("a measured prompt reads as unmeasured")
	}
}

// The threshold is a quarter of whatever window the measurement was
// taken against, which is the one thing a later reader of a stored size
// has to be able to trust.
func TestPromptSizeIsLargeAtAQuarterOfItsOwnWindow(t *testing.T) {
	t.Parallel()
	just := MeasurePromptBytes(PromptWarnTokens * PromptBytesPerToken)
	if !just.Large() {
		t.Errorf("%s does not read as large, at exactly the warn threshold", just)
	}
	under := MeasurePromptBytes(PromptWarnTokens*PromptBytesPerToken - PromptBytesPerToken)
	if under.Large() {
		t.Errorf("%s reads as large, just under the warn threshold", under)
	}
	narrower := PromptSize{Bytes: 4000, EstimatedTokens: 1000, ContextTokens: 4000}
	if !narrower.Large() {
		t.Error("a quarter of a 4,000-token window does not read as large")
	}
}

// Nothing measured is not a prompt of nought bytes, and must not be
// reported as one.
func TestUnmeasuredPromptSaysSo(t *testing.T) {
	t.Parallel()
	var none PromptSize
	if !none.Zero() || none.Large() || none.Share() != 0 {
		t.Errorf("an unmeasured prompt reads as %+v", none)
	}
	if got := none.String(); !strings.Contains(got, "no prompt") {
		t.Errorf("String() of an unmeasured prompt is %q", got)
	}
}

// A share is never quoted without the window it is a share of: the
// window is a stated assumption rather than a fact grain holds, and a
// bare percentage is one a reader cannot check.
func TestPromptSizeStringQuotesTheWindowItJudgedAgainst(t *testing.T) {
	t.Parallel()
	got := MeasurePromptBytes(PromptContextTokens * PromptBytesPerToken / 2).String()
	if !strings.Contains(got, "50%") {
		t.Errorf("half a window does not read as 50%%: %q", got)
	}
	if !strings.Contains(got, strconv.Itoa(PromptContextTokens)) {
		t.Errorf("the share is quoted without its window: %q", got)
	}
}
