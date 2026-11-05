package registry

import (
	"bufio"
	"context"
	"net/http"
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
	conn, err := wsupgrade.Upgrade(w, r, http.Header{
		api.HeaderConnectProtocol: []string{api.ConnectProtocolVersion},
	})
	if err != nil {
		return nil, err
	}
	m := mux.NewMux(conn, mux.Config{
		IsServer:         true,
		MaxStreams:       t.maxConcurrentStreams,
		MaxBufferedBytes: t.maxBufferedBytes,
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
	return http.ReadResponse(bufio.NewReader(stream), r)
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
