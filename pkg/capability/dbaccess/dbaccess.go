// Package dbaccess provides capabilities for direct Grain database read and read/write access.
package dbaccess

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

const (
	// CapabilityDBRead is the grant name for direct read-only Grain database access.
	CapabilityDBRead = "db-read"
	// CapabilityDBWrite is the grant name for direct read/write Grain database access.
	CapabilityDBWrite = "db-write"
)

// QueryResult holds columns, rows, and count from a SQL query execution.
type QueryResult struct {
	Columns []string         `json:"columns"`
	Rows    []map[string]any `json:"rows"`
	Count   int              `json:"count"`
	// Truncated is set when the server cut the result off at its row cap.
	Truncated bool `json:"truncated,omitempty"`
}

// ExecResult holds execution metadata such as rows affected and last insert ID.
type ExecResult struct {
	RowsAffected int64 `json:"rowsAffected"`
	LastInsertID int64 `json:"lastInsertID,omitempty"`
}

// DBExecutor is the interface for executing SQL queries and statements on Grain's database.
type DBExecutor interface {
	QueryDB(ctx context.Context, query string, params []any) (QueryResult, error)
	ExecDB(ctx context.Context, query string, params []any) (ExecResult, error)
}

// Config holds configuration for database access providers and tools.
type Config struct {
	TaskID     string
	Executor   DBExecutor
	AllowWrite bool
}

// ReadProvider implements model.CapabilityProvider for db-read.
type ReadProvider struct {
	model.BaseCapability
}

// WriteProvider implements model.CapabilityProvider for db-write.
type WriteProvider struct {
	model.BaseCapability
}

// NewReadProvider returns a new db-read capability provider.
func NewReadProvider() *ReadProvider {
	return &ReadProvider{}
}

// NewReadProviderWithConfig returns a new db-read capability provider with configuration.
func NewReadProviderWithConfig(_ Config) *ReadProvider {
	return &ReadProvider{}
}

// Spec returns the capability specification for db-read.
func (p *ReadProvider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:        CapabilityDBRead,
		Label:       "Grain DB read",
		Description: "Let this task execute read-only SQL queries directly against Grain's database",
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionGrant,
	}
}

// NewWriteProvider returns a new db-write capability provider.
func NewWriteProvider() *WriteProvider {
	return &WriteProvider{}
}

// NewWriteProviderWithConfig returns a new db-write capability provider with configuration.
func NewWriteProviderWithConfig(_ Config) *WriteProvider {
	return &WriteProvider{}
}

// Spec returns the capability specification for db-write.
func (p *WriteProvider) Spec() model.CapabilitySpec {
	return model.CapabilitySpec{
		Name:        CapabilityDBWrite,
		Label:       "Grain DB read/write",
		Description: "Let this task execute SQL queries and statements (read and write) directly against Grain's database",
		Source:      model.GrantByLabel,
		Provision:   model.ProvisionGrant,
	}
}

// Tools returns the MCP database tools enabled by the given config.
func Tools(cfg Config) []mcp.Tool {
	var tools []mcp.Tool
	tools = append(tools, queryDBTool(cfg))
	if cfg.AllowWrite {
		tools = append(tools, executeDBTool(cfg))
	}
	return tools
}

var queryDBInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"query": map[string]any{
			"type":        "string",
			"description": "SQL query to execute (read-only, e.g. 'SELECT id, state, title FROM tasks LIMIT 10').",
		},
		"params": map[string]any{
			"type":        "array",
			"description": "Optional positional query parameters for parameterized queries (e.g. ['active', 10]).",
			"items":       map[string]any{},
		},
	},
	"required": []string{"query"},
}

var executeDBInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"statement": map[string]any{
			"type":        "string",
			"description": "SQL statement to execute (e.g. 'UPDATE tasks SET ...' or 'INSERT INTO ...').",
		},
		"query": map[string]any{
			"type":        "string",
			"description": "Alias for statement.",
		},
		"params": map[string]any{
			"type":        "array",
			"description": "Optional positional statement parameters.",
			"items":       map[string]any{},
		},
	},
}

func queryDBTool(cfg Config) mcp.Tool {
	return mcp.Tool{
		Name:        "query_grain_db",
		Description: "Execute a read-only SQL query against Grain's database and return the results as structured JSON rows.",
		InputSchema: queryDBInputSchema,
		Handler: func(ctx context.Context, args map[string]any) mcp.Result {
			if cfg.Executor == nil {
				return mcp.Result{
					Text:    "Database access is unavailable: this run has no controller connection.",
					IsError: true,
				}
			}
			queryStr, _ := args["query"].(string)
			queryStr = strings.TrimSpace(queryStr)
			if queryStr == "" {
				return mcp.Result{
					Text:    "query is required",
					IsError: true,
				}
			}

			if !cfg.AllowWrite {
				if err := ValidateReadOnlyQuery(queryStr); err != nil {
					return mcp.Result{
						Text:    fmt.Sprintf("Query rejected: %v", err),
						IsError: true,
					}
				}
			}

			var params []any
			if rawParams, ok := args["params"].([]any); ok {
				params = rawParams
			}

			result, err := cfg.Executor.QueryDB(ctx, queryStr, params)
			if err != nil {
				return mcp.Result{
					Text:    fmt.Sprintf("Query failed: %v", err),
					IsError: true,
				}
			}

			return mcp.Result{
				Text: fmt.Sprintf("Query returned %d row(s)%s:\n%s", result.Count, truncatedNote(result), formatJSON(result)),
			}
		},
	}
}

func executeDBTool(cfg Config) mcp.Tool {
	return mcp.Tool{
		Name:        "execute_grain_db",
		Description: "Execute a SQL statement (modifying or query) directly against Grain's database and return execution details.",
		InputSchema: executeDBInputSchema,
		Handler: func(ctx context.Context, args map[string]any) mcp.Result {
			if cfg.Executor == nil {
				return mcp.Result{
					Text:    "Database access is unavailable: this run has no controller connection.",
					IsError: true,
				}
			}
			stmt, _ := args["statement"].(string)
			if stmt == "" {
				stmt, _ = args["query"].(string)
			}
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				return mcp.Result{
					Text:    "statement is required",
					IsError: true,
				}
			}

			var params []any
			if rawParams, ok := args["params"].([]any); ok {
				params = rawParams
			}

			res, err := cfg.Executor.ExecDB(ctx, stmt, params)
			if err != nil {
				return mcp.Result{
					Text:    fmt.Sprintf("Statement execution failed: %v", err),
					IsError: true,
				}
			}

			return mcp.Result{
				Text: fmt.Sprintf("Statement executed successfully: %s", formatJSON(res)),
			}
		},
	}
}

func formatJSON(v any) string {
	importJSON, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%+v", v)
	}
	return string(importJSON)
}

// truncatedNote tells the agent its result was cut off, so it pages with
// LIMIT/OFFSET rather than reading the first rows as the whole answer.
func truncatedNote(r QueryResult) string {
	if !r.Truncated {
		return ""
	}
	return " (truncated at the server's row cap; add LIMIT/OFFSET to page through the rest)"
}
