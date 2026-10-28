package gitproxy

// Smart-HTTP access to a repository owned by the grain controller. The
// protocol is delegated to git-http-backend, and every push is checked by
// receive-pack under receiveHardening below rather than under git's own
// defaults, which validate nothing a sandbox sends.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/bwsalmon/grain/pkg/model"
)

// receiveHardening is the receive-pack configuration every request to a
// local repository runs under, passed through GIT_CONFIG_* so it holds no
// matter what the repository's own config says and is not something a
// change to that config can turn off.
//
// The repository is grain's own state: the daemon imports what lands in it.
// git's defaults accept any object a pack carries without parsing it
// (receive.fsckObjects is off), so a sandbox could push trees git itself
// would refuse to write -- entries named .git or .., duplicate or
// unsorted names, malformed modes -- and commits whose headers do not
// parse, all of which later readers of the checkout trust. fsckObjects
// makes index-pack check every object it receives and refuse the push
// on the first bad one, before any ref moves. Strict, as receive-pack
// runs it, refuses everything git ranks a warning or worse -- but git
// ranks a file mode it does not know (badFilemode: 100666, 123456) as
// only "info", which strict mode still lets through, so that one is
// raised to an error by name. receive.fsck.<id> is how; an id git does
// not know is fatal, and this one has existed since the setting did.
//
// The other three keep a sandbox off the branch the controller has
// checked out (its state) whatever this repository's config happens to
// say: no update to it, no deletion of it, and nothing about either
// left to a default.
var receiveHardening = [][2]string{
	{"receive.fsckObjects", "true"},
	{"transfer.fsckObjects", "true"},
	{"receive.fsck.badFilemode", "error"},
	{"receive.denyCurrentBranch", "refuse"},
	{"receive.denyDeleteCurrent", "refuse"},
}

func receiveHardeningEnv() []string {
	env := []string{"GIT_CONFIG_COUNT=" + strconv.Itoa(len(receiveHardening))}
	for i, kv := range receiveHardening {
		env = append(env,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, kv[0]),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, kv[1]))
	}
	return env
}

// LocalRepository is a repository the grain controller owns on its own
// disk -- grain's state repository -- served through git http-backend
// instead of being forwarded to GitHub. Owner and Name are its stable
// alias and Dir its checkout. It is the only repository served locally:
// the per-team grain/config-<team> repositories that used to be served
// beside it are gone (RetiredConfigRepo).
type LocalRepository struct {
	Owner   string
	Name    string
	Dir     string
	Enabled func() bool
}

// Matches reports whether (owner, name) is served locally: the alias,
// compared case-insensitively as GitHub compares names.
func (r *LocalRepository) Matches(owner, name string) bool {
	if r == nil || r.Dir == "" || (r.Enabled != nil && !r.Enabled()) {
		return false
	}
	return strings.EqualFold(r.Owner, owner) && strings.EqualFold(r.Name, name)
}

// DisabledFor reports whether (owner, name) names this local repository
// while local hosting is disabled (for example, when an external
// configuration repository is configured). Checked ahead of authorization
// and upstream forwarding so a disabled local alias is refused outright
// rather than forwarded to a forge's grain/ owner.
func (r *LocalRepository) DisabledFor(owner, name string) bool {
	if r == nil || r.Dir == "" || r.Enabled == nil || r.Enabled() {
		return false
	}
	return strings.EqualFold(r.Owner, owner) && strings.EqualFold(r.Name, name)
}

// RetiredConfigRepo reports whether (owner, name) on the default host is
// shaped like one of the per-team configuration repositories
// (grain/config-<team>, grain/config-default) a multi-team grain served
// beside grain/config. No forge holds them and this controller keeps none
// of them, so the proxy answers them not found -- to every sandbox,
// whatever its scope says -- rather than forwarding a request for the
// virtual grain/ owner to a forge with this deployment's credential.
func RetiredConfigRepo(host, owner, name string) bool {
	config := model.ConfigRepo()
	return host == "" && strings.EqualFold(owner, config.Owner) &&
		len(name) > len(config.Name) && strings.EqualFold(name[:len(config.Name)+1], config.Name+"-")
}

// RetiredConfigRepoReason is what a request for a RetiredConfigRepo is
// told.
func RetiredConfigRepoReason(owner, name string) string {
	return fmt.Sprintf("%s/%s not found: only %s exists", owner, name, model.ConfigRepo())
}

// Forward is ForwardRepo for the repository's own alias.
func (r *LocalRepository) Forward(method, action, query string, headers map[string]string, body []byte) (UpstreamResponse, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	return r.ForwardRepoStream(r.Owner, r.Name, method, action, query, headers, reader, int64(len(body)))
}

// ForwardRepo answers one smart-HTTP request for (owner, name) by running
// git http-backend against its directory under receiveHardening. The
// caller has already authorized the request.
func (r *LocalRepository) ForwardRepo(owner, name, method, action, query string, headers map[string]string, body []byte) (UpstreamResponse, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	return r.ForwardRepoStream(owner, name, method, action, query, headers, reader, int64(len(body)))
}

// ForwardRepoStream answers one smart-HTTP request for (owner, name) reading
// the request body from reader.
func (r *LocalRepository) ForwardRepoStream(owner, name, method, action, query string, headers map[string]string, body io.Reader, contentLength int64) (UpstreamResponse, error) {
	if r == nil || r.Dir == "" || (r.Enabled != nil && !r.Enabled()) {
		return UpstreamResponse{Status: http.StatusNotFound, Body: []byte("local config repository is disabled when an external config repository is configured\n")}, nil
	}
	dir := r.Dir
	cmd := exec.Command("git", "http-backend")
	cmd.Env = append(cmd.Environ(),
		"GIT_PROJECT_ROOT="+filepath.Dir(dir),
		"GIT_HTTP_EXPORT_ALL=1",
		"PATH_INFO=/"+filepath.Base(dir)+"/"+action,
		"REQUEST_METHOD="+method,
		"QUERY_STRING="+query,
		"CONTENT_TYPE="+headers["Content-Type"],
		// git gzips a large upload-pack request body (RealForwarder's
		// own comment on Content-Encoding); http-backend only inflates
		// it when this says so.
		"HTTP_CONTENT_ENCODING="+headers["Content-Encoding"],
		"CONTENT_LENGTH="+strconv.FormatInt(contentLength, 10),
		"REMOTE_USER=grain-sandbox",
	)
	cmd.Env = append(cmd.Env, receiveHardeningEnv()...)
	if body != nil {
		cmd.Stdin = body
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return UpstreamResponse{}, fmt.Errorf("git http-backend: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return UpstreamResponse{}, fmt.Errorf("git http-backend: %w", err)
	}
	stream := &cgiStream{cmd: cmd, r: bufio.NewReader(stdout)}
	h, err := textproto.NewReader(stream.r).ReadMIMEHeader()
	if err != nil {
		// Output that ends before the headers do is most often
		// http-backend refusing the request outright and exiting, and
		// its exit status is the more useful of the two errors. Anything
		// else is output that is not CGI headers at all, from a process
		// that may still be running.
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			if waitErr := stream.wait(); waitErr != nil {
				return UpstreamResponse{}, fmt.Errorf("git http-backend: %w", waitErr)
			}
		} else {
			stream.abort()
		}
		return UpstreamResponse{}, fmt.Errorf("reading git http-backend headers: %w", err)
	}
	status := http.StatusOK
	if value := h.Get("Status"); value != "" {
		status, err = strconv.Atoi(strings.Fields(value)[0])
		if err != nil {
			stream.abort()
			return UpstreamResponse{}, fmt.Errorf("reading git http-backend status: %w", err)
		}
		h.Del("Status")
	}
	headersOut := make(map[string]string, len(h))
	for key := range h {
		headersOut[key] = h.Get(key)
	}
	return UpstreamResponse{Status: status, Headers: headersOut, Stream: stream}, nil
}

// cgiStream is the body of git http-backend's response: the rest of its
// stdout once the CGI headers have been read off it. Reaching the end
// reaps the process, and a non-zero exit there is reported as the read's
// error rather than as io.EOF, so a response that ends because
// http-backend failed is not taken for a complete one. Closing it before
// the end -- the client went away mid-clone -- kills the process instead
// of leaving it writing to a pipe nobody reads.
type cgiStream struct {
	cmd     *exec.Cmd
	r       *bufio.Reader
	once    sync.Once
	waitErr error
	ended   bool
}

func (s *cgiStream) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if err == io.EOF {
		s.ended = true
		if waitErr := s.wait(); waitErr != nil {
			return n, fmt.Errorf("git http-backend: %w", waitErr)
		}
	}
	return n, err
}

func (s *cgiStream) Close() error {
	if !s.ended {
		s.abort()
	}
	return nil
}

// abort kills the process, if it is still running, and reaps it,
// returning what Wait said.
func (s *cgiStream) abort() error {
	_ = s.cmd.Process.Kill()
	return s.wait()
}

func (s *cgiStream) wait() error {
	s.once.Do(func() { s.waitErr = s.cmd.Wait() })
	return s.waitErr
}
