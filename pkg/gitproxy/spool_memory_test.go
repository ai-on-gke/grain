package gitproxy

import (
	"bufio"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// packCheckingAuthorizer is a RefAuthorizer that, like ModelAuthorizer
// for a declared non-force update, lets an update through only where the
// push's own pack shows it to be a fast-forward.
type packCheckingAuthorizer struct {
	stubAuthorizer
	walked bool
}

func (a *packCheckingAuthorizer) AuthorizeRefUpdates(ctx context.Context, _, _, _ string, updates []RefUpdate) (string, error) {
	commits, err := pushCheckFrom(ctx).packCommits(20)
	a.walked = true
	for _, u := range updates {
		if err != nil || !commits.descends(u.New, u.Old) {
			return fmt.Sprintf("%s is not shown to fast-forward (%v)", u.Ref, err), nil
		}
	}
	return "", nil
}

// writeLargePush writes, to a file, a receive-pack request body updating
// refs/heads/main from oldTip with a pack carrying a blob of blobBytes
// incompressible bytes and then a commit on top of oldTip. It returns the
// file and the body's sha256. Nothing of the blob is held in
// memory: the push the test makes is as large on disk as it is on the
// wire, and the heap is what the proxy itself puts there.
func writeLargePush(t *testing.T, oldTip string, blobBytes int) (path, sum string) {
	t.Helper()
	commit := []byte("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\nparent " + oldTip +
		"\nauthor a <a@b> 0 +0000\ncommitter a <a@b> 0 +0000\n\nlarge\n")
	newTip := objectName(sha1.New, "commit", commit)

	path = filepath.Join(t.TempDir(), "push")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	w := bufio.NewWriter(io.MultiWriter(f, h))
	w.WriteString(pkt(oldTip + " " + newTip + " refs/heads/main\x00report-status side-band-64k agent=git/2.39.2\n"))
	w.WriteString("0000PACK")
	_ = binary.Write(w, binary.BigEndian, uint32(2))
	_ = binary.Write(w, binary.BigEndian, uint32(2))

	header := func(typ, size int) {
		c := byte(typ<<4) | byte(size&0x0f)
		for size >>= 4; size > 0; size >>= 7 {
			w.WriteByte(c | 0x80)
			c = byte(size & 0x7f)
		}
		w.WriteByte(c)
	}
	header(3 /* blob */, blobBytes)
	zw, _ := zlib.NewWriterLevel(w, zlib.NoCompression)
	rng := rand.New(rand.NewSource(1))
	chunk := make([]byte, 1<<20)
	for left := blobBytes; left > 0; left -= len(chunk) {
		rng.Read(chunk)
		if _, err := zw.Write(chunk[:min(left, len(chunk))]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	header(objCommit, len(commit))
	zw = zlib.NewWriter(w)
	zw.Write(commit)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	w.Write(make([]byte, 20))
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	return path, hex.EncodeToString(h.Sum(nil))
}

// A push over the spool threshold is spooled to disk and stays there: the
// ref check reads its command list and walks its pack from the spool
// file, and it is forwarded as a stream, so what the proxy allocates is
// bounded by the threshold, not by the size of the push (grain/task-3207).
func TestGitProxySpooledPushAllocatesByThresholdNotPushSize(t *testing.T) {
	const (
		threshold = 1 << 20
		pushBytes = 96 << 20
	)
	oldTip := hex.EncodeToString(make([]byte, 19)) + "01"
	path, sum := writeLargePush(t, oldTip, pushBytes)

	var gotSum string
	var gotLen int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := sha256.New()
		gotLen, _ = io.Copy(h, r.Body)
		gotSum = hex.EncodeToString(h.Sum(nil))
		w.WriteHeader(200)
	}))
	defer upstream.Close()

	p, _, _ := newTestProxy(t)
	authorizer := &packCheckingAuthorizer{stubAuthorizer: p.Authorizer.(stubAuthorizer)}
	p.Authorizer = authorizer
	p.Forwarder = &RealForwarder{Host: upstream.Listener.Addr().String(), UseTLS: false}
	p.PushSpoolThresholdBytes = threshold

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	resp := p.HandleRequest(context.Background(), "POST", "/owner/repo.git/git-receive-pack", "",
		postHeaders("git-receive-pack", "tok0"), f)
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	if resp.Status != 200 {
		t.Fatalf("receive-pack status = %d (%s), want 200", resp.Status, resp.Body)
	}
	if !authorizer.walked {
		t.Fatal("the ref check never read the pack")
	}
	if gotSum != sum {
		t.Fatalf("upstream received %d bytes that do not match the push", gotLen)
	}
	// The threshold's worth spoolBody buffers, the pack walk's reader and
	// zlib state, and the HTTP client's buffers -- about 5 MiB, against a
	// push of 96 MiB that a copy in memory would allocate at least once.
	if limit := uint64(16 << 20); allocated > limit {
		t.Errorf("a %d MiB push spooled at a %d MiB threshold allocated %d MiB; want under %d MiB",
			pushBytes>>20, threshold>>20, allocated>>20, limit>>20)
	}
}
