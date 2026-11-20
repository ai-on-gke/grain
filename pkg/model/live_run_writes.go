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

package model

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"time"
	"weak"
)

const (
	// defaultLiveRunFlushInterval is how long coalesced live-run activity and
	// transcript updates wait in memory before flushing to the store in a
	// single batched transaction. Short enough that a crash or external reader
	// sees fresh status promptly, and long enough that 100 live runs
	// heartbeating every 500ms collapse into at most a few batched
	// transactions per second instead of hundreds competing on SQLite's
	// single writer lock.
	defaultLiveRunFlushInterval = 250 * time.Millisecond

	// maxFlushedTranscriptHashes bounds the per-run transcript dedup table so
	// long-running daemons never accumulate unbounded memory.
	maxFlushedTranscriptHashes = 1024
)

type liveRunEntry struct {
	runID              string
	taskID             string
	agentStarted       bool
	hasActivity        bool
	activity           string
	activityAt         time.Time
	flushedHasActivity bool
	flushedActivity    string
	flushedActivityAt  time.Time
	firstTurnAt        *time.Time
	firstTurnFlushed   bool
}

type pendingActivity struct {
	seq        uint64
	runID      string
	taskID     string
	cleared    bool
	activity   string
	activityAt time.Time
	firstTurn  *time.Time
}

type pendingTranscript struct {
	seq        uint64
	runID      string
	transcript string
	hash       [32]byte
}

type pendingFinishSnapshot struct {
	runID      string
	taskID     string
	activity   *pendingActivity
	transcript *pendingTranscript
}

// Weak registry mapping *sql.DB -> open *Store instances so callers holding
// only a *sql.DB (such as staterepo.Export) can flush coalesced live-run
// writes before opening a read snapshot, without retaining closed or
// garbage-collected Stores.
var (
	storesByDBMu sync.Mutex
	storesByDB   = map[*sql.DB][]storeWeakEntry{}
)

type storeWeakEntry struct {
	id uint64
	wp weak.Pointer[Store]
}

type storeCleanupKey struct {
	db *sql.DB
	id uint64
}

func registerStoreForDB(s *Store) {
	if s == nil || s.db == nil {
		return
	}
	db := s.db
	id := s.id
	storesByDBMu.Lock()
	storesByDB[db] = append(storesByDB[db], storeWeakEntry{id: id, wp: weak.Make(s)})
	storesByDBMu.Unlock()
	runtime.AddCleanup(s, unregisterStoreKey, storeCleanupKey{db: db, id: id})
}

func unregisterStoreKey(key storeCleanupKey) {
	storesByDBMu.Lock()
	defer storesByDBMu.Unlock()
	entries := storesByDB[key.db]
	if len(entries) == 0 {
		return
	}
	kept := entries[:0]
	for _, e := range entries {
		if e.id != key.id && e.wp.Value() != nil {
			kept = append(kept, e)
		}
	}
	if len(kept) == 0 {
		delete(storesByDB, key.db)
	} else {
		storesByDB[key.db] = kept
	}
}

// FlushDB flushes any buffered live-run writes held by Stores wrapping db.
// Callers reading db directly (such as staterepo.Export) call this before
// starting a read snapshot so coalesced activity and transcripts are present.
func FlushDB(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return nil
	}
	storesByDBMu.Lock()
	entries := storesByDB[db]
	var live []*Store
	if len(entries) > 0 {
		kept := entries[:0]
		for _, e := range entries {
			if st := e.wp.Value(); st != nil {
				kept = append(kept, e)
				live = append(live, st)
			}
		}
		if len(kept) == 0 {
			delete(storesByDB, db)
		} else {
			storesByDB[db] = kept
		}
	}
	storesByDBMu.Unlock()

	for _, st := range live {
		if err := st.FlushLiveRunWrites(ctx); err != nil {
			return err
		}
	}
	return nil
}

// ActivityChanged returns a channel closed the next time any live-run
// activity update lands through this Store. Kept separate from Changed() so
// live-run heartbeats do not wake event-stream or state waiters when no
// integration event or task state changed.
func (s *Store) ActivityChanged() <-chan struct{} {
	s.activityChangedMu.Lock()
	defer s.activityChangedMu.Unlock()
	if s.activityChanged == nil {
		s.activityChanged = make(chan struct{})
	}
	return s.activityChanged
}

func (s *Store) signalActivityChanged() {
	s.activityChangedMu.Lock()
	defer s.activityChangedMu.Unlock()
	if s.activityChanged != nil {
		close(s.activityChanged)
		s.activityChanged = nil
	}
}

// ActivityVersion returns the process-local live-run activity version counter.
// Response caches keyed on ChangeToken can compare this monotonically
// increasing counter to refresh only live-run activity in memory without
// rebuilding whole-store task lists.
func (s *Store) ActivityVersion() uint64 {
	return s.activityVersion.Load()
}

// ActivityToken returns a process-local version token that advances whenever
// a live-run activity update lands through this Store.
func (s *Store) ActivityToken() string {
	return strconv.FormatUint(s.activityVersion.Load(), 10)
}

// TaskActivityVersion returns the live-run activity version for taskID
// (0 when no live-run activity has been recorded for taskID in this Store).
// Per-task caches can fold this into one task's key so heartbeats on other
// tasks never invalidate it.
func (s *Store) TaskActivityVersion(taskID string) uint64 {
	s.taskActivityMu.RLock()
	v := s.taskActivityVer[taskID]
	s.taskActivityMu.RUnlock()
	return v
}

func (s *Store) bumpActivityVersion(taskID string) {
	v := s.activityVersion.Add(1)
	if taskID != "" {
		s.taskActivityMu.Lock()
		if s.taskActivityVer == nil {
			s.taskActivityVer = make(map[string]uint64)
		}
		s.taskActivityVer[taskID] = v
		s.taskActivityMu.Unlock()
	}
	s.signalActivityChanged()
}

func (s *Store) clearTaskActivityVersion(taskID string) {
	if taskID == "" {
		return
	}
	s.taskActivityMu.Lock()
	delete(s.taskActivityVer, taskID)
	s.taskActivityMu.Unlock()
}

func (s *Store) liveRunFlushInterval() time.Duration {
	if s.liveFlushInterval > 0 {
		return s.liveFlushInterval
	}
	return defaultLiveRunFlushInterval
}

// SetLiveRunFlushIntervalForTest overrides how long coalesced live-run
// activity and transcript updates wait before flushing in tests.
func (s *Store) SetLiveRunFlushIntervalForTest(d time.Duration) {
	s.liveRunMu.Lock()
	s.liveFlushInterval = d
	s.liveRunMu.Unlock()
}

func (s *Store) noteLiveRunStarted(r Run) {
	if r.FinishedAt != nil {
		return
	}
	s.liveRunMu.Lock()
	if s.liveRunsByTask == nil {
		s.liveRunsByTask = make(map[string]*liveRunEntry)
	}
	if s.liveRunsByID == nil {
		s.liveRunsByID = make(map[string]*liveRunEntry)
	}
	entry := &liveRunEntry{
		runID:        r.ID,
		taskID:       r.TaskID,
		agentStarted: r.AgentStartedAt != nil,
	}
	s.liveRunsByTask[r.TaskID] = entry
	s.liveRunsByID[r.ID] = entry
	s.liveRunMu.Unlock()
}

func (s *Store) noteLiveRunAgentStarted(runID string) {
	var taskID string
	var hadActivity bool
	s.liveRunMu.Lock()
	if e := s.liveRunsByID[runID]; e != nil {
		e.agentStarted = true
		taskID = e.taskID
		hadActivity = e.hasActivity
	}
	s.liveRunMu.Unlock()
	if hadActivity {
		s.bumpActivityVersion(taskID)
	}
}

func (s *Store) snapshotPendingForFinish(runID string) pendingFinishSnapshot {
	s.liveRunMu.Lock()
	defer s.liveRunMu.Unlock()
	snap := pendingFinishSnapshot{runID: runID}
	if e := s.liveRunsByID[runID]; e != nil {
		snap.taskID = e.taskID
	}
	if pa := s.pendingActivities[runID]; pa != nil {
		cp := *pa
		if pa.firstTurn != nil {
			ft := *pa.firstTurn
			cp.firstTurn = &ft
		}
		snap.activity = &cp
		if snap.taskID == "" {
			snap.taskID = pa.taskID
		}
	}
	if tr := s.pendingTranscripts[runID]; tr != nil {
		cp := *tr
		snap.transcript = &cp
	}
	return snap
}

func applyPendingForFinishTx(ctx context.Context, tx *sql.Tx, snap pendingFinishSnapshot) error {
	if pa := snap.activity; pa != nil {
		var stamp any = pa.activityAt
		if pa.cleared {
			stamp = nil
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE `task_run` SET `activity` = ?, `activity_at` = ? "+
				"WHERE `id` = ? AND `finished_at` IS NULL",
			nullable(pa.activity), stamp, pa.runID); err != nil {
			return err
		}
		if pa.firstTurn != nil {
			if _, err := tx.ExecContext(ctx,
				"UPDATE `task_run_startup` SET `first_turn_at` = COALESCE(`first_turn_at`, ?) "+
					"WHERE `run_id` = ?",
				*pa.firstTurn, pa.runID); err != nil {
				return err
			}
		}
	}
	if tr := snap.transcript; tr != nil {
		if _, err := tx.ExecContext(ctx,
			"UPDATE `task_run` SET `transcript` = ? WHERE `id` = ?",
			nullable(tr.transcript), tr.runID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) noteLiveRunFinished(runID, taskID string, snap pendingFinishSnapshot) {
	s.liveRunMu.Lock()
	s.liveRunEpoch++
	if e := s.liveRunsByID[runID]; e != nil {
		if taskID == "" {
			taskID = e.taskID
		}
		delete(s.liveRunsByID, runID)
		if s.liveRunsByTask[e.taskID] == e {
			delete(s.liveRunsByTask, e.taskID)
		}
	} else if taskID != "" {
		if e := s.liveRunsByTask[taskID]; e != nil && e.runID == runID {
			delete(s.liveRunsByTask, taskID)
		}
	}
	delete(s.pendingActivities, runID)
	if snap.transcript != nil {
		if cur, ok := s.pendingTranscripts[runID]; ok && cur.seq == snap.transcript.seq {
			delete(s.pendingTranscripts, runID)
			s.recordFlushedTranscriptHashLocked(runID, snap.transcript.hash)
		}
	}
	s.liveRunMu.Unlock()
	s.clearTaskActivityVersion(taskID)
}

func (s *Store) recordFlushedTranscriptHashLocked(runID string, hash [32]byte) {
	if s.flushedTranscriptHash == nil {
		s.flushedTranscriptHash = make(map[string][32]byte)
	}
	if len(s.flushedTranscriptHash) >= maxFlushedTranscriptHashes {
		clear(s.flushedTranscriptHash)
	}
	s.flushedTranscriptHash[runID] = hash
}

func (s *Store) setTaskActivityCoalesced(ctx context.Context, taskID, activity string, at time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if r, ok := WriteRehearsalFrom(ctx); ok && r.Rehearsing() {
		return s.setTaskActivityDirect(ctx, taskID, activity, at)
	}
	atUTC := at.UTC()

	for {
		s.liveRunMu.Lock()
		entry := s.liveRunsByTask[taskID]
		epoch := s.liveRunEpoch
		if entry != nil {
			changed := s.recordPendingActivityLocked(entry, activity, atUTC)
			s.liveRunMu.Unlock()
			if changed {
				s.bumpActivityVersion(taskID)
			}
			return true, nil
		}
		s.liveRunMu.Unlock()

		var runID string
		var agentStarted, dbActAt sql.NullTime
		var dbAct sql.NullString
		err := s.db.QueryRowContext(ctx,
			"SELECT `id`,`agent_started_at`,`activity`,`activity_at` FROM `task_run` "+
				"WHERE `task_id` = ? AND `finished_at` IS NULL", taskID).
			Scan(&runID, &agentStarted, &dbAct, &dbActAt)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}

		s.liveRunMu.Lock()
		if s.liveRunEpoch != epoch {
			s.liveRunMu.Unlock()
			continue
		}
		entry = s.liveRunsByTask[taskID]
		if entry == nil {
			entry = &liveRunEntry{
				runID:              runID,
				taskID:             taskID,
				agentStarted:       agentStarted.Valid,
				hasActivity:        dbAct.Valid,
				activity:           dbAct.String,
				flushedHasActivity: dbAct.Valid,
				flushedActivity:    dbAct.String,
			}
			if dbActAt.Valid {
				entry.activityAt = dbActAt.Time.UTC()
				entry.flushedActivityAt = dbActAt.Time.UTC()
			}
			if s.liveRunsByTask == nil {
				s.liveRunsByTask = make(map[string]*liveRunEntry)
			}
			if s.liveRunsByID == nil {
				s.liveRunsByID = make(map[string]*liveRunEntry)
			}
			s.liveRunsByTask[taskID] = entry
			s.liveRunsByID[runID] = entry
		}
		changed := s.recordPendingActivityLocked(entry, activity, atUTC)
		s.liveRunMu.Unlock()
		if changed {
			s.bumpActivityVersion(taskID)
		}
		return true, nil
	}
}

func (s *Store) recordPendingActivityLocked(entry *liveRunEntry, activity string, atUTC time.Time) bool {
	cleared := activity == ""
	if cleared {
		if !entry.hasActivity {
			return false
		}
		entry.hasActivity = false
		entry.activity = ""
		entry.activityAt = time.Time{}
	} else {
		if entry.hasActivity && entry.activity == activity && entry.activityAt.Equal(atUTC) {
			return false
		}
		entry.hasActivity = true
		entry.activity = activity
		entry.activityAt = atUTC
		if entry.agentStarted && entry.firstTurnAt == nil && !entry.firstTurnFlushed {
			ft := atUTC
			entry.firstTurnAt = &ft
		}
	}

	matchesFlushed := !s.liveFlushing &&
		entry.hasActivity == entry.flushedHasActivity &&
		(!entry.hasActivity || (entry.activity == entry.flushedActivity && entry.activityAt.Equal(entry.flushedActivityAt))) &&
		(entry.firstTurnAt == nil || entry.firstTurnFlushed)

	if matchesFlushed {
		delete(s.pendingActivities, entry.runID)
		return true
	}

	s.liveBatchSeq++
	if s.pendingActivities == nil {
		s.pendingActivities = make(map[string]*pendingActivity)
	}
	pa := &pendingActivity{
		seq:        s.liveBatchSeq,
		runID:      entry.runID,
		taskID:     entry.taskID,
		cleared:    cleared,
		activity:   activity,
		activityAt: atUTC,
	}
	if entry.firstTurnAt != nil && !entry.firstTurnFlushed {
		ft := *entry.firstTurnAt
		pa.firstTurn = &ft
	}
	s.pendingActivities[entry.runID] = pa
	s.scheduleLiveFlushLocked()
	return true
}

func (s *Store) scheduleLiveFlushLocked() {
	if s.liveFlushing || s.liveFlushTimer != nil {
		return
	}
	s.liveFlushTimer = time.AfterFunc(s.liveRunFlushInterval(), s.onLiveFlushTimer)
}

func (s *Store) onLiveFlushTimer() {
	s.liveRunMu.Lock()
	s.liveFlushTimer = nil
	if s.liveFlushing || (len(s.pendingActivities) == 0 && len(s.pendingTranscripts) == 0) {
		s.liveRunMu.Unlock()
		return
	}
	s.liveFlushing = true
	s.liveFlushDone = make(chan struct{})
	s.liveRunMu.Unlock()
	s.flushPendingBatchOnce()
}

// setRunTranscriptCoalesced buffers live-run transcript updates in memory per
// run so streaming runs coalesce into one batched transaction per flush
// interval (and fold atomically into FinishRun). For an already-finished run
// (the final post-FinishRun write), it commits directly on changeConn (skipping
// unchanged values) so the completed transcript is immediately durable without
// moving ChangeToken or signalling Changed().
func (s *Store) setRunTranscriptCoalesced(ctx context.Context, runID, transcript string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r, ok := WriteRehearsalFrom(ctx); ok && r.Rehearsing() {
		return s.setRunTranscriptDirect(ctx, runID, transcript)
	}

	hash := sha256.Sum256([]byte(transcript))

	for {
		s.liveRunMu.Lock()
		epoch := s.liveRunEpoch
		if cur, pending := s.pendingTranscripts[runID]; pending {
			if cur.hash == hash {
				s.liveRunMu.Unlock()
				return nil
			}
		} else if prev, ok := s.flushedTranscriptHash[runID]; ok && prev == hash {
			s.liveRunMu.Unlock()
			return nil
		}

		if s.liveRunsByID[runID] != nil {
			s.recordPendingTranscriptLocked(runID, transcript, hash)
			s.liveRunMu.Unlock()
			return nil
		}
		s.liveRunMu.Unlock()

		// Check whether runID is a live run started outside this Store instance.
		var taskID string
		var agentStarted, finishedAt, dbActAt sql.NullTime
		var dbAct sql.NullString
		err := s.db.QueryRowContext(ctx,
			"SELECT `task_id`,`agent_started_at`,`finished_at`,`activity`,`activity_at` "+
				"FROM `task_run` WHERE `id` = ?", runID).
			Scan(&taskID, &agentStarted, &finishedAt, &dbAct, &dbActAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && !finishedAt.Valid {
			s.liveRunMu.Lock()
			if s.liveRunEpoch != epoch {
				s.liveRunMu.Unlock()
				continue
			}
			if s.liveRunsByID[runID] == nil {
				entry := &liveRunEntry{
					runID:              runID,
					taskID:             taskID,
					agentStarted:       agentStarted.Valid,
					hasActivity:        dbAct.Valid,
					activity:           dbAct.String,
					flushedHasActivity: dbAct.Valid,
					flushedActivity:    dbAct.String,
				}
				if dbActAt.Valid {
					entry.activityAt = dbActAt.Time.UTC()
					entry.flushedActivityAt = dbActAt.Time.UTC()
				}
				if s.liveRunsByTask == nil {
					s.liveRunsByTask = make(map[string]*liveRunEntry)
				}
				if s.liveRunsByID == nil {
					s.liveRunsByID = make(map[string]*liveRunEntry)
				}
				s.liveRunsByTask[taskID] = entry
				s.liveRunsByID[runID] = entry
			}
			s.recordPendingTranscriptLocked(runID, transcript, hash)
			s.liveRunMu.Unlock()
			return nil
		}

		// Finished (or unknown) run: commit immediately without moving
		// ChangeToken or waking Changed().
		if err := s.writeLiveRunBatch(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx,
				"UPDATE `task_run` SET `transcript` = ? WHERE `id` = ?",
				nullable(transcript), runID)
			return err
		}); err != nil {
			return err
		}
		s.liveRunMu.Lock()
		if cur, ok := s.pendingTranscripts[runID]; ok && cur.hash == hash {
			delete(s.pendingTranscripts, runID)
		}
		s.recordFlushedTranscriptHashLocked(runID, hash)
		s.liveRunMu.Unlock()
		return nil
	}
}

func (s *Store) recordPendingTranscriptLocked(runID, transcript string, hash [32]byte) {
	if !s.liveFlushing {
		if prev, ok := s.flushedTranscriptHash[runID]; ok && prev == hash {
			delete(s.pendingTranscripts, runID)
			return
		}
	}
	s.liveBatchSeq++
	if s.pendingTranscripts == nil {
		s.pendingTranscripts = make(map[string]*pendingTranscript)
	}
	s.pendingTranscripts[runID] = &pendingTranscript{
		seq:        s.liveBatchSeq,
		runID:      runID,
		transcript: transcript,
		hash:       hash,
	}
	s.scheduleLiveFlushLocked()
}

// FlushLiveRunWrites flushes any coalesced live-run activity or transcript
// writes currently buffered in memory to the database.
func (s *Store) FlushLiveRunWrites(ctx context.Context) error {
	for {
		s.liveRunMu.Lock()
		if s.liveFlushTimer != nil {
			s.liveFlushTimer.Stop()
			s.liveFlushTimer = nil
		}
		if s.liveFlushing {
			done := s.liveFlushDone
			s.liveRunMu.Unlock()
			select {
			case <-done:
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		if len(s.pendingActivities) == 0 && len(s.pendingTranscripts) == 0 {
			s.liveRunMu.Unlock()
			return nil
		}
		s.liveFlushing = true
		done := make(chan struct{})
		s.liveFlushDone = done
		s.liveRunMu.Unlock()

		err := s.flushPendingBatchOnce()
		if err != nil {
			return err
		}
	}
}

func (s *Store) flushPendingBatchOnce() error {
	defer func() {
		s.liveRunMu.Lock()
		s.liveFlushing = false
		done := s.liveFlushDone
		s.liveFlushDone = nil
		if len(s.pendingActivities) > 0 || len(s.pendingTranscripts) > 0 {
			s.scheduleLiveFlushLocked()
		}
		s.liveRunMu.Unlock()
		if done != nil {
			close(done)
		}
	}()

	s.liveRunMu.Lock()
	if len(s.pendingActivities) == 0 && len(s.pendingTranscripts) == 0 {
		s.liveRunMu.Unlock()
		return nil
	}
	acts := make([]pendingActivity, 0, len(s.pendingActivities))
	for _, a := range s.pendingActivities {
		acts = append(acts, *a)
	}
	trs := make([]pendingTranscript, 0, len(s.pendingTranscripts))
	for _, tr := range s.pendingTranscripts {
		trs = append(trs, *tr)
	}
	s.liveRunMu.Unlock()

	sort.Slice(acts, func(i, j int) bool { return acts[i].runID < acts[j].runID })
	sort.Slice(trs, func(i, j int) bool { return trs[i].runID < trs[j].runID })

	var deadRuns []string
	err := s.writeLiveRunBatch(context.Background(), func(tx *sql.Tx) error {
		deadRuns = deadRuns[:0]
		for _, a := range acts {
			var stamp any = a.activityAt
			if a.cleared {
				stamp = nil
			}
			res, err := tx.ExecContext(context.Background(),
				"UPDATE `task_run` SET `activity` = ?, `activity_at` = ? "+
					"WHERE `id` = ? AND `finished_at` IS NULL",
				nullable(a.activity), stamp, a.runID)
			if err != nil {
				return err
			}
			if n, rerr := res.RowsAffected(); rerr == nil && n == 0 {
				deadRuns = append(deadRuns, a.runID)
				continue
			}
			if a.firstTurn != nil {
				if _, err := tx.ExecContext(context.Background(),
					"UPDATE `task_run_startup` SET `first_turn_at` = COALESCE(`first_turn_at`, ?) "+
						"WHERE `run_id` = ?",
					*a.firstTurn, a.runID); err != nil {
					return err
				}
			}
		}
		for _, tr := range trs {
			if _, err := tx.ExecContext(context.Background(),
				"UPDATE `task_run` SET `transcript` = ? WHERE `id` = ?",
				nullable(tr.transcript), tr.runID); err != nil {
				return err
			}
		}
		return nil
	})

	s.liveRunMu.Lock()
	if err == nil {
		deadSet := make(map[string]bool, len(deadRuns))
		for _, id := range deadRuns {
			deadSet[id] = true
		}
		for _, a := range acts {
			if deadSet[a.runID] {
				if cur, ok := s.pendingActivities[a.runID]; ok && cur.seq == a.seq {
					delete(s.pendingActivities, a.runID)
				}
				if e := s.liveRunsByID[a.runID]; e != nil {
					delete(s.liveRunsByID, a.runID)
					if s.liveRunsByTask[e.taskID] == e {
						delete(s.liveRunsByTask, e.taskID)
					}
				}
				continue
			}
			if e := s.liveRunsByID[a.runID]; e != nil {
				e.flushedHasActivity = !a.cleared
				e.flushedActivity = a.activity
				e.flushedActivityAt = a.activityAt
				if a.firstTurn != nil {
					e.firstTurnFlushed = true
				}
			}
			if cur, ok := s.pendingActivities[a.runID]; ok && cur.seq == a.seq {
				delete(s.pendingActivities, a.runID)
			}
		}
		for _, tr := range trs {
			if cur, ok := s.pendingTranscripts[tr.runID]; ok && cur.seq == tr.seq {
				delete(s.pendingTranscripts, tr.runID)
			}
			s.recordFlushedTranscriptHashLocked(tr.runID, tr.hash)
		}
	} else {
		// Drop superseded-less failed items so a closed database cannot spin
		// the background timer indefinitely.
		for _, a := range acts {
			if cur, ok := s.pendingActivities[a.runID]; ok && cur.seq == a.seq {
				delete(s.pendingActivities, a.runID)
			}
		}
		for _, tr := range trs {
			if cur, ok := s.pendingTranscripts[tr.runID]; ok && cur.seq == tr.seq {
				delete(s.pendingTranscripts, tr.runID)
			}
		}
	}
	s.liveRunMu.Unlock()
	return err
}

// writeLiveRunBatch commits a coalesced batch of live-run activity and
// transcript updates. It intentionally bypasses Store.write's signalChanged()
// and, whenever ChangeToken has checked out changeConn, commits on changeConn
// itself so SQLite's PRAGMA data_version (and Postgres's per-connection
// data_version) does not advance for pure live-run flushes.
func (s *Store) writeLiveRunBatch(ctx context.Context, fn func(*sql.Tx) error) error {
	var lastErr error
	for attempt := 0; attempt < maxWriteAttempts; attempt++ {
		err := s.writeLiveRunBatchOnce(ctx, fn)
		if err == nil {
			return nil
		}
		if !isSerializationFailure(err) {
			return err
		}
		lastErr = err
	}
	return fmt.Errorf("%w after %d attempts (live-run batch): %v", ErrConflict, maxWriteAttempts, lastErr)
}

func (s *Store) writeLiveRunBatchOnce(ctx context.Context, fn func(*sql.Tx) error) error {
	s.changeMu.Lock()
	if s.changeConn != nil {
		defer s.changeMu.Unlock()
		tx, err := s.changeConn.BeginTx(ctx, nil)
		if err != nil {
			if !isSerializationFailure(err) {
				_ = s.changeConn.Close()
				s.changeConn = nil
			}
			return err
		}
		if err := fn(tx); err != nil {
			_ = tx.Rollback()
			if !isSerializationFailure(err) {
				_ = s.changeConn.Close()
				s.changeConn = nil
			}
			return err
		}
		if err := tx.Commit(); err != nil {
			if !isSerializationFailure(err) {
				_ = s.changeConn.Close()
				s.changeConn = nil
			}
			return err
		}
		return nil
	}
	s.changeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) pendingActivityForRun(runID string) (pendingActivity, bool) {
	s.liveRunMu.Lock()
	pa := s.pendingActivities[runID]
	var out pendingActivity
	var ok bool
	if pa != nil {
		out = *pa
		if pa.firstTurn != nil {
			ft := *pa.firstTurn
			out.firstTurn = &ft
		}
		ok = true
	}
	s.liveRunMu.Unlock()
	return out, ok
}

func (s *Store) pendingActivityAtForTask(taskID string) *time.Time {
	s.liveRunMu.Lock()
	defer s.liveRunMu.Unlock()
	e := s.liveRunsByTask[taskID]
	if e == nil {
		return nil
	}
	pa := s.pendingActivities[e.runID]
	if pa == nil || pa.cleared {
		return nil
	}
	t := pa.activityAt
	return &t
}

func (s *Store) pendingFirstTurnForRun(runID string) *time.Time {
	s.liveRunMu.Lock()
	defer s.liveRunMu.Unlock()
	pa := s.pendingActivities[runID]
	if pa == nil || pa.firstTurn == nil {
		return nil
	}
	ft := *pa.firstTurn
	return &ft
}

func (s *Store) pendingTranscriptForRun(runID string) (string, bool) {
	s.liveRunMu.Lock()
	tr := s.pendingTranscripts[runID]
	var out string
	var ok bool
	if tr != nil {
		out = tr.transcript
		ok = true
	}
	s.liveRunMu.Unlock()
	return out, ok
}

func (s *Store) overlayPendingTranscriptSizes(rows []RunTranscript) []RunTranscript {
	s.liveRunMu.Lock()
	if len(s.pendingTranscripts) == 0 {
		s.liveRunMu.Unlock()
		return rows
	}
	pending := make(map[string]int, len(s.pendingTranscripts))
	for id, tr := range s.pendingTranscripts {
		pending[id] = len(tr.transcript)
	}
	s.liveRunMu.Unlock()

	seen := make(map[string]bool, len(pending))
	out := rows[:0]
	for _, r := range rows {
		if n, ok := pending[r.RunID]; ok {
			seen[r.RunID] = true
			if n > 0 {
				r.Bytes = n
				out = append(out, r)
			}
			continue
		}
		out = append(out, r)
	}
	added := false
	for id, n := range pending {
		if !seen[id] && n > 0 {
			out = append(out, RunTranscript{RunID: id, Bytes: n})
			added = true
		}
	}
	if added {
		sort.Slice(out, func(i, j int) bool { return out[i].RunID < out[j].RunID })
	}
	return out
}

func (s *Store) overlayPendingRun(r *Run) {
	if r == nil || r.FinishedAt != nil {
		return
	}
	pa, ok := s.pendingActivityForRun(r.ID)
	if !ok {
		return
	}
	if pa.cleared {
		r.Activity = ""
		r.ActivityAt = nil
	} else {
		r.Activity = pa.activity
		t := pa.activityAt
		r.ActivityAt = &t
	}
	if pa.firstTurn != nil && r.FirstTurnAt == nil {
		ft := *pa.firstTurn
		r.FirstTurnAt = &ft
	}
}
