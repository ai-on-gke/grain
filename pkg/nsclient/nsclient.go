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

// Package nsclient is the client half of grain's namespace (pkg/namespace)
// that both CLIs speak: the sandbox's `grain ls|read|grep`
// (pkg/sandboxcli), over the sandbox proxy's /fs/, and a workstation's
// `grain fs ls|cat|read|grep` (pkg/cli), over the daemon's own /api/fs/
// (design/spec/external-cli/external-cli.spec.md, #XC-25). What differs
// between them is only where the request goes and how it is
// authenticated; how a URI becomes a request, and how its answer is
// printed, is this package's, so the two cannot drift apart.
package nsclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// SandboxPrefix is where the sandbox proxy serves the namespace, and
// APIPrefix where grain's own API does.
const (
	SandboxPrefix = "/fs"
	APIPrefix     = "/api/fs"
)

// ErrReported is an error the client has already printed: the server's
// own words, on Client.Err.
var ErrReported = errors.New("reported")

// Path turns a namespace URI into its request path under prefix and the
// query written inline after it: "ci://owner/repo/run?grep=FAIL" under
// SandboxPrefix is "/fs/ci/owner/repo/run" and grep=FAIL. Each path
// segment is escaped on its own, so a name with a space or a # in it
// reaches the namespace as written.
func Path(prefix, uri string) (string, url.Values, error) {
	scheme, rel, ok := strings.Cut(uri, "://")
	if !ok || scheme == "" {
		return "", nil, fmt.Errorf("%q is not a namespace URI (ci://, src://, task://, config://, memory://, deploy://)", uri)
	}
	var inline url.Values
	if before, query, ok := strings.Cut(rel, "?"); ok {
		q, err := url.ParseQuery(query)
		if err != nil {
			return "", nil, fmt.Errorf("%q: its query %q does not parse: %v", uri, query, err)
		}
		rel, inline = before, q
	}
	segs := strings.Split(strings.Trim(rel, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	p := strings.TrimRight(prefix, "/") + "/" + scheme
	if joined := strings.Join(segs, "/"); joined != "" {
		p += "/" + joined
	}
	return p, inline, nil
}

// Merge is q with the inline query of a URI under it: a flag given on
// the command line wins over the same parameter written into the URI.
func Merge(inline, q url.Values) url.Values {
	out := url.Values{}
	for k, v := range inline {
		out[k] = append([]string(nil), v...)
	}
	for k, v := range q {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// EncodeQuery is q as a query string, sorted, with a parameter whose
// value is empty written bare (?list, not ?list=), the way the namespace
// reads its flags.
func EncodeQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		for _, v := range q[k] {
			if v == "" {
				parts = append(parts, url.QueryEscape(k))
				continue
			}
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// ListQuery is `ls`'s query: a listing, of the entries glob matches when
// it is set.
func ListQuery(glob string) url.Values {
	q := url.Values{"list": {""}}
	if glob != "" {
		q.Set("glob", glob)
	}
	return q
}

// GrepOptions are `grep`'s flags.
type GrepOptions struct {
	IgnoreCase bool
	// Context is -C: lines of context around each match, "" for none.
	Context string
	Glob    string
	// Max is --max, "" for the namespace's own limit.
	Max string
}

// GrepQuery is `grep`'s query for pattern: always with line numbers, as
// both CLIs have always printed them.
func GrepQuery(pattern string, o GrepOptions) url.Values {
	q := url.Values{"grep": {pattern}, "n": {"1"}}
	if o.IgnoreCase {
		q.Set("i", "1")
	}
	if o.Context != "" {
		q.Set("context", o.Context)
	}
	if o.Glob != "" {
		q.Set("glob", o.Glob)
	}
	if o.Max != "" {
		q.Set("max", o.Max)
	}
	return q
}

// Client reads the namespace at Base+Prefix.
type Client struct {
	// Base is the server's address ("http://127.0.0.1:9728", or a
	// daemon's own URL), and Prefix where under it the namespace is
	// served (SandboxPrefix or APIPrefix).
	Base, Prefix string
	// HTTP sends the requests. Its transport is where a caller's
	// credential is added.
	HTTP *http.Client
	// Authorize, when set, adds a credential to each request.
	Authorize func(*http.Request)
	// Name is what Base is, for a request that does not reach it ("the
	// sandbox proxy"); "" leaves the transport's own error as it is.
	Name string
	// Out and Err are where answers and refusals are printed.
	Out, Err io.Writer
}

// Do sends GET uri with q over the URI's own inline query, and hands
// back the response whatever its status.
func (c *Client) Do(uri string, q url.Values, header http.Header) (*http.Response, error) {
	p, inline, err := Path(c.Prefix, uri)
	if err != nil {
		return nil, err
	}
	u := strings.TrimRight(c.Base, "/") + p
	if enc := EncodeQuery(Merge(inline, q)); enc != "" {
		u += "?" + enc
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if c.Authorize != nil {
		c.Authorize(req)
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		if c.Name == "" {
			return nil, err
		}
		return nil, fmt.Errorf("reaching %s at %s: %w", c.Name, c.Base, err)
	}
	return resp, nil
}

// refused prints resp's refusal, if it is one, and reports whether it
// was. A JSON {"error": ...} body (grain's API) is printed as its
// message, a plain one (the namespace's own) as it came.
func (c *Client) refused(resp *http.Response) bool {
	if resp.StatusCode < 400 {
		return false
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	fmt.Fprintln(c.Err, "grain: "+Refusal(body))
	return true
}

// Refusal is the message a refused request's body carries.
func Refusal(body []byte) string {
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(bytes.TrimSpace(body), &payload) == nil && payload.Error != "" {
		return payload.Error
	}
	return strings.TrimSpace(string(body))
}

// Get prints uri's answer to q (a file, a listing) on Out.
func (c *Client) Get(uri string, q url.Values) error {
	resp, err := c.Do(uri, q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if c.refused(resp) {
		return ErrReported
	}
	_, err = io.Copy(c.Out, resp.Body)
	return err
}

// ReadOptions are `read`'s.
type ReadOptions struct {
	// LastLines prints only the last this many lines of the answer, 0
	// for all of it.
	LastLines int
	// SaySize prints the file's whole size on Err first, for a reader
	// paging through it by bytes.
	SaySize bool
	// Range is an HTTP Range header to send, "" for none.
	Range string
}

// Read prints the file uri names, read with q (?tail, ?offset, ...).
func (c *Client) Read(uri string, q url.Values, o ReadOptions) error {
	var header http.Header
	if o.Range != "" {
		header = http.Header{"Range": {o.Range}}
	}
	resp, err := c.Do(uri, q, header)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if c.refused(resp) {
		return ErrReported
	}
	if size := resp.Header.Get("X-Grain-Total-Size"); o.SaySize && size != "" {
		fmt.Fprintf(c.Err, "grain: %s is %s bytes; --tail, --offset and --limit take bytes, --lines N the last N lines\n", uri, size)
	}
	if o.LastLines <= 0 {
		_, err = io.Copy(c.Out, resp.Body)
		return err
	}
	tw := &TailWriter{N: o.LastLines}
	if _, err := io.Copy(tw, resp.Body); err != nil {
		return err
	}
	_, err = c.Out.Write(tw.Bytes())
	return err
}

// GrepResult is what a grep answered, for a caller printing it some
// other way than as it came (grain fs grep --json).
type GrepResult struct {
	URI     string `json:"uri"`
	Output  string `json:"output"`
	Matches string `json:"matches,omitempty"`
	// Truncated is whether the walk stopped before it had searched
	// everything under URI.
	Truncated bool `json:"truncated,omitempty"`
}

// Grep prints the matches q's ?grep= finds under uri. A walk the
// namespace cut short before it found anything is ErrReported (exit 1):
// what it did not search may hold the match, so an empty answer with
// exit 0 would read as "not there".
func (c *Client) Grep(uri string, q url.Values) error {
	return c.grep(uri, q, nil)
}

// GrepTo is Grep handing the answer to emit instead of printing it.
func (c *Client) GrepTo(uri string, q url.Values, emit func(GrepResult) error) error {
	return c.grep(uri, q, emit)
}

func (c *Client) grep(uri string, q url.Values, emit func(GrepResult) error) error {
	resp, err := c.Do(uri, q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if c.refused(resp) {
		return ErrReported
	}
	truncated := resp.Header.Get("X-Grain-Walk-Truncated") == "true"
	matches := resp.Header.Get("X-Grain-Matches")
	if emit != nil {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		if err := emit(GrepResult{URI: uri, Output: string(body), Matches: matches, Truncated: truncated}); err != nil {
			return err
		}
	} else if _, err := io.Copy(c.Out, resp.Body); err != nil {
		return err
	}
	if truncated && matches == "0" {
		fmt.Fprintf(c.Err, "grain grep: no match in the part of %s searched before the walk cap, and the rest was not searched: narrow the path or pass --glob\n", uri)
		return ErrReported
	}
	return nil
}

// TailWriter keeps the last N lines written to it.
type TailWriter struct {
	N     int
	lines [][]byte
	// part is the line still being written.
	part []byte
}

func (t *TailWriter) Write(b []byte) (int, error) {
	written := len(b)
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			t.part = append(t.part, b...)
			break
		}
		t.part = append(t.part, b[:i+1]...)
		t.lines = append(t.lines, t.part)
		t.part = nil
		if len(t.lines) > t.N {
			t.lines = t.lines[len(t.lines)-t.N:]
		}
		b = b[i+1:]
	}
	return written, nil
}

// Bytes is the last N lines written, the unterminated last one included.
func (t *TailWriter) Bytes() []byte {
	lines := t.lines
	if len(t.part) > 0 {
		lines = append(lines, t.part)
	}
	if len(lines) > t.N {
		lines = lines[len(lines)-t.N:]
	}
	return bytes.Join(lines, nil)
}
