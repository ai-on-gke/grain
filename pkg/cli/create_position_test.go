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

import "testing"

// grain/task-202: `grain create -position` is the CLI's half of the
// choice the UI's new-task form makes with its picker -- which end of
// the backlog the task joins. Unset has to stay nil rather than becoming
// false: nil files it wherever the last task added went and leaves that
// memory alone, while false is a real choice that both files it at the
// end and stores that as the default (ui.CreateTaskRequest.AtFront).
func TestBacklogPosition(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		flag    string
		want    *bool
		wantErr bool
	}{
		{name: "unset states no opinion", flag: "", want: nil},
		{name: "front", flag: "front", want: boolPtr(true)},
		{name: "end", flag: "end", want: boolPtr(false)},
		{name: "anything else is rejected", flag: "top", wantErr: true},
		// Neither an end nor a spelling of "no opinion" -- the empty
		// string above is the only way to say that.
		{name: "a near miss is rejected too", flag: "Front", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := backlogPosition(tc.flag)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("backlogPosition(%q) = %v, want an error naming the two ends", tc.flag, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("backlogPosition(%q): %v", tc.flag, err)
			}
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("backlogPosition(%q) = %v, want nil -- no opinion", tc.flag, *got)
			case tc.want != nil && got == nil:
				t.Fatalf("backlogPosition(%q) = nil, want %v", tc.flag, *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("backlogPosition(%q) = %v, want %v", tc.flag, *got, *tc.want)
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }
