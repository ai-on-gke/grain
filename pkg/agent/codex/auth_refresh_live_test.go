package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
)

// TestLiveCodexSharedLoginSurvivesConcurrentRefresh is the measurement
// docs/codex-authentication.md's "Several processes, one login" section
// reports: what the pinned codex does when more than one process holds
// the same ChatGPT subscription auth.json -- which is what WithAuthFile
// arranges for every run on a backend, and what the non-disruptive
// upgrade arranges across daemon generations -- and the login's access
// token has to be refreshed.
//
// The OAuth authority is a stub (CODEX_REFRESH_TOKEN_URL_OVERRIDE, the
// override codex itself reads) that rotates refresh tokens the way
// auth.openai.com does: each refresh token works once, and presenting a
// spent one is refused with refresh_token_reused. The model is a stub
// too, and it only answers a bearer the authority issued and has not
// expired. No credential is involved anywhere.
//
// Both runs go through Framework.Run with one WithAuthFile, so each gets
// the production arrangement -- its own private CODEX_HOME with auth.json
// a symlink to the one shared file. The authority holds the first
// refresh request until a second arrives (or a few seconds pass), which
// turns "two processes might refresh at the same moment" from a timing
// accident into the case being measured.
//
// Two ways into a refresh are covered, because the pinned codex has two
// and they guard differently (see the doc for the source walk-through):
//
//   - expired: the stored access token's exp is in the past, so each
//     process refreshes proactively before its first request
//     (AuthManager::auth, which re-reads auth.json first).
//   - rejected: the access token's exp is an hour away, but the model
//     refuses it with a 401, so each process takes the 401 recovery path
//     (reload auth.json, then refresh from what it reloaded).
//
// What is asserted is the outcome a deployment cares about: both runs
// finish, and the shared file is left holding the refresh token the
// authority will honour next, so the login is not lost. How many times a
// spent refresh token was presented is logged -- that is codex losing the
// race and recovering, and it is the number to watch if the authority
// ever turns out to revoke the whole token family on reuse, which a stub
// cannot know and this test therefore does not model.
//
// The "+refresher" cases run the same two processes with grain's own
// login refresher (Refresher, refresh.go) running against the shared
// file, as the daemon runs it: one pass before the runs start, then on
// its interval throughout. There the assertion is stronger -- codex
// itself sends the authority no refresh request at all, because grain
// has always renewed the login first, and neither run is lost.
func TestLiveCodexSharedLoginSurvivesConcurrentRefresh(t *testing.T) {
	binary, err := exec.LookPath("codex")
	if err != nil {
		skipUnlessLiveRequired(t, "no codex binary on PATH")
	}
	grain := buildGrainBinary(t)
	for _, tc := range []struct {
		name    string
		expired bool
		// stale holds every refusal after the first until the authority
		// has rotated, so the second process meets its 401 holding an
		// access token another process has already replaced on disk.
		stale bool
		// lossTolerated is the one case the pinned codex is known to
		// lose: a run that fails with refresh_token_reused is accepted
		// there (and only there), but the login must still survive it.
		lossTolerated bool
		// refresher runs grain's login refresher beside the runs.
		refresher bool
	}{
		{name: "expired", expired: true},
		{name: "rejected", lossTolerated: true},
		{name: "stale", stale: true},
		{name: "expired+refresher", expired: true, refresher: true},
		{name: "rejected+refresher", refresher: true},
		{name: "stale+refresher", stale: true, refresher: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := newOAuthStub(tc.expired, tc.stale)
			server := httptest.NewServer(stub)
			defer server.Close()
			t.Setenv("CODEX_REFRESH_TOKEN_URL_OVERRIDE", server.URL+"/oauth/token")
			t.Setenv("OPENAI_API_KEY", "")
			t.Setenv("CODEX_API_KEY", "")

			authFile := filepath.Join(t.TempDir(), "auth.json")
			if err := WriteAuthFile(authFile, stub.initialAuthJSON()); err != nil {
				t.Fatal(err)
			}
			extra := strings.Join([]string{
				`chatgpt_base_url = "` + server.URL + `/backend-api/"`,
				`model_provider = "stub"`,
				``,
				`[model_providers.stub]`,
				`name = "stub"`,
				`base_url = "` + server.URL + `/v1"`,
				`wire_api = "responses"`,
				`requires_openai_auth = true`,
				`request_max_retries = 0`,
				`stream_max_retries = 0`,
			}, "\n")

			if tc.refresher {
				refresher := &Refresher{Files: func() []string { return []string{authFile} }, Interval: 200 * time.Millisecond}
				refresher.RefreshDue(context.Background())
				ctx, stop := context.WithCancel(context.Background())
				done := make(chan struct{})
				go func() { defer close(done); refresher.Run(ctx) }()
				defer func() { stop(); <-done }()
			}

			const runs = 2
			var wg sync.WaitGroup
			errs := make([]error, runs)
			results := make([]*agent.Result, runs)
			for i := 0; i < runs; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					f := New(binary, grain, WithModel("gpt-5.5"), WithAuthFile(authFile), withExtraConfig(extra))
					ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
					defer cancel()
					results[i], errs[i] = f.Run(ctx, agent.RunConfig{SandboxRoot: t.TempDir(), Prompt: "Say done."})
				}(i)
			}
			wg.Wait()

			stub.mu.Lock()
			defer stub.mu.Unlock()
			t.Logf("authority: %d refresh requests from codex, %d presented a spent refresh token, %d from grain's refresher; model: %d answered, %d refused",
				stub.refreshes, stub.reused, stub.grainRefreshes, stub.answered, stub.refused)
			for _, line := range stub.log {
				t.Log(line)
			}
			for i := 0; i < runs; i++ {
				if errs[i] == nil {
					continue
				}
				if tc.lossTolerated && strings.Contains(errs[i].Error(), "refresh token was already used") {
					t.Logf("run %d lost the refresh race, as the pinned codex is known to on its 401 path: %v", i, errs[i])
					continue
				}
				transcript := ""
				if results[i] != nil {
					transcript = results[i].Transcript
				}
				t.Errorf("run %d did not finish on the shared login: %v\n%s", i, errs[i], transcript)
			}
			if tc.refresher {
				if stub.grainRefreshes == 0 {
					t.Fatalf("grain's refresher never renewed the login, so nothing here kept codex from refreshing it")
				}
				if stub.refreshes != 0 {
					t.Errorf("codex sent %d refresh requests of its own with grain's refresher running; want none", stub.refreshes)
				}
			} else if stub.refreshes == 0 {
				t.Fatalf("no run asked the authority for a refresh, so nothing here measured a refresh; did codex stop reading %s?", "CODEX_REFRESH_TOKEN_URL_OVERRIDE")
			}
			data, err := os.ReadFile(authFile)
			if err != nil {
				t.Fatalf("the shared login is gone: %v", err)
			}
			var stored struct {
				Tokens struct {
					AccessToken  string `json:"access_token"`
					RefreshToken string `json:"refresh_token"`
				} `json:"tokens"`
			}
			if err := json.Unmarshal(data, &stored); err != nil {
				t.Fatalf("the shared auth.json no longer parses: %v\n%s", err, data)
			}
			if stored.Tokens.RefreshToken != stub.current {
				t.Errorf("the shared auth.json holds refresh token %q, but the authority will only honour %q next: the login is lost", stored.Tokens.RefreshToken, stub.current)
			}
			if _, ok := stub.issued[stored.Tokens.AccessToken]; !ok {
				t.Errorf("the shared auth.json holds an access token the authority never issued")
			}
		})
	}
}

// oauthStub is the token authority and the model endpoint in one, so the
// model can tell which bearers the authority has issued.
type oauthStub struct {
	mu       sync.Mutex
	current  string               // the one refresh token that works next
	spent    map[string]bool      // refresh tokens already exchanged
	issued   map[string]time.Time // access token -> expiry
	initial  string               // the access token auth.json starts with
	serial   int
	waiting  chan struct{} // closed when a second refresh request arrives
	inflight int
	stale    bool
	sessions map[string]int // codex's Session-Id -> 1, 2: which process asked
	rotated  chan struct{}  // closed once the authority has rotated, in stale

	// refreshes counts codex's own refresh requests; grainRefreshes
	// counts grain's refresher's, told apart by its User-Agent.
	refreshes, reused, answered, refused int
	grainRefreshes                       int
	log                                  []string
}

const (
	stubAccountID = "acct-shared-login-probe"
	stubUserID    = "user-shared-login-probe"
)

func newOAuthStub(expired, stale bool) *oauthStub {
	s := &oauthStub{
		stale:    stale,
		sessions: map[string]int{},
		rotated:  make(chan struct{}),
		current:  "rt-0",
		spent:    map[string]bool{},
		issued:   map[string]time.Time{},
		waiting:  make(chan struct{}),
	}
	exp := time.Now().Add(time.Hour)
	if expired {
		exp = time.Now().Add(-time.Minute)
	}
	s.initial = stubJWT(map[string]any{"exp": exp.Unix(), "sub": "at-0", "https://api.openai.com/auth": stubAuthClaims()})
	// The initial token is never honoured by the model: in "expired" it
	// has expired, and otherwise the point is that it is refused.
	if stale {
		// With one refresh expected there is no second request to wait
		// for, and holding the first would only slow the case down.
		close(s.waiting)
	}
	return s
}

func stubAuthClaims() map[string]any {
	return map[string]any{"chatgpt_account_id": stubAccountID, "chatgpt_user_id": stubUserID, "chatgpt_plan_type": "plus"}
}

func stubIDToken() string {
	return stubJWT(map[string]any{"email": "probe@example.invalid", "https://api.openai.com/auth": stubAuthClaims()})
}

// stubJWT is an unsigned JWT: codex reads the claims and never checks a
// signature it has no key for.
func stubJWT(claims map[string]any) string {
	enc := base64.RawURLEncoding
	header, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	return enc.EncodeToString(header) + "." + enc.EncodeToString(payload) + ".c2ln"
}

func (s *oauthStub) initialAuthJSON() []byte {
	data, _ := json.Marshal(map[string]any{
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token":      stubIDToken(),
			"access_token":  s.initial,
			"refresh_token": "rt-0",
			"account_id":    stubAccountID,
		},
		"last_refresh": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	})
	return data
}

func (s *oauthStub) logf(format string, args ...any) {
	s.log = append(s.log, fmt.Sprintf("%s "+format, append([]any{time.Now().Format("15:04:05.000")}, args...)...))
}

func (s *oauthStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/oauth/token":
		s.serveRefresh(w, r)
	case strings.HasSuffix(r.URL.Path, "/responses"):
		s.serveModel(w, r)
	default:
		// /v1/models, analytics and account settings: codex carries on
		// without them, and none of them involves a refresh.
		http.NotFound(w, r)
	}
}

func (s *oauthStub) serveRefresh(w http.ResponseWriter, r *http.Request) {
	var request struct {
		GrantType    string `json:"grant_type"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.GrantType != "refresh_token" {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}
	fromGrain := r.Header.Get("User-Agent") == refreshUserAgent
	if !fromGrain {
		// Hold codex's first request until a second one is in flight, or
		// long enough that a second would have arrived if it was going to.
		s.mu.Lock()
		s.inflight++
		if s.inflight == 2 {
			close(s.waiting)
		}
		s.mu.Unlock()
		select {
		case <-s.waiting:
		case <-time.After(5 * time.Second):
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if fromGrain {
		s.grainRefreshes++
		s.logf("refresh from grain's refresher")
	} else {
		s.inflight--
		s.refreshes++
	}
	if request.RefreshToken != s.current {
		if s.spent[request.RefreshToken] {
			s.reused++
		}
		s.logf("refresh with %s refused (current is %s)", request.RefreshToken, s.current)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"code":"refresh_token_reused","message":"Your refresh token has already been used to generate a new access token."}}`)
		return
	}
	s.spent[request.RefreshToken] = true
	s.serial++
	s.current = fmt.Sprintf("rt-%d", s.serial)
	access := stubJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "sub": fmt.Sprintf("at-%d", s.serial), "https://api.openai.com/auth": stubAuthClaims()})
	s.issued[access] = time.Now().Add(time.Hour)
	s.logf("refresh with %s -> %s", request.RefreshToken, s.current)
	if s.serial == 1 {
		close(s.rotated)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id_token": stubIDToken(), "access_token": access, "refresh_token": s.current})
}

func (s *oauthStub) serveModel(w http.ResponseWriter, r *http.Request) {
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	expiry, ok := s.issued[bearer]
	valid := ok && time.Now().Before(expiry)
	if valid {
		s.answered++
	} else {
		s.refused++
	}
	session := r.Header.Get("Session-Id")
	if _, ok := s.sessions[session]; !ok {
		s.sessions[session] = len(s.sessions) + 1
	}
	process := s.sessions[session]
	s.logf("process %d: model request with %s: valid=%v", process, tokenSubject(bearer), valid)
	holdRefusal := s.stale && !valid && process > 1
	s.mu.Unlock()
	if holdRefusal {
		// Every process but the first is refused only once the first has
		// rotated and had time to write auth.json, so the refusal is of a
		// token that is stale rather than merely old.
		select {
		case <-s.rotated:
			time.Sleep(500 * time.Millisecond)
		case <-time.After(30 * time.Second):
		}
	}
	if !valid {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"Your authentication token has expired.","code":"token_expired"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	write := func(event map[string]any) {
		data, _ := json.Marshal(event)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], data)
	}
	write(map[string]any{"type": "response.created", "response": map[string]any{"id": "response1"}})
	write(map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "role": "assistant", "id": "msg_done", "content": []any{map[string]any{"type": "output_text", "text": "done"}}}})
	write(map[string]any{"type": "response.completed", "response": map[string]any{"id": "response1"}})
}

// tokenSubject names a stub access token in the log by its sub claim
// rather than printing the whole JWT.
func tokenSubject(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "(not a jwt)"
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "(undecodable)"
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	json.Unmarshal(payload, &claims)
	return claims.Sub
}
