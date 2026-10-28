package localexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// StdinPreviewMax is how much of a call's stdin its Request shows: a
// script that reads its instructions from stdin is approved by somebody
// who can see them, up to this many bytes.
const StdinPreviewMax = 4096

// Request is one call waiting for approval (approve: true).
type Request struct {
	// ID names the call: a number, unique in the run.
	ID     string   `json:"id"`
	Tool   string   `json:"tool"`
	Script string   `json:"script"`
	Args   []string `json:"args"`
	// Stdin is how many bytes the call gives the script on stdin.
	Stdin int `json:"stdin_bytes"`
	// StdinPreview is the first StdinPreviewMax bytes of stdin, cut at
	// a character boundary; StdinTruncated is whether that is less than
	// all of it. StdinSHA256 is the hex SHA-256 of the whole of stdin,
	// so a person can check a long one against what they expected.
	StdinPreview   string `json:"stdin_preview,omitempty"`
	StdinTruncated bool   `json:"stdin_truncated,omitempty"`
	StdinSHA256    string `json:"stdin_sha256,omitempty"`
	// Deadline is when the call gives up waiting.
	Deadline time.Time `json:"deadline"`
}

// Summary is the request on one line, for a prompt or a notification.
func (r Request) Summary() string {
	var b strings.Builder
	b.WriteString(r.Script)
	for _, a := range r.Args {
		b.WriteByte(' ')
		b.WriteString(strconv.Quote(a))
	}
	if r.Stdin > 0 {
		fmt.Fprintf(&b, " (and %d bytes on stdin, sha256 %s: %s", r.Stdin, r.StdinSHA256, strconv.Quote(r.StdinPreview))
		if r.StdinTruncated {
			fmt.Fprintf(&b, " [truncated: first %d of %d bytes shown]", len(r.StdinPreview), r.Stdin)
		}
		b.WriteByte(')')
	}
	return b.String()
}

// setStdin fills in r's stdin fields from the whole of stdin.
func (r *Request) setStdin(stdin string) {
	sum := sha256.Sum256([]byte(stdin))
	r.Stdin = len(stdin)
	r.StdinSHA256 = hex.EncodeToString(sum[:])
	r.StdinPreview, r.StdinTruncated = Preview(stdin, StdinPreviewMax)
}

// Preview is at most max bytes of s, cut back to a character boundary,
// and whether that is less than all of s.
func Preview(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// Approver asks a person whether a call may run. Approve returns whether
// it may; it returns ctx's error when ctx ends first, which is the
// call's approval timeout.
type Approver interface {
	Approve(ctx context.Context, r Request) (bool, error)
}

// ApproverFunc adapts a function to Approver.
type ApproverFunc func(ctx context.Context, r Request) (bool, error)

func (f ApproverFunc) Approve(ctx context.Context, r Request) (bool, error) { return f(ctx, r) }

// ErrDenied is a call a person denied.
var ErrDenied = errors.New("the call was denied")

// Queue holds calls until Answer answers them: Silo's approval queue,
// which `silo approve` answered over the session's control socket.
// Notify, when set, is told of each call as it starts waiting -- where
// the surface a person approves from is told there is something to
// approve.
type Queue struct {
	Notify func(Request)

	mu      sync.Mutex
	pending map[string]*pending
}

type pending struct {
	req    Request
	answer chan bool
}

// Approve queues r and waits for its answer.
func (q *Queue) Approve(ctx context.Context, r Request) (bool, error) {
	p := &pending{req: r, answer: make(chan bool, 1)}
	q.mu.Lock()
	if q.pending == nil {
		q.pending = map[string]*pending{}
	}
	q.pending[r.ID] = p
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		delete(q.pending, r.ID)
		q.mu.Unlock()
	}()
	if q.Notify != nil {
		q.Notify(r)
	}
	select {
	case ok := <-p.answer:
		return ok, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// Pending lists the calls waiting, oldest first.
func (q *Queue) Pending() []Request {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Request, 0, len(q.pending))
	for _, p := range q.pending {
		out = append(out, p.req)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(out[i].ID)
		b, _ := strconv.Atoi(out[j].ID)
		return a < b
	})
	return out
}

// Answer approves or denies the call id. With no id it answers the one
// call waiting, and is an error when there are none or several.
func (q *Queue) Answer(id string, approve bool) (Request, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if id == "" {
		switch len(q.pending) {
		case 0:
			return Request{}, errors.New("no call is waiting for approval")
		case 1:
			for k := range q.pending {
				id = k
			}
		default:
			var ids []string
			for k, p := range q.pending {
				ids = append(ids, fmt.Sprintf("%s (%s)", k, p.req.Summary()))
			}
			sort.Strings(ids)
			return Request{}, fmt.Errorf("%d calls are waiting for approval; name one: %s", len(ids), strings.Join(ids, ", "))
		}
	}
	p, ok := q.pending[id]
	if !ok {
		return Request{}, fmt.Errorf("no call %q is waiting for approval", id)
	}
	delete(q.pending, id)
	p.answer <- approve
	return p.req, nil
}
