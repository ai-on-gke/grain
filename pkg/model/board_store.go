package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// NewBoardID allocates a board identity from its own sequence --
// NewSuiteID's own shape and its own reasoning: a board is created
// directly rather than cut from anything, and an id sequence per kind of
// thing keeps "board-3" meaning the third board.
func (s *Store) NewBoardID(ctx context.Context) (id string, err error) {
	err = s.write(ctx, "allocate a board id", func(tx *sql.Tx) error {
		id, err = newBoardID(ctx, tx)
		return err
	})
	return id, err
}

func newBoardID(ctx context.Context, tx *sql.Tx) (string, error) {
	res, err := tx.ExecContext(ctx,
		"INSERT INTO `board_sequence` (`issued_at`) VALUES (?)", time.Now().UTC())
	if err != nil {
		return "", fmt.Errorf("allocating a board id: %w", err)
	}
	n, err := res.LastInsertId()
	if err != nil {
		return "", fmt.Errorf("reading the allocated board id: %w", err)
	}
	return "board-" + strconv.FormatInt(n, 10), nil
}

const boardColumns = "`id`,`name`,`folder`,`kind`,`description`,`query`,`order_key`"

func scanBoard(scan func(...any) error) (Board, error) {
	var b Board
	var kind string
	if err := scan(&b.ID, &b.Name, &b.Folder, &kind, &b.Description, &b.Query, &b.OrderKey); err != nil {
		return Board{}, err
	}
	b.Kind = BoardKind(kind)
	return b, nil
}

// boardColumnStates is how a column's states are stored and read back:
// one comma-separated field, since the vocabulary is closed and the
// order within a column means nothing (schema.go's own note on the
// board_column table).
func boardColumnStates(states []State) string {
	parts := make([]string, len(states))
	for i, st := range states {
		parts[i] = string(st)
	}
	return strings.Join(parts, ",")
}

func parseBoardColumnStates(raw string) []State {
	var states []State
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		states = append(states, State(part))
	}
	return states
}

func boardColumnsOf(ctx context.Context, q querier, boardID string) ([]BoardColumn, error) {
	var out []BoardColumn
	err := each(ctx, q,
		"SELECT `name`,`states` FROM `board_column` WHERE `board_id` = ? ORDER BY `order_key`",
		boardID, func(rows *sql.Rows) error {
			var c BoardColumn
			var states string
			if err := rows.Scan(&c.Name, &states); err != nil {
				return err
			}
			c.States = parseBoardColumnStates(states)
			out = append(out, c)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading columns of board %s: %w", boardID, err)
	}
	return out, nil
}

// boardPanelsOf reads a dashboard's panels in display order -- the same
// shape boardColumnsOf has, for the other child table a board can carry
// (schema.go's own note on board_panel).
func boardPanelsOf(ctx context.Context, q querier, boardID string) ([]BoardPanel, error) {
	var out []BoardPanel
	err := each(ctx, q,
		"SELECT `kind`,`title`,`query`,`range`,`width`,`order_key` FROM `board_panel` WHERE `board_id` = ? ORDER BY `order_key`",
		boardID, func(rows *sql.Rows) error {
			var p BoardPanel
			var kind, width string
			if err := rows.Scan(&kind, &p.Title, &p.Query, &p.Range, &width, &p.OrderKey); err != nil {
				return err
			}
			p.Kind = PanelKind(kind)
			p.Width = PanelWidth(width)
			out = append(out, p)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading panels of board %s: %w", boardID, err)
	}
	return out, nil
}

// boardSectionsOf reads a report board's sections in print order -- the
// third child table a board can carry, read exactly as the other two are
// (schema.go's own note on board_section).
func boardSectionsOf(ctx context.Context, q querier, boardID string) ([]BoardSection, error) {
	var out []BoardSection
	err := each(ctx, q,
		"SELECT `kind`,`title`,`order_key` FROM `board_section` WHERE `board_id` = ? ORDER BY `order_key`",
		boardID, func(rows *sql.Rows) error {
			var s BoardSection
			var kind string
			if err := rows.Scan(&kind, &s.Title, &s.OrderKey); err != nil {
				return err
			}
			s.Kind = ReportSection(kind)
			out = append(out, s)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("reading sections of board %s: %w", boardID, err)
	}
	return out, nil
}

func getBoard(ctx context.Context, q querier, id string) (*Board, error) {
	board, err := scanBoard(q.QueryRowContext(ctx,
		"SELECT "+boardColumns+" FROM `board` WHERE `id` = ?", id).Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	cols, err := boardColumnsOf(ctx, q, id)
	if err != nil {
		return nil, err
	}
	board.Columns = cols
	panels, err := boardPanelsOf(ctx, q, id)
	if err != nil {
		return nil, err
	}
	board.Panels = panels
	sections, err := boardSectionsOf(ctx, q, id)
	if err != nil {
		return nil, err
	}
	board.Sections = sections
	return &board, nil
}

// GetBoard returns a board, or nil if there is none with that ID.
func (s *Store) GetBoard(ctx context.Context, id string) (*Board, error) {
	return getBoard(ctx, s.db, id)
}

// ListBoards returns every board in the order the nav rail shows them:
// by folder, then by each board's own place within it, then by id so
// that two boards nobody has ordered still come back the same way twice.
//
// The id is compared shortest first, then as text, because the ids this
// store issues are "board-" and a counter: compared as text alone,
// board-10 sorts above board-2, so a folder of boards nobody had dragged
// yet came back in neither the order they were filed nor any order a
// reader could name (2026-10-12 boards walkthrough). Shortest first is
// filing order for every id newBoardID issues, and still one fixed order
// for an id somebody wrote by hand.
func (s *Store) ListBoards(ctx context.Context) ([]Board, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+boardColumns+" FROM `board` ORDER BY `folder`, `order_key`, LENGTH(`id`), `id`")
	if err != nil {
		return nil, fmt.Errorf("listing boards: %w", err)
	}
	var out []Board
	for rows.Next() {
		row, err := scanBoard(rows.Scan)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for i := range out {
		cols, err := boardColumnsOf(ctx, s.db, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Columns = cols
		panels, err := boardPanelsOf(ctx, s.db, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Panels = panels
		sections, err := boardSectionsOf(ctx, s.db, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Sections = sections
	}
	return out, nil
}

// PutBoard inserts or replaces a board wholesale -- putSuite's own
// "child rows are deleted and re-inserted rather than diffed", for a
// board's own columns.
func (s *Store) PutBoard(ctx context.Context, board Board) error {
	return s.write(ctx, "put board "+board.ID,
		func(tx *sql.Tx) error { return putBoard(ctx, tx, board) })
}

func putBoard(ctx context.Context, tx *sql.Tx, board Board) error {
	_, err := tx.ExecContext(ctx,
		"REPLACE INTO `board` ("+boardColumns+") VALUES (?,?,?,?,?,?,?)",
		board.ID, board.Name, board.Folder, string(board.Kind),
		board.Description, board.Query, board.OrderKey)
	if err != nil {
		return fmt.Errorf("writing board %s: %w", board.ID, err)
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM `board_column` WHERE `board_id` = ?", board.ID); err != nil {
		return err
	}
	for i, c := range board.Columns {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `board_column` (`board_id`,`name`,`states`,`order_key`) VALUES (?,?,?,?)",
			board.ID, c.Name, boardColumnStates(c.States), float64(i)); err != nil {
			return fmt.Errorf("writing column %q of board %s: %w", c.Name, board.ID, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM `board_panel` WHERE `board_id` = ?", board.ID); err != nil {
		return err
	}
	panelsUnnumbered := true
	for _, p := range board.Panels {
		if p.OrderKey != 0 {
			panelsUnnumbered = false
			break
		}
	}
	for i, p := range board.Panels {
		// A wholly unnumbered set is numbered by position, the way columns
		// are. Otherwise every key is significant, including zero: a
		// dashboard hand-numbered 0, 10, 20 in the state repository or by
		// a reorder must survive a round trip unchanged.
		order := p.OrderKey
		if panelsUnnumbered {
			order = float64(i)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `board_panel` (`board_id`,`kind`,`title`,`query`,`range`,`width`,`order_key`) VALUES (?,?,?,?,?,?,?)",
			board.ID, string(p.Kind), p.Title, p.Query, p.Range, string(p.Width), order); err != nil {
			return fmt.Errorf("writing panel %d of board %s: %w", i+1, board.ID, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM `board_section` WHERE `board_id` = ?", board.ID); err != nil {
		return err
	}
	sectionsUnnumbered := true
	for _, s := range board.Sections {
		if s.OrderKey != 0 {
			sectionsUnnumbered = false
			break
		}
	}
	for i, s := range board.Sections {
		// A wholly unnumbered set is numbered by position; otherwise zero
		// is a real key, exactly as it is for a panel above.
		order := s.OrderKey
		if sectionsUnnumbered {
			order = float64(i)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `board_section` (`board_id`,`kind`,`title`,`order_key`) VALUES (?,?,?,?)",
			board.ID, string(s.Kind), s.Title, order); err != nil {
			return fmt.Errorf("writing section %d of board %s: %w", i+1, board.ID, err)
		}
	}
	return nil
}

// UpdateBoard reads a board, applies mutate, and writes it back --
// UpdateSuite's own read-modify-write shape.
func (s *Store) UpdateBoard(ctx context.Context, id string, mutate func(*Board) error) error {
	var missing bool
	err := s.write(ctx, "update board "+id, func(tx *sql.Tx) error {
		missing = false
		board, err := getBoard(ctx, tx, id)
		if err != nil {
			return err
		}
		if board == nil {
			missing = true
			return nil
		}
		if err := mutate(board); err != nil {
			return err
		}
		return putBoard(ctx, tx, *board)
	})
	if err != nil {
		return err
	}
	if missing {
		return fmt.Errorf("updating board %s: no such board", id)
	}
	return nil
}

// DeleteBoard removes a board, its columns, its panels and its report
// sections. A board is
// only ever a way of looking at tasks -- nothing refers to one and no
// run reads one -- so there is nothing here to keep and nothing to
// cascade beyond its own child rows, which carry no foreign key onto it
// (nothing in this schema does) and would otherwise be left behind for
// good.
func (s *Store) DeleteBoard(ctx context.Context, id string) error {
	return s.write(ctx, "delete board "+id, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM `board_column` WHERE `board_id` = ?", id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM `board_panel` WHERE `board_id` = ?", id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM `board_section` WHERE `board_id` = ?", id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM `board` WHERE `id` = ?", id)
		return err
	})
}
