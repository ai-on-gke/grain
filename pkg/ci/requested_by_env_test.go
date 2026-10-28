package ci

import (
	"encoding/json"
	"testing"
)

// A person-only workflow (.grain/workflows/promote-release.yml) is told
// who asked only by grain's own callers: the variables appear when
// Request.RequestedByKind is set, never otherwise, and no JSON caller can
// set it -- or every other job's key would move with it.
func TestRequestedByKindReachesStepsOnlyFromGrainsOwnCallers(t *testing.T) {
	plain := runEnv(Request{Repo: "a/b", Ref: "main", RequestedBy: "alice"}, "w", "j")
	for _, k := range []string{RequestedByKindEnv, RequestedByEnv, ReleaseVersionEnv} {
		if _, ok := plain[k]; ok {
			t.Errorf("%s is set on a run nobody vouched for: %v", k, plain)
		}
	}
	vouched := runEnv(Request{Repo: "a/b", Ref: "main", RequestedBy: "alice@example.com",
		RequestedByKind: "human", ReleaseVersion: "v2.4.0"}, "w", "j")
	if vouched[RequestedByKindEnv] != "human" || vouched[RequestedByEnv] != "alice@example.com" ||
		vouched[ReleaseVersionEnv] != "v2.4.0" {
		t.Errorf("a vouched run's env = %v, want the person and the release", vouched)
	}

	var req Request
	if err := json.Unmarshal([]byte(`{"Repo":"a/b","Ref":"main","RequestedByKind":"human","ReleaseVersion":"v2.4.0"}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.RequestedByKind != "" || req.ReleaseVersion != "" {
		t.Errorf("JSON set RequestedByKind=%q ReleaseVersion=%q, want neither settable by a caller",
			req.RequestedByKind, req.ReleaseVersion)
	}
}
