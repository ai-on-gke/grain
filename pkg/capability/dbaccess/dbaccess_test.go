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

package dbaccess_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/dbaccess"
	"github.com/ai-on-gke/grain/pkg/model"
)

type mockDBExecutor struct {
	lastQuery string
	lastStmt  string
	queryErr  error
	execErr   error
	queryRes  dbaccess.QueryResult
	execRes   dbaccess.ExecResult
}

func (m *mockDBExecutor) QueryDB(ctx context.Context, query string, params []any) (dbaccess.QueryResult, error) {
	m.lastQuery = query
	if m.queryErr != nil {
		return dbaccess.QueryResult{}, m.queryErr
	}
	return m.queryRes, nil
}

func (m *mockDBExecutor) ExecDB(ctx context.Context, query string, params []any) (dbaccess.ExecResult, error) {
	m.lastStmt = query
	if m.execErr != nil {
		return dbaccess.ExecResult{}, m.execErr
	}
	return m.execRes, nil
}

func TestReadProviderSpec(t *testing.T) {
	p := dbaccess.NewReadProvider()
	spec := p.Spec()
	if spec.Name != dbaccess.CapabilityDBRead {
		t.Errorf("name = %q, want %q", spec.Name, dbaccess.CapabilityDBRead)
	}
	if spec.Provision != model.ProvisionGrant {
		t.Errorf("provision = %q, want %q", spec.Provision, model.ProvisionGrant)
	}
	if spec.Description == "" {
		t.Error("expected non-empty description")
	}
}

func TestWriteProviderSpec(t *testing.T) {
	p := dbaccess.NewWriteProvider()
	spec := p.Spec()
	if spec.Name != dbaccess.CapabilityDBWrite {
		t.Errorf("name = %q, want %q", spec.Name, dbaccess.CapabilityDBWrite)
	}
	if spec.Provision != model.ProvisionGrant {
		t.Errorf("provision = %q, want %q", spec.Provision, model.ProvisionGrant)
	}
	if spec.Description == "" {
		t.Error("expected non-empty description")
	}
}

func TestReadOnlyToolsOnlyProvidesQueryTool(t *testing.T) {
	tools := dbaccess.Tools(dbaccess.Config{AllowWrite: false})
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool for read-only access, got %d", len(tools))
	}
	if tools[0].Name != "query_grain_db" {
		t.Errorf("tool name = %q, want %q", tools[0].Name, "query_grain_db")
	}
}

func TestWriteToolsProvidesBothQueryAndExecuteTools(t *testing.T) {
	tools := dbaccess.Tools(dbaccess.Config{AllowWrite: true})
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools for read/write access, got %d", len(tools))
	}
	names := []string{tools[0].Name, tools[1].Name}
	if names[0] != "query_grain_db" || names[1] != "execute_grain_db" {
		t.Errorf("tools = %v, want query_grain_db and execute_grain_db", names)
	}
}

func TestQueryToolExecution(t *testing.T) {
	mock := &mockDBExecutor{
		queryRes: dbaccess.QueryResult{
			Columns: []string{"id", "title"},
			Rows: []map[string]any{
				{"id": "t-1", "title": "Test Task 1"},
				{"id": "t-2", "title": "Test Task 2"},
			},
			Count: 2,
		},
	}
	tools := dbaccess.Tools(dbaccess.Config{Executor: mock, AllowWrite: false})
	queryTool := tools[0]

	res := queryTool.Handler(context.Background(), map[string]any{
		"query": "SELECT id, title FROM tasks",
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Text)
	}
	if !strings.Contains(res.Text, "Query returned 2 row(s)") {
		t.Errorf("result text = %q, want mention of 2 rows", res.Text)
	}
	if !strings.Contains(res.Text, "Test Task 1") {
		t.Errorf("result text = %q, want task title in output", res.Text)
	}
}

func TestQueryToolRejectsMutatingQueriesInReadOnlyMode(t *testing.T) {
	mock := &mockDBExecutor{}
	tools := dbaccess.Tools(dbaccess.Config{Executor: mock, AllowWrite: false})
	queryTool := tools[0]

	mutatingQueries := []string{
		"INSERT INTO tasks (id) VALUES ('t-1')",
		"UPDATE tasks SET title = 'hacked'",
		"DELETE FROM tasks",
		"DROP TABLE tasks",
		"ALTER TABLE tasks ADD COLUMN secret TEXT",
	}

	for _, q := range mutatingQueries {
		res := queryTool.Handler(context.Background(), map[string]any{
			"query": q,
		})
		if !res.IsError {
			t.Errorf("expected error for query %q, got success", q)
		}
		if !strings.Contains(res.Text, "Query rejected") {
			t.Errorf("expected 'Query rejected' in error for query %q, got %q", q, res.Text)
		}
	}
}

func TestExecuteToolExecution(t *testing.T) {
	mock := &mockDBExecutor{
		execRes: dbaccess.ExecResult{
			RowsAffected: 1,
			LastInsertID: 42,
		},
	}
	tools := dbaccess.Tools(dbaccess.Config{Executor: mock, AllowWrite: true})
	var execTool *dbaccess.Config
	for _, tool := range tools {
		if tool.Name == "execute_grain_db" {
			res := tool.Handler(context.Background(), map[string]any{
				"statement": "UPDATE tasks SET title = 'updated' WHERE id = 't-1'",
			})
			if res.IsError {
				t.Fatalf("unexpected error: %s", res.Text)
			}
			if !strings.Contains(res.Text, "Statement executed successfully") {
				t.Errorf("result text = %q, want mention of success", res.Text)
			}
			if !strings.Contains(res.Text, "rowsAffected") {
				t.Errorf("result text = %q, want rowsAffected in output", res.Text)
			}
			return
		}
	}
	if execTool == nil {
		t.Errorf("execute_grain_db tool not found")
	}
}

func TestToolWithNilExecutorReturnsError(t *testing.T) {
	tools := dbaccess.Tools(dbaccess.Config{Executor: nil, AllowWrite: true})
	for _, tool := range tools {
		res := tool.Handler(context.Background(), map[string]any{"query": "SELECT 1"})
		if !res.IsError {
			t.Errorf("tool %s with nil executor should return error", tool.Name)
		}
		if !strings.Contains(res.Text, "Database access is unavailable") {
			t.Errorf("tool %s error text = %q, want unavailable message", tool.Name, res.Text)
		}
	}
}

func TestToolErrorHandling(t *testing.T) {
	mock := &mockDBExecutor{
		queryErr: errors.New("table does not exist"),
		execErr:  errors.New("syntax error"),
	}
	tools := dbaccess.Tools(dbaccess.Config{Executor: mock, AllowWrite: true})
	for _, tool := range tools {
		if tool.Name == "query_grain_db" {
			res := tool.Handler(context.Background(), map[string]any{"query": "SELECT * FROM missing"})
			if !res.IsError || !strings.Contains(res.Text, "table does not exist") {
				t.Errorf("query tool error = %q, want table does not exist", res.Text)
			}
		}
		if tool.Name == "execute_grain_db" {
			res := tool.Handler(context.Background(), map[string]any{"statement": "INVALID SQL"})
			if !res.IsError || !strings.Contains(res.Text, "syntax error") {
				t.Errorf("exec tool error = %q, want syntax error", res.Text)
			}
		}
	}
}
