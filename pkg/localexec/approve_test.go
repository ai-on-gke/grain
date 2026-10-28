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

package localexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestQueue(t *testing.T) {
	var mu sync.Mutex
	var notified []string
	q := &Queue{Notify: func(r Request) { mu.Lock(); notified = append(notified, r.ID); mu.Unlock() }}
	if _, err := q.Answer("", true); err == nil || !strings.Contains(err.Error(), "no call") {
		t.Errorf("empty queue: %v", err)
	}
	type answer struct {
		ok  bool
		err error
	}
	wait := func(id string, ctx context.Context) chan answer {
		ch := make(chan answer, 1)
		go func() {
			ok, err := q.Approve(ctx, Request{ID: id, Tool: "local_x", Script: "x"})
			ch <- answer{ok, err}
		}()
		for {
			for _, r := range q.Pending() {
				if r.ID == id {
					return ch
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	a := wait("1", context.Background())
	if r, err := q.Answer("", true); err != nil || r.ID != "1" {
		t.Fatalf("the only call: %v %v", r, err)
	}
	if got := <-a; !got.ok || got.err != nil {
		t.Errorf("approved: %+v", got)
	}
	b := wait("2", context.Background())
	c := wait("3", context.Background())
	if _, err := q.Answer("", true); err == nil || !strings.Contains(err.Error(), "2 calls are waiting") {
		t.Errorf("ambiguous: %v", err)
	}
	if _, err := q.Answer("9", true); err == nil {
		t.Error("answered a call that is not waiting")
	}
	q.Answer("3", false)
	q.Answer("2", true)
	if got := <-c; got.ok || got.err != nil {
		t.Errorf("denied: %+v", got)
	}
	if got := <-b; !got.ok {
		t.Errorf("approved: %+v", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	d := wait("4", ctx)
	if got := <-d; !errors.Is(got.err, context.DeadlineExceeded) {
		t.Errorf("timeout: %+v", got)
	}
	if len(q.Pending()) != 0 {
		t.Errorf("left pending: %v", q.Pending())
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(notified, ",") != "1,2,3,4" {
		t.Errorf("notified %v", notified)
	}
}

func TestApprovedScripts(t *testing.T) {
	d := folder(t, `scripts:
  list:
    - path: scripts/deploy
      approve: true
      timeout: 1s
`, map[string]string{"scripts/deploy": "#!/bin/sh\necho deployed \"$1\"\n", "scripts/free": "#!/bin/sh\necho free\n"})
	q := &Queue{}
	ex := executor(t, d, Config{Approver: q})
	answer := func(approve bool) {
		go func() {
			for len(q.Pending()) == 0 {
				time.Sleep(10 * time.Millisecond)
			}
			q.Answer("", approve)
		}()
	}

	answer(true)
	r, text, isErr := call(t, ex, "local_deploy", map[string]any{"args": []any{"main"}})
	if isErr || r.Stdout != "deployed main\n" {
		t.Fatalf("approved: %s", text)
	}
	answer(false)
	if _, text, isErr := call(t, ex, "local_deploy", map[string]any{"args": []any{"main"}}); !isErr || !strings.Contains(text, ErrDenied.Error()) {
		t.Errorf("denied: %s", text)
	}
	start := time.Now()
	_, text, isErr = call(t, ex, "local_deploy", map[string]any{"args": []any{"main"}})
	if !isErr || !strings.Contains(text, "not approved within 1s") {
		t.Errorf("timeout: %s", text)
	}
	if d := time.Since(start); d < time.Second || d > 5*time.Second {
		t.Errorf("waited %s for a 1s timeout", d)
	}
	// A script without approve: true never asks.
	if r, text, isErr := call(t, ex, "local_free", map[string]any{"args": []any{}}); isErr || r.Stdout != "free\n" {
		t.Errorf("free: %s", text)
	}

	// Without an approver, approve: true calls cannot run.
	ex2 := executor(t, d, Config{})
	if _, text, isErr := call(t, ex2, "local_deploy", map[string]any{"args": []any{}}); !isErr || !strings.Contains(text, "no way to ask") {
		t.Errorf("no approver: %s", text)
	}
}

func TestApproverFuncSeesTheRequest(t *testing.T) {
	d := folder(t, "scripts: {list: [{path: scripts/x, approve: true}]}\n", map[string]string{"scripts/x": "#!/bin/sh\necho ran\n"})
	var got Request
	ex := executor(t, d, Config{Approver: ApproverFunc(func(_ context.Context, r Request) (bool, error) {
		got = r
		return true, nil
	})})
	if _, text, isErr := call(t, ex, "local_x", map[string]any{"args": []any{"a b"}, "stdin": "1234"}); isErr {
		t.Fatal(text)
	}
	const sum = "03ac674216f3e15c761ee1a5e255f067953623c8b388b4459e13f978d7c846f4" // sha256("1234")
	if got.Tool != "local_x" || got.Script != "x" || got.Stdin != 4 || got.StdinPreview != "1234" || got.StdinTruncated || got.StdinSHA256 != sum || got.Deadline.IsZero() {
		t.Fatalf("request %+v", got)
	}
	if want := `x "a b" (and 4 bytes on stdin, sha256 ` + sum + `: "1234")`; got.Summary() != want {
		t.Fatalf("summary %q, want %q", got.Summary(), want)
	}
}

// A script that reads its instructions from stdin is approved by
// somebody who can see them: the approver gets a bounded preview of
// stdin and a hash of the whole of it, and a long stdin is marked as cut.
func TestApproverSeesAStdinPreview(t *testing.T) {
	d := folder(t, "scripts: {list: [{path: scripts/x, approve: true}]}\n", map[string]string{"scripts/x": "#!/bin/sh\nwc -c\n"})
	var got Request
	ex := executor(t, d, Config{Approver: ApproverFunc(func(_ context.Context, r Request) (bool, error) {
		got = r
		return true, nil
	})})
	// A two-byte character straddles the cut, which backs off to before it.
	stdin := strings.Repeat("a", StdinPreviewMax-1) + "é" + strings.Repeat("rm -rf /\n", 100)
	r, text, isErr := call(t, ex, "local_x", map[string]any{"args": []any{}, "stdin": stdin})
	if isErr {
		t.Fatal(text)
	}
	sum := sha256.Sum256([]byte(stdin))
	if got.Stdin != len(stdin) || got.StdinSHA256 != hex.EncodeToString(sum[:]) || !got.StdinTruncated {
		t.Fatalf("request stdin %d bytes, sha256 %s, truncated %v", got.Stdin, got.StdinSHA256, got.StdinTruncated)
	}
	if got.StdinPreview != strings.Repeat("a", StdinPreviewMax-1) {
		t.Fatalf("preview is %d bytes, ending %q", len(got.StdinPreview), got.StdinPreview[len(got.StdinPreview)-4:])
	}
	marker := fmt.Sprintf("[truncated: first %d of %d bytes shown])", StdinPreviewMax-1, len(stdin))
	if s := got.Summary(); !strings.HasSuffix(s, marker) || strings.Contains(s, "rm -rf") {
		t.Fatalf("summary does not end in %q: ...%s", marker, s[len(s)-80:])
	}
	// The script itself still gets all of it.
	if strings.TrimSpace(r.Stdout) != strconv.Itoa(len(stdin)) {
		t.Fatalf("script read %q bytes, want %d", r.Stdout, len(stdin))
	}
}

func TestPreview(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		want string
		cut  bool
	}{
		{"", 4, "", false},
		{"abcd", 4, "abcd", false},
		{"abcde", 4, "abcd", true},
		{"abcé", 4, "abc", true},
		{"日本", 4, "日", true},
	} {
		if got, cut := Preview(c.in, c.max); got != c.want || cut != c.cut {
			t.Errorf("Preview(%q, %d) = %q, %v; want %q, %v", c.in, c.max, got, cut, c.want, c.cut)
		}
	}
}
