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
	"database/sql"
	"sync"
)

// maxPreparedReads bounds how many distinct statements preparedReads
// keeps. Every query routed through it is a constant string, so the
// bound is never reached in practice; it is there so a query built from
// its arguments, routed through it by mistake, costs an unprepared read
// past the bound rather than a statement per call held for the life of
// the store.
const maxPreparedReads = 64

// preparedReads is the querier the store's hottest single-row reads go
// through (Store.reads): each query is prepared once and the statement
// reused, rather than handed to the database as text to be parsed and
// planned again on every call the way sql.DB.QueryContext does.
//
// That parsing was most of a dispatch cycle. dispatch.Cycle asks the
// same handful of questions -- the task row and what hangs off it, its
// failure streak, its grant wait, whether it is still ready -- of every
// ready candidate, every tick, and with a 10,000-task backlog SQLite
// spent 45% of the cycle's CPU in its parser (the task_ready view alone
// expands into a large statement), which put a tick within a few
// seconds of TestScalabilityLimits' 30s budget.
//
// Nothing about what a read returns changes: sql.Stmt prepares the
// statement on each pooled connection it runs on, a statement's rows are
// reset (not left open in a read transaction) when they are closed, and
// SQLite re-prepares a statement itself when the schema changes under
// it. Writes and transactions are untouched -- they go through Store.db
// and Store.write as before. A statement that cannot be prepared is
// read unprepared, so this can only ever be slower, never wrong.
type preparedReads struct {
	db    *sql.DB
	mu    sync.Mutex
	stmts map[string]*sql.Stmt
}

func newPreparedReads(db *sql.DB) *preparedReads {
	return &preparedReads{db: db, stmts: map[string]*sql.Stmt{}}
}

// stmt is query prepared, or nil when it could not be (or the cache is
// full), for the caller to run unprepared.
func (p *preparedReads) stmt(ctx context.Context, query string) *sql.Stmt {
	p.mu.Lock()
	defer p.mu.Unlock()
	if st, ok := p.stmts[query]; ok {
		return st
	}
	if p.stmts == nil || len(p.stmts) >= maxPreparedReads {
		return nil
	}
	st, err := p.db.PrepareContext(ctx, query)
	if err != nil {
		return nil
	}
	p.stmts[query] = st
	return st
}

func (p *preparedReads) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return p.db.ExecContext(ctx, query, args...)
}

func (p *preparedReads) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if st := p.stmt(ctx, query); st != nil {
		return st.QueryContext(ctx, args...)
	}
	return p.db.QueryContext(ctx, query, args...)
}

func (p *preparedReads) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if st := p.stmt(ctx, query); st != nil {
		return st.QueryRowContext(ctx, args...)
	}
	return p.db.QueryRowContext(ctx, query, args...)
}

// close closes every statement prepared so far; a later read prepares
// afresh.
func (p *preparedReads) close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var first error
	for query, st := range p.stmts {
		if err := st.Close(); err != nil && first == nil {
			first = err
		}
		delete(p.stmts, query)
	}
	return first
}

// reads is the querier for a hot single-row read: preparedReads when the
// store was made by New, s.db otherwise.
func (s *Store) reads() querier {
	if s.prepared == nil {
		return s.db
	}
	return s.prepared
}
