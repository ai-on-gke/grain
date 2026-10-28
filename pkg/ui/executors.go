package ui

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/bwsalmon/grain/pkg/apierror"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/modelexec"
	"github.com/bwsalmon/grain/pkg/tunnel/wsupgrade"
)

//go:embed executor_install.sh
var executorInstallScript []byte

// ExecutorLister is implemented by whatever can report the snapshot of
// connected remote executors -- modelexec.Server in a real deployment.
type ExecutorLister interface {
	Connected() []modelexec.Status
}

// handleExecutorInstallScript serves the single-command executor bootstrap and
// execution script.
func (s *Server) handleExecutorInstallScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(executorInstallScript)
}

// executorBootstrapRoute reports whether r is one of remote execution's
// own routes: the install script, the grain binaries it downloads, and
// the connection an executor upgrades. Each authenticates in its own
// protocol or has nothing to keep from anybody: the script and binaries
// are grain's own, the same bytes /install-executor.sh and
// /install/{os}/{arch} have always served anybody, and a connection is
// let in only for the executor token its hello frame carries
// (modelexec.Server.HandleConn), whatever the request's own headers
// say. So none of them asks who the caller is (requiresAccess), and a
// single-tenant daemon serves them without its access code
// (singleTenantOpenRoute) -- and does not count the executor or IAP
// token pkg/modelexec sends as a bearer as a guess at it.
func executorBootstrapRoute(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	switch r.URL.Path {
	case "/_grain/executor", "/api/executors/connect", "/api/executors/stream",
		"/install-executor.sh", "/api/executors/install.sh":
		return true
	}
	if name, ok := strings.CutPrefix(r.URL.Path, "/api/executors/bin/"); ok {
		return pathSegment(name)
	}
	if rest, ok := strings.CutPrefix(r.URL.Path, "/install/"); ok {
		goos, arch, ok := strings.Cut(rest, "/")
		return ok && pathSegment(goos) && pathSegment(arch)
	}
	return false
}

// pathSegment reports whether seg is one plain path segment.
func pathSegment(seg string) bool {
	return seg != "" && seg != "." && seg != ".." && !strings.Contains(seg, "/")
}

// handleListExecutors answers GET /api/executors with the connected remote executors snapshot.
func (s *Server) handleListExecutors(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.Executors == nil {
		writeJSON(w, http.StatusOK, []modelexec.Status{})
		return
	}
	executors := s.tasks.Config.Executors.Connected()
	if executors == nil {
		executors = []modelexec.Status{}
	}
	writeJSON(w, http.StatusOK, executors)
}

type readerConn struct {
	net.Conn
	r io.Reader
}

func (c *readerConn) Read(b []byte) (int, error) {
	return c.r.Read(b)
}

// handleExecutorConnect handles incoming remote executor connections over HTTP Upgrade.
// It upgrades the connection to 101 Switching Protocols and hands the connection
// to the modelexec.Server.
func (s *Server) handleExecutorConnect(w http.ResponseWriter, r *http.Request) {
	if s.tasks == nil || s.tasks.Config.ExecutorServer == nil {
		writeError(w, http.StatusServiceUnavailable, apierror.Public("remote execution is not enabled on this daemon"))
		return
	}

	// Verify HTTP Upgrade request
	if !strings.EqualFold(r.Header.Get("Connection"), "upgrade") && r.Header.Get("Upgrade") == "" {
		writeError(w, http.StatusBadRequest, errors.New("expected HTTP Upgrade request for executor connection"))
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("server does not support connection hijacking"))
		return
	}

	conn, brw, err := hijacker.Hijack()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("hijacking connection: %w", err))
		return
	}
	defer conn.Close()

	upgradeProto := r.Header.Get("Upgrade")
	if upgradeProto == "" {
		upgradeProto = "grain-executor"
	}

	_, _ = fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: %s\r\nConnection: Upgrade\r\n%s\r\n", upgradeProto, wsupgrade.AcceptHeader(r))
	if err := brw.Flush(); err != nil {
		return
	}

	var clientReader io.Reader = conn
	if brw != nil && brw.Reader.Buffered() > 0 {
		clientReader = io.MultiReader(brw.Reader, conn)
	}

	wrappedConn := &readerConn{
		Conn: conn,
		r:    clientReader,
	}

	s.tasks.Config.ExecutorServer.HandleConn(r.Context(), wrappedConn)
}

// ExecutorServer is the daemon side of remote execution that
// /api/executors/connect hands each upgraded connection to --
// modelexec.Server in a real deployment. RevokeToken drops whatever
// executor is connected with the token a revocation just removed.
type ExecutorServer interface {
	HandleConn(ctx context.Context, conn net.Conn)
	RevokeToken(tokenID string)
}

// CreateExecutorTokenRequest is the body of POST /api/executors/tokens.
// An empty Team is the default team.
type CreateExecutorTokenRequest struct {
	Name string `json:"name"`
}

// CreateExecutorTokenResponse is what POST /api/executors/tokens answers
// with: the raw token, which is shown this once and never stored, and the
// record kept in its place.
type CreateExecutorTokenResponse struct {
	Token  string              `json:"token"`
	Record model.ExecutorToken `json:"record"`
}

// ListExecutorTokens lists every executor token, revoked ones included.
// System admins only.
func (c *Client) ListExecutorTokens(ctx context.Context) ([]model.ExecutorToken, error) {
	if c.Store == nil {
		return []model.ExecutorToken{}, nil
	}
	tokens, err := c.Store.ListExecutorTokens(ctx)
	if err != nil {
		return nil, err
	}
	if tokens == nil {
		tokens = []model.ExecutorToken{}
	}
	return tokens, nil
}

// maxExecutorTokenNameLen caps an executor token's name: room for a long
// fully-qualified hostname, but not for a name that would swamp the
// executors list.
const maxExecutorTokenNameLen = 128

// CreateExecutorToken mints an executor token and returns the raw token
// beside its stored record. System admins only.
func (c *Client) CreateExecutorToken(ctx context.Context, name string) (string, *model.ExecutorToken, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil, validationErrorf("executor token name is required")
	}
	if utf8.RuneCountInString(name) > maxExecutorTokenNameLen {
		return "", nil, validationErrorf("executor token name cannot be longer than %d characters", maxExecutorTokenNameLen)
	}
	if c.Store == nil {
		return "", nil, errors.New("ui: store is not configured")
	}
	rawToken, tok, err := model.GenerateExecutorToken(name)
	if err != nil {
		return "", nil, err
	}
	created, err := c.Store.CreateExecutorToken(ctx, tok)
	if err != nil {
		return "", nil, err
	}
	return rawToken, created, nil
}

// RevokeExecutorToken revokes token id and disconnects any executor using
// it. System admins only.
func (c *Client) RevokeExecutorToken(ctx context.Context, id string) (*model.ExecutorToken, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, validationErrorf("executor token id is required")
	}
	if c.Store == nil {
		return nil, errors.New("ui: store is not configured")
	}
	tok, err := c.Store.RevokeExecutorToken(ctx, id)
	if err != nil {
		if errors.Is(err, model.ErrExecutorTokenNotFound) {
			return nil, &NotFoundError{ID: id, message: "no executor token " + id}
		}
		return nil, err
	}
	if c.Config.ExecutorServer != nil {
		c.Config.ExecutorServer.RevokeToken(id)
	}
	return tok, nil
}

func (s *Server) handleListExecutorTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.tasks.ListExecutorTokens(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

func (s *Server) handleCreateExecutorToken(w http.ResponseWriter, r *http.Request) {
	var req CreateExecutorTokenRequest
	if !readJSONRefusing(w, r, &req, retiredTeamField) {
		return
	}
	rawToken, record, err := s.tasks.CreateExecutorToken(r.Context(), req.Name)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, CreateExecutorTokenResponse{
		Token:  rawToken,
		Record: *record,
	})
}

func (s *Server) handleRevokeExecutorToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		id = strings.TrimPrefix(r.URL.Path, "/api/executors/tokens/")
		id = strings.TrimSuffix(id, "/revoke")
		id = strings.TrimPrefix(id, "/")
	}
	id = strings.TrimSpace(id)
	record, err := s.tasks.RevokeExecutorToken(r.Context(), id)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, record)
}

// executorBinaryNameRE is the set of file names /api/executors/bin/{binary}
// will look up: "grain" or "grain-<os>-<arch>", optionally with ".exe".
// Anything else is refused before it reaches the filesystem, so the endpoint
// cannot be used to read arbitrary files out of the search directories.
var executorBinaryNameRE = regexp.MustCompile(`^grain(-[a-z0-9]+-[a-z0-9]+)?(\.exe)?$`)

// executorPlatformRE bounds the {os} and {arch} segments of /install/{os}/{arch}.
var executorPlatformRE = regexp.MustCompile(`^[a-z0-9]+$`)

// executorChecksumSuffix, appended to either binary download path, asks for
// the binary's SHA-256 instead of the binary itself. run-remote-executor.sh
// fetches it beside every download and refuses a binary that does not match.
const executorChecksumSuffix = ".sha256"

// handleExecutorBinary serves prebuilt or current grain binaries at
// /api/executors/bin/{binary}, and their checksums at
// /api/executors/bin/{binary}.sha256.
func (s *Server) handleExecutorBinary(w http.ResponseWriter, r *http.Request) {
	binaryName, checksum := strings.CutSuffix(r.PathValue("binary"), executorChecksumSuffix)
	if !executorBinaryNameRE.MatchString(binaryName) {
		writeError(w, http.StatusBadRequest, errors.New("invalid binary name"))
		return
	}

	targetOS, targetArch := parseBinaryOSArch(binaryName)
	path, err := findGrainBinary(binaryName, targetOS, targetArch)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("grain binary %q not found", binaryName))
		return
	}

	if checksum {
		s.serveBinaryChecksum(w, path, binaryName)
		return
	}
	s.serveBinary(w, r, path, binaryName)
}

// handleExecutorBinaryByArch serves prebuilt or current grain binaries at
// /install/{os}/{arch}, and their checksums at /install/{os}/{arch}.sha256.
func (s *Server) handleExecutorBinaryByArch(w http.ResponseWriter, r *http.Request) {
	osName := strings.ToLower(r.PathValue("os"))
	archName, checksum := strings.CutSuffix(strings.ToLower(r.PathValue("arch")), executorChecksumSuffix)
	if !executorPlatformRE.MatchString(osName) || !executorPlatformRE.MatchString(archName) {
		writeError(w, http.StatusBadRequest, errors.New("invalid os or arch"))
		return
	}

	binaryName := fmt.Sprintf("grain-%s-%s", osName, archName)

	path, err := findGrainBinary(binaryName, osName, archName)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("grain binary for %s/%s not found", osName, archName))
		return
	}

	if checksum {
		s.serveBinaryChecksum(w, path, binaryName)
		return
	}
	s.serveBinary(w, r, path, binaryName)
}

// serveBinaryChecksum answers with binaryPath's SHA-256 in sha256sum's own
// "<hex>  <name>" format, so a client can check it with sha256sum -c.
func (s *Server) serveBinaryChecksum(w http.ResponseWriter, binaryPath, downloadName string) {
	f, err := os.Open(binaryPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusNotFound, fmt.Errorf("grain binary %q not found", downloadName))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(h.Sum(nil)), downloadName)
}

func (s *Server) serveBinary(w http.ResponseWriter, r *http.Request, binaryPath, downloadName string) {
	f, err := os.Open(binaryPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusNotFound, fmt.Errorf("grain binary %q not found", downloadName))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", downloadName))
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, downloadName, fi.ModTime(), f)
}

func parseBinaryOSArch(name string) (osName, archName string) {
	base := strings.TrimPrefix(name, "grain-")
	base = strings.TrimSuffix(base, ".exe")
	parts := strings.Split(base, "-")
	if len(parts) == 2 {
		return strings.ToLower(parts[0]), strings.ToLower(parts[1])
	}
	return "", ""
}

// grainInstallDirs are the fixed places an installed grain lives, searched
// after the GRAIN_BIN_DIR-style overrides and before the running binary's
// own directory: a grain installed in one of them is served ahead of the
// daemon's own executable.
var grainInstallDirs = []string{
	"/var/lib/grain/data/bin",
	"/var/cache/grain/bin",
	"/usr/local/bin",
}

func findGrainBinary(binaryName, targetOS, targetArch string) (string, error) {
	isCurrentHost := (targetOS == runtime.GOOS && targetArch == runtime.GOARCH) ||
		(targetOS == "" && targetArch == "" && (binaryName == "grain" || binaryName == fmt.Sprintf("grain-%s-%s", runtime.GOOS, runtime.GOARCH)))

	var candidateNames []string
	if binaryName != "" {
		candidateNames = append(candidateNames, binaryName)
	}
	if targetOS != "" && targetArch != "" {
		archBin := fmt.Sprintf("grain-%s-%s", targetOS, targetArch)
		if archBin != binaryName {
			candidateNames = append(candidateNames, archBin)
		}
	}
	if isCurrentHost {
		if binaryName != "grain" {
			candidateNames = append(candidateNames, "grain")
		}
		hostArchBin := fmt.Sprintf("grain-%s-%s", runtime.GOOS, runtime.GOARCH)
		if hostArchBin != binaryName {
			candidateNames = append(candidateNames, hostArchBin)
		}
	}

	var candidateDirs []string
	for _, envKey := range []string{"GRAIN_BIN_DIR", "GRAIN_BINARY_STORAGE", "GRAIN_BIN_CACHE", "GRAIN_DIST_DIR"} {
		if val := os.Getenv(envKey); val != "" {
			candidateDirs = append(candidateDirs, val)
		}
	}

	candidateDirs = append(candidateDirs, grainInstallDirs...)

	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidateDirs = append(candidateDirs, filepath.Join(home, ".grain", "bin"))
	}

	if self, err := os.Executable(); err == nil && self != "" {
		candidateDirs = append(candidateDirs, filepath.Dir(self))
	}

	// Deliberately not "." -- the daemon's working directory holds
	// config and state that must never be served.
	candidateDirs = append(candidateDirs, "bin", "dist")

	for _, dir := range candidateDirs {
		if dir == "" {
			continue
		}
		for _, name := range candidateNames {
			p := filepath.Join(dir, name)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p, nil
			}
		}
	}

	if isCurrentHost {
		if envBin := os.Getenv("GRAIN_BIN"); envBin != "" {
			if fi, err := os.Stat(envBin); err == nil && !fi.IsDir() {
				return envBin, nil
			}
		}

		if self, err := os.Executable(); err == nil && self != "" {
			if fi, err := os.Stat(self); err == nil && !fi.IsDir() {
				return self, nil
			}
		}

		if lp, err := exec.LookPath("grain"); err == nil && lp != "" {
			if fi, err := os.Stat(lp); err == nil && !fi.IsDir() {
				return lp, nil
			}
		}
	}

	return "", fmt.Errorf("no grain binary found for %q", binaryName)
}

// executorDialAddr is the listener address /api/config reports for a
// worker to dial: bound as given, except that a wildcard host (":8422",
// "0.0.0.0:8422", "[::]:8422") is dropped to ":port", because the
// machine's own name for itself is not one a worker can reach it by --
// the pane fills in the hostname the browser used. "" stays "".
func executorDialAddr(bound string) string {
	if bound == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(bound)
	if err != nil {
		return bound
	}
	if host == "" {
		return ":" + port
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return ":" + port
	}
	return net.JoinHostPort(host, port)
}
