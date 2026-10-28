package grit

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/attest"
	"github.com/ai-on-gke/grain/pkg/gritapi"
	_ "modernc.org/sqlite"
)

// Store is grit's durable SQLite state store for clients, trees, runs,
// append-only events, job logs, artifacts, and signed attestations.
type Store struct {
	mu      sync.Mutex
	db      *sql.DB
	treeDir string
	epoch   string
	notify  chan struct{}
}

// OpenStore opens or initializes a grit SQLite store inside dataDir.
func OpenStore(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("grit store mkdir: %w", err)
	}
	treeDir := filepath.Join(dataDir, "trees")
	if err := os.MkdirAll(treeDir, 0o755); err != nil {
		return nil, fmt.Errorf("grit tree store mkdir: %w", err)
	}
	dbPath := filepath.Join(dataDir, "grit.db")
	dsn := dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("grit open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	s := &Store{
		db:      db,
		treeDir: treeDir,
		notify:  make(chan struct{}),
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying SQLite database.
func (s *Store) Close() error {
	return s.db.Close()
}

// Epoch returns this store's durable epoch identifier.
func (s *Store) Epoch() string {
	return s.epoch
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS grit_meta (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS grit_clients (
			id              TEXT PRIMARY KEY,
			secret_sha256   TEXT NOT NULL,
			max_jobs        INTEGER NOT NULL DEFAULT 0,
			runs_per_min    INTEGER NOT NULL DEFAULT 0,
			upload_per_min  INTEGER NOT NULL DEFAULT 0,
			revoked         INTEGER NOT NULL DEFAULT 0,
			created_at       TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS grit_client_scopes (
			client_id TEXT NOT NULL,
			scope     TEXT NOT NULL,
			repo      TEXT NOT NULL,
			PRIMARY KEY (client_id, scope, repo)
		)`,
		`CREATE TABLE IF NOT EXISTS grit_trees (
			sha256     TEXT PRIMARY KEY,
			size_bytes INTEGER NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS grit_runs (
			id                 TEXT PRIMARY KEY,
			request_id         TEXT NOT NULL UNIQUE,
			repo               TEXT NOT NULL,
			ref                TEXT NOT NULL,
			commit_sha         TEXT NOT NULL,
			git_tree           TEXT NOT NULL,
			tree_sha256        TEXT NOT NULL,
			event              TEXT NOT NULL,
			trusted_release    INTEGER NOT NULL DEFAULT 0,
			status             TEXT NOT NULL,
			conclusion         TEXT NOT NULL DEFAULT '',
			jobs_json          TEXT NOT NULL DEFAULT '[]',
			attestation_sha256 TEXT NOT NULL DEFAULT '',
			attestation_json   TEXT NOT NULL DEFAULT '',
			created_at         TEXT NOT NULL,
			started_at         TEXT NOT NULL DEFAULT '',
			completed_at       TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS grit_events (
			seq        INTEGER PRIMARY KEY AUTOINCREMENT,
			at         TEXT NOT NULL,
			run_id     TEXT NOT NULL,
			repo       TEXT NOT NULL,
			kind       TEXT NOT NULL,
			event_json TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS grit_logs (
			run_id   TEXT NOT NULL,
			job_id   TEXT NOT NULL,
			data     BLOB NOT NULL,
			complete INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (run_id, job_id)
		)`,
		`CREATE TABLE IF NOT EXISTS grit_artifacts (
			run_id TEXT NOT NULL,
			job_id TEXT NOT NULL,
			name   TEXT NOT NULL,
			data   BLOB NOT NULL,
			PRIMARY KEY (run_id, job_id, name)
		)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("grit migrate: %w", err)
		}
	}

	var epoch string
	err := s.db.QueryRow(`SELECT value FROM grit_meta WHERE key = 'epoch'`).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		var raw [6]byte
		_, _ = rand.Read(raw[:])
		epoch = "e-" + hex.EncodeToString(raw[:])
		if _, err := s.db.Exec(`INSERT INTO grit_meta (key, value) VALUES ('epoch', ?)`, epoch); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	s.epoch = epoch

	var oldest string
	if err := s.db.QueryRow(`SELECT value FROM grit_meta WHERE key = 'oldest_seq'`).Scan(&oldest); errors.Is(err, sql.ErrNoRows) {
		if _, err := s.db.Exec(`INSERT INTO grit_meta (key, value) VALUES ('oldest_seq', '1')`); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) broadcast() {
	s.mu.Lock()
	ch := s.notify
	s.notify = make(chan struct{})
	close(ch)
	s.mu.Unlock()
}

// WaitChannel returns a channel closed the next time an event is appended.
func (s *Store) WaitChannel() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notify
}

// CreateOrUpdateClient creates or replaces a client's secret hash and scopes.
func (s *Store) CreateOrUpdateClient(rec ClientRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	rev := 0
	if rec.Revoked {
		rev = 1
	}
	_, err = tx.Exec(`
		INSERT INTO grit_clients (id, secret_sha256, max_jobs, runs_per_min, upload_per_min, revoked, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			secret_sha256 = excluded.secret_sha256,
			max_jobs = excluded.max_jobs,
			runs_per_min = excluded.runs_per_min,
			upload_per_min = excluded.upload_per_min,
			revoked = excluded.revoked`,
		rec.ID, rec.SecretSHA256, rec.MaxConcurrentJobs, rec.RunsPerMinute, rec.TreeUploadBytesPerMinute, rev, rec.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM grit_client_scopes WHERE client_id = ?`, rec.ID); err != nil {
		return err
	}
	for _, sc := range rec.Scopes {
		if _, err := tx.Exec(`INSERT INTO grit_client_scopes (client_id, scope, repo) VALUES (?, ?, ?)`, rec.ID, string(sc.Scope), sc.Repo); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetClientScopes replaces the scope grants of an existing client without
// rotating its secret.
func (s *Store) SetClientScopes(clientID string, scopes []ClientScope) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM grit_clients WHERE id = ?`, clientID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("grit: client %q not found", clientID)
		}
		return err
	}
	if _, err := tx.Exec(`DELETE FROM grit_client_scopes WHERE client_id = ?`, clientID); err != nil {
		return err
	}
	for _, sc := range scopes {
		if _, err := tx.Exec(`INSERT INTO grit_client_scopes (client_id, scope, repo) VALUES (?, ?, ?)`, clientID, string(sc.Scope), sc.Repo); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RevokeClient marks clientID revoked and removes its scope grants.
func (s *Store) RevokeClient(clientID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.Exec(`UPDATE grit_clients SET revoked = 1 WHERE id = ?`, clientID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("grit: client %q not found", clientID)
	}
	_, err = s.db.Exec(`DELETE FROM grit_client_scopes WHERE client_id = ?`, clientID)
	return err
}

// GetClient looks up a client by ID.
func (s *Store) GetClient(clientID string) (ClientRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var rec ClientRecord
	var rev int
	var createdStr string
	err := s.db.QueryRow(`
		SELECT id, secret_sha256, max_jobs, runs_per_min, upload_per_min, revoked, created_at
		FROM grit_clients WHERE id = ?`, clientID).Scan(
		&rec.ID, &rec.SecretSHA256, &rec.MaxConcurrentJobs, &rec.RunsPerMinute, &rec.TreeUploadBytesPerMinute, &rev, &createdStr,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ClientRecord{}, false, nil
	}
	if err != nil {
		return ClientRecord{}, false, err
	}
	rec.Revoked = rev != 0
	rec.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdStr)

	rows, err := s.db.Query(`SELECT scope, repo FROM grit_client_scopes WHERE client_id = ? ORDER BY scope, repo`, clientID)
	if err != nil {
		return ClientRecord{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var scStr, repo string
		if err := rows.Scan(&scStr, &repo); err != nil {
			return ClientRecord{}, false, err
		}
		rec.Scopes = append(rec.Scopes, ClientScope{Scope: gritapi.Scope(scStr), Repo: repo})
	}
	return rec, true, rows.Err()
}

// ListClients returns all stored client records ordered by ID.
func (s *Store) ListClients() ([]ClientRecord, error) {
	s.mu.Lock()
	rows, err := s.db.Query(`SELECT id FROM grit_clients ORDER BY id`)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			s.mu.Unlock()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	s.mu.Unlock()

	var out []ClientRecord
	for _, id := range ids {
		rec, ok, err := s.GetClient(id)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, rec)
		}
	}
	return out, nil
}

// HasTree reports whether the canonical ustar archive with digest sha256Hex is stored.
func (s *Store) HasTree(sha256Hex string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var one int
	err := s.db.QueryRow(`SELECT 1 FROM grit_trees WHERE sha256 = ?`, sha256Hex).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, statErr := os.Stat(s.treePath(sha256Hex))
	return statErr == nil, nil
}

func (s *Store) treePath(sha256Hex string) string {
	return filepath.Join(s.treeDir, sha256Hex+".tar")
}

// PutTree writes the canonical ustar archive for sha256Hex to disk and records it.
func (s *Store) PutTree(sha256Hex string, ustarArchive []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.treePath(sha256Hex)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, ustarArchive, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_, err := s.db.Exec(`
		INSERT INTO grit_trees (sha256, size_bytes, created_at)
		VALUES (?, ?, ?)
		ON CONFLICT(sha256) DO NOTHING`,
		sha256Hex, len(ustarArchive), time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

// ReadTree returns the canonical ustar archive bytes for sha256Hex.
func (s *Store) ReadTree(sha256Hex string) ([]byte, error) {
	return os.ReadFile(s.treePath(sha256Hex))
}

// EvictTree deletes a tree from the store (used in tests for 428 eviction recovery).
func (s *Store) EvictTree(sha256Hex string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_ = os.Remove(s.treePath(sha256Hex))
	_, err := s.db.Exec(`DELETE FROM grit_trees WHERE sha256 = ?`, sha256Hex)
	return err
}

// SaveRun inserts or updates a run status record. If request_id already exists
// under a different run ID, it returns the existing RunStatus and created=false.
func (s *Store) CreateOrGetRun(run gritapi.RunStatus) (gritapi.RunStatus, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok, err := s.getRunByKeyLocked("request_id", run.RequestID)
	if err != nil {
		return gritapi.RunStatus{}, false, err
	}
	if ok {
		return existing, false, nil
	}

	jobsJSON, err := json.Marshal(run.Jobs)
	if err != nil {
		return gritapi.RunStatus{}, false, err
	}
	trusted := 0
	if run.TrustedRelease {
		trusted = 1
	}
	var startedStr, completedStr string
	if run.StartedAt != nil {
		startedStr = run.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	if run.CompletedAt != nil {
		completedStr = run.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err = s.db.Exec(`
		INSERT INTO grit_runs (
			id, request_id, repo, ref, commit_sha, git_tree, tree_sha256,
			event, trusted_release, status, conclusion, jobs_json,
			attestation_sha256, created_at, started_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.RequestID, run.Repo, run.Ref, run.Commit, run.Tree.GitTree, run.Tree.SHA256,
		run.Event, trusted, run.Status, run.Conclusion, string(jobsJSON),
		run.AttestationSHA256, run.CreatedAt.UTC().Format(time.RFC3339Nano), startedStr, completedStr,
	)
	if err != nil {
		return gritapi.RunStatus{}, false, err
	}
	return run, true, nil
}

// UpdateRun updates the mutable fields of an existing run and optionally stores
// its signed DSSE attestation.
func (s *Store) UpdateRun(run gritapi.RunStatus, att *attest.Envelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobsJSON, err := json.Marshal(run.Jobs)
	if err != nil {
		return err
	}
	var startedStr, completedStr, attJSONStr string
	if run.StartedAt != nil {
		startedStr = run.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	if run.CompletedAt != nil {
		completedStr = run.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	if att != nil {
		b, err := json.Marshal(att)
		if err != nil {
			return err
		}
		attJSONStr = string(b)
	}
	if attJSONStr != "" {
		_, err = s.db.Exec(`
			UPDATE grit_runs SET
				status = ?, conclusion = ?, jobs_json = ?,
				attestation_sha256 = ?, attestation_json = ?,
				started_at = ?, completed_at = ?
			WHERE id = ?`,
			run.Status, run.Conclusion, string(jobsJSON),
			run.AttestationSHA256, attJSONStr,
			startedStr, completedStr, run.ID,
		)
	} else {
		_, err = s.db.Exec(`
			UPDATE grit_runs SET
				status = ?, conclusion = ?, jobs_json = ?,
				attestation_sha256 = ?,
				started_at = ?, completed_at = ?
			WHERE id = ?`,
			run.Status, run.Conclusion, string(jobsJSON),
			run.AttestationSHA256,
			startedStr, completedStr, run.ID,
		)
	}
	return err
}

// GetRun returns a RunStatus by run ID.
func (s *Store) GetRun(id string) (gritapi.RunStatus, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getRunByKeyLocked("id", id)
}

func (s *Store) getRunByKeyLocked(col, val string) (gritapi.RunStatus, bool, error) {
	var run gritapi.RunStatus
	var trusted int
	var jobsJSON, createdStr, startedStr, completedStr string
	q := fmt.Sprintf(`
		SELECT id, request_id, repo, ref, commit_sha, git_tree, tree_sha256,
		       event, trusted_release, status, conclusion, jobs_json,
		       attestation_sha256, created_at, started_at, completed_at
		FROM grit_runs WHERE %s = ?`, col)
	err := s.db.QueryRow(q, val).Scan(
		&run.ID, &run.RequestID, &run.Repo, &run.Ref, &run.Commit, &run.Tree.GitTree, &run.Tree.SHA256,
		&run.Event, &trusted, &run.Status, &run.Conclusion, &jobsJSON,
		&run.AttestationSHA256, &createdStr, &startedStr, &completedStr,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return gritapi.RunStatus{}, false, nil
	}
	if err != nil {
		return gritapi.RunStatus{}, false, err
	}
	run.TrustedRelease = trusted != 0
	_ = json.Unmarshal([]byte(jobsJSON), &run.Jobs)
	run.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdStr)
	if startedStr != "" {
		if t, pErr := time.Parse(time.RFC3339Nano, startedStr); pErr == nil {
			run.StartedAt = &t
		}
	}
	if completedStr != "" {
		if t, pErr := time.Parse(time.RFC3339Nano, completedStr); pErr == nil {
			run.CompletedAt = &t
		}
	}
	return run, true, nil
}

// GetAttestation returns the signed DSSE envelope for run ID.
func (s *Store) GetAttestation(id string) (*attest.Envelope, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var attJSON string
	err := s.db.QueryRow(`SELECT attestation_json FROM grit_runs WHERE id = ?`, id).Scan(&attJSON)
	if errors.Is(err, sql.ErrNoRows) || attJSON == "" {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var env attest.Envelope
	if err := json.Unmarshal([]byte(attJSON), &env); err != nil {
		return nil, false, err
	}
	return &env, true, nil
}

// WriteLog stores or updates the log bytes for (runID, jobID).
func (s *Store) WriteLog(runID, jobID string, data []byte, complete bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	comp := 0
	if complete {
		comp = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO grit_logs (run_id, job_id, data, complete)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(run_id, job_id) DO UPDATE SET
			data = excluded.data,
			complete = excluded.complete`,
		runID, jobID, data, comp,
	)
	return err
}

// ReadLog returns a slice of the stored log for (runID, jobID).
func (s *Store) ReadLog(runID, jobID string, offset, limit int64) ([]byte, int64, bool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var full []byte
	var comp int
	err := s.db.QueryRow(`SELECT data, complete FROM grit_logs WHERE run_id = ? AND job_id = ?`, runID, jobID).Scan(&full, &comp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, false, false, nil
	}
	if err != nil {
		return nil, 0, false, false, err
	}
	total := int64(len(full))
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := total
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return full[offset:end], end, comp != 0, true, nil
}

// WriteArtifact stores a tar artifact bundle for (runID, jobID, name).
func (s *Store) WriteArtifact(runID, jobID, name string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO grit_artifacts (run_id, job_id, name, data)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(run_id, job_id, name) DO UPDATE SET data = excluded.data`,
		runID, jobID, name, data,
	)
	return err
}

// ReadArtifact reads a stored artifact bundle.
func (s *Store) ReadArtifact(runID, jobID, name string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var data []byte
	err := s.db.QueryRow(`SELECT data FROM grit_artifacts WHERE run_id = ? AND job_id = ? AND name = ?`, runID, jobID, name).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// AppendEvent appends a transition event to `grit_events`, sets its monotonic
// `Seq`, and wakes waiting `/v1/events` subscribers.
func (s *Store) AppendEvent(ev gritapi.Event) (gritapi.Event, error) {
	s.mu.Lock()
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	res, err := s.db.Exec(`
		INSERT INTO grit_events (at, run_id, repo, kind, event_json)
		VALUES (?, ?, ?, ?, '{}')`,
		ev.At.Format(time.RFC3339Nano), ev.RunID, ev.Repo, string(ev.Kind),
	)
	if err != nil {
		s.mu.Unlock()
		return gritapi.Event{}, err
	}
	seq, err := res.LastInsertId()
	if err != nil {
		s.mu.Unlock()
		return gritapi.Event{}, err
	}
	ev.Seq = seq
	b, err := json.Marshal(ev)
	if err != nil {
		s.mu.Unlock()
		return gritapi.Event{}, err
	}
	if _, err := s.db.Exec(`UPDATE grit_events SET event_json = ? WHERE seq = ?`, string(b), seq); err != nil {
		s.mu.Unlock()
		return gritapi.Event{}, err
	}
	s.mu.Unlock()
	s.broadcast()
	return ev, nil
}

// PruneEventsUpTo deletes events with `seq <= upToSeq` and advances `oldest_seq`
// to `upToSeq + 1` so clients requesting a pruned cursor receive HTTP 410.
func (s *Store) PruneEventsUpTo(upToSeq int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.db.Exec(`DELETE FROM grit_events WHERE seq <= ?`, upToSeq); err != nil {
		return err
	}
	oldest := upToSeq + 1
	_, err := s.db.Exec(`UPDATE grit_meta SET value = ? WHERE key = 'oldest_seq'`, fmt.Sprintf("%d", oldest))
	return err
}

// OldestSeq returns the lowest sequence number still retained in the event log.
func (s *Store) OldestSeq() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.oldestSeqLocked()
}

func (s *Store) oldestSeqLocked() (int64, error) {
	var val string
	if err := s.db.QueryRow(`SELECT value FROM grit_meta WHERE key = 'oldest_seq'`).Scan(&val); err != nil {
		return 1, err
	}
	var n int64
	_, _ = fmt.Sscanf(val, "%d", &n)
	if n < 1 {
		n = 1
	}
	return n, nil
}

// EventsAfter returns up to limit events with `seq > after` for which allowRepo
// returns true. If `after > 0` and `after + 1 < oldestSeq`, it returns a 410
// `cursor_gone` *gritapi.APIError.
func (s *Store) EventsAfter(after int64, limit int, allowRepo func(repo string) bool) (gritapi.EventBatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	oldest, err := s.oldestSeqLocked()
	if err != nil {
		return gritapi.EventBatch{}, err
	}
	if after > 0 && after+1 < oldest {
		return gritapi.EventBatch{}, &gritapi.APIError{
			StatusCode: 410,
			Code:       "cursor_gone",
			Epoch:      s.epoch,
			OldestSeq:  oldest,
			Message:    fmt.Sprintf("cursor %d precedes oldest retained event %d", after, oldest),
		}
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}

	var maxSeq sql.NullInt64
	_ = s.db.QueryRow(`SELECT MAX(seq) FROM grit_events`).Scan(&maxSeq)
	headSeq := int64(0)
	if maxSeq.Valid {
		headSeq = maxSeq.Int64
	}

	rows, err := s.db.Query(`SELECT seq, repo, event_json FROM grit_events WHERE seq > ? ORDER BY seq ASC`, after)
	if err != nil {
		return gritapi.EventBatch{}, err
	}
	defer rows.Close()

	var events []gritapi.Event
	lastScanned := after
	for rows.Next() {
		var seq int64
		var repo, evJSON string
		if err := rows.Scan(&seq, &repo, &evJSON); err != nil {
			return gritapi.EventBatch{}, err
		}
		lastScanned = seq
		if allowRepo != nil && !allowRepo(repo) {
			continue
		}
		var ev gritapi.Event
		if err := json.Unmarshal([]byte(evJSON), &ev); err != nil {
			return gritapi.EventBatch{}, err
		}
		events = append(events, ev)
		if len(events) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return gritapi.EventBatch{}, err
	}

	nextSeq := lastScanned + 1
	if len(events) == 0 && headSeq > after {
		nextSeq = headSeq + 1
	} else if len(events) == 0 {
		nextSeq = after + 1
		if headSeq >= nextSeq {
			nextSeq = headSeq + 1
		}
	}
	return gritapi.EventBatch{
		Epoch:   s.epoch,
		Events:  events,
		NextSeq: nextSeq,
	}, nil
}

// IncompleteRuns returns all runs whose status is `queued` or `running`.
func (s *Store) IncompleteRuns() ([]gritapi.RunStatus, error) {
	s.mu.Lock()
	rows, err := s.db.Query(`SELECT id FROM grit_runs WHERE status IN ('queued', 'running') ORDER BY created_at ASC`)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			s.mu.Unlock()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	s.mu.Unlock()

	var out []gritapi.RunStatus
	for _, id := range ids {
		r, ok, err := s.GetRun(id)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, r)
		}
	}
	return out, nil
}
