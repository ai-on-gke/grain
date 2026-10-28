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

package gitproxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/model"
)

// ServerGitRemote is the remote a git command grain runs on this host
// itself -- not a sandbox's, which reaches its repository through the git
// proxy -- names for a repository only its host's own tool reaches: a
// forge behind a front end, where plain `git clone https://<host>/...`
// is redirected to a login page (AmbientCommandSource.ForgeViaCommand).
//
// The route is the one the proxy's own upstream hop takes for that host
// (GitProxy.forwarderFor): every smart-HTTP request is one `<tool> api`
// run with the whole URL, carrying the credential's token in the shape
// the source says its git endpoints take (CommandForgeTransport.args). git cannot speak through a Go RoundTripper, so it
// is handed a loopback URL instead, served by a listener that exists for
// the one command and forwards only that repository's paths.
//
// URL is what to pass to git in place of https://<host>/<owner>/<repo>.git,
// and Close stops the listener; both are safe to use on a nil remote,
// which is the answer for a repository that needs none of this.
type ServerGitRemote struct {
	URL   string
	close func()

	mu  sync.Mutex
	err error
}

// Close stops the listener. Safe on nil and more than once.
func (r *ServerGitRemote) Close() {
	if r != nil && r.close != nil {
		r.close()
		r.close = nil
	}
}

// Err returns the first upstream forwarding error seen by this listener, if any.
func (r *ServerGitRemote) Err() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *ServerGitRemote) recordErr(err error) {
	if r == nil || err == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err == nil {
		r.err = err
	}
}

// ServerGitRemoteFor is the ServerGitRemote for owner/repo on host with
// credential, or nil when credential's git traffic needs no tool in front
// of it -- the caller's own https URL and askpass are right for it, as
// they always were. The listener lives until Close or ctx ends.
//
// Every server-side git command against a repository (grain CI's
// workflow and history reads, repository declarations, and whatever is
// added next) asks this first, so a command-backed host is reached the same way
// however the command came to be run.
func (c *CredentialSet) ServerGitRemoteFor(ctx context.Context, credential Credential, host, owner, repo string) (*ServerGitRemote, error) {
	if c == nil || credential.Name == "" {
		return nil, nil
	}
	src, h, ok := c.forgeCommand(credential.Name)
	if !ok || h.Proxy != "" {
		// A proxy an operator recorded for the host is their way round
		// the front end, for git as for the proxy's own forwarder.
		return nil, nil
	}
	if !model.SameGitHost(h.Host, host, model.DefaultGitHost) {
		return nil, fmt.Errorf("credential %q is for git host %s, not %s", credential.Name, h.Host, host)
	}
	forwarder := &RealForwarder{Host: h.hostPort(), UseTLS: h.useTLS(), Client: commandForgeClient(src, h, forwardTimeout)}
	prefix := "/" + owner + "/" + repo
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listening for %s/%s/%s's git traffic: %w", host, owner, repo, err)
	}
	remote := &ServerGitRemote{
		URL: "http://" + ln.Addr().String() + prefix + ".git",
	}
	srv := &http.Server{Handler: serverGitHandlerWithErr(forwarder, credential.Token, prefix, remote.recordErr)}
	go func() { _ = srv.Serve(ln) }()
	stop := context.AfterFunc(ctx, func() { srv.Close() })
	remote.close = func() {
		stop()
		srv.Close()
	}
	return remote, nil
}

// serverGitHandler forwards git's smart-HTTP requests under prefix (and
// prefix.git) to forwarder with token, and refuses anything else: the
// listener is on loopback, but it is a credentialed door, and the one
// repository it was opened for is all it opens onto.
func serverGitHandler(forwarder *RealForwarder, token *string, prefix string) http.Handler {
	return serverGitHandlerWithErr(forwarder, token, prefix, nil)
}

func serverGitHandlerWithErr(forwarder *RealForwarder, token *string, prefix string, onErr func(error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if !strings.HasPrefix(path, prefix+".git/") && !strings.HasPrefix(path, prefix+"/") {
			http.Error(w, "not this listener's repository", http.StatusForbidden)
			return
		}
		if strings.Contains(path, "..") {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		headers := map[string]string{}
		for _, k := range []string{"Content-Type", "Accept", "User-Agent", "Content-Encoding", "Git-Protocol"} {
			if v := r.Header.Get(k); v != "" {
				headers[k] = v
			}
		}
		up, err := forwarder.ForwardStream(r.Method, path, r.URL.RawQuery, headers, r.Body, r.ContentLength, token)
		if err != nil {
			if onErr != nil {
				onErr(err)
			}
			// Always answer 502 (never 401): git talks to this loopback URL
			// without credentials in the URL and with GIT_TERMINAL_PROMPT=0,
			// so a 401 makes git discard the response body and fail with
			// "could not read Username for 'http://127.0.0.1:...': terminal
			// prompts disabled" instead of printing the upstream error
			// (such as LoginRedirectError's login remediation) as "remote:".
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		for k, v := range up.Headers {
			if k == "Content-Length" || k == "Transfer-Encoding" || k == "Connection" {
				continue
			}
			w.Header().Set(k, v)
		}
		status := up.Status
		if status == http.StatusUnauthorized {
			status = http.StatusForbidden
		}
		w.WriteHeader(status)
		if up.Stream != nil {
			defer up.Stream.Close()
			_, _ = io.Copy(w, up.Stream)
		} else {
			_, _ = w.Write(up.Body)
		}
	})
}
