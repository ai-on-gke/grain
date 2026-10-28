package model

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"
)

var (
	ErrExecutorTokenNotFound = errors.New("model: executor token not found")
)

type ExecutorToken struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	TokenHash       string     `json:"-"` // the verifier never leaves the server
	Prefix          string     `json:"prefix"`
	CreatedAt       time.Time  `json:"created_at"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
	LastConnectedAt *time.Time `json:"last_connected_at,omitempty"`
}

func HashExecutorToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// GenerateExecutorToken creates a random raw token, computes its SHA-256 hash and prefix,
// and returns the raw token and the initialized ExecutorToken.
func GenerateExecutorToken(name string) (string, ExecutorToken, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", ExecutorToken{}, fmt.Errorf("generating executor token: %w", err)
	}
	rawToken := "gxt_" + hex.EncodeToString(b)
	prefix := rawToken[:8]
	return rawToken, ExecutorToken{
		Name:      name,
		TokenHash: HashExecutorToken(rawToken),
		Prefix:    prefix,
		CreatedAt: time.Now().UTC(),
	}, nil
}

func (s *Store) NewExecutorTokenID(ctx context.Context) (id string, err error) {
	err = s.write(ctx, "allocate an executor token id", func(tx *sql.Tx) error {
		id, err = newExecutorTokenID(ctx, tx)
		return err
	})
	return id, err
}

func newExecutorTokenID(ctx context.Context, tx *sql.Tx) (string, error) {
	res, err := tx.ExecContext(ctx,
		"INSERT INTO `executor_token_sequence` (`issued_at`) VALUES (?)", time.Now().UTC())
	if err != nil {
		return "", fmt.Errorf("allocating an executor token id: %w", err)
	}
	n, err := res.LastInsertId()
	if err != nil {
		return "", fmt.Errorf("reading the allocated executor token id: %w", err)
	}
	return "exec-tok-" + strconv.FormatInt(n, 10), nil
}

const executorTokenColumns = "`id`,`name`,`token_hash`,`prefix`,`created_at`,`revoked_at`,`last_connected_at`"

func scanExecutorToken(scan func(...any) error) (ExecutorToken, error) {
	var tok ExecutorToken
	var revokedAt, lastConnectedAt sql.NullTime
	if err := scan(&tok.ID, &tok.Name, &tok.TokenHash, &tok.Prefix, &tok.CreatedAt, &revokedAt, &lastConnectedAt); err != nil {
		return ExecutorToken{}, err
	}
	if revokedAt.Valid {
		t := revokedAt.Time.UTC()
		tok.RevokedAt = &t
	}
	if lastConnectedAt.Valid {
		t := lastConnectedAt.Time.UTC()
		tok.LastConnectedAt = &t
	}
	return tok, nil
}

// CreateExecutorToken persists a new executor token record.
func (s *Store) CreateExecutorToken(ctx context.Context, tok ExecutorToken) (*ExecutorToken, error) {
	if tok.Name == "" {
		return nil, errors.New("model: executor token name is required")
	}
	if tok.CreatedAt.IsZero() {
		tok.CreatedAt = time.Now().UTC()
	}
	var created ExecutorToken
	err := s.write(ctx, "create executor token", func(tx *sql.Tx) error {
		if tok.ID == "" {
			var err error
			tok.ID, err = newExecutorTokenID(ctx, tx)
			if err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx,
			"INSERT INTO `executor_token` ("+executorTokenColumns+") VALUES (?, ?, ?, ?, ?, ?, ?)",
			tok.ID, tok.Name, tok.TokenHash, tok.Prefix, tok.CreatedAt, tok.RevokedAt, tok.LastConnectedAt)
		if err != nil {
			return fmt.Errorf("inserting executor token: %w", err)
		}
		created = tok
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &created, nil
}

// ListExecutorTokens returns all executor tokens ordered by created_at DESC.
func (s *Store) ListExecutorTokens(ctx context.Context) ([]ExecutorToken, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+executorTokenColumns+" FROM `executor_token` ORDER BY `created_at` DESC")
	if err != nil {
		return nil, fmt.Errorf("listing executor tokens: %w", err)
	}
	defer rows.Close()
	var tokens []ExecutorToken
	for rows.Next() {
		tok, err := scanExecutorToken(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scanning executor token: %w", err)
		}
		tokens = append(tokens, tok)
	}
	return tokens, rows.Err()
}

// GetExecutorToken retrieves an executor token by ID.
func (s *Store) GetExecutorToken(ctx context.Context, id string) (*ExecutorToken, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+executorTokenColumns+" FROM `executor_token` WHERE `id` = ?", id)
	tok, err := scanExecutorToken(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting executor token %q: %w", id, err)
	}
	return &tok, nil
}

// GetExecutorTokenByHash retrieves an executor token by its token hash.
func (s *Store) GetExecutorTokenByHash(ctx context.Context, tokenHash string) (*ExecutorToken, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+executorTokenColumns+" FROM `executor_token` WHERE `token_hash` = ?", tokenHash)
	tok, err := scanExecutorToken(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting executor token by hash: %w", err)
	}
	return &tok, nil
}

// RevokeExecutorToken revokes an executor token by setting its revoked_at timestamp.
func (s *Store) RevokeExecutorToken(ctx context.Context, id string) (*ExecutorToken, error) {
	now := time.Now().UTC()
	var updated *ExecutorToken
	err := s.write(ctx, "revoke executor token", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"UPDATE `executor_token` SET `revoked_at` = ? WHERE `id` = ? AND `revoked_at` IS NULL", now, id)
		if err != nil {
			return fmt.Errorf("revoking executor token: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("reading revoked rows affected: %w", err)
		}
		row := tx.QueryRowContext(ctx,
			"SELECT "+executorTokenColumns+" FROM `executor_token` WHERE `id` = ?", id)
		tok, err := scanExecutorToken(row.Scan)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrExecutorTokenNotFound
		}
		if err != nil {
			return fmt.Errorf("reading revoked executor token: %w", err)
		}
		if n == 0 && tok.RevokedAt == nil {
			return ErrExecutorTokenNotFound
		}
		updated = &tok
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// TouchExecutorTokenConnected updates the last_connected_at timestamp for an executor token.
func (s *Store) TouchExecutorTokenConnected(ctx context.Context, id string, now time.Time) error {
	return s.write(ctx, "touch executor token connected", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `executor_token` SET `last_connected_at` = ? WHERE `id` = ?", now, id)
		if err != nil {
			return fmt.Errorf("updating last connected: %w", err)
		}
		return nil
	})
}
