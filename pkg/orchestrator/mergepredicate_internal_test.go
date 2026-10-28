package orchestrator

import (
	"strings"
	"testing"
)

// The predicate on its own, arm by arm (grain/task-471, simplified in
// grain/task-1023). Every case below is one sentence of the rule, and a new
// condition appearing in mayMerge that is not one of these is the leak this
// file exists to catch.
func TestTheMergePredicateHasOneArmAndOneException(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		req  mergeRequest
		want mergeArm
		why  string // a phrase the refusal must contain, for a refusal
	}{{
		name: "green on its own head",
		req:  mergeRequest{taskID: "t1", branch: "main", clean: true, greenHead: true},
		want: mergeArmOwnHead,
	}, {
		name: "no verdict for its own head",
		req:  mergeRequest{taskID: "t1", branch: "main", clean: true},
		why:  "has not passed for this change's own head",
	}, {
		name: "green but conflicted",
		req:  mergeRequest{taskID: "t1", branch: "main", greenHead: true},
		why:  "does not merge cleanly",
	}, {
		name: "an operator's override, on a change nothing is green about",
		req: mergeRequest{taskID: "t1", branch: "main", clean: true, head: "reviewed",
			forced: &forcedMerge{reason: "the release is broken and this is the fix", by: "ada",
				headSHA: "reviewed"}},
		want: mergeArmForced,
	}, {
		// The one thing an override cannot do: a merge GitHub will not
		// perform is not something anybody can authorise.
		name: "an operator's override on a conflicted change",
		req: mergeRequest{taskID: "t1", branch: "main", head: "reviewed",
			forced: &forcedMerge{reason: "please", by: "ada", headSHA: "reviewed"}},
		why: "no override can change",
	}, {
		// bwsalmon/grain#2399: a push after the override is code nobody
		// reviewed, and the override does not reach it -- however green
		// or clean anything else is.
		name: "an operator's override after the branch has moved",
		req: mergeRequest{taskID: "t1", branch: "main", clean: true, greenHead: true,
			head:   "pushed-since",
			forced: &forcedMerge{reason: "please", by: "ada", headSHA: "reviewed"}},
		why: "branch has moved since the force merge",
	}, {
		// A stamp from before stamps carried a head cannot say which
		// commit anybody reviewed, so it authorises none of them.
		name: "an operator's override that names no head",
		req: mergeRequest{taskID: "t1", branch: "main", clean: true, head: "reviewed",
			forced: &forcedMerge{reason: "please", by: "ada"}},
		why: "does not record which commit was reviewed",
	}, {
		// Nor a prefix: an abbreviated sha is a collision somebody
		// pushing to the branch could manufacture.
		name: "an operator's override naming an abbreviation of the head",
		req: mergeRequest{taskID: "t1", branch: "main", clean: true,
			head:   "0123456789abcdef0123456789abcdef01234567",
			forced: &forcedMerge{reason: "please", by: "ada", headSHA: "0123456"}},
		why: "branch has moved since the force merge",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := mayMerge(tc.req)
			if tc.want != "" {
				if !got.ok || got.arm != tc.want {
					t.Fatalf("mayMerge = %s, want it eligible under %q", got, tc.want)
				}
				return
			}
			if got.ok {
				t.Fatalf("mayMerge = %s, want a refusal mentioning %q", got, tc.why)
			}
			if !strings.Contains(got.why, tc.why) {
				t.Fatalf("the refusal reads %q, want it to say %q", got.why, tc.why)
			}
		})
	}
}
