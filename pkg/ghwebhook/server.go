package ghwebhook

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"
)

const (
	// MaxLongPollWait caps how long a single GET /v1/events request may hold
	// the HTTP connection waiting for new deliveries.
	MaxLongPollWait = 25 * time.Second
	maxRelayBody    = 8 << 10
)

// EventsResponse is the JSON payload returned by GET /v1/events.
type EventsResponse struct {
	Events    []Event `json:"events"`
	LatestSeq uint64  `json:"latest_seq"`
}

// ServerConfig wires the public webhook receiver and internal daemon relay.
type ServerConfig struct {
	Store             *Store
	WebhookSecret     string
	WebhookSecretFunc func() string
	RelaySecret       string
	Now               func() time.Time
	// OnEvent is an optional callback invoked synchronously after a new
	// non-duplicate event is persisted (used by in-process daemon listeners
	// to wake the reconcile loop directly).
	OnEvent func(Event)
}

// Server serves POST /webhooks/github (public, verified via GitHub's
// X-Hub-Signature-256), GET /v1/events (internal, verified via SignRelay),
// and GET /healthz.
type Server struct {
	store             *Store
	webhookSecret     string
	webhookSecretFunc func() string
	relaySecret       string
	now               func() time.Time
	onEvent           func(Event)
	mux               *http.ServeMux
}

// NewServer constructs a webhook gateway HTTP handler.
func NewServer(cfg ServerConfig) *Server {
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	s := &Server{
		store:             cfg.Store,
		webhookSecret:     cfg.WebhookSecret,
		webhookSecretFunc: cfg.WebhookSecretFunc,
		relaySecret:       cfg.RelaySecret,
		now:               nowFn,
		onEvent:           cfg.OnEvent,
		mux:               http.NewServeMux(),
	}
	s.mux.HandleFunc("POST /webhooks/github", s.receiveWebhook)
	s.mux.HandleFunc("GET /v1/events", s.readEvents)
	s.mux.HandleFunc("GET /healthz", s.health)
	return s
}

func (s *Server) currentWebhookSecret() string {
	if s.webhookSecretFunc != nil {
		return s.webhookSecretFunc()
	}
	return s.webhookSecret
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

// WebhookHandler returns only the public POST /webhooks/github handler,
// suitable for mounting on a dedicated public listener without exposing
// internal relay routes.
func (s *Server) WebhookHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks/github", s.receiveWebhook)
	mux.HandleFunc("GET /healthz", s.health)
	return mux
}

func (s *Server) receiveWebhook(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		http.Error(w, "ghwebhook: store is not configured\n", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxWebhookBody))
	if err != nil {
		http.Error(w, "ghwebhook: request body is too large\n", http.StatusRequestEntityTooLarge)
		return
	}
	if err := VerifyGitHubSignature(s.currentWebhookSecret(), r.Header.Get(GitHubSignatureHeader), body); err != nil {
		if err == ErrNoSecret {
			log.Printf("ghwebhook: refusing POST /webhooks/github -- webhook secret is not configured")
			http.Error(w, "ghwebhook: service is not configured\n", http.StatusServiceUnavailable)
			return
		}
		log.Printf("ghwebhook: refusing POST /webhooks/github: %v", err)
		http.Error(w, "ghwebhook: invalid webhook signature\n", http.StatusUnauthorized)
		return
	}

	eventName := r.Header.Get(GitHubEventHeader)
	deliveryID := r.Header.Get(GitHubDeliveryHeader)
	if eventName == "ping" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "event": "ping"})
		return
	}

	ev, err := ParsePayload(eventName, deliveryID, body)
	if err != nil {
		log.Printf("ghwebhook: rejecting malformed webhook (event=%q delivery=%q): %v", eventName, deliveryID, err)
		http.Error(w, "ghwebhook: malformed webhook payload\n", http.StatusBadRequest)
		return
	}

	stored, duplicate, err := s.store.Append(ev)
	if err != nil {
		log.Printf("ghwebhook: persisting delivery %s: %v", deliveryID, err)
		http.Error(w, "ghwebhook: could not persist event\n", http.StatusInternalServerError)
		return
	}
	if !duplicate {
		log.Printf("ghwebhook: seq=%d event=%s action=%s repo=%s sender=%s delivery=%s",
			stored.Seq, stored.Event, stored.Action, stored.Repo, stored.SenderLogin, stored.DeliveryID)
		if s.onEvent != nil {
			s.onEvent(stored)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"accepted":  true,
		"seq":       stored.Seq,
		"duplicate": duplicate,
	})
}

func (s *Server) readEvents(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		http.Error(w, "ghwebhook: store is not configured\n", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRelayBody))
	if err != nil {
		http.Error(w, "ghwebhook: request body is too large\n", http.StatusRequestEntityTooLarge)
		return
	}
	if err := VerifyRelay(
		s.relaySecret,
		r.Header.Get(RelaySignatureHeader),
		r.Header.Get(RelayTimestampHeader),
		r.Method,
		r.URL.RequestURI(),
		body,
		s.now(),
	); err != nil {
		if err == ErrNoSecret {
			http.Error(w, "ghwebhook: relay is not configured\n", http.StatusServiceUnavailable)
			return
		}
		log.Printf("ghwebhook: refusing %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, "ghwebhook: unauthorized\n", http.StatusUnauthorized)
		return
	}

	q := r.URL.Query()
	var after uint64
	if rawAfter := q.Get("after"); rawAfter != "" {
		parsed, err := strconv.ParseUint(rawAfter, 10, 64)
		if err != nil {
			http.Error(w, "ghwebhook: invalid after cursor\n", http.StatusBadRequest)
			return
		}
		after = parsed
	}
	limit := 100
	if rawLimit := q.Get("limit"); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed <= 0 {
			http.Error(w, "ghwebhook: invalid limit\n", http.StatusBadRequest)
			return
		}
		if parsed > 500 {
			parsed = 500
		}
		limit = parsed
	}
	var wait time.Duration
	if rawWait := q.Get("wait"); rawWait != "" {
		secs, err := strconv.Atoi(rawWait)
		if err != nil || secs < 0 {
			http.Error(w, "ghwebhook: invalid wait\n", http.StatusBadRequest)
			return
		}
		wait = time.Duration(secs) * time.Second
		if wait > MaxLongPollWait {
			wait = MaxLongPollWait
		}
	}

	var (
		events []Event
		latest uint64
	)
	if wait > 0 {
		waitCtx, cancel := context.WithTimeout(r.Context(), wait)
		events, latest = s.store.WaitAfter(waitCtx, after, limit)
		cancel()
	} else {
		events, latest = s.store.After(after, limit)
	}
	if events == nil {
		events = []Event{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(EventsResponse{
		Events:    events,
		LatestSeq: latest,
	})
}

// PruneEvery runs periodic retention sweeps until stop is closed.
func (s *Server) PruneEvery(d time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(d)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if err := s.store.Prune(); err != nil {
				log.Printf("ghwebhook: pruning: %v", err)
			}
		}
	}
}
