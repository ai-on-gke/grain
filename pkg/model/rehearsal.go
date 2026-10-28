package model

import (
	"context"
	"database/sql"
)

// WriteRehearsal turns the Store writes made under a context into
// rehearsals: each one runs inside its transaction as it always would,
// the rehearsal reads what it did, and the transaction is rolled back
// rather than committed.
//
// It exists for a deployment whose configuration lives in an external
// config repository (pkg/ui's checkSettingsWritable). Such a deployment
// refuses the write, and what the refusal owes its caller is the edit
// to commit there instead -- which is exactly the difference the write
// would have made to the exported configuration, and nothing but the
// write itself knows that difference. Rehearsing it is how it is
// computed from the same code path that would have made it, rather than
// from a second description of every route that could drift from it.
//
// This package knows nothing about the export (pkg/staterepo imports
// this one, not the other way round), so all it does is hand the
// transaction over.
type WriteRehearsal interface {
	// Rehearsing reports whether the next write should be rehearsed.
	// A rehearsal carried on a context but not armed leaves every
	// write alone.
	Rehearsing() bool
	// Rehearse runs write on tx and returns what the caller of the
	// Store method should see instead of success: an error, always,
	// since the write is not kept. tx is rolled back afterwards
	// whatever this returns.
	Rehearse(ctx context.Context, tx *sql.Tx, write func(*sql.Tx) error) error
}

type writeRehearsalKey struct{}

// WithWriteRehearsal returns ctx carrying r.
func WithWriteRehearsal(ctx context.Context, r WriteRehearsal) context.Context {
	return context.WithValue(ctx, writeRehearsalKey{}, r)
}

// WriteRehearsalFrom returns the rehearsal ctx carries, if any.
func WriteRehearsalFrom(ctx context.Context) (WriteRehearsal, bool) {
	r, ok := ctx.Value(writeRehearsalKey{}).(WriteRehearsal)
	return r, ok && r != nil
}

// rehearseOnce is writeOnce for an armed rehearsal: the same
// transaction, never committed.
func (s *Store) rehearseOnce(ctx context.Context, r WriteRehearsal, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return r.Rehearse(ctx, tx, fn)
}
