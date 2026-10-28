package ci

import "testing"

func TestSupersedingRunReadsOnlyGrainsOwnSupersede(t *testing.T) {
	t.Parallel()
	cancelled := func(id, reason string, by CancelSource) Run {
		return Run{ID: id, Conclusion: ConclusionCancelled, CancelReason: reason, CancelledBy: by}
	}
	for _, tc := range []struct {
		name string
		run  Run
		want string
	}{
		{"same task", cancelled("r1", SupersededByRunReason("r2"), CancelBySystem), "r2"},
		{"operator", cancelled("r1", SupersededByRunReason("r2"), CancelByOperator), ""},
		{"branch moved", cancelled("r1", "superseded: grain/task-1 moved to abc while this run was judging def", CancelBySystem), ""},
		{"no id", cancelled("r1", SupersededByRunReason(""), CancelBySystem), ""},
		{"itself", cancelled("r1", SupersededByRunReason("r1"), CancelBySystem), ""},
		{"not cancelled", Run{ID: "r1", Conclusion: ConclusionFailure, CancelReason: SupersededByRunReason("r2")}, ""},
		{"a person's note", cancelled("r1", "superseded by run r2, started for the same task, I think", CancelBySystem), ""},
	} {
		got, ok := SupersedingRun(tc.run)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("%s: SupersedingRun = %q, %v, want %q", tc.name, got, ok, tc.want)
		}
	}
}
