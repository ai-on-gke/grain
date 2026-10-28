package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// ToolInfo is one entry of a tools/list response -- what a client needs to
// advertise the tool to a model.
type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// CallResult is a tools/call response: the same {content, isError} shape
// Registry.handleCall writes.
type CallResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// Text concatenates every text content block, which in practice is always
// exactly one -- every Tool.Handler in this package returns a single Result.
func (r *CallResult) Text() string {
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

// Client speaks the client side of the newline-delimited JSON-RPC protocol
// Serve answers, over any io.Writer/*bufio.Reader pair -- a subprocess's
// stdin/stdout, or one end of an in-process pipe (see NewInProcess). It is
// the only type in this package that can drive a Registry from outside;
// nothing about the tools themselves is reachable except through it.
type Client struct {
	mu     sync.Mutex
	w      io.Writer
	r      *bufio.Reader
	closer io.Closer
	nextID int64
}

// NewClient wraps an already-connected pair. closer, if non-nil, is what
// Close calls -- typically the write half, whose closure is what lets the
// server side's blocking read return io.EOF and its Serve loop exit.
func NewClient(w io.Writer, r io.Reader, closer io.Closer) *Client {
	return &Client{w: w, r: bufio.NewReader(r), closer: closer}
}

func (c *Client) Close() error {
	if c.closer == nil {
		return nil
	}
	return c.closer.Close()
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := atomic.AddInt64(&c.nextID, 1)

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}
	line, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	line = append(line, '\n')

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := c.w.Write(line); err != nil {
		return nil, fmt.Errorf("mcp: writing request: %w", err)
	}

	respLine, err := c.r.ReadBytes('\n')
	if err != nil && len(respLine) == 0 {
		return nil, fmt.Errorf("mcp: reading response: %w", err)
	}

	var resp struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(respLine, &resp); err != nil {
		return nil, fmt.Errorf("mcp: decoding response: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("mcp: %s (code %d)", resp.Error.Message, resp.Error.Code)
	}
	wantID := strconv.FormatInt(id, 10)
	if gotID := string(resp.ID); gotID != wantID {
		return nil, fmt.Errorf("mcp: response id %s does not match request id %s", gotID, wantID)
	}
	return resp.Result, nil
}

// ListTools issues tools/list and returns the advertised tools in order.
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	raw, err := c.call(ctx, "tools/list", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []ToolInfo `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("mcp: decoding tools/list result: %w", err)
	}
	return out.Tools, nil
}

// CallTool issues tools/call for name with the given arguments.
func (c *Client) CallTool(ctx context.Context, name string, arguments map[string]any) (*CallResult, error) {
	raw, err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments})
	if err != nil {
		return nil, err
	}
	var out CallResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("mcp: decoding tools/call result: %w", err)
	}
	return &out, nil
}
