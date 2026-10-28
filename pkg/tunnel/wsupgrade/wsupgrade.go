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

// Package wsupgrade dresses grain's own HTTP upgrades (the daemon's relay
// tunnel, an executor's stream) as WebSocket handshakes.
//
// Google's front end, which sits before every *.run.app service, passes
// on only one kind of HTTP/1.1 upgrade: a WebSocket handshake. Anything
// else, such as "Upgrade: grain-relay", it answers itself with a 400 page
// that never reaches IAP or the service. Once the 101 is through it
// relays bytes either way without reading frames, so grain's mux can run
// over the upgraded connection as it does over a direct one.
package wsupgrade

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"net/http"
)

// Protocol is the Upgrade token the clients send.
const Protocol = "websocket"

// magic is RFC 6455's GUID for deriving Sec-WebSocket-Accept.
const magic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// SetRequestHeaders marks h as a WebSocket handshake with a fresh key.
func SetRequestHeaders(h http.Header) {
	var key [16]byte
	_, _ = rand.Read(key[:])
	h.Set("Upgrade", Protocol)
	h.Set("Connection", "Upgrade")
	h.Set("Sec-WebSocket-Version", "13")
	h.Set("Sec-WebSocket-Key", base64.StdEncoding.EncodeToString(key[:]))
}

// AcceptHeader is the "Sec-WebSocket-Accept: ...\r\n" line a 101 answering
// r carries, or "" when r sent no Sec-WebSocket-Key (an older client).
func AcceptHeader(r *http.Request) string {
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return ""
	}
	return "Sec-WebSocket-Accept: " + Accept(key) + "\r\n"
}

// Accept is the Sec-WebSocket-Accept value for key.
func Accept(key string) string {
	sum := sha1.Sum([]byte(key + magic))
	return base64.StdEncoding.EncodeToString(sum[:])
}
