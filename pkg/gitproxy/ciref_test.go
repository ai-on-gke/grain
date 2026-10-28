package gitproxy

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

// A release workflow's refs (docs/design/release-from-trunk.md §6):
// release tags created, `latest` and `candidate` force-moved, and main
// never rewritten.
func TestCIRepoWriteAllowsReleaseRefsAndRefusesRewritingMain(t *testing.T) {
	writes := NewWriteScopes()
	writes.Grant("ci-job", model.RepoRef{Owner: "bwsalmon", Name: "grain"})
	a := &ReadOnlyScopeAuthorizer{WriteScopes: writes, Next: stubAuthorizer{}}
	zero := strings.Repeat("0", 40)

	for _, tc := range []struct {
		name   string
		update RefUpdate
		// commits is what the push shows: nil shows nothing.
		commits packCommits
		allow   bool
		kind    string
	}{
		{name: "create a release tag", update: RefUpdate{Old: zero, New: oidA, Ref: "refs/tags/v2.4.0"}, allow: true, kind: "create"},
		{name: "create an rc tag", update: RefUpdate{Old: zero, New: oidA, Ref: "refs/tags/v2.4.0-rc.1"}, allow: true, kind: "create"},
		{name: "force-move latest", update: RefUpdate{Old: oidA, New: oidB, Ref: "refs/tags/latest"}, allow: true, kind: "force"},
		{name: "force-move candidate", update: RefUpdate{Old: oidA, New: oidB, Ref: "refs/heads/candidate"}, allow: true, kind: "update"},
		{name: "force-push main", update: RefUpdate{Old: oidA, New: oidB, Ref: "refs/heads/main"}},
		{name: "delete main", update: RefUpdate{Old: oidA, New: zero, Ref: "refs/heads/main"}},
		{
			name:    "fast-forward main",
			update:  RefUpdate{Old: oidA, New: oidB, Ref: "refs/heads/main"},
			commits: packCommits{oidB: {oidA}},
			allow:   true, kind: "update",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := shownPush(tc.commits)
			reason, err := a.AuthorizeRefUpdates(withPushCheck(context.Background(), check),
				"ci-job", "bwsalmon", "grain", []RefUpdate{tc.update})
			if err != nil {
				t.Fatal(err)
			}
			if tc.allow {
				if reason != "" {
					t.Fatalf("refused: %s", reason)
				}
				if len(check.grants) != 1 || check.grants[0].Kind != tc.kind || check.grants[0].Via != ciRefVia {
					t.Errorf("grants = %+v, want one %q grant via %s", check.grants, tc.kind, ciRefVia)
				}
				return
			}
			if reason == "" || !strings.Contains(reason, "refs/heads/main") {
				t.Errorf("reason = %q, want a refusal naming refs/heads/main", reason)
			}
			if len(check.grants) != 0 {
				t.Errorf("a refused push recorded grants %+v", check.grants)
			}
		})
	}
}

// One refused ref refuses the whole push, as receive-pack's atomic
// answer would: a release cut cannot get its tag out while its main
// rewrite is turned back.
func TestCIRepoWriteRefusesAPushThatRewritesMainAlongsideATag(t *testing.T) {
	check := shownPush(nil)
	reason := CIRefUpdates(check, "bwsalmon", "grain", []RefUpdate{
		{Old: strings.Repeat("0", 40), New: oidA, Ref: "refs/tags/v2.4.0"},
		{Old: oidA, New: oidB, Ref: "refs/heads/main"},
	})
	if reason == "" {
		t.Fatal("a push rewriting main beside a tag was allowed")
	}
	if strings.Contains(reason, "refs/tags/v2.4.0") {
		t.Errorf("the refusal blames the tag, which is allowed: %s", reason)
	}
}

// shownPush is a push whose pack has already been read as commits.
func shownPush(commits packCommits) *pushCheck {
	if commits == nil {
		commits = packCommits{}
	}
	return &pushCheck{
		body:    func() (io.Reader, error) { return nil, errors.New("the pack is already read") },
		read:    true,
		commits: commits,
	}
}
