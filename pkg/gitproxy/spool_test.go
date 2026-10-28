package gitproxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestSpoolBodyUnderThreshold(t *testing.T) {
	data := []byte("hello world")
	spool, err := spoolBody(bytes.NewReader(data), 1024, 16)
	if err != nil {
		t.Fatalf("spoolBody failed: %v", err)
	}
	defer spool.Close()

	if spool.file != nil {
		t.Errorf("expected spool to stay in memory, got file %v", spool.file)
	}
	if spool.Len() != int64(len(data)) {
		t.Errorf("Len() = %d, want %d", spool.Len(), len(data))
	}
	gotBytes, err := spool.Bytes()
	if err != nil {
		t.Fatalf("Bytes() err: %v", err)
	}
	if !bytes.Equal(gotBytes, data) {
		t.Errorf("got %q, want %q", gotBytes, data)
	}
	head, err := spool.Head(5)
	if err != nil {
		t.Fatalf("Head(5) err: %v", err)
	}
	if string(head) != "hello" {
		t.Errorf("Head(5) = %q, want 'hello'", head)
	}
}

func TestSpoolBodyOverThreshold(t *testing.T) {
	data := make([]byte, 100)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}

	spool, err := spoolBody(bytes.NewReader(data), 1024, 16)
	if err != nil {
		t.Fatalf("spoolBody failed: %v", err)
	}
	filePath := spool.path
	if filePath == "" {
		t.Fatal("expected temp file path, got empty")
	}
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("expected temp file to exist: %v", err)
	}

	if spool.Len() != 100 {
		t.Errorf("Len() = %d, want 100", spool.Len())
	}

	gotBytes, err := spool.Bytes()
	if err != nil {
		t.Fatalf("Bytes() err: %v", err)
	}
	if !bytes.Equal(gotBytes, data) {
		t.Errorf("Bytes() mismatch")
	}

	head, err := spool.Head(10)
	if err != nil {
		t.Fatalf("Head() err: %v", err)
	}
	if !bytes.Equal(head, data[:10]) {
		t.Errorf("Head() mismatch")
	}

	r, err := spool.NewReader()
	if err != nil {
		t.Fatalf("NewReader() err: %v", err)
	}
	defer func() {
		if closer, ok := r.(io.Closer); ok {
			closer.Close()
		}
	}()
	readAll, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll(r) err: %v", err)
	}
	if !bytes.Equal(readAll, data) {
		t.Errorf("ReadAll(r) mismatch")
	}

	if err := spool.Close(); err != nil {
		t.Fatalf("Close() err: %v", err)
	}
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Errorf("expected file to be removed after Close(), got err: %v", err)
	}
}

func TestSpoolBodyOverLimit(t *testing.T) {
	data := make([]byte, 100)
	spool, err := spoolBody(bytes.NewReader(data), 50, 16)
	if !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("expected errBodyTooLarge, got %v", err)
	}
	if spool != nil {
		defer spool.Close()
		t.Fatalf("expected nil spool on error, got %+v", spool)
	}
}

func TestSpoolBodyNil(t *testing.T) {
	spool, err := spoolBody(nil, 100, 16)
	if err != nil {
		t.Fatal(err)
	}
	if spool != nil {
		t.Fatalf("expected nil spool, got %+v", spool)
	}
}

func TestGitProxySpoolPushBodyAndCleanup(t *testing.T) {
	p, forwarder, _ := newTestProxy(t)
	p.PushSpoolThresholdBytes = 16
	p.MaxPushBodyBytes = 1024

	// Count temp files before
	getSpoolTempFiles := func() []string {
		files, _ := os.ReadDir(os.TempDir())
		var matched []string
		for _, f := range files {
			if strings.HasPrefix(f.Name(), "grain-gitproxy-push-") {
				matched = append(matched, f.Name())
			}
		}
		return matched
	}

	initialTempFiles := getSpoolTempFiles()

	// Push over threshold (32 bytes > 16 byte threshold)
	body := bytes.Repeat([]byte("p"), 32)
	resp := p.Handle(context.Background(), "POST", "/owner/repo.git/git-receive-pack", "", postHeaders("git-receive-pack", "tok0"), body)
	if resp.Status != 200 {
		t.Fatalf("receive-pack status = %d, want 200", resp.Status)
	}
	if len(forwarder.Calls) != 1 {
		t.Fatalf("forward calls = %d, want 1", len(forwarder.Calls))
	}
	if !bytes.Equal(forwarder.Calls[0].Body, body) {
		t.Errorf("forwarded body mismatch")
	}

	// Verify temp file was cleaned up
	afterTempFiles := getSpoolTempFiles()
	if len(afterTempFiles) != len(initialTempFiles) {
		t.Errorf("temporary file leaked! before=%d, after=%d", len(initialTempFiles), len(afterTempFiles))
	}
}
