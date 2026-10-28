package gitproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
)

var (
	oidA = strings.Repeat("a", 40)
	oidB = strings.Repeat("b", 40)
	zero = strings.Repeat("0", 40)
)

func pkt(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

// pushBody is a receive-pack request as git sends one: the first command
// carries the capabilities, a flush ends the list, and the packfile
// follows.
func pushBody(commands ...string) []byte {
	var b strings.Builder
	for i, c := range commands {
		if i == 0 {
			c += "\x00report-status side-band-64k agent=git/2.39.2"
		}
		b.WriteString(pkt(c + "\n"))
	}
	b.WriteString("0000PACK\x00\x00\x00\x02not really a pack")
	return []byte(b.String())
}

func TestParseReceivePackCommandsReadsEveryCommand(t *testing.T) {
	body := pushBody(
		oidA+" "+oidB+" refs/heads/grain/task-7",
		zero+" "+oidB+" refs/heads/new",
		oidA+" "+zero+" refs/tags/v1",
	)
	updates, err := ParseReceivePackCommands(body)
	if err != nil {
		t.Fatal(err)
	}
	want := []RefUpdate{
		{oidA, oidB, "refs/heads/grain/task-7"},
		{zero, oidB, "refs/heads/new"},
		{oidA, zero, "refs/tags/v1"},
	}
	if fmt.Sprint(updates) != fmt.Sprint(want) {
		t.Errorf("updates = %v, want %v", updates, want)
	}
}

func TestParseReceivePackCommandsSkipsShallowLinesAndReadsSHA256(t *testing.T) {
	long := strings.Repeat("c", 64)
	body := []byte(pkt("shallow "+oidA+"\n") + string(pushBody(long+" "+long+" refs/heads/x")))
	updates, err := ParseReceivePackCommands(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || updates[0].Ref != "refs/heads/x" {
		t.Errorf("updates = %v", updates)
	}
}

// Every way a command list can fail to be one is refused, never read as
// "no commands": that is the difference between this failing closed and
// failing open.
func TestParseReceivePackCommandsRefusesWhatItCannotRead(t *testing.T) {
	cases := map[string]string{
		"empty":           "",
		"no flush":        pkt(oidA + " " + oidB + " refs/heads/main\n"),
		"bad length":      "zzzz",
		"short length":    "0003",
		"overlong length": "0fff" + oidA,
		"two fields":      pkt(oidA+" refs/heads/main\n") + "0000",
		"four fields":     pkt(oidA+" "+oidB+" refs/heads/main extra\n") + "0000",
		"bad oid":         pkt(strings.Repeat("g", 40)+" "+oidB+" refs/heads/main\n") + "0000",
		"empty ref":       pkt(oidA+" "+oidB+" \n") + "0000",
		"signed push":     pkt("push-cert\x00report-status\n") + pkt("certificate version 0.1\n") + "0000",
		"shallow after a command": pkt(oidA+" "+oidB+" refs/heads/x\n") +
			pkt("shallow "+oidA+"\n") + "0000",
	}
	for name, body := range cases {
		if updates, err := ParseReceivePackCommands([]byte(body)); err == nil {
			t.Errorf("%s: parsed as %v, want an error", name, updates)
		}
	}
}

func TestCommandListInflatesAGzipEncodedPush(t *testing.T) {
	raw := pushBody(oidA + " " + oidB + " refs/heads/main")
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write(raw)
	_ = w.Close()
	data, err := commandList(bytes.NewReader(gz.Bytes()), "gzip")
	if err != nil {
		t.Fatal(err)
	}
	updates, err := ParseReceivePackCommands(data)
	if err != nil || len(updates) != 1 || updates[0].Ref != "refs/heads/main" {
		t.Fatalf("updates = %v, err = %v", updates, err)
	}
	if _, err := commandList(bytes.NewReader(raw), "br"); err == nil {
		t.Error("an encoding the proxy cannot read should be refused")
	}
}

type stubBranchLookup struct {
	branch string
	live   bool
	err    error
}

func (s stubBranchLookup) GitPushBranch(context.Context, string) (string, bool, error) {
	return s.branch, s.live, s.err
}

func TestAuthorizeRefUpdatesAllowsOnlyTheRunsOwnBranch(t *testing.T) {
	a := &ModelAuthorizer{
		Store:    stubScopeLookup{target: &model.RepoRef{Owner: "acme", Name: "widgets"}},
		Branches: stubBranchLookup{branch: "grain/task-7", live: true},
	}
	ctx := context.Background()
	own := RefUpdate{oidA, oidB, "refs/heads/grain/task-7"}
	for _, ok := range [][]RefUpdate{
		{own},
		{{zero, oidB, own.Ref}}, // creating it
		{{oidA, zero, own.Ref}}, // deleting it
		nil,
	} {
		if reason, err := a.AuthorizeRefUpdates(ctx, "sandbox-0", "acme", "widgets", ok); err != nil || reason != "" {
			t.Errorf("%v: refused (%q, %v), want allowed", ok, reason, err)
		}
	}
	for _, refused := range [][]RefUpdate{
		{{oidA, oidB, "refs/heads/main"}},
		{{oidA, zero, "refs/heads/main"}},
		{{oidA, oidB, "refs/heads/grain/task-8"}},
		{{zero, oidB, "refs/tags/grain/task-7"}},
		{{oidA, oidB, "grain/task-7"}},
		{own, {oidA, oidB, "refs/heads/main"}},
	} {
		reason, err := a.AuthorizeRefUpdates(ctx, "sandbox-0", "acme", "widgets", refused)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(reason, "only its own task's branch, refs/heads/grain/task-7") {
			t.Errorf("%v: reason = %q, want a refusal naming the run's own branch", refused, reason)
		}
	}
}

func TestAuthorizeRefUpdatesFailsClosed(t *testing.T) {
	ctx := context.Background()
	update := []RefUpdate{{oidA, oidB, "refs/heads/grain/task-7"}}
	for name, a := range map[string]*ModelAuthorizer{
		"no live run":   {Store: stubScopeLookup{}, Branches: stubBranchLookup{}},
		"empty branch":  {Store: stubScopeLookup{}, Branches: stubBranchLookup{live: true}},
		"no lookup set": {Store: stubScopeLookup{}},
	} {
		if reason, err := a.AuthorizeRefUpdates(ctx, "sandbox-0", "acme", "widgets", update); err != nil || reason == "" {
			t.Errorf("%s: (%q, %v), want a refusal", name, reason, err)
		}
	}
	a := &ModelAuthorizer{Store: stubScopeLookup{}, Branches: stubBranchLookup{err: errors.New("db down")}}
	if _, err := a.AuthorizeRefUpdates(ctx, "sandbox-0", "acme", "widgets", update); err == nil {
		t.Error("a lookup error should be returned, not read as an answer")
	}
}

// refAuthorizer is stubAuthorizer plus ModelAuthorizer's ref rule, for
// what Handle does with it.
type refAuthorizer struct {
	stubAuthorizer
	model *ModelAuthorizer
}

func (r refAuthorizer) AuthorizeRefUpdates(ctx context.Context, sandbox, owner, repo string, updates []RefUpdate) (string, error) {
	return r.model.AuthorizeRefUpdates(ctx, sandbox, owner, repo, updates)
}

func TestHandleForwardsAPushOnlyOfTheRunsOwnBranch(t *testing.T) {
	p, forwarder, audit := newTestProxy(t)
	p.Authorizer = refAuthorizer{
		stubAuthorizer: p.Authorizer.(stubAuthorizer),
		model:          &ModelAuthorizer{Store: stubScopeLookup{}, Branches: stubBranchLookup{branch: "grain/task-7", live: true}},
	}
	headers := merge(gitHeaders, map[string]string{
		"Authorization": basicAuthHeader("tok0"),
		"Content-Type":  "application/x-git-receive-pack-request",
		"Accept":        "application/x-git-receive-pack-result",
	})
	push := func(body []byte) ProxyResponse {
		return p.Handle(context.Background(), "POST", "/owner/repo.git/git-receive-pack", "", headers, body)
	}

	if resp := push(pushBody(oidA + " " + oidB + " refs/heads/grain/task-7")); resp.Status != 200 {
		t.Fatalf("pushing the run's own branch: %d %s", resp.Status, resp.Body)
	}
	if len(forwarder.Calls) != 1 {
		t.Fatalf("the run's own branch was not forwarded: %d calls", len(forwarder.Calls))
	}

	// A refused ref is answered the way receive-pack answers one --
	// report-status naming it ng -- so git prints the reason.
	for name, body := range map[string][]byte{
		"default branch":    pushBody(oidA + " " + oidB + " refs/heads/main"),
		"default delete":    pushBody(oidA + " " + zero + " refs/heads/main"),
		"other task":        pushBody(zero + " " + oidB + " refs/heads/grain/task-8"),
		"tag":               pushBody(zero + " " + oidB + " refs/tags/v1"),
		"own branch + main": pushBody(oidA+" "+oidB+" refs/heads/grain/task-7", oidA+" "+oidB+" refs/heads/main"),
	} {
		resp := push(body)
		if resp.Status != 200 || resp.Headers["Content-Type"] != "application/x-git-receive-pack-result" ||
			!strings.Contains(string(resp.Body), "\x01000eunpack ok\n") || !strings.Contains(string(resp.Body), "ng refs/") ||
			!strings.Contains(string(resp.Body), "\x02refusing") {
			t.Errorf("%s: %d %v %q, want a report-status refusal that says why", name, resp.Status, resp.Headers, resp.Body)
		}
	}
	// One whose commands cannot be read has no report to put them in.
	if resp := push([]byte("not a pkt-line")); resp.Status != 403 || !strings.Contains(string(resp.Body), "refusing") {
		t.Errorf("unreadable: %d %q, want a 403 that says why", resp.Status, resp.Body)
	}
	if len(forwarder.Calls) != 1 {
		t.Errorf("a refused push reached the upstream: %d calls", len(forwarder.Calls))
	}
	last := audit.Entries[len(audit.Entries)-1]
	if !strings.HasPrefix(last.Outcome, "denied:") {
		t.Errorf("audit outcome = %q, want a denial", last.Outcome)
	}
}

func TestRejectPushAnswersInTheShapeTheClientAskedFor(t *testing.T) {
	updates := []RefUpdate{{Old: oidA, New: oidB, Ref: "refs/heads/main"}}
	if _, ok := rejectPush(updates, []string{"side-band-64k", "agent=git/2.43.0"}, "no"); ok {
		t.Error("a client that did not ask for report-status was sent one")
	}
	resp, ok := rejectPush(updates, []string{"report-status"}, "refusing\nmain")
	if !ok {
		t.Fatal("report-status without side-band was not answered with a report")
	}
	if want := pkt("unpack ok\n") + pkt("ng refs/heads/main refusing main\n") + "0000"; string(resp.Body) != want {
		t.Errorf("body = %q, want %q", resp.Body, want)
	}
	resp, _ = rejectPush(updates, []string{"report-status-v2", "side-band-64k"}, strings.Repeat("x", 2000))
	for rest := string(resp.Body); rest != "0000"; {
		n, err := strconv.ParseUint(rest[:4], 16, 16)
		if err != nil || n <= 5 || n > 1000 || (rest[4] != 1 && rest[4] != 2) {
			t.Fatalf("not a side-band packet at %q", rest)
		}
		rest = rest[n:]
	}
}
