package gitproxy

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

func TestFrontierIsWhereAWalkLeavesThePack(t *testing.T) {
	c := packCommits{
		"m": {"a", "x"}, // a merge of a carried commit and one the remote has
		"a": {"y"},
		"r": nil, // a root
	}
	for name, tc := range map[string]struct {
		tip  string
		want []string
	}{
		"a tip the pack does not carry": {"z", []string{"z"}},
		"every parent left uncarried":   {"m", []string{"x", "y"}},
		"a root carried whole":          {"r", nil},
	} {
		got, ok := c.frontier(tc.tip, maxFrontierCommits)
		slices.Sort(got)
		if !ok || !slices.Equal(got, tc.want) {
			t.Errorf("%s: frontier = %v, %v; want %v", name, got, ok, tc.want)
		}
	}
	if _, ok := c.frontier("m", 1); ok {
		t.Error("a frontier past its limit was answered")
	}
}

func TestUploadPackRequestAsksForARawWholePack(t *testing.T) {
	got := string(uploadPackRequest([]string{oidA, oidB}, oidC, 20))
	want := "005awant " + oidA + " ofs-delta no-progress agent=grain-proxy\n" +
		"0032want " + oidB + "\n0000" +
		"0032have " + oidC + "\n0009done\n"
	if got != want {
		t.Errorf("request = %q, want %q", got, want)
	}
	if !strings.Contains(string(uploadPackRequest([]string{oidA}, oidC, 32)), "object-format=sha256") {
		t.Error("a SHA-256 request does not say so")
	}
}

func TestReadUploadPackResultSaysWhatTheRemoteRefused(t *testing.T) {
	if _, err := readUploadPackResult(strings.NewReader("0023ERR upload-pack: not our ref x\n"), 20); err == nil ||
		!strings.Contains(err.Error(), "not our ref") {
		t.Errorf("an ERR answer: %v", err)
	}
	if _, err := readUploadPackResult(strings.NewReader("0008NAK\n"), 20); err == nil {
		t.Error("an answer with no pack was read as one")
	}
	empty := "PACK\x00\x00\x00\x02\x00\x00\x00\x00"
	if c, err := readUploadPackResult(strings.NewReader("0031ACK "+oidC+"\n"+empty), 20); err != nil || len(c) != 0 {
		t.Errorf("an ACK and an empty pack: %v, %v", c, err)
	}
}

// The repository's answer decides a fast-forward the pack cannot show,
// and fails closed: an error asking it is a refusal that says why.
func TestADeclaredFastForwardTheRemoteMustShow(t *testing.T) {
	own := stubBranchLookup{branch: "grain/task-2112", live: true}
	a := &ModelAuthorizer{Store: stubScopeLookup{}, Branches: stubDeclaredPush{
		stubBranchLookup: own, declared: releaseDeclaration(model.RefPush{Pattern: "refs/heads/main"}),
	}}
	authorize := func(remote packCommits, remoteErr error) string {
		t.Helper()
		var asked []string
		check := &pushCheck{
			body: func() (io.Reader, error) { return strings.NewReader("0000PACK\x00\x00\x00\x02\x00\x00\x00\x00"), nil },
			upstream: func(wants []string, have string, _ int) (packCommits, error) {
				if have != oidA {
					t.Errorf("asked with have %s, want the old tip %s", have, oidA)
				}
				asked = append(asked, wants...)
				return remote, remoteErr
			},
		}
		reason, err := a.AuthorizeRefUpdates(withPushCheck(context.Background(), check), "sandbox-0", "acme", "widgets",
			[]RefUpdate{{Old: oidA, New: oidB, Ref: "refs/heads/main"}})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(asked, []string{oidB}) {
			t.Errorf("asked the remote about %v, want the new tip only", asked)
		}
		return reason
	}
	if reason := authorize(packCommits{oidB: {oidC}, oidC: {oidA}}, nil); reason != "" {
		t.Errorf("a fast-forward the remote shows was refused: %s", reason)
	}
	if reason := authorize(packCommits{oidB: {oidC}, oidC: nil}, nil); !strings.Contains(reason, "could not show") {
		t.Errorf("a move the remote does not show descends was let through: %q", reason)
	}
	if reason := authorize(nil, errors.New("the remote answered 502")); !strings.Contains(reason, "the remote answered 502") {
		t.Errorf("a failed ask was not refused with its reason: %q", reason)
	}
}

type stubFastForwardOnly struct {
	stubBranchLookup
	bound bool
	err   error
}

func (s stubFastForwardOnly) GitPushFastForwardOnly(context.Context, string) (bool, error) {
	return s.bound, s.err
}

// A run whose branch is the change it reviews may move that branch
// forward and nothing else: no delete, no create, no force-push -- and
// a run that is not bound so keeps its own branch its own business.
func TestABorrowedBranchMayOnlyBeFastForwarded(t *testing.T) {
	own := stubBranchLookup{branch: "grain/task-984", live: true}
	ref := "refs/heads/grain/task-984"
	authorize := func(lookup BranchLookup, u RefUpdate) (string, error) {
		t.Helper()
		check := &pushCheck{
			body: func() (io.Reader, error) { return strings.NewReader("0000PACK\x00\x00\x00\x02\x00\x00\x00\x00"), nil },
			upstream: func([]string, string, int) (packCommits, error) {
				return packCommits{oidB: {oidC}, oidC: nil}, nil // oidB does not descend from oidA
			},
		}
		return (&ModelAuthorizer{Store: stubScopeLookup{}, Branches: lookup}).AuthorizeRefUpdates(
			withPushCheck(context.Background(), check), "sandbox-0", "acme", "widgets", []RefUpdate{u})
	}
	bound := stubFastForwardOnly{stubBranchLookup: own, bound: true}
	for name, u := range map[string]RefUpdate{
		"delete": {Old: oidA, New: zero, Ref: ref},
		"create": {Old: zero, New: oidB, Ref: ref},
		"force":  {Old: oidA, New: oidB, Ref: ref},
	} {
		if reason, err := authorize(bound, u); err != nil || reason == "" {
			t.Errorf("%s of the reviewed branch was let through (%v)", name, err)
		}
		if reason, err := authorize(stubFastForwardOnly{stubBranchLookup: own}, u); err != nil || reason != "" {
			t.Errorf("%s of an unbound run's own branch was refused: %q (%v)", name, reason, err)
		}
	}
	forward := RefUpdate{Old: oidA, New: oidC, Ref: ref}
	ffCheck := func(lookup BranchLookup) (string, error) {
		check := &pushCheck{
			body: func() (io.Reader, error) { return strings.NewReader("0000PACK\x00\x00\x00\x02\x00\x00\x00\x00"), nil },
			upstream: func([]string, string, int) (packCommits, error) {
				return packCommits{oidC: {oidA}}, nil
			},
		}
		return (&ModelAuthorizer{Store: stubScopeLookup{}, Branches: lookup}).AuthorizeRefUpdates(
			withPushCheck(context.Background(), check), "sandbox-0", "acme", "widgets", []RefUpdate{forward})
	}
	if reason, err := ffCheck(bound); err != nil || reason != "" {
		t.Errorf("a fast-forward of the reviewed branch was refused: %q (%v)", reason, err)
	}
	if _, err := authorize(stubFastForwardOnly{stubBranchLookup: own, err: errors.New("db down")}, forward); err == nil {
		t.Error("a lookup that failed let the push through")
	}
}

var oidC = strings.Repeat("c", 40)
