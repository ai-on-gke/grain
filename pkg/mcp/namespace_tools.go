package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/namespace"
	"github.com/bwsalmon/grain/pkg/redact"
)

const elisionAdviceNamespace = "fetch the full uncapped content in the sandbox with " +
	"curl -s http://127.0.0.1:9728/fs/<scheme>/<path> or copy it into the workspace with grain_copy"

func namespaceElisionAdvice(scheme, relPath, rawQuery string) string {
	restURL := "http://127.0.0.1:9728/fs/" + scheme
	if relPath != "" {
		restURL += "/" + relPath
	}
	if rawQuery != "" {
		restURL += "?" + rawQuery
	}
	return fmt.Sprintf("fetch the full uncapped content in the sandbox with curl -s %q or copy it into the workspace with grain_copy", restURL)
}

// NamespaceToolsConfig configures the MCP wrapper tools (grain_ls, grain_read,
// grain_grep, grain_copy) over the grain namespace server.
type NamespaceToolsConfig struct {
	Server      *namespace.Server
	Scope       namespace.Scope
	CIReader    GrainCIRunner
	ArtifactDir string
	SrcDir      string
	Redactor    *redact.Redactor
	WriteFile   func(ctx context.Context, args map[string]any) Result
}

func nsErrorf(format string, args ...any) Result {
	return Result{Text: fmt.Sprintf(format, args...), IsError: true}
}

func nsIntArg(args map[string]any, key string) (int, bool) {
	v, ok := args[key]
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}

// SandboxWriteFile extracts the write_file handler from a sandbox tool slice so
// grain_copy can write into Host, Docker container, or Granule VM sandboxes
// through the same sandbox tool interface.
func SandboxWriteFile(tools []Tool) func(ctx context.Context, args map[string]any) Result {
	for _, t := range tools {
		if t.Name == "write_file" && t.Handler != nil {
			return t.Handler
		}
	}
	return nil
}

type namespaceCIRunStoreAdapter struct {
	reader GrainCIRunner
}

func (a namespaceCIRunStoreAdapter) Put(ci.Run)                   {}
func (a namespaceCIRunStoreAdapter) Update(string, func(*ci.Run)) {}
func (a namespaceCIRunStoreAdapter) Get(id string) (ci.Run, bool) {
	if a.reader == nil {
		return ci.Run{}, false
	}
	run, err := a.reader.GrainCIRun(context.Background(), id)
	if err != nil {
		return ci.Run{}, false
	}
	return run, true
}
func (a namespaceCIRunStoreAdapter) List() []ci.Run {
	if a.reader == nil {
		return nil
	}
	page, err := a.reader.FindGrainCIRuns(context.Background(), ci.RunFilter{}, 100)
	if err != nil {
		return nil
	}
	return page.Runs
}
func (a namespaceCIRunStoreAdapter) Find(f ci.RunFilter, limit int) ci.RunPage {
	if a.reader == nil {
		return ci.RunPage{}
	}
	page, err := a.reader.FindGrainCIRuns(context.Background(), f, limit)
	if err != nil {
		return ci.RunPage{}
	}
	return page
}
func (a namespaceCIRunStoreAdapter) Live(ci.Key) (ci.Run, bool) {
	return ci.Run{}, false
}

func (cfg NamespaceToolsConfig) resolvedServer() *namespace.Server {
	if cfg.Server != nil {
		return cfg.Server
	}
	var ciStore ci.RunStore
	if cfg.CIReader != nil {
		ciStore = namespaceCIRunStoreAdapter{reader: cfg.CIReader}
	}
	redactor := cfg.Redactor
	if redactor == nil {
		redactor = redact.New(nil)
	}
	return &namespace.Server{
		CIStore:     ciStore,
		ArtifactDir: cfg.ArtifactDir,
		SrcDir:      cfg.SrcDir,
		Redactor:    redactor,
	}
}

func serveNamespaceCall(ctx context.Context, srv *namespace.Server, scope namespace.Scope, scheme, relPath string, query url.Values, headers map[string]string) (int, string) {
	reqPath := "/fs/" + scheme
	if relPath != "" {
		reqPath += "/" + relPath
	}
	u := &url.URL{Path: reqPath, RawQuery: query.Encode()}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return http.StatusBadRequest, err.Error()
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req, scope)
	return rec.Code, rec.Body.String()
}

// NewNamespaceTools returns the thin MCP wrapper tools over the grain namespace:
// grain_ls, grain_read, grain_grep, and grain_copy.
func NewNamespaceTools(cfg NamespaceToolsConfig) []Tool {
	srv := cfg.resolvedServer()
	scope := cfg.Scope

	return []Tool{
		{
			Name: "grain_ls",
			Description: "List a directory or match a glob in the grain namespace (e.g. \"ci://owner/repo\", " +
				"\"ci://owner/repo/<run>/go/tests\", \"src://pkg\"). Results are capped at the normal tool-result limit; " +
				"for uncapped listings in the sandbox, curl http://127.0.0.1:9728/fs/<scheme>/<path>?list.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Namespace URI or path to list (e.g. \"ci://bwsalmon/grain/<run>/go\" or \"src://pkg\").",
					},
					"glob": map[string]any{
						"type":        "string",
						"description": "Optional glob pattern to filter entries recursively (e.g. \"*Test*\").",
					},
				},
				"required": []string{"path"},
			},
			ElisionAdvice: elisionAdviceNamespace,
			Handler: func(ctx context.Context, args map[string]any) Result {
				rawPath, _ := args["path"].(string)
				scheme, relPath, err := namespace.ParseURI(rawPath)
				if err != nil {
					return nsErrorf("grain_ls: %v", err)
				}
				q := url.Values{}
				if glob, _ := args["glob"].(string); strings.TrimSpace(glob) != "" {
					q.Set("glob", strings.TrimSpace(glob))
				} else {
					q.Set("list", "1")
				}
				status, body := serveNamespaceCall(ctx, srv, scope, scheme, relPath, q, nil)
				if status != http.StatusOK {
					return nsErrorf("grain_ls %s://%s: %s (HTTP %d)", scheme, relPath, strings.TrimSpace(body), status)
				}
				advice := namespaceElisionAdvice(scheme, relPath, q.Encode())
				return Result{Text: elideMiddle(body, int(maxToolResultBytes.Load()), advice)}
			},
		},
		{
			Name: "grain_read",
			Description: "Read a file from the grain namespace (e.g. \"ci://owner/repo/<run>/<job>/Test.log\", " +
				"\"ci://owner/repo/<run>/<job>/attempts/1/tests/<pkg>/<Test>.log\", \"ci://owner/repo/<run>/<job>/gotest.log\", " +
				"or \"src://pkg/ci/engine.go\"). Results are capped at the tool-result limit; when cut, use " +
				"curl -s http://127.0.0.1:9728/fs/<scheme>/<path> in run_command or grain_copy to read without a cap.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Namespace URI or path to read.",
					},
					"offset": map[string]any{
						"type":        "integer",
						"description": "Optional zero-based byte offset to start reading from.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Optional maximum number of bytes to read.",
					},
					"tail": map[string]any{
						"type":        "integer",
						"description": "Optional number of bytes to read from the end of the file.",
					},
				},
				"required": []string{"path"},
			},
			ElisionAdvice: elisionAdviceNamespace,
			Handler: func(ctx context.Context, args map[string]any) Result {
				rawPath, _ := args["path"].(string)
				scheme, relPath, err := namespace.ParseURI(rawPath)
				if err != nil {
					return nsErrorf("grain_read: %v", err)
				}
				q := url.Values{}
				headers := map[string]string{}
				if tail, ok := nsIntArg(args, "tail"); ok && tail > 0 {
					q.Set("tail", strconv.Itoa(tail))
				} else {
					offset, hasOffset := nsIntArg(args, "offset")
					limit, hasLimit := nsIntArg(args, "limit")
					if hasOffset || hasLimit {
						if offset < 0 {
							offset = 0
						}
						if hasLimit && limit > 0 {
							headers["Range"] = fmt.Sprintf("bytes=%d-%d", offset, offset+limit-1)
						} else {
							headers["Range"] = fmt.Sprintf("bytes=%d-", offset)
						}
					}
				}
				status, body := serveNamespaceCall(ctx, srv, scope, scheme, relPath, q, headers)
				if status != http.StatusOK && status != http.StatusPartialContent {
					return nsErrorf("grain_read %s://%s: %s (HTTP %d)", scheme, relPath, strings.TrimSpace(body), status)
				}
				advice := namespaceElisionAdvice(scheme, relPath, q.Encode())
				return Result{Text: elideMiddle(body, int(maxToolResultBytes.Load()), advice)}
			},
		},
		{
			Name: "grain_grep",
			Description: "Search a file or directory subtree in the grain namespace with an RE2 regular expression " +
				"(e.g. \"ci://owner/repo/<run>/go/gotest.log\" or \"src://pkg/ci\"). Results are capped at the tool-result limit; " +
				"when cut, use curl -s \"http://127.0.0.1:9728/fs/<scheme>/<path>?grep=<re>\" or grain_copy.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Namespace URI or path to search.",
					},
					"pattern": map[string]any{
						"type":        "string",
						"description": "RE2 regular expression to search for.",
					},
					"ignore_case": map[string]any{
						"type":        "boolean",
						"description": "Match case-insensitively.",
					},
					"line_numbers": map[string]any{
						"type":        "boolean",
						"description": "Prefix matches with 1-based line numbers (default true).",
					},
					"context": map[string]any{
						"type":        "integer",
						"description": "Number of context lines before and after each match.",
					},
					"max_matches": map[string]any{
						"type":        "integer",
						"description": "Maximum number of matching lines to return (default 100).",
					},
					"glob": map[string]any{
						"type":        "string",
						"description": "Optional file glob when searching a directory subtree.",
					},
				},
				"required": []string{"path", "pattern"},
			},
			ElisionAdvice: elisionAdviceNamespace,
			Handler: func(ctx context.Context, args map[string]any) Result {
				rawPath, _ := args["path"].(string)
				pattern, _ := args["pattern"].(string)
				if strings.TrimSpace(pattern) == "" {
					return nsErrorf("grain_grep: pattern is required")
				}
				scheme, relPath, err := namespace.ParseURI(rawPath)
				if err != nil {
					return nsErrorf("grain_grep: %v", err)
				}
				q := url.Values{}
				q.Set("grep", pattern)
				if ic, ok := args["ignore_case"].(bool); ok && ic {
					q.Set("i", "1")
				}
				lineNums := true
				if ln, ok := args["line_numbers"].(bool); ok {
					lineNums = ln
				}
				if lineNums {
					q.Set("n", "1")
				}
				if ctxLines, ok := nsIntArg(args, "context"); ok && ctxLines > 0 {
					q.Set("context", strconv.Itoa(ctxLines))
				}
				if maxMatches, ok := nsIntArg(args, "max_matches"); ok && maxMatches > 0 {
					q.Set("max", strconv.Itoa(maxMatches))
				}
				if glob, _ := args["glob"].(string); strings.TrimSpace(glob) != "" {
					q.Set("glob", strings.TrimSpace(glob))
				}
				status, body := serveNamespaceCall(ctx, srv, scope, scheme, relPath, q, nil)
				if status != http.StatusOK {
					return nsErrorf("grain_grep %s://%s: %s (HTTP %d)", scheme, relPath, strings.TrimSpace(body), status)
				}
				advice := namespaceElisionAdvice(scheme, relPath, q.Encode())
				return Result{Text: elideMiddle(body, int(maxToolResultBytes.Load()), advice)}
			},
		},
		{
			Name: "grain_copy",
			Description: "Copy a file or directory tree from the grain namespace into the sandbox workspace " +
				"without passing its contents through the model context. Over REST in run_command, the equivalent " +
				"is curl -s http://127.0.0.1:9728/fs/<scheme>/<path> -o <dest>.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Namespace URI or path to copy (e.g. \"ci://owner/repo/<run>/go/gotest.log\" or \"src://pkg/ci\").",
					},
					"dest": map[string]any{
						"type":        "string",
						"description": "Destination path in the sandbox workspace.",
					},
				},
				"required": []string{"path", "dest"},
			},
			ElisionAdvice: elisionAdviceNamespace,
			Handler: func(ctx context.Context, args map[string]any) Result {
				if cfg.WriteFile == nil {
					return nsErrorf("grain_copy: no sandbox write_file handler is configured for this session")
				}
				rawPath, _ := args["path"].(string)
				dest, _ := args["dest"].(string)
				dest = strings.TrimSpace(dest)
				if dest == "" {
					return nsErrorf("grain_copy: dest is required")
				}
				scheme, relPath, err := namespace.ParseURI(rawPath)
				if err != nil {
					return nsErrorf("grain_copy: %v", err)
				}
				files, totalBytes, err := copyNamespaceTree(ctx, srv, scope, scheme, relPath, dest, cfg.WriteFile)
				if err != nil {
					return nsErrorf("grain_copy %s://%s -> %s: %v", scheme, relPath, dest, err)
				}
				if files == 1 {
					return Result{Text: fmt.Sprintf("Copied %s://%s to %s (%d bytes)", scheme, relPath, dest, totalBytes)}
				}
				return Result{Text: fmt.Sprintf("Copied %d files (%d bytes) from %s://%s to %s", files, totalBytes, scheme, relPath, dest)}
			},
		},
	}
}

func copyNamespaceTree(ctx context.Context, srv *namespace.Server, scope namespace.Scope, scheme, relPath, dest string, writeFile func(context.Context, map[string]any) Result) (int, int64, error) {
	node, err := srv.Resolve(ctx, scope, scheme, relPath)
	if err != nil {
		return 0, 0, err
	}
	if !node.IsDir {
		data, err := srv.ReadRedacted(node, namespace.DefaultMaxResponseBytes)
		if err != nil {
			return 0, 0, err
		}
		res := writeFile(ctx, map[string]any{
			"file_path": dest,
			"content":   data,
		})
		if res.IsError {
			return 0, 0, fmt.Errorf("%s", strings.TrimSpace(res.Text))
		}
		return 1, int64(len(data)), nil
	}

	var totalFiles int
	var totalBytes int64
	for _, entry := range node.Entries {
		childRel := entry.Name
		if relPath != "" {
			childRel = path.Join(relPath, entry.Name)
		}
		childDest := path.Join(dest, entry.Name)
		f, b, err := copyNamespaceTree(ctx, srv, scope, scheme, childRel, childDest, writeFile)
		if err != nil {
			return totalFiles, totalBytes, err
		}
		totalFiles += f
		totalBytes += b
	}
	return totalFiles, totalBytes, nil
}
