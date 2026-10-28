package githubsandbox

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/bwsalmon/grain/pkg/github"
)

// apiVersion pins every request against GitHub's own dated REST
// contract -- the same "field shapes are pinned against GitHub's own
// reference, not guessed" bar pkg/github's own doc comment sets.
const apiVersion = "2022-11-28"

// Installation is the one GitHub App installation FindInstallation
// expects to find -- an installation id (MintToken's own path
// parameter) and the login of the account it's installed on (CreateRepo
// and ListRepos's own org parameter).
type Installation struct {
	ID      int64
	Account string
}

// Token is one installation access token, from MintToken.
type Token struct {
	Token     string
	ExpiresAt time.Time
}

// Repo is what CreateRepo returns -- just enough to confirm the repo
// this package asked for now exists.
type Repo struct {
	Name     string
	FullName string
}

// RepoSummary is one entry from ListRepos -- just enough for Reap to
// decide ownership (the name) and age.
type RepoSummary struct {
	Name      string
	CreatedAt time.Time
}

// appClient is the narrow surface Materialize, PromptSection, Revoke,
// and Reap need against the GitHub API -- narrow enough that this
// package's own tests fake it and need no network, the same "no
// sandbox and no cloud" bar model/capability_test.go sets for a MINT
// provider.
type appClient interface {
	// FindInstallation returns the one account this App is installed
	// on, erroring if that isn't exactly one -- see githubsandbox.go's
	// doc comment on why no config names an account instead.
	FindInstallation(ctx context.Context) (Installation, error)
	// MintToken mints an installation access token carrying
	// permissions, scoped to repos if given or installation-wide if
	// repos is empty. repos names repos by name, not numeric id, so a
	// caller minting a token for a repo it just created (CreateRepo
	// returns no id this package uses) needs no second lookup.
	MintToken(ctx context.Context, installationID int64, repos []string, permissions map[string]string) (Token, error)
	// CreateRepo creates a new private repo named name in org,
	// authenticated with token (an installation-wide token carrying
	// createRepoPermissions).
	CreateRepo(ctx context.Context, token, org, name string) (Repo, error)
	// DeleteRepo deletes owner/name, authenticated with token (a
	// token scoped to just that repo, carrying deleteRepoPermissions).
	// Treats an already-gone repo as success -- see Revoke's own doc
	// comment.
	DeleteRepo(ctx context.Context, token, owner, name string) error
	// ListRepos lists up to 100 private repos in org, authenticated
	// with token (an installation-wide token carrying
	// createRepoPermissions) -- see Reap's own doc comment on why more
	// than that is never expected.
	ListRepos(ctx context.Context, token, org string) ([]RepoSummary, error)
}

// realAppClient is the real appClient, against the live GitHub API.
type realAppClient struct {
	transport github.Transport
	appID     string
	key       *rsa.PrivateKey
	// now is time.Now, overridden by tests -- see newAppClient.
	now func() time.Time
}

// newAppClient parses privateKeyPEM and builds a realAppClient talking
// to host -- the appClient constructor every Provider.client call
// rebuilds fresh; see githubsandbox.go's doc comment on Provider.newClient
// for why nothing here is cached across calls.
func newAppClient(ctx context.Context, appID, privateKeyPEM, host string, insecureHTTP bool) (appClient, error) {
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(privateKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("parsing the App's private key: %w", err)
	}
	rt := github.NewRealTransport(host)
	rt.UseTLS = !insecureHTTP
	return &realAppClient{transport: rt, appID: appID, key: key, now: time.Now}, nil
}

// jwt signs a short-lived App JWT -- GitHub's own bound is 10 minutes;
// this asks for 9, and backdates IssuedAt by 60 seconds, the same clock-
// skew allowance GitHub's own documentation recommends, so a
// controller's clock running slightly ahead of GitHub's never produces
// a JWT GitHub itself would consider "not yet valid".
func (c *realAppClient) signedJWT() (string, error) {
	now := c.now()
	claims := jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now.Add(-60 * time.Second)),
		ExpiresAt: jwt.NewNumericDate(now.Add(9 * time.Minute)),
		Issuer:    c.appID,
	}
	return jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(c.key)
}

// appRequest sends a request authenticated as the App itself (a fresh
// JWT) -- the /app/... endpoints only the App's own identity, never an
// installation token, may call.
func (c *realAppClient) appRequest(method, path string, body []byte) (github.ApiResponse, error) {
	token, err := c.signedJWT()
	if err != nil {
		return github.ApiResponse{}, fmt.Errorf("signing the App JWT: %w", err)
	}
	return c.request(method, path, "Bearer "+token, body)
}

func (c *realAppClient) request(method, path, authorization string, body []byte) (github.ApiResponse, error) {
	headers := map[string]string{
		"Authorization":        authorization,
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": apiVersion,
	}
	if body != nil {
		headers["Content-Type"] = "application/json"
	}
	return c.transport.Request(method, path, headers, body)
}

// expectStatus returns an error describing resp unless its status is
// one of want.
func expectStatus(resp github.ApiResponse, want ...int) error {
	for _, w := range want {
		if resp.Status == w {
			return nil
		}
	}
	return fmt.Errorf("unexpected status %d: %s", resp.Status, resp.Body)
}

func (c *realAppClient) FindInstallation(ctx context.Context) (Installation, error) {
	resp, err := c.appRequest("GET", "/app/installations?per_page=100", nil)
	if err != nil {
		return Installation{}, err
	}
	if err := expectStatus(resp, 200); err != nil {
		return Installation{}, fmt.Errorf("listing installations: %w", err)
	}
	var raw []struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return Installation{}, fmt.Errorf("parsing installations: %w", err)
	}
	if len(raw) != 1 {
		logins := make([]string, len(raw))
		for i, r := range raw {
			logins[i] = r.Account.Login
		}
		return Installation{}, fmt.Errorf(
			"this App must be installed on exactly one account for github-sandbox to know "+
				"where to create repos; found %d (%v)", len(raw), logins,
		)
	}
	return Installation{ID: raw[0].ID, Account: raw[0].Account.Login}, nil
}

func (c *realAppClient) MintToken(ctx context.Context, installationID int64, repos []string, permissions map[string]string) (Token, error) {
	body := map[string]any{"permissions": permissions}
	if len(repos) > 0 {
		body["repositories"] = repos
	}
	data, err := json.Marshal(body)
	if err != nil {
		return Token{}, err
	}
	resp, err := c.appRequest("POST", fmt.Sprintf("/app/installations/%d/access_tokens", installationID), data)
	if err != nil {
		return Token{}, err
	}
	if err := expectStatus(resp, 201); err != nil {
		return Token{}, fmt.Errorf("minting an installation token: %w", err)
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return Token{}, fmt.Errorf("parsing installation token: %w", err)
	}
	return Token{Token: out.Token, ExpiresAt: out.ExpiresAt}, nil
}

func (c *realAppClient) CreateRepo(ctx context.Context, token, org, name string) (Repo, error) {
	data, err := json.Marshal(map[string]any{"name": name, "private": true})
	if err != nil {
		return Repo{}, err
	}
	resp, err := c.request("POST", "/orgs/"+url.PathEscape(org)+"/repos", "Bearer "+token, data)
	if err != nil {
		return Repo{}, err
	}
	if err := expectStatus(resp, 201); err != nil {
		return Repo{}, fmt.Errorf("creating repo: %w", err)
	}
	var out struct {
		Name     string `json:"name"`
		FullName string `json:"full_name"`
	}
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return Repo{}, fmt.Errorf("parsing created repo: %w", err)
	}
	return Repo{Name: out.Name, FullName: out.FullName}, nil
}

func (c *realAppClient) DeleteRepo(ctx context.Context, token, owner, name string) error {
	resp, err := c.request("DELETE", "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), "Bearer "+token, nil)
	if err != nil {
		return err
	}
	if resp.Status == 404 {
		return nil
	}
	if err := expectStatus(resp, 204); err != nil {
		return fmt.Errorf("deleting repo: %w", err)
	}
	return nil
}

func (c *realAppClient) ListRepos(ctx context.Context, token, org string) ([]RepoSummary, error) {
	resp, err := c.request("GET", "/orgs/"+url.PathEscape(org)+"/repos?type=private&per_page=100", "Bearer "+token, nil)
	if err != nil {
		return nil, err
	}
	if err := expectStatus(resp, 200); err != nil {
		return nil, fmt.Errorf("listing repos: %w", err)
	}
	var raw []struct {
		Name      string    `json:"name"`
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		return nil, fmt.Errorf("parsing repo list: %w", err)
	}
	out := make([]RepoSummary, len(raw))
	for i, r := range raw {
		out[i] = RepoSummary{Name: r.Name, CreatedAt: r.CreatedAt}
	}
	return out, nil
}
