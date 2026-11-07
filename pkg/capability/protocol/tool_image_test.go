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

package protocol

import (
	"context"
	"testing"
)

// Outside a tools/call there is nobody to hand an image to, and the
// handler is told so, to carry the bytes in its JSON instead.
func TestToolImageNeedsAToolsCall(t *testing.T) {
	if ToolImage(context.Background(), "image/png", []byte{1}) {
		t.Fatal("ToolImage accepted an image with no tools/call to attach it to")
	}
	sink := &imageSink{}
	ctx := context.WithValue(context.Background(), imageSinkKey{}, sink)
	if !ToolImage(ctx, "image/png", []byte{1, 2}) || !ToolImage(ctx, "image/jpeg", []byte{3}) {
		t.Fatal("ToolImage refused an image inside a tools/call")
	}
	got := sink.take()
	if len(got) != 2 || got[0].MIMEType != "image/png" || string(got[1].Data) != "\x03" {
		t.Fatalf("collected %+v", got)
	}
}
