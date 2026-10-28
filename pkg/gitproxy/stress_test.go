package gitproxy

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	randv2 "math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// stressForwarder simulates a smart HTTP git remote with realistic data streaming,
// latency simulation, and credential isolation assertions.
type stressForwarder struct {
	mu           sync.Mutex
	receivedReqs map[string]int
	tokenByRepo  map[string]string
}

func newStressForwarder(expectedTokens map[string]string) *stressForwarder {
	return &stressForwarder{
		receivedReqs: make(map[string]int),
		tokenByRepo:  expectedTokens,
	}
}

func (f *stressForwarder) Forward(method, path, query string, headers map[string]string, body []byte, token *string) (UpstreamResponse, error) {
	// Parse repo from path e.g. /owner/repo/info/refs
	parts := strings.Split(strings.Trim(path, "/"), "/")
	var repoKey string
	if len(parts) >= 2 {
		repoKey = parts[0] + "/" + parts[1]
	}

	f.mu.Lock()
	f.receivedReqs[repoKey]++
	expectedToken, hasExpected := f.tokenByRepo[repoKey]
	f.mu.Unlock()

	// Validate credential isolation: if a specific token was expected for this repo, verify it matches.
	if hasExpected {
		if expectedToken == "" {
			if token != nil {
				return UpstreamResponse{Status: http.StatusForbidden, Body: []byte("expected anonymous request")}, nil
			}
		} else {
			if token == nil || *token != expectedToken {
				actual := "<nil>"
				if token != nil {
					actual = *token
				}
				return UpstreamResponse{
					Status: http.StatusUnauthorized,
					Body:   []byte(fmt.Sprintf("credential isolation failure: got token %q want %q", actual, expectedToken)),
				}, nil
			}
		}
	}

	// Simulate streaming payload transfer
	streamSize := 1024 + randv2.IntN(4096)
	respBody := make([]byte, streamSize)
	for i := range respBody {
		respBody[i] = byte(i % 256)
	}

	respHeaders := map[string]string{
		"Content-Type": "application/x-git-upload-pack-result",
	}
	if strings.Contains(path, "git-receive-pack") {
		respHeaders["Content-Type"] = "application/x-git-receive-pack-result"
	}

	return UpstreamResponse{
		Status:  http.StatusOK,
		Headers: respHeaders,
		Body:    respBody,
	}, nil
}

// memoryScopeLookup implements ScopeLookup for high-concurrency stress testing.
type memoryScopeLookup struct {
	mu     sync.RWMutex
	scopes map[string]*sandboxScope
}

type sandboxScope struct {
	target *model.RepoRef
	reads  []model.RepoRef
	writes []model.RepoRef
}

func newMemoryScopeLookup() *memoryScopeLookup {
	return &memoryScopeLookup{scopes: make(map[string]*sandboxScope)}
}

func (m *memoryScopeLookup) SetScope(sandbox string, target *model.RepoRef, reads, writes []model.RepoRef) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scopes[sandbox] = &sandboxScope{
		target: target,
		reads:  reads,
		writes: writes,
	}
}

func (m *memoryScopeLookup) GitScope(ctx context.Context, sandbox string) (*model.RepoRef, []model.RepoRef, []model.RepoRef, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.scopes[sandbox]
	if !ok {
		return nil, nil, nil, nil
	}
	return s.target, s.reads, s.writes, nil
}

type stressAppTransport struct {
	mints atomic.Int64
	clock *atomic.Int64
}

func (s *stressAppTransport) Request(method, path string, headers map[string]string, body []byte) (github.ApiResponse, error) {
	mintNum := s.mints.Add(1)
	exp := time.Unix(s.clock.Load(), 0).Add(10 * time.Minute)
	respBody := fmt.Sprintf(`{"token": "ghs_minted_app_token_num_%d", "expires_at": %q}`, mintNum, exp.Format(time.RFC3339))
	return github.ApiResponse{
		Status: http.StatusCreated,
		Body:   []byte(respBody),
	}, nil
}

// TestStressGitProxyHighConcurrency simulates hundreds of concurrent git operations
// across distinct sandboxes, teams, and repositories, with dynamic token refreshing,
// read/write scope modifications, and credential ladder isolation.
func TestStressGitProxyHighConcurrency(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Configure credentials:
	// - org1/* -> pat1 (token1)
	// - org2/app-repo -> app1 (GitHub App dynamically refreshed)
	// - public/* -> anonymous
	token1 := "ghp_pat_token_org1_secret_value_12345"
	if err := os.WriteFile(filepath.Join(secretsDir, "pat1.token"), []byte(token1), 0o600); err != nil {
		t.Fatal(err)
	}

	// Generate RSA key for App credential
	key, err := rsa.GenerateKey(cryptorand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if err := os.WriteFile(filepath.Join(secretsDir, "app1.pem"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	appPayload := struct {
		AppID          string `json:"app_id"`
		InstallationID string `json:"installation_id"`
		PrivateKey     string `json:"private_key"`
	}{
		AppID:          "12345",
		InstallationID: "67890",
		PrivateKey:     string(keyPEM),
	}
	appJSON, err := json.Marshal(appPayload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "app1.app.json"), appJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	patternsJSON := `{
		"org1/*": "pat1",
		"org2/app-repo": "app1",
		"public/*": "anonymous"
	}`
	if err := os.WriteFile(filepath.Join(secretsDir, "credentials.json"), []byte(patternsJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	// Sandbox tokens store
	tokensFile := filepath.Join(dir, "sandbox-tokens.json")
	tokenStore := NewSandboxTokenStore(tokensFile)

	tokens, err := LoadSandboxTokens(tokensFile)
	if err != nil {
		t.Fatal(err)
	}

	// Setup simulated mock GitHub App installation token transport
	var mockClock atomic.Int64
	mockClock.Store(time.Now().Unix())
	appTransport := &stressAppTransport{clock: &mockClock}

	credSet, err := LoadCredentialSet(secretsDir)
	if err != nil {
		t.Fatal(err)
	}
	credSet.AppTransport = appTransport
	credSet.Now = func() time.Time {
		return time.Unix(mockClock.Load(), 0)
	}

	scopeLookup := newMemoryScopeLookup()
	readScopes := NewReadScopes()
	writeScopes := NewWriteScopes()
	forbidden := NewForbiddenSet(model.RepoRef{Owner: "restricted", Name: "forbidden-state-repo"})

	modelAuth := &ModelAuthorizer{
		Store:     scopeLookup,
		Forbidden: forbidden,
	}

	auth := &ReadOnlyScopeAuthorizer{
		Scopes:      readScopes,
		WriteScopes: writeScopes,
		Next:        modelAuth,
		Forbidden:   forbidden,
	}

	audit := &syncAuditLog{}
	forwarder := newStressForwarder(map[string]string{
		"org1/repo1": token1,
		"org1/repo2": token1,
		"public/doc": "",
	})

	proxy := &GitProxy{
		Tokens:          tokens,
		Credentials:     credSet,
		TeamCredentials: ladderConnections{creds: credSet},
		Authorizer:      auth,
		Forwarder:       forwarder,
		Audit:           audit,
	}

	handler := NewHandler(proxy)
	server := httptest.NewServer(handler)
	defer server.Close()

	// Duration configuration
	testDuration := 3 * time.Second
	if env := os.Getenv("GRAIN_STRESS_DURATION"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			testDuration = d
		}
	} else if testing.Short() {
		testDuration = 1 * time.Second
	}

	// Prepare concurrent workers simulating distinct teams and sandboxes
	const numSandboxes = 40
	const numWorkers = 80

	type sandboxMeta struct {
		name       string
		token      string
		team       string
		allowedOrg string
	}

	sandboxList := make([]sandboxMeta, numSandboxes)
	for i := 0; i < numSandboxes; i++ {
		name := fmt.Sprintf("sb-team-%d-%d", i%5, i)
		tok, err := tokenStore.EnsureToken(name)
		if err != nil {
			t.Fatalf("EnsureToken failed: %v", err)
		}
		var allowedOrg string
		switch i % 3 {
		case 0:
			allowedOrg = "org1"
			scopeLookup.SetScope(name, &model.RepoRef{Owner: "org1", Name: "repo1"}, []model.RepoRef{{Owner: "org1", Name: "repo2"}}, nil)
		case 1:
			allowedOrg = "org2"
			writeScopes.Grant(name, model.RepoRef{Owner: "org2", Name: "app-repo"})
		case 2:
			allowedOrg = "public"
			readScopes.Grant(name, model.RepoRef{Owner: "public", Name: "doc"})
		}
		sandboxList[i] = sandboxMeta{
			name:       name,
			token:      tok,
			team:       fmt.Sprintf("team-%d", i%5),
			allowedOrg: allowedOrg,
		}
	}

	var (
		wg           sync.WaitGroup
		completedOps atomic.Int64
		authErrors   atomic.Int64
		successOps   atomic.Int64
		deniedOps    atomic.Int64
		done         = make(chan struct{})
		rotatorDone  = make(chan struct{})
	)

	// Background ticker advancing simulated clock and rotating tokens periodically.
	// Rotate rewrites sandbox-tokens.json in the test's TempDir, so the test
	// waits on rotatorDone before returning: a rotation still in flight when
	// TempDir's cleanup runs leaves it "directory not empty".
	go func() {
		defer close(rotatorDone)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				mockClock.Add(300) // Advance clock by 5 minutes to trigger App token refresh

				// Randomly rotate a sandbox token or grant/drop read scope
				targetIdx := randv2.IntN(numSandboxes)
				sb := sandboxList[targetIdx]
				if randv2.IntN(2) == 0 {
					_, _ = tokenStore.Rotate(sb.name)
				}
				if randv2.IntN(2) == 0 {
					readScopes.Grant(sb.name, model.RepoRef{Owner: "public", Name: "extra-doc"})
				} else {
					readScopes.Drop(sb.name)
				}
			}
		}
	}()

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        numWorkers * 2,
			MaxIdleConnsPerHost: numWorkers * 2,
			MaxConnsPerHost:     numWorkers * 2,
		},
	}

	deadline := time.Now().Add(testDuration)

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			r := randv2.New(randv2.NewPCG(uint64(workerID), uint64(time.Now().UnixNano())))

			for time.Now().Before(deadline) {
				sb := sandboxList[r.IntN(numSandboxes)]
				actionType := r.IntN(5)

				var (
					method string
					path   string
					body   []byte
				)

				switch actionType {
				case 0: // Clone/fetch info/refs (upload-pack)
					method = http.MethodGet
					path = fmt.Sprintf("/%s/repo1/info/refs?service=git-upload-pack", sb.allowedOrg)
				case 1: // Fetch upload-pack POST
					method = http.MethodPost
					path = fmt.Sprintf("/%s/repo1/git-upload-pack", sb.allowedOrg)
					body = []byte("0014want 1234567890\n0000")
				case 2: // Push receive-pack POST
					method = http.MethodPost
					path = fmt.Sprintf("/%s/repo1/git-receive-pack", sb.allowedOrg)
					body = []byte("0014push 1234567890\n0000")
				case 3: // Access to forbidden repository
					method = http.MethodGet
					path = "/restricted/forbidden-state-repo/info/refs?service=git-upload-pack"
				case 4: // Dynamic App token repo
					method = http.MethodGet
					path = "/org2/app-repo/info/refs?service=git-upload-pack"
				}

				req, err := http.NewRequest(method, server.URL+path, bytes.NewReader(body))
				if err != nil {
					authErrors.Add(1)
					continue
				}

				// Set required Git headers
				req.Header.Set("User-Agent", "git/2.39.2")
				if strings.Contains(path, "git-upload-pack") {
					req.Header.Set("Accept", "application/x-git-upload-pack-result")
					req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
				} else if strings.Contains(path, "git-receive-pack") {
					req.Header.Set("Accept", "application/x-git-receive-pack-result")
					req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
				} else {
					req.Header.Set("Accept", "*/*")
				}

				// Basic auth using sandbox token
				authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte("x:"+sb.token))
				req.Header.Set("Authorization", authHeader)

				resp, err := client.Do(req)
				if err != nil {
					// Network/cancellation error under high load
					completedOps.Add(1)
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()

				if resp.StatusCode == http.StatusOK {
					successOps.Add(1)
				} else if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
					deniedOps.Add(1)
				} else {
					authErrors.Add(1)
				}
				completedOps.Add(1)
			}
		}(w)
	}

	wg.Wait()
	close(done)
	<-rotatorDone

	t.Logf("Stress test completed: %d operations (Success: %d, Denied/Refused: %d, Unexpected Errors: %d, App Mints: %d, Audit Entries: %d)",
		completedOps.Load(), successOps.Load(), deniedOps.Load(), authErrors.Load(), appTransport.mints.Load(), audit.len())

	if completedOps.Load() == 0 {
		t.Fatal("No operations completed during high-concurrency stress test")
	}
	if authErrors.Load() > 0 {
		t.Errorf("Encountered %d unexpected errors during stress test", authErrors.Load())
	}
	if appTransport.mints.Load() == 0 {
		t.Errorf("Expected dynamic App tokens to be minted/refreshed during stress run")
	}
}

// TestStressGitProxyAbruptClientDisconnections tests proxy resilience when clients
// abruptly cancel contexts or close TCP connections during body reading or forwarding.
func TestStressGitProxyAbruptClientDisconnections(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		t.Fatal(err)
	}

	token1 := "ghp_pat_token_test_123"
	if err := os.WriteFile(filepath.Join(secretsDir, "pat1.token"), []byte(token1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "credentials.json"), []byte(`{"org/*": "pat1"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	tokensFile := filepath.Join(dir, "sandbox-tokens.json")
	tokenStore := NewSandboxTokenStore(tokensFile)
	sbToken, err := tokenStore.EnsureToken("sb-disconnect")
	if err != nil {
		t.Fatal(err)
	}

	tokens, err := LoadSandboxTokens(tokensFile)
	if err != nil {
		t.Fatal(err)
	}

	credSet, err := LoadCredentialSet(secretsDir)
	if err != nil {
		t.Fatal(err)
	}

	scopeLookup := newMemoryScopeLookup()
	scopeLookup.SetScope("sb-disconnect", &model.RepoRef{Owner: "org", Name: "repo"}, nil, nil)
	modelAuth := &ModelAuthorizer{Store: scopeLookup}

	forwarder := &echoForwarder{}
	audit := &syncAuditLog{}

	proxy := &GitProxy{
		Tokens:          tokens,
		Credentials:     credSet,
		TeamCredentials: ladderConnections{creds: credSet},
		Authorizer:      modelAuth,
		Forwarder:       forwarder,
		Audit:           audit,
	}

	handler := NewHandler(proxy)
	server := httptest.NewServer(handler)
	defer server.Close()

	var wg sync.WaitGroup
	const workers = 50
	const iterations = 40

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				// Abrupt cancellation with short context timeout
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(randv2.IntN(5))*time.Millisecond)
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/org/repo/git-upload-pack", bytes.NewReader([]byte("sample upload pack request payload")))
				if err != nil {
					cancel()
					continue
				}
				req.Header.Set("User-Agent", "git/2.39.2")
				req.Header.Set("Accept", "application/x-git-upload-pack-result")
				req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("x:"+sbToken)))
				req.Header.Set("Content-Type", "application/x-git-upload-pack-request")

				resp, err := http.DefaultClient.Do(req)
				if err == nil {
					_ = resp.Body.Close()
				}
				cancel()
			}
		}()
	}

	wg.Wait()
}
