package pkgcache

// The Go build cache half: serving GOCACHEPROG action and output objects
// under /gocache/ so compiled packages and test objects are shared across
// sandboxes and CI VMs.
//
// Both action mappings (/gocache/action/<actionIDHex>) and compiled output
// blobs (/gocache/output/<outputIDHex>) are stored in Cache under
// KindGoBuild, so they participate in the same content-addressed disk store,
// size accounting, Flush, and LRU eviction as every other ecosystem.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// goCachePrefix is the HTTP route prefix for the Go build cache.
	goCachePrefix = "/gocache/"
	// goCacheActionPrefix is where GOCACHEPROG action metadata is stored and queried.
	goCacheActionPrefix = "/gocache/action/"
	// goCacheOutputPrefix is where GOCACHEPROG compiled output objects are stored and fetched.
	goCacheOutputPrefix = "/gocache/output/"

	// GoCacheProgPath is where the standalone GOCACHEPROG client helper binary
	// is compiled inside a sandbox when the package cache is configured.
	GoCacheProgPath = "/tmp/grain-gocacheprog"
	// GoCacheProgLocalDir is the default local on-disk directory inside a
	// sandbox where GoCacheProgPath keeps compiled objects for DiskPath.
	GoCacheProgLocalDir = "/tmp/grain-gocache"
)

// GoCacheAction is the metadata stored for one Go build action ID.
type GoCacheAction struct {
	OutputID string    `json:"OutputID"`
	Size     int64     `json:"Size"`
	Time     time.Time `json:"Time"`
}

// GoCacheProgCmd is a command in Go's cmd/go/internal/cacheprog protocol.
type GoCacheProgCmd string

const (
	GoCacheProgCmdGet   GoCacheProgCmd = "get"
	GoCacheProgCmdPut   GoCacheProgCmd = "put"
	GoCacheProgCmdClose GoCacheProgCmd = "close"
)

// GoCacheProgRequest is one request sent from cmd/go to GOCACHEPROG on stdin.
type GoCacheProgRequest struct {
	ID       int64          `json:"ID"`
	Command  GoCacheProgCmd `json:"Command"`
	ActionID []byte         `json:"ActionID,omitempty"`
	OutputID []byte         `json:"OutputID,omitempty"`
	BodySize int64          `json:"BodySize,omitempty"`
}

// GoCacheProgResponse is one response sent from GOCACHEPROG to cmd/go on stdout.
type GoCacheProgResponse struct {
	ID            int64            `json:"ID"`
	Err           string           `json:"Err,omitempty"`
	KnownCommands []GoCacheProgCmd `json:"KnownCommands,omitempty"`
	Miss          bool             `json:"Miss,omitempty"`
	OutputID      []byte           `json:"OutputID,omitempty"`
	Size          int64            `json:"Size,omitempty"`
	Time          *time.Time       `json:"Time,omitempty"`
	DiskPath      string           `json:"DiskPath,omitempty"`
}

func goCacheActionKey(actionIDHex string) string {
	return "gocache/action/" + actionIDHex
}

func goCacheTrustedActionKey(actionIDHex string) string {
	return "gocache/trusted/action/" + actionIDHex
}

func goCacheUntrustedActionKey(actionIDHex string) string {
	return "gocache/untrusted/action/" + actionIDHex
}

func goCacheOutputKey(outputIDHex string) string {
	return "gocache/output/" + outputIDHex
}

func goCacheProgValue(addr string) string {
	return fmt.Sprintf("%s http://%s", GoCacheProgPath, addr)
}

// isValidHexID reports whether s is a valid lower-case hexadecimal identifier.
// When wantBytes > 0, s must decode to exactly wantBytes bytes; when wantBytes == 0,
// any non-empty hex string up to 64 bytes (128 hex characters) is accepted.
func isValidHexID(s string, wantBytes int) bool {
	if s == "" || len(s)%2 != 0 || len(s) > 128 {
		return false
	}
	if wantBytes > 0 && len(s) != wantBytes*2 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// serveGoCache handles requests under /gocache/.
func (s *Server) serveGoCache(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.URL.EscapedPath(), "..") || strings.Contains(r.URL.Path, "..") {
		s.refuse(w, r, http.StatusBadRequest, "grain's package cache will not follow a path containing \"..\".")
		return
	}
	path := r.URL.Path
	pathNS := ""
	switch {
	case strings.HasPrefix(path, "/gocache/trusted/"):
		pathNS = "trusted"
		path = "/gocache/" + strings.TrimPrefix(path, "/gocache/trusted/")
	case strings.HasPrefix(path, "/gocache/untrusted/"):
		pathNS = "untrusted"
		path = "/gocache/" + strings.TrimPrefix(path, "/gocache/untrusted/")
	}
	if rest, ok := strings.CutPrefix(path, goCacheActionPrefix); ok {
		s.serveGoCacheAction(w, r, rest, pathNS)
		return
	}
	if rest, ok := strings.CutPrefix(path, goCacheOutputPrefix); ok {
		s.serveGoCacheOutput(w, r, rest)
		return
	}
	s.refuse(w, r, http.StatusNotFound,
		"grain's package cache serves the Go build cache under /gocache/action/<actionIDHex> and /gocache/output/<outputIDHex>.")
}

// goCacheNamespace resolves the action namespace ("trusted", "untrusted", or ""
// for single-host shared mode) for r. When JobTokens is set, a job cannot
// escalate its own trust via path or header: its token decides.
func (s *Server) goCacheNamespace(r *http.Request, pathNS string) string {
	if s.jobTokens != nil {
		if access, ok := s.jobTokens.Lookup(r); ok && access.Trusted {
			return "trusted"
		}
		return "untrusted"
	}
	if pathNS == "trusted" || pathNS == "untrusted" {
		return pathNS
	}
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Grit-Cache-Trust"))) {
	case "trusted":
		return "trusted"
	case "untrusted":
		return "untrusted"
	}
	if s.splitGoCacheTrust {
		return "untrusted"
	}
	return ""
}

func (s *Server) openGoCacheAction(actionID, ns string) (Object, bool) {
	var keys []string
	switch ns {
	case "trusted":
		keys = []string{goCacheTrustedActionKey(actionID)}
	case "untrusted":
		keys = []string{goCacheTrustedActionKey(actionID), goCacheUntrustedActionKey(actionID)}
	default:
		keys = []string{goCacheActionKey(actionID), goCacheTrustedActionKey(actionID)}
	}
	for _, k := range keys {
		if f, _, ok := s.cache.Open(KindGoBuild, k); ok {
			return f, true
		}
	}
	return nil, false
}

func goCacheWriteActionKey(actionID, ns string) string {
	switch ns {
	case "trusted":
		return goCacheTrustedActionKey(actionID)
	case "untrusted":
		return goCacheUntrustedActionKey(actionID)
	default:
		return goCacheActionKey(actionID)
	}
}

func (s *Server) serveGoCacheAction(w http.ResponseWriter, r *http.Request, rawID, pathNS string) {
	actionID := strings.ToLower(strings.TrimSpace(rawID))
	if !isValidHexID(actionID, 0) {
		s.refuse(w, r, http.StatusBadRequest,
			fmt.Sprintf("grain's package cache expects a hexadecimal action ID under %s, not %q.", goCacheActionPrefix, rawID))
		return
	}
	ns := s.goCacheNamespace(r, pathNS)

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		f, ok := s.openGoCacheAction(actionID, ns)
		if !ok {
			w.Header().Set(cacheStatusHeader, "miss")
			http.Error(w, "action not found", http.StatusNotFound)
			return
		}
		data, err := io.ReadAll(f)
		name := f.Name()
		f.Close()
		var act GoCacheAction
		if err != nil || json.Unmarshal(data, &act) != nil || !isValidHexID(strings.ToLower(act.OutputID), sha256.Size) || act.Size < 0 {
			os.Remove(name)
			os.Remove(name + ".meta")
			w.Header().Set(cacheStatusHeader, "miss")
			http.Error(w, "action not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Header().Set(cacheStatusHeader, "hit")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(data)
		}

	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			s.refuse(w, r, http.StatusBadRequest, fmt.Sprintf("grain's package cache could not read action %s: %v", actionID, err))
			return
		}
		var act GoCacheAction
		if err := json.Unmarshal(body, &act); err != nil {
			s.refuse(w, r, http.StatusBadRequest, fmt.Sprintf("grain's package cache could not parse action %s JSON: %v", actionID, err))
			return
		}
		act.OutputID = strings.ToLower(strings.TrimSpace(act.OutputID))
		if !isValidHexID(act.OutputID, sha256.Size) {
			s.refuse(w, r, http.StatusBadRequest, fmt.Sprintf("grain's package cache refused action %s: OutputID %q is not a 64-character hex SHA-256 digest.", actionID, act.OutputID))
			return
		}
		if act.Size < 0 {
			s.refuse(w, r, http.StatusBadRequest, fmt.Sprintf("grain's package cache refused action %s: negative size %d.", actionID, act.Size))
			return
		}
		if act.Time.IsZero() {
			act.Time = time.Now().UTC()
		}
		encoded, err := json.Marshal(act)
		if err != nil {
			s.refuse(w, r, http.StatusInternalServerError, fmt.Sprintf("grain's package cache could not encode action %s: %v", actionID, err))
			return
		}
		entry, err := s.cache.NewWriter(KindGoBuild, goCacheWriteActionKey(actionID, ns))
		if err != nil {
			s.refuse(w, r, http.StatusInternalServerError, fmt.Sprintf("grain's package cache could not store action %s: %v", actionID, err))
			return
		}
		defer entry.Abort()
		_, _ = entry.Write(encoded)
		if !entry.Commit(Meta{URL: goCacheActionPrefix + actionID, ContentType: "application/json"}) {
			s.refuse(w, r, http.StatusInternalServerError, fmt.Sprintf("grain's package cache could not commit action %s", actionID))
			return
		}
		w.Header().Set(cacheStatusHeader, "stored")
		w.WriteHeader(http.StatusOK)

	default:
		s.refuse(w, r, http.StatusMethodNotAllowed,
			fmt.Sprintf("this is grain's Go build cache: it answers GET, HEAD and PUT, not %s.", r.Method))
	}
}

func (s *Server) serveGoCacheOutput(w http.ResponseWriter, r *http.Request, rawID string) {
	outputID := strings.ToLower(strings.TrimSpace(rawID))
	if !isValidHexID(outputID, sha256.Size) {
		s.refuse(w, r, http.StatusBadRequest,
			fmt.Sprintf("grain's package cache expects a 64-character hex SHA-256 output ID under %s, not %q.", goCacheOutputPrefix, rawID))
		return
	}

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		f, meta, ok := s.cache.Open(KindGoBuild, goCacheOutputKey(outputID))
		if !ok {
			w.Header().Set(cacheStatusHeader, "miss")
			http.Error(w, "output not found", http.StatusNotFound)
			return
		}
		defer f.Close()

		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil || hex.EncodeToString(h.Sum(nil)) != outputID {
			name := f.Name()
			f.Close()
			os.Remove(name)
			os.Remove(name + ".meta")
			s.logf("package cache: cached Go build output %s failed SHA-256 verification -- removing corrupted entry", outputID)
			w.Header().Set(cacheStatusHeader, "miss")
			http.Error(w, "output not found", http.StatusNotFound)
			return
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			w.Header().Set(cacheStatusHeader, "miss")
			http.Error(w, "output not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
		w.Header().Set(cacheStatusHeader, "hit")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = io.Copy(w, f)
		}

	case http.MethodPut:
		entry, err := s.cache.NewWriter(KindGoBuild, goCacheOutputKey(outputID))
		if err != nil {
			s.refuse(w, r, http.StatusInternalServerError, fmt.Sprintf("grain's package cache could not store output %s: %v", outputID, err))
			return
		}
		defer entry.Abort()

		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(entry, h), r.Body)
		if err != nil {
			s.refuse(w, r, http.StatusBadRequest, fmt.Sprintf("grain's package cache could not read output %s: %v", outputID, err))
			return
		}
		if r.ContentLength >= 0 && n != r.ContentLength {
			s.refuse(w, r, http.StatusBadRequest,
				fmt.Sprintf("grain's package cache received %d bytes for output %s, expected %d", n, outputID, r.ContentLength))
			return
		}
		if entry.Oversize() {
			s.logf("package cache: Go build output %s is larger than the %dMB a single entry may be -- not caching it",
				outputID, s.cache.MaxEntryBytes()/(1<<20))
			s.refuse(w, r, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("grain's package cache will not store output %s: it exceeds the per-entry size limit.", outputID))
			return
		}
		gotSum := hex.EncodeToString(h.Sum(nil))
		if gotSum != outputID {
			s.refuse(w, r, http.StatusBadRequest,
				fmt.Sprintf("grain's package cache refused Go build output %s: body hashed to %s.", outputID, gotSum))
			return
		}
		if !entry.Commit(Meta{URL: goCacheOutputPrefix + outputID, ContentType: "application/octet-stream"}) {
			s.refuse(w, r, http.StatusInternalServerError, fmt.Sprintf("grain's package cache could not commit output %s", outputID))
			return
		}
		w.Header().Set(cacheStatusHeader, "stored")
		w.WriteHeader(http.StatusOK)

	default:
		s.refuse(w, r, http.StatusMethodNotAllowed,
			fmt.Sprintf("this is grain's Go build cache: it answers GET, HEAD and PUT, not %s.", r.Method))
	}
}

// RunGoCacheProg speaks Go's GOCACHEPROG stdin/stdout JSON protocol against
// a local disk directory (localDir) backed by the HTTP package cache at baseURL.
//
// Repeated "get" requests within the same VM are served directly from localDir
// via DiskPath. On a local miss, the helper queries baseURL/gocache/action/<id>
// and baseURL/gocache/output/<id>, verifies the output's SHA-256, writes it to
// localDir, and returns DiskPath. On "put", the helper writes to localDir,
// returns DiskPath immediately, and uploads the output and action to baseURL
// on a bounded best-effort basis so an unreachable or failing cache server
// never fails a build.
func RunGoCacheProg(ctx context.Context, stdin io.Reader, stdout io.Writer, baseURL, localDir string) error {
	if strings.TrimSpace(localDir) == "" {
		localDir = strings.TrimSpace(os.Getenv("GRAIN_GOCACHE_DIR"))
	}
	if localDir == "" {
		localDir = GoCacheProgLocalDir
	}
	absDir, err := filepath.Abs(localDir)
	if err != nil {
		return err
	}
	localDir = absDir
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return err
	}
	baseURL = strings.TrimSuffix(strings.TrimSpace(baseURL), "/")

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
			TLSHandshakeTimeout:   2 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
			MaxIdleConns:          32,
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       30 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	var writeMu sync.Mutex
	enc := json.NewEncoder(stdout)
	send := func(resp GoCacheProgResponse) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return enc.Encode(resp)
	}

	if err := send(GoCacheProgResponse{
		ID:            0,
		KnownCommands: []GoCacheProgCmd{GoCacheProgCmdGet, GoCacheProgCmdPut, GoCacheProgCmdClose},
	}); err != nil {
		return err
	}

	actionPath := func(actionIDHex string) string {
		prefix := actionIDHex
		if len(prefix) > 2 {
			prefix = prefix[:2]
		}
		return filepath.Join(localDir, "action", prefix, actionIDHex+".json")
	}
	outputPath := func(outputIDHex string) string {
		prefix := outputIDHex
		if len(prefix) > 2 {
			prefix = prefix[:2]
		}
		return filepath.Join(localDir, "output", prefix, outputIDHex)
	}

	writeAtomic := func(dst string, data []byte) error {
		dir := filepath.Dir(dst)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(dir, ".tmp-*")
		if err != nil {
			return err
		}
		tmpName := tmp.Name()
		if _, err := tmp.Write(data); err != nil {
			tmp.Close()
			os.Remove(tmpName)
			return err
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmpName)
			return err
		}
		if err := os.Rename(tmpName, dst); err != nil {
			os.Remove(tmpName)
			return err
		}
		return nil
	}

	verifyLocalOutput := func(p, outputIDHex string, size int64) bool {
		info, err := os.Stat(p)
		if err != nil || info.IsDir() || info.Size() != size {
			return false
		}
		f, err := os.Open(p)
		if err != nil {
			return false
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return false
		}
		return hex.EncodeToString(h.Sum(nil)) == outputIDHex
	}

	var reqWg sync.WaitGroup
	var uploadWg sync.WaitGroup
	reqSem := make(chan struct{}, 16)
	uploadSem := make(chan struct{}, 8)

	handleGet := func(req GoCacheProgRequest) {
		actionIDHex := hex.EncodeToString(req.ActionID)
		if !isValidHexID(actionIDHex, 0) {
			_ = send(GoCacheProgResponse{ID: req.ID, Miss: true})
			return
		}

		// 1. Try local disk directory first.
		actFile := actionPath(actionIDHex)
		if raw, err := os.ReadFile(actFile); err == nil {
			var act GoCacheAction
			if json.Unmarshal(raw, &act) == nil {
				act.OutputID = strings.ToLower(strings.TrimSpace(act.OutputID))
				outBytes, decErr := hex.DecodeString(act.OutputID)
				outFile := outputPath(act.OutputID)
				if decErr == nil && len(outBytes) == sha256.Size && verifyLocalOutput(outFile, act.OutputID, act.Size) {
					t := act.Time
					_ = send(GoCacheProgResponse{
						ID:       req.ID,
						OutputID: outBytes,
						Size:     act.Size,
						Time:     &t,
						DiskPath: outFile,
					})
					return
				}
			}
		}

		// 2. Local miss: query remote pkgcache server.
		if baseURL == "" {
			_ = send(GoCacheProgResponse{ID: req.ID, Miss: true})
			return
		}
		actReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+goCacheActionPrefix+actionIDHex, nil)
		if err != nil {
			_ = send(GoCacheProgResponse{ID: req.ID, Miss: true})
			return
		}
		actResp, err := client.Do(actReq)
		if err != nil {
			_ = send(GoCacheProgResponse{ID: req.ID, Miss: true})
			return
		}
		actBody, readErr := io.ReadAll(io.LimitReader(actResp.Body, 64*1024))
		actResp.Body.Close()
		if actResp.StatusCode != http.StatusOK || readErr != nil {
			_ = send(GoCacheProgResponse{ID: req.ID, Miss: true})
			return
		}
		var act GoCacheAction
		if err := json.Unmarshal(actBody, &act); err != nil {
			_ = send(GoCacheProgResponse{ID: req.ID, Miss: true})
			return
		}
		act.OutputID = strings.ToLower(strings.TrimSpace(act.OutputID))
		outBytes, err := hex.DecodeString(act.OutputID)
		if err != nil || len(outBytes) != sha256.Size || act.Size < 0 {
			_ = send(GoCacheProgResponse{ID: req.ID, Miss: true})
			return
		}

		outFile := outputPath(act.OutputID)
		if !verifyLocalOutput(outFile, act.OutputID, act.Size) {
			outReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+goCacheOutputPrefix+act.OutputID, nil)
			if err != nil {
				_ = send(GoCacheProgResponse{ID: req.ID, Miss: true})
				return
			}
			outResp, err := client.Do(outReq)
			if err != nil {
				_ = send(GoCacheProgResponse{ID: req.ID, Miss: true})
				return
			}
			if outResp.StatusCode != http.StatusOK {
				outResp.Body.Close()
				_ = send(GoCacheProgResponse{ID: req.ID, Miss: true})
				return
			}
			outDir := filepath.Dir(outFile)
			if err := os.MkdirAll(outDir, 0o755); err != nil {
				outResp.Body.Close()
				_ = send(GoCacheProgResponse{ID: req.ID, Miss: true})
				return
			}
			tmp, err := os.CreateTemp(outDir, ".tmp-*")
			if err != nil {
				outResp.Body.Close()
				_ = send(GoCacheProgResponse{ID: req.ID, Miss: true})
				return
			}
			tmpName := tmp.Name()
			h := sha256.New()
			n, copyErr := io.Copy(io.MultiWriter(tmp, h), outResp.Body)
			outResp.Body.Close()
			closeErr := tmp.Close()
			if copyErr != nil || closeErr != nil || n != act.Size || hex.EncodeToString(h.Sum(nil)) != act.OutputID {
				os.Remove(tmpName)
				_ = send(GoCacheProgResponse{ID: req.ID, Miss: true})
				return
			}
			if err := os.Rename(tmpName, outFile); err != nil {
				os.Remove(tmpName)
				_ = send(GoCacheProgResponse{ID: req.ID, Miss: true})
				return
			}
		}

		if encodedAct, err := json.Marshal(act); err == nil {
			_ = writeAtomic(actFile, encodedAct)
		}
		if act.Time.IsZero() {
			act.Time = time.Now().UTC()
		}
		t := act.Time
		_ = send(GoCacheProgResponse{
			ID:       req.ID,
			OutputID: outBytes,
			Size:     act.Size,
			Time:     &t,
			DiskPath: outFile,
		})
	}

	handlePut := func(req GoCacheProgRequest, body []byte) {
		if int64(len(body)) != req.BodySize {
			_ = send(GoCacheProgResponse{ID: req.ID, Err: fmt.Sprintf("body size %d != expected %d", len(body), req.BodySize)})
			return
		}
		sum := sha256.Sum256(body)
		if len(req.OutputID) > 0 && !bytes.Equal(sum[:], req.OutputID) {
			_ = send(GoCacheProgResponse{ID: req.ID, Err: "output SHA-256 mismatch"})
			return
		}
		outputIDHex := hex.EncodeToString(sum[:])
		actionIDHex := hex.EncodeToString(req.ActionID)
		if !isValidHexID(actionIDHex, 0) {
			_ = send(GoCacheProgResponse{ID: req.ID, Err: "invalid action ID"})
			return
		}

		outFile := outputPath(outputIDHex)
		if !verifyLocalOutput(outFile, outputIDHex, int64(len(body))) {
			if err := writeAtomic(outFile, body); err != nil {
				_ = send(GoCacheProgResponse{ID: req.ID, Err: err.Error()})
				return
			}
		}

		now := time.Now().UTC()
		act := GoCacheAction{
			OutputID: outputIDHex,
			Size:     int64(len(body)),
			Time:     now,
		}
		encodedAct, err := json.Marshal(act)
		if err == nil {
			_ = writeAtomic(actionPath(actionIDHex), encodedAct)
		}

		// Respond with DiskPath immediately before uploading to the remote cache.
		_ = send(GoCacheProgResponse{
			ID:       req.ID,
			DiskPath: outFile,
			Size:     int64(len(body)),
		})

		if baseURL == "" || err != nil {
			return
		}
		uploadWg.Add(1)
		go func(actionIDHex, outputIDHex string, body, encodedAct []byte) {
			defer uploadWg.Done()
			uploadSem <- struct{}{}
			defer func() { <-uploadSem }()

			putOutReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPut, baseURL+goCacheOutputPrefix+outputIDHex, bytes.NewReader(body))
			if reqErr != nil {
				return
			}
			putOutReq.ContentLength = int64(len(body))
			putOutReq.Header.Set("Content-Type", "application/octet-stream")
			outResp, doErr := client.Do(putOutReq)
			if doErr != nil {
				return
			}
			_, _ = io.Copy(io.Discard, outResp.Body)
			outResp.Body.Close()
			if outResp.StatusCode < 200 || outResp.StatusCode >= 300 {
				return
			}

			putActReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPut, baseURL+goCacheActionPrefix+actionIDHex, bytes.NewReader(encodedAct))
			if reqErr != nil {
				return
			}
			putActReq.ContentLength = int64(len(encodedAct))
			putActReq.Header.Set("Content-Type", "application/json")
			actResp, doErr := client.Do(putActReq)
			if doErr != nil {
				return
			}
			_, _ = io.Copy(io.Discard, actResp.Body)
			actResp.Body.Close()
		}(actionIDHex, outputIDHex, body, encodedAct)
	}

	jd := json.NewDecoder(stdin)
	for {
		var req GoCacheProgRequest
		if err := jd.Decode(&req); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		switch req.Command {
		case GoCacheProgCmdPut:
			var body []byte
			if req.BodySize > 0 {
				if err := jd.Decode(&body); err != nil {
					return err
				}
			}
			reqWg.Wait()
			handlePut(req, body)

		case GoCacheProgCmdGet:
			reqWg.Add(1)
			reqSem <- struct{}{}
			go func(r GoCacheProgRequest) {
				defer reqWg.Done()
				defer func() { <-reqSem }()
				handleGet(r)
			}(req)

		case GoCacheProgCmdClose:
			reqWg.Wait()
			uploadWg.Wait()
			return send(GoCacheProgResponse{ID: req.ID})

		default:
			_ = send(GoCacheProgResponse{ID: req.ID, Err: fmt.Sprintf("unknown command %q", req.Command)})
		}
	}

	reqWg.Wait()
	uploadWg.Wait()
	return nil
}

// GoCacheProgSource is the standalone, zero-dependency Go source compiled into
// GoCacheProgPath (/tmp/grain-gocacheprog) inside sandboxes when the package
// cache is configured.
const GoCacheProgSource = `package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type actionMeta struct {
	OutputID string    ` + "`json:\"OutputID\"`" + `
	Size     int64     ` + "`json:\"Size\"`" + `
	Time     time.Time ` + "`json:\"Time\"`" + `
}

type progRequest struct {
	ID       int64  ` + "`json:\"ID\"`" + `
	Command  string ` + "`json:\"Command\"`" + `
	ActionID []byte ` + "`json:\"ActionID,omitempty\"`" + `
	OutputID []byte ` + "`json:\"OutputID,omitempty\"`" + `
	BodySize int64  ` + "`json:\"BodySize,omitempty\"`" + `
}

type progResponse struct {
	ID            int64      ` + "`json:\"ID\"`" + `
	Err           string     ` + "`json:\"Err,omitempty\"`" + `
	KnownCommands []string   ` + "`json:\"KnownCommands,omitempty\"`" + `
	Miss          bool       ` + "`json:\"Miss,omitempty\"`" + `
	OutputID      []byte     ` + "`json:\"OutputID,omitempty\"`" + `
	Size          int64      ` + "`json:\"Size,omitempty\"`" + `
	Time          *time.Time ` + "`json:\"Time,omitempty\"`" + `
	DiskPath      string     ` + "`json:\"DiskPath,omitempty\"`" + `
}

func main() {
	var baseURL string
	if len(os.Args) > 1 {
		baseURL = strings.TrimSuffix(strings.TrimSpace(os.Args[1]), "/")
	}
	localDir := strings.TrimSpace(os.Getenv("GRAIN_GOCACHE_DIR"))
	if len(os.Args) > 2 && strings.TrimSpace(os.Args[2]) != "" {
		localDir = strings.TrimSpace(os.Args[2])
	}
	if localDir == "" {
		localDir = "/tmp/grain-gocache"
	}
	if abs, err := filepath.Abs(localDir); err == nil {
		localDir = abs
	}
	_ = os.MkdirAll(localDir, 0o755)

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
			TLSHandshakeTimeout:   2 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
			MaxIdleConns:          32,
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       30 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	var writeMu sync.Mutex
	enc := json.NewEncoder(os.Stdout)
	send := func(resp progResponse) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = enc.Encode(resp)
	}

	send(progResponse{
		ID:            0,
		KnownCommands: []string{"get", "put", "close"},
	})

	actionPath := func(actionIDHex string) string {
		prefix := actionIDHex
		if len(prefix) > 2 {
			prefix = prefix[:2]
		}
		return filepath.Join(localDir, "action", prefix, actionIDHex+".json")
	}
	outputPath := func(outputIDHex string) string {
		prefix := outputIDHex
		if len(prefix) > 2 {
			prefix = prefix[:2]
		}
		return filepath.Join(localDir, "output", prefix, outputIDHex)
	}
	writeAtomic := func(dst string, data []byte) error {
		dir := filepath.Dir(dst)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(dir, ".tmp-*")
		if err != nil {
			return err
		}
		tmpName := tmp.Name()
		if _, err := tmp.Write(data); err != nil {
			tmp.Close()
			os.Remove(tmpName)
			return err
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmpName)
			return err
		}
		if err := os.Rename(tmpName, dst); err != nil {
			os.Remove(tmpName)
			return err
		}
		return nil
	}
	verifyLocalOutput := func(p, outputIDHex string, size int64) bool {
		info, err := os.Stat(p)
		if err != nil || info.IsDir() || info.Size() != size {
			return false
		}
		f, err := os.Open(p)
		if err != nil {
			return false
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return false
		}
		return hex.EncodeToString(h.Sum(nil)) == outputIDHex
	}

	ctx := context.Background()
	var reqWg sync.WaitGroup
	var uploadWg sync.WaitGroup
	reqSem := make(chan struct{}, 16)
	uploadSem := make(chan struct{}, 8)

	handleGet := func(req progRequest) {
		actionIDHex := hex.EncodeToString(req.ActionID)
		if actionIDHex == "" {
			send(progResponse{ID: req.ID, Miss: true})
			return
		}
		actFile := actionPath(actionIDHex)
		if raw, err := os.ReadFile(actFile); err == nil {
			var act actionMeta
			if json.Unmarshal(raw, &act) == nil {
				act.OutputID = strings.ToLower(strings.TrimSpace(act.OutputID))
				outBytes, decErr := hex.DecodeString(act.OutputID)
				outFile := outputPath(act.OutputID)
				if decErr == nil && len(outBytes) == sha256.Size && verifyLocalOutput(outFile, act.OutputID, act.Size) {
					t := act.Time
					send(progResponse{
						ID:       req.ID,
						OutputID: outBytes,
						Size:     act.Size,
						Time:     &t,
						DiskPath: outFile,
					})
					return
				}
			}
		}
		if baseURL == "" {
			send(progResponse{ID: req.ID, Miss: true})
			return
		}
		actReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/gocache/action/"+actionIDHex, nil)
		if err != nil {
			send(progResponse{ID: req.ID, Miss: true})
			return
		}
		actResp, err := client.Do(actReq)
		if err != nil {
			send(progResponse{ID: req.ID, Miss: true})
			return
		}
		actBody, readErr := io.ReadAll(io.LimitReader(actResp.Body, 64*1024))
		actResp.Body.Close()
		if actResp.StatusCode != http.StatusOK || readErr != nil {
			send(progResponse{ID: req.ID, Miss: true})
			return
		}
		var act actionMeta
		if err := json.Unmarshal(actBody, &act); err != nil {
			send(progResponse{ID: req.ID, Miss: true})
			return
		}
		act.OutputID = strings.ToLower(strings.TrimSpace(act.OutputID))
		outBytes, err := hex.DecodeString(act.OutputID)
		if err != nil || len(outBytes) != sha256.Size || act.Size < 0 {
			send(progResponse{ID: req.ID, Miss: true})
			return
		}
		outFile := outputPath(act.OutputID)
		if !verifyLocalOutput(outFile, act.OutputID, act.Size) {
			outReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/gocache/output/"+act.OutputID, nil)
			if err != nil {
				send(progResponse{ID: req.ID, Miss: true})
				return
			}
			outResp, err := client.Do(outReq)
			if err != nil {
				send(progResponse{ID: req.ID, Miss: true})
				return
			}
			if outResp.StatusCode != http.StatusOK {
				outResp.Body.Close()
				send(progResponse{ID: req.ID, Miss: true})
				return
			}
			outDir := filepath.Dir(outFile)
			if err := os.MkdirAll(outDir, 0o755); err != nil {
				outResp.Body.Close()
				send(progResponse{ID: req.ID, Miss: true})
				return
			}
			tmp, err := os.CreateTemp(outDir, ".tmp-*")
			if err != nil {
				outResp.Body.Close()
				send(progResponse{ID: req.ID, Miss: true})
				return
			}
			tmpName := tmp.Name()
			h := sha256.New()
			n, copyErr := io.Copy(io.MultiWriter(tmp, h), outResp.Body)
			outResp.Body.Close()
			closeErr := tmp.Close()
			if copyErr != nil || closeErr != nil || n != act.Size || hex.EncodeToString(h.Sum(nil)) != act.OutputID {
				os.Remove(tmpName)
				send(progResponse{ID: req.ID, Miss: true})
				return
			}
			if err := os.Rename(tmpName, outFile); err != nil {
				os.Remove(tmpName)
				send(progResponse{ID: req.ID, Miss: true})
				return
			}
		}
		if encodedAct, err := json.Marshal(act); err == nil {
			_ = writeAtomic(actFile, encodedAct)
		}
		if act.Time.IsZero() {
			act.Time = time.Now().UTC()
		}
		t := act.Time
		send(progResponse{
			ID:       req.ID,
			OutputID: outBytes,
			Size:     act.Size,
			Time:     &t,
			DiskPath: outFile,
		})
	}

	handlePut := func(req progRequest, body []byte) {
		if int64(len(body)) != req.BodySize {
			send(progResponse{ID: req.ID, Err: fmt.Sprintf("body size %d != expected %d", len(body), req.BodySize)})
			return
		}
		sum := sha256.Sum256(body)
		if len(req.OutputID) > 0 && !bytes.Equal(sum[:], req.OutputID) {
			send(progResponse{ID: req.ID, Err: "output SHA-256 mismatch"})
			return
		}
		outputIDHex := hex.EncodeToString(sum[:])
		actionIDHex := hex.EncodeToString(req.ActionID)
		if actionIDHex == "" {
			send(progResponse{ID: req.ID, Err: "empty action ID"})
			return
		}
		outFile := outputPath(outputIDHex)
		if !verifyLocalOutput(outFile, outputIDHex, int64(len(body))) {
			if err := writeAtomic(outFile, body); err != nil {
				send(progResponse{ID: req.ID, Err: err.Error()})
				return
			}
		}
		now := time.Now().UTC()
		act := actionMeta{
			OutputID: outputIDHex,
			Size:     int64(len(body)),
			Time:     now,
		}
		encodedAct, err := json.Marshal(act)
		if err == nil {
			_ = writeAtomic(actionPath(actionIDHex), encodedAct)
		}
		send(progResponse{
			ID:       req.ID,
			DiskPath: outFile,
			Size:     int64(len(body)),
		})
		if baseURL == "" || err != nil {
			return
		}
		uploadWg.Add(1)
		go func(actionIDHex, outputIDHex string, body, encodedAct []byte) {
			defer uploadWg.Done()
			uploadSem <- struct{}{}
			defer func() { <-uploadSem }()

			putOutReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPut, baseURL+"/gocache/output/"+outputIDHex, bytes.NewReader(body))
			if reqErr != nil {
				return
			}
			putOutReq.ContentLength = int64(len(body))
			putOutReq.Header.Set("Content-Type", "application/octet-stream")
			outResp, doErr := client.Do(putOutReq)
			if doErr != nil {
				return
			}
			_, _ = io.Copy(io.Discard, outResp.Body)
			outResp.Body.Close()
			if outResp.StatusCode < 200 || outResp.StatusCode >= 300 {
				return
			}

			putActReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPut, baseURL+"/gocache/action/"+actionIDHex, bytes.NewReader(encodedAct))
			if reqErr != nil {
				return
			}
			putActReq.ContentLength = int64(len(encodedAct))
			putActReq.Header.Set("Content-Type", "application/json")
			actResp, doErr := client.Do(putActReq)
			if doErr != nil {
				return
			}
			_, _ = io.Copy(io.Discard, actResp.Body)
			actResp.Body.Close()
		}(actionIDHex, outputIDHex, body, encodedAct)
	}

	jd := json.NewDecoder(os.Stdin)
	for {
		var req progRequest
		if err := jd.Decode(&req); err != nil {
			break
		}
		switch req.Command {
		case "put":
			var body []byte
			if req.BodySize > 0 {
				if err := jd.Decode(&body); err != nil {
					os.Exit(1)
				}
			}
			reqWg.Wait()
			handlePut(req, body)
		case "get":
			reqWg.Add(1)
			reqSem <- struct{}{}
			go func(r progRequest) {
				defer reqWg.Done()
				defer func() { <-reqSem }()
				handleGet(r)
			}(req)
		case "close":
			reqWg.Wait()
			uploadWg.Wait()
			send(progResponse{ID: req.ID})
			return
		default:
			send(progResponse{ID: req.ID, Err: "unknown command: " + req.Command})
		}
	}
	reqWg.Wait()
	uploadWg.Wait()
}
`
