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

import "testing"

func TestCommentPathIsTheTasksPathWithTheCommentAsItsFragment(t *testing.T) {
	if got, want := TaskPath("31"), "/tasks/31"; got != want {
		t.Errorf("TaskPath = %q, want %q", got, want)
	}
	if got, want := CommentPath("31", 13), "/tasks/31#comment=13"; got != want {
		t.Errorf("CommentPath = %q, want %q", got, want)
	}
}
