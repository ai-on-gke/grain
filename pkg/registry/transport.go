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

package registry

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/tunnel/mux"
	"github.com/ai-on-gke/grain/pkg/tunnel/wsupgrade"
)

// wsMuxTransport is the default "ws-mux" ConnectTransport implementation
// backed by pkg/tunnel/wsupgrade and pkg/tunnel/mux.
type wsMuxTransport struct {
	maxConcurrentStreams int
	maxBufferedBytes     int
}

// NewWSMuxTransport constructs the default "ws-mux" ConnectTransport.
func NewWSMuxTransport(cfg TunnelConfig) ConnectTransport {
	maxStreams := cfg.MaxConcurrentStreams
	if maxStreams <= 0 {
		maxStreams = DefaultTunnelMaxConcurrentStreams
	}
	maxBuf := cfg.MaxBufferedBytes
	if maxBuf <= 0 {
		maxBuf = DefaultTunnelMaxBufferedBytes
	}
	return &wsMuxTransport{
		maxConcurrentStreams: maxStreams,
		maxBufferedBytes:     maxBuf,
	}
}

func (t *wsMuxTransport) Name() string {
	return DefaultConnectTransportName
}

func (t *wsMuxTransport) Accept(w http.ResponseWriter, r *http.Request) (ConnectSession, error) {
	hdr := http.Header{
		api.HeaderConnectProtocol: []string{api.ConnectProtocolVersion},
	}
	if sid := w.Header().Get(api.HeaderTunnelSession); sid != "" {
		hdr.Set(api.HeaderTunnelSession, sid)
	}
	conn, err := wsupgrade.Upgrade(w, r, hdr)
	if err != nil {
		return nil, err
	}
	m := mux.NewMux(conn, mux.Config{
		IsServer:                     true,
		PingInterval:                 15 * time.Second,
		PingTimeout:                  15 * time.Second,
		MaxStreams:                   t.maxConcurrentStreams,
		MaxPeerInitiatedStreams:      0,
		DisallowPeerInitiatedStreams: true,
		MaxPeerStreamViolations:      16,
		MaxBufferedBytes:             t.maxBufferedBytes,
	})
	return &wsMuxSession{m: m}, nil
}

type wsMuxSession struct {
	m *mux.Mux
}

func (s *wsMuxSession) RoundTrip(r *http.Request) (*http.Response, error) {
	stream, err := s.m.OpenStream(r.Context())
	if err != nil {
		return nil, err
	}
	if err := r.Write(stream); err != nil {
		_ = stream.Close()
		return nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(stream), r)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	resp.Body = &streamClosingBody{
		ReadCloser: resp.Body,
		stream:     stream,
	}
	return resp, nil
}

type streamClosingBody struct {
	io.ReadCloser
	stream io.Closer
	once   sync.Once
	err    error
}

func (b *streamClosingBody) Close() error {
	b.once.Do(func() {
		b.err = errors.Join(b.ReadCloser.Close(), b.stream.Close())
	})
	return b.err
}

func (s *wsMuxSession) Ping(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-s.m.Done():
		return 0, mux.ErrSessionClosed
	default:
		return time.Since(start), nil
	}
}

func (s *wsMuxSession) Done() <-chan struct{} {
	return s.m.Done()
}

func (s *wsMuxSession) Close() error {
	return s.m.Close()
}

func init() {
	RegisterConnectTransport(DefaultConnectTransportName, func(cfg TunnelConfig) (ConnectTransport, error) {
		return NewWSMuxTransport(cfg), nil
	})
}
