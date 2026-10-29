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

package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DefaultModelRetryCutoff is the longest one model turn may run while
// agy's own log shows it retrying a provider error. It is the ceiling the
// live refusal probe already used as the point where a vendor retry ladder
// stopped being cheap enough to leave alone. agy 1.2.2 crossed it: a
// persistent 503 was still being retried five minutes and ten requests
// later, and nothing about it reached the stream. 1.4.9, the release the
// image pins now, still crosses it: it gives up on its own after about
// two and a half minutes and nine attempts, so this cutoff is still what
// ends a refused turn, and the refusal agy prints when it gives up comes
// too late for grain to read it off the stream.
//
// DefaultModelTurnTimeout is the longest a turn may run when that log
// shows no such retries -- a model that is slow rather than refused. Some
// providers reached through agy are legitimately that slow, and cutting
// them at the retry cutoff cost every such run a resume per turn.
//
// Neither is an inactivity deadline. Streaming thinking does not reset
// them, since that would let a retry implementation keep a refused turn
// alive indefinitely by printing progress. Tool calls pause them
// completely: wait_for_checks and chat are allowed to block far longer,
// and their own deadlines remain the authority for those waits.
//
// A connection may override either (model.AgentBackend's
// ModelRetryCutoff and ModelTurnTimeout, through WithTurnLimits).
const (
	DefaultModelRetryCutoff = 100 * time.Second
	DefaultModelTurnTimeout = 15 * time.Minute
)

// The tiers a turn deadline can fire under. Each says what grain knew
// about the turn when it stopped it, and so which setting would have
// changed the outcome.
const (
	// turnTierRetry: agy's log showed it retrying a provider error, and
	// the turn had run past the retry cutoff.
	turnTierRetry = "retry cutoff"
	// turnTierSlow: no retries were logged, and the turn had run past
	// the slow-turn ceiling.
	turnTierSlow = "slow-turn ceiling"
	// turnTierFallback: agy's log could not be read, so grain could not
	// tell the two apart and applied the retry cutoff to the turn, as it
	// did before the log was consulted at all.
	turnTierFallback = "retry cutoff, log unavailable"
)

// turnRetryPollLimit caps how often an open turn past its retry cutoff
// has its log re-read. Each read is one bounded tail of a private file,
// remote on a run host, so this is a cost a long slow turn pays every few
// seconds rather than once per stream line.
const turnRetryPollLimit = 5 * time.Second

// turnLogReadTimeout bounds one of those reads. A probe that cannot
// finish in time is a log that is unavailable for this turn.
const turnLogReadTimeout = 10 * time.Second

// turnExcerptLimit keeps the quoted log record in a timeout message to
// something a transcript note can carry.
const turnExcerptLimit = 300

// turnVerdict is what stopped a turn: the tier that fired, the limit it
// enforced, and for the retry tier the redacted log record that showed
// agy retrying.
type turnVerdict struct {
	tier    string
	limit   time.Duration
	excerpt string
}

// turnDeadline watches agy's live stream and cancels an attempt that
// spends too long in one model turn. A turn starts with the attempt
// itself or when the last active tool finishes, and ends when its
// agent_response step completes. Tool time is outside the clock.
//
// How long is too long depends on agy's --log-file, which is the only
// place agy says it is retrying a provider error (capacity.go). From the
// retry cutoff on, the log is read every few seconds: a turn whose log has
// gained a retry record since the turn began is cancelled at once, and
// one that has not runs on until the slow-turn ceiling. A log that cannot
// be read -- no capture file, an older agy that writes none, a format
// diagnosticRecord no longer recognizes -- is no signal at all, and the
// turn is cancelled at the retry cutoff, as every turn was before the log
// was consulted.
//
// Like turnCap, one watcher is reused across retries, so retarget replaces
// its cancel, its log path and its partial-line buffer. generation keeps a
// watcher from an earlier turn or attempt from cancelling the next one
// after a tight Stop/retarget race.
type turnDeadline struct {
	cutoff     time.Duration
	ceiling    time.Duration
	poll       time.Duration
	connection string
	// probe reads the complete, redacted records at the tail of agy's
	// log at path. Nil, like an empty path, means there is no log.
	probe func(path string) ([]string, error)

	mu         sync.Mutex
	cancel     context.CancelFunc
	logPath    string
	watchStop  chan struct{}
	generation uint64
	buf        []byte
	activeTool map[int]bool
	waiting    bool
	stopped    bool
	// laterTurn is set once an attempt's first turn has been armed: the
	// first turn's log is a file created for this exec, so everything in
	// it belongs to that turn, and only a later turn needs a baseline.
	laterTurn   bool
	verdict     turnVerdict
	noLogLogged bool
}

func newTurnDeadline(cutoff, ceiling time.Duration, connection string) *turnDeadline {
	if ceiling > 0 && ceiling < cutoff {
		// A ceiling below the cutoff is the stricter of the two for
		// every turn, logged retries or not.
		cutoff = ceiling
	}
	poll := cutoff / 4
	if poll > turnRetryPollLimit {
		poll = turnRetryPollLimit
	}
	if poll <= 0 {
		poll = time.Millisecond
	}
	return &turnDeadline{cutoff: cutoff, ceiling: ceiling, poll: poll, connection: connection}
}

// retarget points the watcher at a new attempt: its cancel, and the log
// file agy was given for it ("" for none).
func (d *turnDeadline) retarget(cancel context.CancelFunc, logPath string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.disarmLocked()
	d.cancel = cancel
	d.logPath = logPath
	d.buf = nil
	d.activeTool = make(map[int]bool)
	d.stopped = false
	d.laterTurn = false
	d.verdict = turnVerdict{}
	d.armLocked()
}

// stop ends the current attempt's clock without marking it timed out.
func (d *turnDeadline) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.disarmLocked()
	d.cancel = nil
}

// Write consumes complete stream-json lines while preserving arbitrary
// exec.Cmd chunk boundaries. It never returns an error, because it is one
// leg of the MultiWriter carrying the live transcript.
func (d *turnDeadline) Write(p []byte) (int, error) {
	if d.cutoff <= 0 {
		return len(p), nil
	}
	d.mu.Lock()
	d.buf = append(d.buf, p...)
	for {
		i := bytes.IndexByte(d.buf, '\n')
		if i < 0 {
			break
		}
		line := make([]byte, i)
		copy(line, d.buf[:i])
		d.buf = d.buf[i+1:]
		d.applyLineLocked(line)
	}
	d.mu.Unlock()
	return len(p), nil
}

func (d *turnDeadline) applyLineLocked(line []byte) {
	var ev rawEvent
	if json.Unmarshal(bytes.TrimSpace(line), &ev) != nil {
		return
	}
	if ev.Event == "result" {
		d.disarmLocked()
		return
	}
	if ev.Event != "step_update" || ev.Step == nil {
		return
	}
	step := ev.Step
	switch step.StepType {
	case stepTypeAgentResponse:
		if step.State == stateDone {
			d.disarmLocked()
		}
	case stepTypeTool:
		switch step.State {
		case stateActive:
			d.activeTool[step.StepIndex] = true
			d.disarmLocked()
		case stateDone, stateError:
			delete(d.activeTool, step.StepIndex)
			if len(d.activeTool) == 0 {
				d.armLocked()
			}
		}
	}
}

func (d *turnDeadline) armLocked() {
	if d.cutoff <= 0 || d.waiting || d.stopped {
		return
	}
	d.generation++
	d.waiting = true
	d.watchStop = make(chan struct{})
	go d.watch(d.generation, d.logPath, d.laterTurn, d.watchStop)
	d.laterTurn = true
}

func (d *turnDeadline) disarmLocked() {
	d.generation++
	d.waiting = false
	if d.watchStop != nil {
		close(d.watchStop)
		d.watchStop = nil
	}
}

// watch is one turn's clock. For a turn after an attempt's first, a
// baseline is read the moment the turn opens, so a retry an earlier turn
// of the same exec logged -- and recovered from -- is not held against
// this one.
func (d *turnDeadline) watch(generation uint64, path string, baselined bool, done <-chan struct{}) {
	start := time.Now()
	seen := make(map[string]bool)
	if baselined {
		baseline, _ := d.retryRecords(path)
		for _, record := range baseline {
			seen[record] = true
		}
	}
	if !sleepUnless(done, d.cutoff-time.Since(start)) {
		return
	}
	for {
		records, ok := d.retryRecords(path)
		if !ok {
			d.noteNoLog(path)
			d.expire(generation, turnVerdict{tier: turnTierFallback, limit: d.cutoff})
			return
		}
		fresh := ""
		for _, record := range records {
			if !seen[record] {
				fresh = record
			}
		}
		if fresh != "" {
			d.expire(generation, turnVerdict{tier: turnTierRetry, limit: d.cutoff, excerpt: excerpt(fresh)})
			return
		}
		remaining := d.ceiling - time.Since(start)
		if remaining <= 0 {
			d.expire(generation, turnVerdict{tier: turnTierSlow, limit: d.ceiling})
			return
		}
		if !sleepUnless(done, min(d.poll, remaining)) {
			return
		}
	}
}

// sleepUnless waits for wait, reporting false if done closed first.
func sleepUnless(done <-chan struct{}, wait time.Duration) bool {
	if wait <= 0 {
		select {
		case <-done:
			return false
		default:
			return true
		}
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-done:
		return false
	case <-t.C:
		return true
	}
}

// retryRecords reads the log at path and returns the records in it that
// show agy retrying a provider error. ok is false when there is no log to
// read, or one holding no record diagnosticRecord recognizes: agy writes
// records from startup, so an empty log a cutoff into a turn is an agy
// that is not logging where grain asked, or not in a shape grain reads.
func (d *turnDeadline) retryRecords(path string) (records []string, ok bool) {
	if path == "" || d.probe == nil {
		return nil, false
	}
	all, err := d.probe(path)
	if err != nil || len(all) == 0 {
		return nil, false
	}
	for _, record := range all {
		if providerRetryRecord(record) {
			records = append(records, record)
		}
	}
	return records, true
}

// noteNoLog says once per run that the log-based signal is unavailable,
// so an operator wondering why slow turns are still cut at the retry
// cutoff has the answer in the daemon log.
func (d *turnDeadline) noteNoLog(path string) {
	d.mu.Lock()
	logged := d.noLogLogged
	d.noLogLogged = true
	d.mu.Unlock()
	if logged {
		return
	}
	why := "agy's --log-file held no records grain recognizes"
	if path == "" || d.probe == nil {
		why = "agy was given no --log-file"
	}
	log.Printf("antigravity: %s: %s, so provider retries cannot be told from a slow model; model turns fall back to the %s retry cutoff",
		d.connectionLabel(), why, d.cutoff)
}

func (d *turnDeadline) expire(generation uint64, verdict turnVerdict) {
	d.mu.Lock()
	if generation != d.generation || !d.waiting || d.stopped {
		d.mu.Unlock()
		return
	}
	d.stopped = true
	d.waiting = false
	d.verdict = verdict
	cancel := d.cancel
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (d *turnDeadline) tripped() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stopped
}

// trippedVerdict is what stopped the current attempt's turn, if anything
// did.
func (d *turnDeadline) trippedVerdict() (turnVerdict, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.verdict, d.stopped
}

func (d *turnDeadline) connectionLabel() string {
	if d.connection == "" {
		return "connection (deployment default)"
	}
	return fmt.Sprintf("connection %q", d.connection)
}

// describe is the sentence a timed-out turn is reported with: which tier
// fired, at what value, on which connection, and the setting that moves
// it. It begins "model turn exceeded <limit>" in every tier, which is
// what an operator searching transcripts for these has always matched.
func (d *turnDeadline) describe(v turnVerdict) string {
	switch v.tier {
	case turnTierRetry:
		return fmt.Sprintf("model turn exceeded %s (%s, %s) while agy was retrying provider errors; agy log: %q. "+
			"The connection's Model retry cutoff setting (modelRetryCutoff) changes this limit",
			v.limit, v.tier, d.connectionLabel(), v.excerpt)
	case turnTierSlow:
		return fmt.Sprintf("model turn exceeded %s (%s, %s) with no provider errors logged; the model is slow. "+
			"The connection's Model turn timeout setting (modelTurnTimeout) changes this limit",
			v.limit, v.tier, d.connectionLabel())
	default:
		return fmt.Sprintf("model turn exceeded %s (%s, %s): agy's log could not be read, so a slow model could not be told from a retried one. "+
			"The connection's Model retry cutoff setting (modelRetryCutoff) changes this limit",
			v.limit, v.tier, d.connectionLabel())
	}
}

// providerRetryLog are the shapes, beyond the refusal classifiers' own
// markers, that mean agy is retrying a provider error. They are looser
// than capacityMarkers on purpose, and safe only because of where they
// are read: agy's private log carries agy's own records, never a
// repository's text, so a bare HTTP status in an error record is the
// provider's answer and not a line of somebody's test output.
var providerRetryLog = regexp.MustCompile(`(?i)\b(?:error|status(?: code)?|http(?:/[\d.]+)?|code)\s*[:=]?\s*(?:429|5\d\d)\b|\bretrying\b|\bwill retry\b|\bretry attempt\b|\bresource_exhausted\b`)

// providerRetryRecord reports whether one record of agy's log shows it
// retrying a provider error: anything the quota and capacity classifiers
// would read as a refusal, or one of providerRetryLog's shapes.
func providerRetryRecord(record string) bool {
	return mentionsUsageLimit(record) ||
		capacityFailure(record, nil) != nil ||
		providerRetryLog.MatchString(record)
}

func excerpt(record string) string {
	if len(record) <= turnExcerptLimit {
		return record
	}
	return strings.ToValidUTF8(record[:turnExcerptLimit], "") + "…"
}
