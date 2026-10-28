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

package hostinit

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// fakeSystem is System with no host touched at all -- the same "no
// sandbox and no cloud" bar gcpsetup_test.go's own fakeAdmin holds
// itself to. Each call is matched against a table of canned responses
// keyed by the command line it would have run (name plus args, joined
// with a space, dir and env ignored for matching but recorded), and
// every call actually made is logged so a test can assert on order and
// shape, not only on the final Result.
type fakeSystem struct {
	mu sync.Mutex

	// responses maps "name arg1 arg2 ..." to a canned (output, error).
	// A key with no entry fails with errNotFound, which every step in
	// this package treats as "not installed"/"does not exist yet" --
	// exactly the behavior an unconfigured command ought to produce in a
	// test that starts from a bare host.
	responses map[string]fakeResponse
	// http maps a URL to a canned (body, error) the same way.
	http map[string]fakeHTTPResponse
	// httpRequests maps "METHOD url" to a canned (status, body, error)
	// for HTTPRequest, the same way responses does for Run.
	httpRequests map[string]fakeHTTPRequestResponse

	root bool

	calls            []string
	httpCalls        []string
	httpRequestCalls []fakeHTTPRequestCall
}

type fakeResponse struct {
	output string
	err    error
}

type fakeHTTPResponse struct {
	body []byte
	err  error
}

type fakeHTTPRequestResponse struct {
	status int
	body   []byte
	err    error
}

type fakeHTTPRequestCall struct {
	method string
	url    string
	body   []byte
}

func newFakeSystem() *fakeSystem {
	return &fakeSystem{
		responses:    map[string]fakeResponse{},
		http:         map[string]fakeHTTPResponse{},
		httpRequests: map[string]fakeHTTPRequestResponse{},
	}
}

func cmdKey(name string, args ...string) string {
	return strings.TrimSpace(name + " " + strings.Join(args, " "))
}

var errNotFound = fmt.Errorf("fakeSystem: no such command")

func (f *fakeSystem) on(output string, err error, name string, args ...string) {
	f.responses[cmdKey(name, args...)] = fakeResponse{output: output, err: err}
}

func (f *fakeSystem) onHTTP(url string, body []byte, err error) {
	f.http[url] = fakeHTTPResponse{body: body, err: err}
}

func requestKey(method, url string) string {
	return strings.TrimSpace(method + " " + url)
}

func (f *fakeSystem) onHTTPRequest(method, url string, status int, body []byte, err error) {
	f.httpRequests[requestKey(method, url)] = fakeHTTPRequestResponse{status: status, body: body, err: err}
}

func (f *fakeSystem) Run(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := cmdKey(name, args...)
	f.calls = append(f.calls, key)
	resp, ok := f.responses[key]
	if !ok {
		return "", fmt.Errorf("%s: %w", key, errNotFound)
	}
	return resp.output, resp.err
}

func (f *fakeSystem) HTTPGet(ctx context.Context, url string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.httpCalls = append(f.httpCalls, url)
	resp, ok := f.http[url]
	if !ok {
		return nil, fmt.Errorf("fakeSystem: no response configured for GET %s", url)
	}
	return resp.body, resp.err
}

func (f *fakeSystem) HTTPRequest(ctx context.Context, method, url string, body []byte) (int, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.httpRequestCalls = append(f.httpRequestCalls, fakeHTTPRequestCall{method: method, url: url, body: body})
	resp, ok := f.httpRequests[requestKey(method, url)]
	if !ok {
		return 0, nil, fmt.Errorf("fakeSystem: no response configured for %s %s", method, url)
	}
	return resp.status, resp.body, resp.err
}

func (f *fakeSystem) IsRoot() bool { return f.root }

// calledAny reports whether any call this run made exactly matches key.
func (f *fakeSystem) calledAny(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == key {
			return true
		}
	}
	return false
}
