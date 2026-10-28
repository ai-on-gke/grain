package dockervm

import (
	"context"
	"testing"
)

// A container that has exited and one that was never created are both
// "not running", but a caller is told to do different things about them
// (read the logs, or create the VM again), so Inspect keeps them apart
// rather than collapsing both into a bool.
func TestInspect_ContainerStateSeparatesExitedFromMissing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		env   func(t *testing.T)
		state ContainerState
	}{
		{"exited", func(t *testing.T) { t.Setenv("FAKEDOCKER_RUNNING", "false") }, ContainerExited},
		{"never created", func(t *testing.T) { t.Setenv("FAKEDOCKER_MISSING", "granule-vm-web") }, ContainerMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, calls := testDocker(t)
			tc.env(t)

			st, err := Inspect(context.Background(), d, "web")
			if err != nil {
				t.Fatalf("Inspect error = %v, want the state reported instead", err)
			}
			if st.State != tc.state {
				t.Errorf("State = %q, want %q", st.State, tc.state)
			}
			if st.Running() || st.Ready {
				t.Errorf("status = %+v, want neither running nor ready", st)
			}
			// Nothing to probe: the container's state is the whole
			// answer, and an exec would only fail saying so again.
			for _, c := range calls() {
				if len(c) > 0 && c[0] == "exec" {
					t.Errorf("probed a container that isn't running: %v", c)
				}
			}
		})
	}
}
