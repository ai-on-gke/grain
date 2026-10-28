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
