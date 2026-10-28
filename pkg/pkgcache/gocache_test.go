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

package pkgcache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGoCacheActionAndOutputEndpointsRoundTrip(t *testing.T) {
	s, cache := newTestServer(t, Config{})

	actionSum := sha256.Sum256([]byte("compile-action-1"))
	actionID := hex.EncodeToString(actionSum[:])
	payload := []byte("compiled object bytes for package foo")
	outputSum := sha256.Sum256(payload)
	outputID := hex.EncodeToString(outputSum[:])

	// Initial GETs should 404 with X-Grain-Package-Cache: miss.
	for _, target := range []string{goCacheActionPrefix + actionID, goCacheOutputPrefix + outputID} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s on empty cache = %d, want 404", target, rec.Code)
		}
		if got := rec.Header().Get(cacheStatusHeader); got != "miss" {
			t.Errorf("GET %s %s = %q, want miss", target, cacheStatusHeader, got)
		}
	}

	// PUT output bytes.
	putOut := httptest.NewRequest(http.MethodPut, goCacheOutputPrefix+outputID, bytes.NewReader(payload))
	putOutRec := httptest.NewRecorder()
	s.ServeHTTP(putOutRec, putOut)
	if putOutRec.Code != http.StatusOK {
		t.Fatalf("PUT output = %d: %s", putOutRec.Code, putOutRec.Body.String())
	}

	// PUT action mapping.
	now := time.Now().UTC().Truncate(time.Second)
	actBytes, err := json.Marshal(GoCacheAction{
		OutputID: outputID,
		Size:     int64(len(payload)),
		Time:     now,
	})
	if err != nil {
		t.Fatal(err)
	}
	putAct := httptest.NewRequest(http.MethodPut, goCacheActionPrefix+actionID, bytes.NewReader(actBytes))
	putActRec := httptest.NewRecorder()
	s.ServeHTTP(putActRec, putAct)
	if putActRec.Code != http.StatusOK {
		t.Fatalf("PUT action = %d: %s", putActRec.Code, putActRec.Body.String())
	}

	// GET action mapping.
	getActRec := httptest.NewRecorder()
	s.ServeHTTP(getActRec, httptest.NewRequest(http.MethodGet, goCacheActionPrefix+actionID, nil))
	if getActRec.Code != http.StatusOK {
		t.Fatalf("GET action = %d: %s", getActRec.Code, getActRec.Body.String())
	}
	if got := getActRec.Header().Get(cacheStatusHeader); got != "hit" {
		t.Errorf("GET action %s = %q, want hit", cacheStatusHeader, got)
	}
	var gotAct GoCacheAction
	if err := json.Unmarshal(getActRec.Body.Bytes(), &gotAct); err != nil {
		t.Fatalf("decoding GET action: %v", err)
	}
	if gotAct.OutputID != outputID || gotAct.Size != int64(len(payload)) || !gotAct.Time.Equal(now) {
		t.Errorf("GET action = %+v, want OutputID=%s Size=%d Time=%v", gotAct, outputID, len(payload), now)
	}

	// HEAD output and GET output.
	headOutRec := httptest.NewRecorder()
	s.ServeHTTP(headOutRec, httptest.NewRequest(http.MethodHead, goCacheOutputPrefix+outputID, nil))
	if headOutRec.Code != http.StatusOK || headOutRec.Body.Len() != 0 {
		t.Errorf("HEAD output = %d (body %d bytes), want 200 with empty body", headOutRec.Code, headOutRec.Body.Len())
	}
	getOutRec := httptest.NewRecorder()
	s.ServeHTTP(getOutRec, httptest.NewRequest(http.MethodGet, goCacheOutputPrefix+outputID, nil))
	if getOutRec.Code != http.StatusOK {
		t.Fatalf("GET output = %d: %s", getOutRec.Code, getOutRec.Body.String())
	}
	if !bytes.Equal(getOutRec.Body.Bytes(), payload) {
		t.Errorf("GET output body = %q, want %q", getOutRec.Body.Bytes(), payload)
	}

	wantAccounted(t, cache, "after storing one Go build action and output")
}

func TestGoCacheSHA256IntegrityVerification(t *testing.T) {
	s, cache := newTestServer(t, Config{})

	goodPayload := []byte("authentic compiled archive bytes")
	goodSum := sha256.Sum256(goodPayload)
	outputID := hex.EncodeToString(goodSum[:])

	// 1. PUT with tampered bytes that do not hash to outputID must be refused with 400.
	badPut := httptest.NewRequest(http.MethodPut, goCacheOutputPrefix+outputID, strings.NewReader("tampered bytes"))
	badPutRec := httptest.NewRecorder()
	s.ServeHTTP(badPutRec, badPut)
	if badPutRec.Code != http.StatusBadRequest {
		t.Fatalf("PUT with mismatched SHA-256 = %d, want 400 Bad Request", badPutRec.Code)
	}
	if cache.Entries() != 0 || cache.Bytes() != 0 {
		t.Fatalf("rejected PUT left %d entries (%d bytes) in cache", cache.Entries(), cache.Bytes())
	}

	// 2. Store valid output, then corrupt its file on disk; GET must detect the SHA-256 mismatch and return 404.
	goodPut := httptest.NewRequest(http.MethodPut, goCacheOutputPrefix+outputID, bytes.NewReader(goodPayload))
	goodPutRec := httptest.NewRecorder()
	s.ServeHTTP(goodPutRec, goodPut)
	if goodPutRec.Code != http.StatusOK {
		t.Fatalf("valid PUT output = %d: %s", goodPutRec.Code, goodPutRec.Body.String())
	}

	diskPath := cache.key(KindGoBuild, goCacheOutputKey(outputID))
	corrupted := bytes.Repeat([]byte("X"), len(goodPayload))
	if err := os.WriteFile(diskPath, corrupted, 0o644); err != nil {
		t.Fatalf("corrupting on-disk cache file: %v", err)
	}

	getRec := httptest.NewRecorder()
	s.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, goCacheOutputPrefix+outputID, nil))
	if getRec.Code != http.StatusNotFound {
		t.Fatalf("GET corrupted output = %d, want 404 Not Found", getRec.Code)
	}
	if _, err := os.Stat(diskPath); !os.IsNotExist(err) {
		t.Errorf("corrupted output file %s was not removed after failed verification", diskPath)
	}

	// 3. Invalid hex IDs and path traversal are rejected with 400.
	for _, badPath := range []string{
		"/gocache/action/not-hex!",
		"/gocache/output/abcd",
		"/gocache/output/" + strings.Repeat("z", 64),
		"/gocache/action/../secret",
	} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, badPath, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400 Bad Request", badPath, rec.Code)
		}
	}
}

func TestGoCacheLRUEvictionAndAccounting(t *testing.T) {
	// Bound cache to 20 KiB and write ten 4 KiB compiled objects.
	cache, err := NewCache(t.TempDir(), 20*1024)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	s := NewServer(Config{Cache: cache, Log: func(string, ...any) {}})

	for i := 0; i < 10; i++ {
		blob := bytes.Repeat([]byte{byte('a' + i)}, 4096)
		outSum := sha256.Sum256(blob)
		outID := hex.EncodeToString(outSum[:])
		actSum := sha256.Sum256([]byte(fmt.Sprintf("action-%d", i)))
		actID := hex.EncodeToString(actSum[:])

		recOut := httptest.NewRecorder()
		s.ServeHTTP(recOut, httptest.NewRequest(http.MethodPut, goCacheOutputPrefix+outID, bytes.NewReader(blob)))
		if recOut.Code != http.StatusOK {
			t.Fatalf("PUT output %d = %d: %s", i, recOut.Code, recOut.Body.String())
		}

		actBody, _ := json.Marshal(GoCacheAction{OutputID: outID, Size: int64(len(blob)), Time: time.Now().UTC()})
		recAct := httptest.NewRecorder()
		s.ServeHTTP(recAct, httptest.NewRequest(http.MethodPut, goCacheActionPrefix+actID, bytes.NewReader(actBody)))
		if recAct.Code != http.StatusOK {
			t.Fatalf("PUT action %d = %d: %s", i, recAct.Code, recAct.Body.String())
		}
	}

	if cache.Bytes() > cache.MaxBytes() {
		t.Errorf("cache holds %d bytes, exceeding MaxBytes %d", cache.Bytes(), cache.MaxBytes())
	}
	wantAccounted(t, cache, "after KindGoBuild LRU eviction")

	stats, err := cache.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	var gobuild KindStats
	for _, st := range stats {
		if st.Kind == KindGoBuild {
			gobuild = st
		}
	}
	if gobuild.Entries == 0 || gobuild.Entries >= 20 {
		t.Errorf("KindGoBuild stats = %+v, want some entries retained and oldest evicted", gobuild)
	}

	entries, freed, err := cache.Flush(KindGoBuild)
	if err != nil {
		t.Fatalf("Flush(KindGoBuild): %v", err)
	}
	if entries == 0 || freed == 0 {
		t.Errorf("Flush(KindGoBuild) removed %d entries (%d bytes), want > 0", entries, freed)
	}
	wantAccounted(t, cache, "after flushing KindGoBuild")
	if cache.Bytes() != 0 || cache.Entries() != 0 {
		t.Errorf("after flushing KindGoBuild, cache holds %d entries (%d bytes), want 0", cache.Entries(), cache.Bytes())
	}
}

// runProgSession sends a slice of lines/requests to RunGoCacheProg and returns all decoded responses by ID.
func runProgSession(t *testing.T, baseURL, localDir string, writeRequests func(w io.Writer)) map[int64]GoCacheProgResponse {
	t.Helper()
	var in bytes.Buffer
	writeRequests(&in)
	var out bytes.Buffer
	if err := RunGoCacheProg(context.Background(), &in, &out, baseURL, localDir); err != nil {
		t.Fatalf("RunGoCacheProg: %v", err)
	}
	resps := map[int64]GoCacheProgResponse{}
	dec := json.NewDecoder(&out)
	for {
		var resp GoCacheProgResponse
		if err := dec.Decode(&resp); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decoding response: %v", err)
		}
		resps[resp.ID] = resp
	}
	return resps
}

func TestGoCacheProgProtocolRoundTripAcrossSandboxes(t *testing.T) {
	s, cache := newTestServer(t, Config{})
	srv := httptest.NewServer(s)
	defer srv.Close()

	actionID := sha256.Sum256([]byte("action-stdlib-fmt"))
	body := []byte("compiled fmt.a archive payload")
	outputID := sha256.Sum256(body)

	vm1Local := t.TempDir()
	vm2Local := t.TempDir()

	// 1. VM 1 queries action (miss), then puts it, then closes.
	resps1 := runProgSession(t, srv.URL, vm1Local, func(w io.Writer) {
		enc := json.NewEncoder(w)
		_ = enc.Encode(GoCacheProgRequest{ID: 1, Command: GoCacheProgCmdGet, ActionID: actionID[:]})
		_ = enc.Encode(GoCacheProgRequest{
			ID:       2,
			Command:  GoCacheProgCmdPut,
			ActionID: actionID[:],
			OutputID: outputID[:],
			BodySize: int64(len(body)),
		})
		_ = enc.Encode(body) // base64-encoded JSON string literal
		_ = enc.Encode(GoCacheProgRequest{ID: 3, Command: GoCacheProgCmdClose})
	})

	if len(resps1[0].KnownCommands) != 3 {
		t.Fatalf("handshake KnownCommands = %v, want [get put close]", resps1[0].KnownCommands)
	}
	if !resps1[1].Miss {
		t.Errorf("initial get on cold cache: Miss = false, want true")
	}
	if resps1[2].Err != "" || resps1[2].DiskPath == "" {
		t.Fatalf("put response = %+v, want DiskPath with no error", resps1[2])
	}
	if got, err := os.ReadFile(resps1[2].DiskPath); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("reading put DiskPath %s: %v, got %q", resps1[2].DiskPath, err, got)
	}

	// Verify the host cache received both the output and the action.
	if cache.Entries() != 2 {
		t.Fatalf("host cache entries = %d, want 2 (1 output + 1 action)", cache.Entries())
	}

	// 2. VM 2 starts with an empty local disk directory (vm2Local) and gets a remote hit from srv.URL.
	resps2 := runProgSession(t, srv.URL, vm2Local, func(w io.Writer) {
		enc := json.NewEncoder(w)
		_ = enc.Encode(GoCacheProgRequest{ID: 10, Command: GoCacheProgCmdGet, ActionID: actionID[:]})
		_ = enc.Encode(GoCacheProgRequest{ID: 11, Command: GoCacheProgCmdClose})
	})
	hit2 := resps2[10]
	if hit2.Miss || hit2.Err != "" {
		t.Fatalf("VM 2 get response = %+v, want remote cache hit", hit2)
	}
	if !bytes.Equal(hit2.OutputID, outputID[:]) || hit2.Size != int64(len(body)) {
		t.Errorf("VM 2 hit OutputID=%x Size=%d, want %x %d", hit2.OutputID, hit2.Size, outputID[:], len(body))
	}
	if got, err := os.ReadFile(hit2.DiskPath); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("reading VM 2 DiskPath %s: %v, got %q", hit2.DiskPath, err, got)
	}

	// 3. Shut down the remote cache server; VM 2 still serves repeated gets from its local disk directory.
	srv.Close()
	resps3 := runProgSession(t, srv.URL, vm2Local, func(w io.Writer) {
		enc := json.NewEncoder(w)
		_ = enc.Encode(GoCacheProgRequest{ID: 20, Command: GoCacheProgCmdGet, ActionID: actionID[:]})
		_ = enc.Encode(GoCacheProgRequest{ID: 21, Command: GoCacheProgCmdClose})
	})
	if resps3[20].Miss || resps3[20].DiskPath == "" {
		t.Fatalf("repeated get on VM 2 with server down = %+v, want local disk hit", resps3[20])
	}
}

func TestGoCacheProgGracefulFallbackWhenServerUnreachable(t *testing.T) {
	// Point at a closed httptest server so every HTTP dial fails immediately.
	s, _ := newTestServer(t, Config{})
	srv := httptest.NewServer(s)
	srv.Close()

	localDir := t.TempDir()
	missActionID := sha256.Sum256([]byte("action-miss-unreachable"))
	putActionID := sha256.Sum256([]byte("action-put-unreachable"))
	body := []byte("compiled bytes while cache server is down")
	outputID := sha256.Sum256(body)

	resps := runProgSession(t, srv.URL, localDir, func(w io.Writer) {
		enc := json.NewEncoder(w)
		_ = enc.Encode(GoCacheProgRequest{ID: 1, Command: GoCacheProgCmdGet, ActionID: missActionID[:]})
		_ = enc.Encode(GoCacheProgRequest{
			ID:       2,
			Command:  GoCacheProgCmdPut,
			ActionID: putActionID[:],
			OutputID: outputID[:],
			BodySize: int64(len(body)),
		})
		_ = enc.Encode(body)
		_ = enc.Encode(GoCacheProgRequest{ID: 3, Command: GoCacheProgCmdClose})
	})

	if !resps[1].Miss || resps[1].Err != "" {
		t.Errorf("get against unreachable server = %+v, want Miss=true with no Err", resps[1])
	}
	if resps[2].Err != "" || resps[2].DiskPath == "" {
		t.Fatalf("put against unreachable server = %+v, want local DiskPath with no Err", resps[2])
	}
	if got, err := os.ReadFile(resps[2].DiskPath); err != nil || !bytes.Equal(got, body) {
		t.Errorf("local DiskPath content = %q (%v), want %q", got, err, body)
	}

	// Subsequent session in the same VM still hits local disk even with server unreachable.
	respsAgain := runProgSession(t, srv.URL, localDir, func(w io.Writer) {
		enc := json.NewEncoder(w)
		_ = enc.Encode(GoCacheProgRequest{ID: 4, Command: GoCacheProgCmdGet, ActionID: putActionID[:]})
		_ = enc.Encode(GoCacheProgRequest{ID: 5, Command: GoCacheProgCmdClose})
	})
	if respsAgain[4].Miss || respsAgain[4].DiskPath == "" {
		t.Errorf("second get in same VM with unreachable server = %+v, want local hit", respsAgain[4])
	}
}

func TestGoCacheProgStandaloneBinaryWithGoBuild(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}

	// Compile GoCacheProgSource once into a temporary binary.
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "main.go")
	binPath := filepath.Join(tmpDir, "grain-gocacheprog")
	if err := os.WriteFile(srcPath, []byte(GoCacheProgSource), 0o644); err != nil {
		t.Fatal(err)
	}
	buildHelper := exec.Command("go", "build", "-o", binPath, srcPath)
	buildHelper.Env = append(os.Environ(), "GOCACHEPROG=", "CGO_ENABLED=0")
	if out, err := buildHelper.CombinedOutput(); err != nil {
		t.Fatalf("compiling GoCacheProgSource: %v\n%s", err, out)
	}

	// Create a tiny multi-package Go module to build.
	modDir := filepath.Join(tmpDir, "samplemod")
	if err := os.MkdirAll(filepath.Join(modDir, "greeter"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "go.mod"), []byte("module example.com/samplemod\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "greeter", "greeter.go"), []byte("package greeter\n\nfunc Hello() string { return \"hello from cached build\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "main.go"), []byte("package main\n\nimport (\n\t\"fmt\"\n\t\"example.com/samplemod/greeter\"\n)\n\nfunc main() { fmt.Println(greeter.Hello()) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, cache := newTestServer(t, Config{})
	srv := httptest.NewServer(s)
	defer srv.Close()

	// Build 1 (VM 1): populates pkgcache.Server via GOCACHEPROG.
	vm1CacheDir := filepath.Join(tmpDir, "vm1-local")
	outBin1 := filepath.Join(tmpDir, "app1")
	cmd1 := exec.Command("go", "build", "-o", outBin1, ".")
	cmd1.Dir = modDir
	cmd1.Env = append(os.Environ(),
		"GOCACHE="+filepath.Join(tmpDir, "gocache1"),
		"GOCACHEPROG="+binPath+" "+srv.URL+" "+vm1CacheDir,
	)
	if out, err := cmd1.CombinedOutput(); err != nil {
		t.Fatalf("go build (VM 1) with GOCACHEPROG: %v\n%s", err, out)
	}
	if cache.Entries() == 0 || cache.Bytes() == 0 {
		t.Fatalf("after VM 1 go build, host cache has %d entries (%d bytes), want > 0", cache.Entries(), cache.Bytes())
	}

	// Build 2 (VM 2): empty local dir, fetches compiled objects from pkgcache.Server without recompiling.
	vm2CacheDir := filepath.Join(tmpDir, "vm2-local")
	outBin2 := filepath.Join(tmpDir, "app2")
	cmd2 := exec.Command("go", "build", "-x", "-o", outBin2, ".")
	cmd2.Dir = modDir
	cmd2.Env = append(os.Environ(),
		"GOCACHE="+filepath.Join(tmpDir, "gocache2"),
		"GOCACHEPROG="+binPath+" "+srv.URL+" "+vm2CacheDir,
	)
	out2, err := cmd2.CombinedOutput()
	if err != nil {
		t.Fatalf("go build (VM 2) with GOCACHEPROG: %v\n%s", err, out2)
	}
	// Because greeter and main were cached by VM 1, VM 2's `go build -x` should not run `compile` for greeter.go.
	if strings.Contains(string(out2), "greeter.go") {
		t.Errorf("VM 2 recompiled greeter.go despite warm host cache:\n%s", out2)
	}

	// Build 3: shut down srv; a fresh build with an unreachable cache server still succeeds by compiling locally.
	srv.Close()
	vm3CacheDir := filepath.Join(tmpDir, "vm3-local")
	outBin3 := filepath.Join(tmpDir, "app3")
	cmd3 := exec.Command("go", "build", "-o", outBin3, ".")
	cmd3.Dir = modDir
	cmd3.Env = append(os.Environ(),
		"GOCACHE="+filepath.Join(tmpDir, "gocache3"),
		"GOCACHEPROG="+binPath+" "+srv.URL+" "+vm3CacheDir,
	)
	if out, err := cmd3.CombinedOutput(); err != nil {
		t.Fatalf("go build (VM 3) with unreachable GOCACHEPROG server failed instead of falling back: %v\n%s", err, out)
	}
}
