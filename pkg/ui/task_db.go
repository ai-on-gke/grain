package ui

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/bwsalmon/grain/pkg/apierror"
	"github.com/bwsalmon/grain/pkg/capability/dbaccess"
	"github.com/bwsalmon/grain/pkg/model"
)

// TaskDBQueryRequest is the request body for POST /api/tasks/{id}/db/query.
type TaskDBQueryRequest struct {
	Query  string `json:"query"`
	Params []any  `json:"params,omitempty"`
}

// TaskDBQueryResponse is the response body for POST /api/tasks/{id}/db/query.
type TaskDBQueryResponse struct {
	Columns []string         `json:"columns"`
	Rows    []map[string]any `json:"rows"`
	Count   int              `json:"count"`
	// Truncated is set when the query had more than MaxTaskDBQueryRows
	// rows and only the first MaxTaskDBQueryRows came back.
	Truncated bool `json:"truncated,omitempty"`
}

// MaxTaskDBQueryRows is the most rows one POST /api/tasks/{id}/db/query
// answers with. The whole result is held in memory and written as one
// JSON body, so a SELECT * over a large table would otherwise be as big
// as the table; a caller that wants more pages with LIMIT/OFFSET.
const MaxTaskDBQueryRows = 1000

// TaskDBExecRequest is the request body for POST /api/tasks/{id}/db/execute.
type TaskDBExecRequest struct {
	Statement string `json:"statement,omitempty"`
	Query     string `json:"query,omitempty"`
	Params    []any  `json:"params,omitempty"`
}

// TaskDBExecResponse is the response body for POST /api/tasks/{id}/db/execute.
type TaskDBExecResponse struct {
	RowsAffected int64 `json:"rowsAffected"`
	LastInsertID int64 `json:"lastInsertID,omitempty"`
}

func taskHasGrant(task *model.Task, capability string) bool {
	if task == nil {
		return false
	}
	for _, g := range task.Grants {
		if g.Capability == capability {
			return true
		}
	}
	return false
}

// authorizeTaskDB answers whether r's caller may use task's db-read or
// db-write grant, writing the error response itself when not.
//
// The grant is the task's run's, not the task row's, so besides the
// grant itself the task has to have a live run: running, or parked on a
// chat turn (awaiting_reply) that the run is still polling. A grant
// stays on the row after the work is done, and a closed task's grant
// working for ever is not what granting it meant. The caller is one
// ServeHTTP's access gate already trusts as this daemon itself (an
// agent's tool hop, cmd/grain's daemonDB).
func (s *Server) authorizeTaskDB(w http.ResponseWriter, r *http.Request, task *model.Task) bool {
	ctx := r.Context()
	state, err := s.tasks.Store.State(ctx, task.ID)
	if err != nil {
		writeClientError(w, err)
		return false
	}
	switch state {
	case model.StateRunning, model.StateAwaitingReply:
		return true
	}
	writeError(w, http.StatusForbidden, fmt.Errorf(
		"task %q is %s: its database access is only for a live run", task.ID, state))
	return false
}

func (s *Server) handleTaskDBQuery(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	task, err := s.tasks.Store.GetTask(r.Context(), taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if task == nil {
		writeError(w, http.StatusNotFound, &NotFoundError{ID: taskID})
		return
	}
	if !s.authorizeTaskDB(w, r, task) {
		return
	}
	if !taskHasGrant(task, dbaccess.CapabilityDBRead) && !taskHasGrant(task, dbaccess.CapabilityDBWrite) {
		writeError(w, http.StatusForbidden, fmt.Errorf("task %q does not hold capability db-read or db-write", taskID))
		return
	}
	if s.tasks.Config.SourceDB == nil {
		writeError(w, http.StatusServiceUnavailable, apierror.Public("database is not available"))
		return
	}

	var req TaskDBQueryRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, errors.New("query is required"))
		return
	}

	// If the task does NOT hold db-write, enforce read-only validation
	if !taskHasGrant(task, dbaccess.CapabilityDBWrite) {
		if err := dbaccess.ValidateReadOnlyQuery(req.Query); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("read-only validation failed: %w", err))
			return
		}
	}

	var (
		cols       []string
		resultRows []map[string]any
		truncated  bool
		// readErr is a failure reading the result, which is the
		// server's (500), as opposed to the query's own failure (400).
		readErr error
	)
	read := func(rows *sql.Rows) error {
		readErr = readTaskDBRows(rows, &cols, &resultRows, &truncated)
		return readErr
	}

	// A db-write grant may write through this endpoint as well; a
	// db-read one is held to reading by the database itself, since the
	// validator above is only text inspection.
	if taskHasGrant(task, dbaccess.CapabilityDBWrite) {
		err = queryWithWrite(r.Context(), s.tasks.Config.SourceDB, req.Query, req.Params, read)
	} else {
		err = dbaccess.QueryReadOnly(r.Context(), s.tasks.Config.SourceDB, req.Query, req.Params, read)
	}
	if readErr != nil {
		writeError(w, http.StatusInternalServerError, readErr)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("query execution failed: %w", err))
		return
	}
	if resultRows == nil {
		resultRows = []map[string]any{}
	}

	writeJSON(w, http.StatusOK, TaskDBQueryResponse{
		Columns:   cols,
		Rows:      resultRows,
		Count:     len(resultRows),
		Truncated: truncated,
	})
}

// readTaskDBRows reads up to MaxTaskDBQueryRows rows of a db query's
// result into the response's columns and rows.
func readTaskDBRows(rows *sql.Rows, cols *[]string, out *[]map[string]any, truncated *bool) error {
	var err error
	if *cols, err = rows.Columns(); err != nil {
		return fmt.Errorf("reading column names: %w", err)
	}
	for rows.Next() {
		if len(*out) == MaxTaskDBQueryRows {
			*truncated = true
			break
		}
		colValues := make([]any, len(*cols))
		colPointers := make([]any, len(*cols))
		for i := range colValues {
			colPointers[i] = &colValues[i]
		}
		if err := rows.Scan(colPointers...); err != nil {
			return fmt.Errorf("scanning row: %w", err)
		}
		rowMap := make(map[string]any, len(*cols))
		for i, colName := range *cols {
			val := colValues[i]
			if b, ok := val.([]byte); ok {
				val = string(b)
			}
			rowMap[colName] = val
		}
		*out = append(*out, rowMap)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating rows: %w", err)
	}
	return nil
}

// queryWithWrite runs query for a db-write grant, on the pool as it is.
func queryWithWrite(ctx context.Context, db *sql.DB, query string, params []any, read func(*sql.Rows) error) error {
	rows, err := db.QueryContext(ctx, query, params...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return read(rows)
}

func (s *Server) handleTaskDBExec(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	task, err := s.tasks.Store.GetTask(r.Context(), taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if task == nil {
		writeError(w, http.StatusNotFound, &NotFoundError{ID: taskID})
		return
	}
	if !s.authorizeTaskDB(w, r, task) {
		return
	}
	if !taskHasGrant(task, dbaccess.CapabilityDBWrite) {
		writeError(w, http.StatusForbidden, fmt.Errorf("task %q does not hold capability db-write", taskID))
		return
	}
	if s.tasks.Config.SourceDB == nil {
		writeError(w, http.StatusServiceUnavailable, apierror.Public("database is not available"))
		return
	}

	var req TaskDBExecRequest
	if !readJSON(w, r, &req) {
		return
	}
	stmt := strings.TrimSpace(req.Statement)
	if stmt == "" {
		stmt = strings.TrimSpace(req.Query)
	}
	if stmt == "" {
		writeError(w, http.StatusBadRequest, errors.New("statement is required"))
		return
	}

	res, err := s.tasks.Config.SourceDB.ExecContext(r.Context(), stmt, req.Params...)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("statement execution failed: %w", err))
		return
	}

	rowsAffected, _ := res.RowsAffected()
	lastInsertID, _ := res.LastInsertId()

	writeJSON(w, http.StatusOK, TaskDBExecResponse{
		RowsAffected: rowsAffected,
		LastInsertID: lastInsertID,
	})
}

// TaskDBQuery calls POST /api/tasks/{id}/db/query to run a SQL query on Grain's database.
func (c *HTTPClient) TaskDBQuery(ctx context.Context, taskID, query string, params []any) (*TaskDBQueryResponse, error) {
	req := TaskDBQueryRequest{
		Query:  query,
		Params: params,
	}
	var res TaskDBQueryResponse
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/tasks/%s/db/query", taskID), req, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// TaskDBExec calls POST /api/tasks/{id}/db/execute to run a SQL statement on Grain's database.
func (c *HTTPClient) TaskDBExec(ctx context.Context, taskID, statement string, params []any) (*TaskDBExecResponse, error) {
	req := TaskDBExecRequest{
		Statement: statement,
		Params:    params,
	}
	var res TaskDBExecResponse
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/api/tasks/%s/db/execute", taskID), req, &res); err != nil {
		return nil, err
	}
	return &res, nil
}
