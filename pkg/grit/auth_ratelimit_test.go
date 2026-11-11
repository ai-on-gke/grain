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

package grit

import (
	"testing"
	"time"
)

// A burst straddling a wall-clock minute boundary must not get twice the
// limit through: the window slides with the requests.
func TestClientRateLimiterSlidesAcrossMinuteBoundary(t *testing.T) {
	l := newClientRateLimiter()
	c := ClientRecord{ID: "c", RunsPerMinute: 2, TreeUploadBytesPerMinute: 100}
	base := time.Date(2026, 11, 11, 8, 0, 59, 500_000_000, time.UTC)

	if !l.allowRun(c, base) || !l.allowRun(c, base.Add(100*time.Millisecond)) {
		t.Fatal("runs within the limit refused")
	}
	if l.allowRun(c, base.Add(time.Second)) {
		t.Fatal("3rd run just past the minute boundary allowed")
	}
	if !l.allowRun(c, base.Add(time.Minute+time.Millisecond)) {
		t.Fatal("run a minute after the first refused")
	}

	if !l.allowTreeUpload(c, 80, base) {
		t.Fatal("upload within the limit refused")
	}
	if l.allowTreeUpload(c, 30, base.Add(time.Second)) {
		t.Fatal("upload over the limit just past the minute boundary allowed")
	}
	if !l.allowTreeUpload(c, 30, base.Add(time.Minute+time.Millisecond)) {
		t.Fatal("upload a minute after the first refused")
	}
}
