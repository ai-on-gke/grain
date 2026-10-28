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

// The SSH transport: serving a sandbox's smart-HTTP request by running
// git-upload-pack or git-receive-pack on the credential's host over SSH.
//
// Sandboxes always speak HTTP to the proxy, and HTTP smart git is
// stateless: every request stands alone. SSH git is a conversation on one
// connection -- the server advertises first, then reads. So each HTTP
// request gets an SSH session of its own that replays that conversation:
//
//   - GET info/refs?service=git-X runs git-X, reads its advertisement up
//     to the flush-pkt that ends it, and answers with it (behind the
//     "# service=" header for protocol v0, as git http-backend does).
//   - POST git-X runs git-X again, reads and discards the advertisement,
//     writes the request body, closes stdin, and streams what the server
//     writes back as the response.
//
// That is protocol v2 fetch exactly: each v2 command stands alone, and
// the server ends at EOF. It is v0 fetch too: the stateless client
// resends its wants and every have on each round, which a fresh session
// reads as a whole negotiation, answering the same ACK/NAK lines and
// then, on EOF, ending. A push is one round in either protocol: the
// command list and the pack, then the server's report-status.
//
// GIT_PROTOCOL rides from the sandbox's Git-Protocol header to the
// session's environment. A server that will not take it answers v0, and
// both requests of a fetch then see the same v0 conversation.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshDialTimeout bounds connecting and authenticating to the SSH host.
const sshDialTimeout = 30 * time.Second

// maxSSHStderrBytes is how much of the remote's stderr a refusal quotes.
const maxSSHStderrBytes = 4 << 10

// forwardSSH answers req by running it over SSH with credential, and
// records the outcome in the audit log the way an HTTPS forward is.
func (p *GitProxy) forwardSSH(ctx context.Context, sandbox string, req GitRequest, method, query string,
	headers map[string]string, body []byte, credential Credential) ProxyResponse {
	resp, refusal, err := SSHForward(ctx, credential, req.Owner, req.Repo, req.Action, query, headers, body,
		p.maxBodyBytes(req.Action))
	record := func(outcome string) {
		p.audit().Record(AuditEntry{
			Time: p.now(), Sandbox: sandbox, Host: req.Host, Owner: req.Owner, Repo: req.Repo,
			Action: req.Action, Credential: credential.Name, Outcome: outcome,
		})
	}
	if err != nil {
		record(fmt.Sprintf("error: ssh to %s failed: %v", credential.SSH.Addr, err))
		return Denial(502, fmt.Sprintf("reaching %s/%s over SSH at %s with credential %q failed: %v",
			req.Owner, req.Repo, credential.SSH.Addr, credential.Name, err))
	}
	if refusal != "" {
		record("denied by ssh host: " + refusal)
		status := 403
		if strings.Contains(strings.ToLower(refusal), "not found") || strings.Contains(strings.ToLower(refusal), "does not exist") {
			status = 404
		}
		msg := fmt.Sprintf("%s refused %s/%s for SSH credential %q: %s", credential.SSH.Host, req.Owner, req.Repo,
			credential.Name, refusal)
		if team, ok := p.SandboxTeams.Team(sandbox); ok {
			msg += "\n" + TeamCredentialRefusedNote(team, req.Owner+"/"+req.Repo)
		}
		return Denial(status, msg)
	}
	record(fmt.Sprintf("forwarded over ssh: %d", resp.Status))
	return ProxyResponse(resp)
}

// SSHForward runs one smart-HTTP request (action and query as ParsePath
// and the URL give them) against owner/repo on credential's SSH host.
// refusal is the remote's own explanation when it would not serve the
// repository at all (stderr from a session that advertised nothing); err
// is a failure to reach or trust the host. maxBody bounds an inflated
// gzip body.
func SSHForward(ctx context.Context, credential Credential, owner, repo, action, query string,
	headers map[string]string, body []byte, maxBody int64) (resp UpstreamResponse, refusal string, err error) {
	sc := credential.SSH
	if sc == nil {
		return UpstreamResponse{}, "", fmt.Errorf("credential %q is not an SSH key", credential.Name)
	}
	service := action
	if action == "info/refs" {
		service = serviceParam(query)
	}
	if service != "git-upload-pack" && service != "git-receive-pack" {
		return UpstreamResponse{}, "", fmt.Errorf("unsupported git service %q", service)
	}
	if strings.ContainsAny(owner+repo, "'\\ \t\r\n") {
		return UpstreamResponse{}, "", fmt.Errorf("refusing to quote repository %s/%s into a remote command", owner, repo)
	}
	if action != "info/refs" {
		if body, err = inflateBody(body, headers["Content-Encoding"], maxBody); err != nil {
			return UpstreamResponse{}, "", err
		}
	}

	client, err := dialSSH(ctx, sc)
	if err != nil {
		return UpstreamResponse{}, "", err
	}
	session, err := client.NewSession()
	if err != nil {
		client.Close()
		return UpstreamResponse{}, "", fmt.Errorf("opening an SSH session: %w", err)
	}
	closeAll := func() {
		session.Close()
		client.Close()
	}
	if gp := gitProtocolEnv(headers["Git-Protocol"]); gp != "" {
		// Refused by a server whose sshd does not AcceptEnv it, which
		// then speaks v0 -- what the client gets either way agrees.
		_ = session.Setenv("GIT_PROTOCOL", gp)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		closeAll()
		return UpstreamResponse{}, "", err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		closeAll()
		return UpstreamResponse{}, "", err
	}
	stderr := &cappedBuffer{limit: maxSSHStderrBytes}
	session.Stderr = stderr
	path := sc.PathPrefix + owner + "/" + strings.TrimSuffix(repo, ".git") + ".git"
	if err := session.Start(fmt.Sprintf("%s '%s'", service, path)); err != nil {
		closeAll()
		return UpstreamResponse{}, "", fmt.Errorf("starting %s: %w", service, err)
	}

	out := bufio.NewReaderSize(stdout, 64<<10)
	advertisement, v2, err := readAdvertisement(out)
	if err != nil {
		stdin.Close()
		_ = session.Wait()
		closeAll()
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return UpstreamResponse{}, msg, nil
		}
		if msg, ok := strings.CutPrefix(err.Error(), "ERR "); ok {
			return UpstreamResponse{}, msg, nil
		}
		return UpstreamResponse{}, "", fmt.Errorf("reading %s's advertisement: %w", service, err)
	}

	if action == "info/refs" {
		stdin.Close()
		closeAll()
		var buf bytes.Buffer
		if !v2 {
			writePktLine(&buf, "# service="+service+"\n")
			buf.WriteString("0000")
		}
		buf.Write(advertisement)
		return UpstreamResponse{
			Status: 200,
			Headers: map[string]string{
				"Content-Type":  "application/x-" + service + "-advertisement",
				"Cache-Control": "no-cache",
			},
			Body: buf.Bytes(),
		}, "", nil
	}

	go func() {
		_, _ = stdin.Write(body)
		stdin.Close()
	}()
	return UpstreamResponse{
		Status: 200,
		Headers: map[string]string{
			"Content-Type":  "application/x-" + service + "-result",
			"Cache-Control": "no-cache",
		},
		Stream: &sshStream{r: out, close: func() {
			_ = session.Wait()
			closeAll()
		}},
	}, "", nil
}

// dialSSH connects to sc's host, accepting only a host key it pins and
// offering only those keys' algorithms, and authenticates with its key.
func dialSSH(ctx context.Context, sc *SSHCredential) (*ssh.Client, error) {
	var algorithms []string
	for _, k := range sc.hostKeys {
		for _, alg := range hostKeyAlgorithms(k.Type()) {
			if !containsString(algorithms, alg) {
				algorithms = append(algorithms, alg)
			}
		}
	}
	config := &ssh.ClientConfig{
		User:              sc.User,
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(sc.signer)},
		HostKeyAlgorithms: algorithms,
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			for _, pinned := range sc.hostKeys {
				if bytes.Equal(pinned.Marshal(), key.Marshal()) {
					return nil
				}
			}
			return fmt.Errorf("%s presented host key %s, which is not one pinned for it -- refusing to connect "+
				"(if the server's key really changed, update known_hosts on the credential)",
				sc.Addr, ssh.FingerprintSHA256(key))
		},
		Timeout: sshDialTimeout,
	}
	dialer := net.Dialer{Timeout: sshDialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", sc.Addr)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(sshDialTimeout))
	c, chans, reqs, err := ssh.NewClientConn(conn, sc.Addr, config)
	if err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

// hostKeyAlgorithms is the host key algorithms a key of type keyType
// verifies: an RSA key signs with SHA-2 as well as the legacy SHA-1.
func hostKeyAlgorithms(keyType string) []string {
	if keyType == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{keyType}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// serviceParam is the service= value of an info/refs query.
func serviceParam(query string) string {
	for _, part := range strings.Split(query, "&") {
		if v, ok := strings.CutPrefix(part, "service="); ok {
			return v
		}
	}
	return ""
}

// gitProtocolEnv is the GIT_PROTOCOL value a Git-Protocol header asks
// for, kept to the key=value[:key=value] shape git itself sends.
func gitProtocolEnv(header string) string {
	header = strings.TrimSpace(header)
	for _, r := range header {
		if !(r == '=' || r == ':' || r == '.' || r == '-' || r == '_' ||
			r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return ""
		}
	}
	return header
}

// inflateBody is body as the git service reads it: gzip removed when the
// client sent it compressed, bounded by limit.
func inflateBody(body []byte, contentEncoding string, limit int64) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(contentEncoding)) {
	case "", "identity":
		return body, nil
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("reading gzip-encoded body: %w", err)
		}
		data, err := io.ReadAll(io.LimitReader(zr, limit+1))
		if err != nil {
			return nil, fmt.Errorf("reading gzip-encoded body: %w", err)
		}
		if int64(len(data)) > limit {
			return nil, errBodyTooLarge
		}
		return data, nil
	default:
		return nil, fmt.Errorf("unsupported Content-Encoding %q", contentEncoding)
	}
}

// readAdvertisement reads pkt-lines up to and including the flush-pkt
// that ends a service's advertisement, returning them as read and
// whether the server spoke protocol v2. An "ERR" line is an error.
func readAdvertisement(r *bufio.Reader) ([]byte, bool, error) {
	var buf bytes.Buffer
	first := true
	v2 := false
	for {
		var head [4]byte
		if _, err := io.ReadFull(r, head[:]); err != nil {
			return nil, false, err
		}
		n, err := strconv.ParseUint(string(head[:]), 16, 16)
		if err != nil {
			return nil, false, fmt.Errorf("malformed pkt-line length %q", head)
		}
		buf.Write(head[:])
		if n == 0 {
			return buf.Bytes(), v2, nil
		}
		if n < 4 {
			// A delim or response-end packet has no place in an
			// advertisement.
			return nil, false, fmt.Errorf("unexpected special packet %q in advertisement", head)
		}
		line := make([]byte, n-4)
		if _, err := io.ReadFull(r, line); err != nil {
			return nil, false, err
		}
		if msg, ok := bytes.CutPrefix(line, []byte("ERR ")); ok {
			return nil, false, fmt.Errorf("ERR %s", bytes.TrimSpace(msg))
		}
		if first {
			v2 = string(bytes.TrimSpace(line)) == "version 2"
			first = false
		}
		buf.Write(line)
	}
}

// writePktLine writes s as one pkt-line.
func writePktLine(w *bytes.Buffer, s string) {
	fmt.Fprintf(w, "%04x%s", len(s)+4, s)
}

// sshStream is a POST's response body: the remote's stdout, with the
// session closed when the reader is done with it.
type sshStream struct {
	r     io.Reader
	once  sync.Once
	close func()
}

func (s *sshStream) Read(p []byte) (int, error) { return s.r.Read(p) }

func (s *sshStream) Close() error {
	s.once.Do(s.close)
	return nil
}

// cappedBuffer keeps the first limit bytes written to it.
type cappedBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.limit - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
