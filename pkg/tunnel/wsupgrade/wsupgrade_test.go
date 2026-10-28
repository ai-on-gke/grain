package wsupgrade

import (
	"net/http"
	"testing"
)

func TestAcceptRFC6455Example(t *testing.T) {
	// RFC 6455 section 1.3.
	if got, want := Accept("dGhlIHNhbXBsZSBub25jZQ=="), "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="; got != want {
		t.Fatalf("Accept = %q, want %q", got, want)
	}
}

func TestSetRequestHeaders(t *testing.T) {
	h := http.Header{}
	SetRequestHeaders(h)
	if h.Get("Upgrade") != "websocket" || h.Get("Connection") != "Upgrade" || h.Get("Sec-WebSocket-Version") != "13" {
		t.Fatalf("headers = %v", h)
	}
	if len(h.Get("Sec-WebSocket-Key")) != 24 {
		t.Fatalf("Sec-WebSocket-Key = %q, want 16 bytes base64", h.Get("Sec-WebSocket-Key"))
	}
	r := &http.Request{Header: h}
	if got, want := AcceptHeader(r), "Sec-WebSocket-Accept: "+Accept(h.Get("Sec-WebSocket-Key"))+"\r\n"; got != want {
		t.Fatalf("AcceptHeader = %q, want %q", got, want)
	}
	if AcceptHeader(&http.Request{Header: http.Header{}}) != "" {
		t.Fatal("AcceptHeader without a key should be empty")
	}
}
