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

package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
)

// turnCap is the io.Writer Framework.Run splices into codex's live
// stdout to enforce RunConfig.MaxTurns. codex has no --max-turns flag to
// push the cap down into the binary (see this package's own doc
// comment), so the cap is counted here instead, off the same events the
// transcript is built from: every completed assistant message is one
// turn, and reaching max cancels the run's context, which
// procgroup.Prepare turns into a kill of codex and its MCP child both.
//
// It is agent/antigravity's turnCap over codex's own vocabulary, down to
// the reasoning: counting on the live stream rather than on the finished
// capture is the whole point -- a cap applied after the process exits
// would report a runaway run without ever having stopped one.
//
// Writes arrive as arbitrary byte chunks, not whole lines: exec.Cmd
// copies whatever it reads from the pipe. A partial trailing line is
// therefore held in buf and completed by a later write, rather than
// being parsed (and miscounted, or dropped) as if it were whole.
//
// One turnCap outlives the subprocess it is watching. Framework.Run may
// exec codex more than once for a single run -- starting the task again
// after a transient provider refusal, see retry.go -- and the cap is
// deliberately cumulative across those attempts: RunConfig.MaxTurns is a
// ceiling on the run, so a run that spent nine of its ten turns and was
// then refused gets one more, not ten. What does not carry across an
// attempt is cancel (each attempt has a context of its own, and
// cancelling the previous one stops nothing) and buf (a killed
// subprocess can leave half a line behind, and gluing it to the next
// attempt's first line would make both unparseable).
type turnCap struct {
	max int

	mu      sync.Mutex
	cancel  context.CancelFunc
	buf     []byte
	turns   int
	stopped bool
}

// retarget points the cap at the attempt about to start: its context's
// cancel, and a clean line buffer.
func (c *turnCap) retarget(cancel context.CancelFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancel = cancel
	c.buf = nil
}

// Write implements io.Writer. It never reports an error: this writer is
// one leg of the io.MultiWriter carrying codex's stdout, and failing
// here would stop the transcript mirror alongside it. A cap that cannot
// parse a line simply does not count it.
func (c *turnCap) Write(p []byte) (int, error) {
	if c.max <= 0 {
		return len(p), nil
	}
	c.mu.Lock()
	c.buf = append(c.buf, p...)
	var complete [][]byte
	for {
		i := bytes.IndexByte(c.buf, '\n')
		if i < 0 {
			break
		}
		line := make([]byte, i)
		copy(line, c.buf[:i])
		complete = append(complete, line)
		c.buf = c.buf[i+1:]
	}
	trip := false
	for _, line := range complete {
		if !isCompletedTurn(line) {
			continue
		}
		c.turns++
		if c.turns >= c.max && !c.stopped {
			c.stopped = true
			trip = true
		}
	}
	cancel := c.cancel
	c.mu.Unlock()
	if trip && cancel != nil {
		cancel()
	}
	return len(p), nil
}

// tripped reports whether this cap is what stopped the run -- what
// Framework.Run reads to tell "codex failed" from "we stopped codex",
// the subprocess error being an indistinguishable context cancellation
// either way.
func (c *turnCap) tripped() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}

// isCompletedTurn reports whether one line is a finished assistant
// message -- the event this package counts turns in, in either of the
// two vocabularies transcript.go reads, and matching exactly what
// parsedEvents.turns counts over a whole capture so the live cap and a
// post-hoc read of the same stream can never disagree.
func isCompletedTurn(line []byte) bool {
	var ev rawEvent
	if err := json.Unmarshal(bytes.TrimSpace(line), &ev); err != nil {
		return false
	}
	if ev.Msg != nil {
		return ev.Msg.Type == "agent_message" && ev.Msg.Message != ""
	}
	return ev.Type == "item.completed" && ev.Item != nil &&
		ev.Item.itemType() == itemAgentMessage && ev.Item.Text != ""
}
