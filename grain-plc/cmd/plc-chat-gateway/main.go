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

// Command plc-chat-gateway receives verified chat webhook commands (status,
// file, reply), forwards them to Grain on behalf of the verified chat user,
// and streams outbound Grain integration events (GET /api/events SSE) back to
// chat threads.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ai-on-gke/grain/grain-plc/pkg/grainclient"
	"github.com/ai-on-gke/grain/grain-plc/pkg/perf"
	"github.com/ai-on-gke/grain/grain-plc/pkg/plcseams"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := Run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "plc-chat-gateway: %v\n", err)
		os.Exit(1)
	}
}

type chatBinding struct {
	SpaceID  string
	ThreadID string
}

// Gateway bridges chat webhooks and Grain's HTTP/SSE APIs.
type Gateway struct {
	client       *grainclient.Client
	perf         *perf.Recorder
	defaultSpace string

	mu           sync.RWMutex
	refBindings  map[string]chatBinding
	taskBindings map[string]chatBinding

	// lastEventID is the newest GET /api/events id WatchSSE has handled,
	// so a reconnect resumes after it rather than replaying the stream.
	lastEventID atomic.Int64
}

// NewGateway creates a new Gateway backed by client.
func NewGateway(client *grainclient.Client, recorder *perf.Recorder, defaultSpace string) *Gateway {
	if recorder == nil {
		recorder = perf.Default()
	}
	return &Gateway{
		client:       client,
		perf:         recorder,
		defaultSpace: defaultSpace,
		refBindings:  make(map[string]chatBinding),
		taskBindings: make(map[string]chatBinding),
	}
}

// Handler returns the HTTP handler for the chat gateway.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", g.handleHealthz)
	mux.Handle("/api/chat/perf", g.perf.Handler())
	mux.Handle("/api/chat/events", g.perf.Middleware("chat.webhook", http.HandlerFunc(g.handleChatWebhook)))
	return mux
}

func (g *Gateway) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"service": "plc-chat-gateway",
	})
}

func (g *Gateway) handleChatWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeChatError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	inv, err := plcseams.CurrentChatAdapter().VerifyAndParse(r.Context(), r)
	if err != nil {
		status := http.StatusUnauthorized
		if !errors.Is(err, plcseams.ErrInvalidSignature) && !errors.Is(err, plcseams.ErrUnauthenticated) {
			status = http.StatusBadRequest
		}
		writeChatError(w, status, "unauthorized_chat_event", err.Error())
		return
	}

	ctx := plcseams.WithPrincipal(r.Context(), &plcseams.Principal{
		Email:   inv.UserEmail,
		Subject: inv.UserEmail,
	})

	if inv.SpaceID != "" && inv.ExternalRef != "" {
		g.bindRef(inv.ExternalRef, inv.SpaceID, inv.ThreadID)
	}

	replyText, taskID, err := g.executeCommand(ctx, inv)
	if err != nil {
		writeChatError(w, http.StatusBadGateway, "command_failed", err.Error())
		return
	}

	if taskID != "" && inv.SpaceID != "" {
		g.bindTask(taskID, inv.SpaceID, inv.ThreadID)
	}

	msg := plcseams.ChatMessage{
		SpaceID:     inv.SpaceID,
		ThreadID:    inv.ThreadID,
		Text:        replyText,
		ExternalRef: inv.ExternalRef,
		TaskID:      taskID,
		EventType:   "chat.reply",
	}
	_ = plcseams.CurrentChatAdapter().PostMessage(ctx, msg)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(msg)
}

func (g *Gateway) executeCommand(ctx context.Context, inv *plcseams.ChatInvocation) (string, string, error) {
	switch strings.ToLower(strings.TrimSpace(inv.Command)) {
	case "status":
		ref := strings.TrimSpace(inv.ExternalRef)
		if len(inv.Args) > 0 && inv.Args[0] != "" {
			ref = inv.Args[0]
		}
		if ref == "" {
			return "", "", errors.New("status requires an externalRef argument")
		}
		inst, _, err := g.client.ResolveInstance(ctx, inv.TargetInstance, "")
		if err != nil {
			return "", "", err
		}
		summary, err := inst.RefSummary(ctx, ref)
		if err != nil {
			return "", "", err
		}
		text := fmt.Sprintf("Rollup for %s: total=%d active=%d blocked=%d merged=%d",
			summary.ExternalRef,
			summary.Counts.Total,
			summary.Counts.Active,
			summary.Counts.Blocked,
			summary.Counts.Merged,
		)
		return text, "", nil

	case "file":
		if len(inv.Args) < 2 {
			return "", "", errors.New("file requires <owner/repo> <title...>")
		}
		repo := inv.Args[0]
		title := strings.Join(inv.Args[1:], " ")
		if !strings.HasPrefix(title, "[PLC]") {
			title = "[PLC] " + title
		}
		inst, _, err := g.client.ResolveInstance(ctx, inv.TargetInstance, repo)
		if err != nil {
			return "", "", err
		}
		created, err := inst.CreateTask(ctx, grainclient.CreateTaskRequest{
			Title:       title,
			Body:        fmt.Sprintf("Filed from chat by %s: %s", inv.UserEmail, title),
			Repo:        repo,
			ExternalRef: inv.ExternalRef,
			FiledVia:    grainclient.ClientPLCChatGateway,
		})
		if err != nil {
			return "", "", err
		}
		return fmt.Sprintf("Filed task #%s (%s) in %s", created.ID, created.Title, repo), created.ID, nil

	case "reply":
		if len(inv.Args) < 2 {
			return "", "", errors.New("reply requires <task-id> <message...>")
		}
		taskID := strings.TrimPrefix(inv.Args[0], "#")
		message := strings.Join(inv.Args[1:], " ")
		inst, _, err := g.client.ResolveInstance(ctx, inv.TargetInstance, "")
		if err != nil {
			return "", "", err
		}
		if err := inst.ReplyTask(ctx, taskID, message); err != nil {
			return "", "", err
		}
		return fmt.Sprintf("Replied to task #%s", taskID), taskID, nil

	default:
		return "", "", fmt.Errorf("unknown chat command %q (supported: status, file, reply)", inv.Command)
	}
}

func (g *Gateway) bindRef(externalRef, spaceID, threadID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.refBindings[externalRef] = chatBinding{SpaceID: spaceID, ThreadID: threadID}
}

func (g *Gateway) bindTask(taskID, spaceID, threadID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.taskBindings[taskID] = chatBinding{SpaceID: spaceID, ThreadID: threadID}
}

// ForwardEvent converts a Grain IntegrationEvent into an outbound ChatMessage
// and delivers it through plcseams.CurrentChatAdapter().
func (g *Gateway) ForwardEvent(ctx context.Context, ev grainclient.IntegrationEvent) error {
	g.mu.RLock()
	binding, ok := g.taskBindings[ev.TaskID]
	if !ok && ev.ExternalRef != "" {
		binding, ok = g.refBindings[ev.ExternalRef]
	}
	g.mu.RUnlock()

	if !ok {
		if g.defaultSpace == "" {
			return nil
		}
		binding = chatBinding{SpaceID: g.defaultSpace}
	}

	var text string
	switch ev.Type {
	case grainclient.EventTaskQuestion:
		text = fmt.Sprintf("Task #%s is parked on a question: %s — %s", ev.TaskID, ev.Summary, ev.Question)
	case grainclient.EventTaskState:
		text = fmt.Sprintf("Task #%s transitioned to state %s", ev.TaskID, ev.State)
		if ev.PRURL != "" {
			text += " (" + ev.PRURL + ")"
		}
	case grainclient.EventTaskCI:
		text = fmt.Sprintf("Task #%s CI status: %s", ev.TaskID, ev.Status)
	case grainclient.EventTaskSecret:
		text = fmt.Sprintf("Task #%s requested secret %s: %s", ev.TaskID, ev.Secret, ev.Summary)
	case grainclient.EventTaskAccess:
		text = fmt.Sprintf("Task #%s requested access: %s", ev.TaskID, ev.Summary)
	default:
		text = fmt.Sprintf("Task #%s event %s", ev.TaskID, ev.Type)
	}

	return plcseams.CurrentChatAdapter().PostMessage(ctx, plcseams.ChatMessage{
		SpaceID:     binding.SpaceID,
		ThreadID:    binding.ThreadID,
		Text:        text,
		ExternalRef: ev.ExternalRef,
		TaskID:      ev.TaskID,
		EventType:   ev.Type,
	})
}

// WatchSSE subscribes to the default Grain instance's GET /api/events SSE stream
// and forwards each event via ForwardEvent.
func (g *Gateway) WatchSSE(ctx context.Context, since int64) error {
	inst, err := g.client.DefaultInstance(ctx)
	if err != nil {
		return err
	}
	return inst.StreamEvents(ctx, grainclient.EventsFilter{Since: since}, func(ev grainclient.IntegrationEvent) error {
		if ev.ID > g.lastEventID.Load() {
			g.lastEventID.Store(ev.ID)
		}
		return g.ForwardEvent(ctx, ev)
	})
}

// watchSSEForever keeps WatchSSE connected: a stream that could not open
// (a refused token, a daemon not up yet) or that ended is reported on
// stderr and retried, resuming after the last event handled. Before this
// the gateway dropped WatchSSE's error and never retried, so a gateway
// started before its daemon, or with a token the daemon refused, silently
// consumed nothing.
func (g *Gateway) watchSSEForever(ctx context.Context, stderr io.Writer, retry time.Duration) {
	for ctx.Err() == nil {
		err := g.WatchSSE(ctx, g.lastEventID.Load())
		if ctx.Err() != nil {
			return
		}
		fmt.Fprintf(stderr, "plc-chat-gateway: GET /api/events stream ended (%v); reconnecting in %s\n", err, retry)
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
	}
}

func writeChatError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": msg,
		},
	})
}

// Run parses flags and starts the PLC Chat Gateway HTTP server (and optional
// background SSE consumer) until ctx is cancelled.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("plc-chat-gateway", flag.ContinueOnError)
	fs.SetOutput(stderr)

	listenAddr := fs.String("listen", envOrDefault("PLC_CHAT_GATEWAY_LISTEN", ":4286"), "HTTP listen address")
	registryURL := fs.String("registry-url", os.Getenv("GRAIN_REGISTRY_URL"), "Grain Registry base URL")
	grainURL := fs.String("grain-url", os.Getenv("GRAIN_URL"), "Default Grain daemon base URL")
	grainToken := fs.String("grain-token", os.Getenv("GRAIN_TOKEN"), "Bearer token sent to the -grain-url daemon only (an access code or an external-agent token; $GRAIN_TOKEN)")
	webhookSecret := fs.String("webhook-secret", os.Getenv("PLC_CHAT_WEBHOOK_SECRET"), "Shared secret for HMAC chat webhook verification")
	defaultSpace := fs.String("default-space", os.Getenv("PLC_CHAT_DEFAULT_SPACE"), "Default chat space ID for outbound events")
	watchSSE := fs.Bool("watch-sse", false, "Subscribe to GET /api/events SSE on startup")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *webhookSecret != "" {
		plcseams.RegisterChatAdapter(plcseams.NewHMACChatAdapter(*webhookSecret))
	}

	recorder := perf.Default()
	client := grainclient.New(grainclient.Config{
		RegistryURL: *registryURL,
		GrainURL:    *grainURL,
		GrainToken:  *grainToken,
		ClientName:  grainclient.ClientPLCChatGateway,
		Perf:        recorder,
	})
	gw := NewGateway(client, recorder, *defaultSpace)

	if *watchSSE {
		go gw.watchSSEForever(ctx, stderr, 5*time.Second)
	}

	ln, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", *listenAddr, err)
	}
	fmt.Fprintf(stdout, "plc-chat-gateway listening on http://%s\n", ln.Addr().String())

	httpSrv := &http.Server{
		Handler:           gw.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
		return <-errCh
	case err := <-errCh:
		return err
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
