package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/ai-on-gke/grain/pkg/agent/claude"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/modelproxy"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// vertexScope is the OAuth scope a Vertex AI call is authorized with --
// the one the claude CLI's own Google auth asks for.
const vertexScope = "https://www.googleapis.com/auth/cloud-platform"

// defaultVertexRegion is the region Claude is called in on Vertex AI when
// CLOUD_ML_REGION names none, as the claude CLI defaults it.
const defaultVertexRegion = "us-east5"

// vertexTokenSources holds one access-token source per connection, so a
// run's every proxied call does not mint a fresh token from its
// service account: a source reuses its token until shortly before it
// expires. Keyed by the connection and checked against a hash of the key
// it was built from, so a key replaced in Team settings builds a new one
// on the next request.
type vertexTokenSources struct {
	mu      sync.Mutex
	sources map[string]vertexTokenSource
}

type vertexTokenSource struct {
	key    [32]byte
	tokens oauth2.TokenSource
}

func (v *vertexTokenSources) token(connection string, keyJSON []byte) (string, error) {
	sum := sha256.Sum256(keyJSON)
	v.mu.Lock()
	source, ok := v.sources[connection]
	if !ok || source.key != sum {
		// Not the request's context: the source outlives this request,
		// and refreshes on a later one. A client of its own bounds how
		// long that may take.
		ctx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Timeout: 30 * time.Second})
		// A service account key and nothing else: an external-account
		// configuration names a command or a URL for this host to run or
		// fetch, which is not what a team admin pasting a key is granting.
		creds, err := google.CredentialsFromJSONWithType(ctx, keyJSON, google.ServiceAccount, vertexScope)
		if err != nil {
			v.mu.Unlock()
			return "", err
		}
		if v.sources == nil {
			v.sources = make(map[string]vertexTokenSource)
		}
		source = vertexTokenSource{key: sum, tokens: creds.TokenSource}
		v.sources[connection] = source
	}
	v.mu.Unlock()
	token, err := source.tokens.Token()
	if err != nil {
		return "", err
	}
	return token.AccessToken, nil
}

// modelProxyVertex is where, and as whom, vendor's proxy forwards a
// request on connection when that connection authenticates through
// Vertex AI -- a Claude connection whose AuthMode is vertex, or any
// Claude connection of a deployment running with CLAUDE_CODE_USE_VERTEX=1,
// exactly as an agent run on it decides (buildClaudeFramework). nil, nil
// for every other connection, which forwards with its key instead
// (modelProxyCredential), and for one that is gone, which that refuses.
//
// It authenticates as the connection's own service account key, the one
// its agent runs hand claude through GOOGLE_APPLICATION_CREDENTIALS, in
// the project that key belongs to unless ANTHROPIC_VERTEX_PROJECT_ID or
// GOOGLE_CLOUD_PROJECT names another, and the region CLOUD_ML_REGION
// names -- the variables claude reads. A connection with no key is
// refused rather than forwarded on this host's own ambient Google login,
// which is the deployment's rather than the team's.
func modelProxyVertex(ctx context.Context, backends []model.AgentBackend, vendor modelproxy.Vendor, connection modelproxy.Connection, secretStore *secrets.Store, tokens *vertexTokenSources) (*modelproxy.VertexTarget, error) {
	if vendor.Name != modelproxy.Claude.Name {
		return nil, nil
	}
	backend := findBackendByID(backends, connection.ID)
	if backend == nil {
		return nil, nil
	}
	if backend.AuthMode != model.ClaudeAuthVertex && os.Getenv("CLAUDE_CODE_USE_VERTEX") != "1" {
		return nil, nil
	}
	refuse := func(why string) (*modelproxy.VertexTarget, error) {
		return nil, &modelproxy.CredentialRefusal{Reason: "connection " + connection.ID +
			" authenticates through Vertex AI, and " + why}
	}
	credential, err := backendCredential(ctx, Config{AgentBackendID: connection.ID}, secretStore, vendor.Secret, "")
	if err != nil {
		return refuse("its service account key could not be read.")
	}
	keyJSON := []byte(credential)
	if credential != "" && !strings.HasPrefix(credential, "{") {
		// A path to a key on this host, which the claude framework
		// accepts in the same field (setupVertexCredentials).
		if keyJSON, err = os.ReadFile(credential); err != nil {
			return refuse("the service account key file it names could not be read on this host.")
		}
	}
	if len(keyJSON) == 0 {
		return refuse("holds no service account key for this grain proxy to authenticate with: set " +
			"one on it in Settings -> Models (/settings/models).")
	}
	var key struct {
		ProjectID string `json:"project_id"`
	}
	_ = json.Unmarshal(keyJSON, &key)
	project := key.ProjectID
	for _, name := range []string{"GOOGLE_CLOUD_PROJECT", "ANTHROPIC_VERTEX_PROJECT_ID"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			project = value
		}
	}
	if project == "" {
		return refuse("neither its service account key nor this deployment names a Google Cloud project to call Claude in.")
	}
	region := strings.TrimSpace(os.Getenv("CLOUD_ML_REGION"))
	if region == "" {
		region = defaultVertexRegion
	}
	// The error is Google's, about a key it was shown, and is not
	// repeated: it can quote the key it failed to parse.
	access, err := tokens.token(connection.ID, keyJSON)
	if err != nil {
		return refuse("this grain proxy could not mint a Google Cloud access token from its service account key; " +
			"check the key in Settings -> Models (/settings/models).")
	}
	target := &modelproxy.VertexTarget{Project: project, Region: region, Token: access}
	for _, m := range claude.VertexModels() {
		target.Models = append(target.Models, modelproxy.VertexModel{ID: m.ID, DisplayName: m.Label})
	}
	return target, nil
}
