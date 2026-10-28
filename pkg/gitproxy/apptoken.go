package gitproxy

// GitHub App authentication: minting a short-lived installation access
// token from an App's own private key, so a credential in
// secrets/github/ can be an App installation instead of a bare PAT.
//
// terraform/gcp/README.md's "What the PAT needs" section explains why
// this exists: a fine-grained PAT has no "Checks" permission to grant at
// all (GitHub offered one initially and withdrew it -- only GitHub Apps
// may use that API in the meantime), so ListCheckRuns returns a permanent
// 403 to a PAT-backed deployment -- the fact bwsalmon/agents#483 surfaced
// as a UI warning (AutoMergeDegraded) rather than fixed, because fixing
// it needed a credential kind this package didn't have yet. This is that
// credential kind (bwsalmon/agents#491).
//
// An App is no longer the *only* way to get auto-merge working, though:
// pkg/orchestrator's checkRunsFor now falls back to the Actions API,
// which a fine-grained PAT can reach with "Actions" read, so a deployment
// whose CI is GitHub Actions needs no App. What still needs one is CI
// reported through the Checks API by anything other than Actions -- a
// third-party provider, or a review bot -- which that fallback cannot
// see and only a checks-capable credential can.
//
// GitHub's own flow ("Authenticating as a GitHub App" in its REST API
// docs): sign a short-lived RS256 JWT claiming iss=<app ID>, then
// exchange it for an installation access token via
// POST /app/installations/{installation_id}/access_tokens. The token
// that comes back is bearer-shaped exactly like a PAT -- RealForwarder's
// Basic-auth convention and RESTClient's Authorization header both
// already send whatever string Credential.Token holds, with no
// PAT-versus-App-token distinction anywhere in either -- but it expires
// in about an hour, so unlike a *.token file's contents it cannot just be
// read once and cached forever; see CredentialSet.load for the refresh.

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
)

// MintFailure is one failed attempt to turn an App credential into an
// installation access token, kept rather than discarded so the failure
// can be *shown* to somebody.
//
// load serves a credential it cannot mint a token for anonymously --
// deliberately, so a public repo goes on being readable through a
// transient failure -- and until this type existed that decision was
// visible nowhere but the daemon's own log (grain/task-158). A private
// repo answered 401 to the unauthenticated request and the sandbox's git
// said "Authentication failed"; a public one went on working right up to
// the first push, which failed with "No anonymous write access"; and the
// credential's row on Settings -> GitHub read "present" throughout.
// Every one of those is a sentence about something other than the App
// installation that actually broke.
//
// The material was always there -- mintInstallationToken returns
// GitHub's own status and body as a *github.Error -- it was just thrown
// away at the call site. This is that material, carried on the
// Credential the failure produced (Credential.MintFailure) and so
// reaching everything that already handles one: the proxy's refusal
// messages, the named-token capability check, and the Settings pane.
type MintFailure struct {
	// Time is when the mint was attempted, by the CredentialSet's own
	// clock (CredentialSet.Now).
	Time time.Time
	// Status is what GitHub answered the token exchange with, and 0 when
	// the exchange never got an answer at all -- a key that would not
	// sign, a name that would not resolve, a body that would not parse.
	Status int
	// Message is what to tell a human: GitHub's own sentence when the
	// response carried one (a suspended installation answers
	// {"message":"This installation has been suspended"}), and this
	// package's own error text otherwise. Empty when neither was fit to
	// repeat.
	//
	// Already reduced to a short, single-line, plain-text string by
	// shortMessage below, because these are somebody else's bytes and
	// they end up in a git "remote:" line, in a JSON API response and on
	// a settings pane.
	Message string
	// NoExchange marks a failure decided before any token exchange was
	// attempted (a credential on a host with no App API, appTransportFor), whose Message is the
	// whole reason rather than something GitHub or the exchange said.
	NoExchange bool
}

// Describe is the failure as one clause, for a sentence that has already
// named the credential it is about.
func (f MintFailure) Describe() string {
	switch {
	case f.NoExchange && f.Message != "":
		return f.Message
	case f.Status != 0 && f.Message != "":
		return fmt.Sprintf("GitHub answered %d to the token exchange: %s", f.Status, strconv.Quote(f.Message))
	case f.Status != 0:
		return fmt.Sprintf("GitHub answered %d to the token exchange, with no message", f.Status)
	case f.Message != "":
		return "the token exchange did not complete: " + f.Message
	}
	return "the token exchange failed for a reason that could not be repeated here -- the daemon's own log has it"
}

// newMintFailure reads a failed mintInstallationToken call into
// something reportable: GitHub's status and its own message where the
// call reached GitHub at all, and the error's own text where it did not.
func newMintFailure(err error, now time.Time) *MintFailure {
	failure := &MintFailure{Time: now}
	var apiErr *github.Error
	if errors.As(err, &apiErr) {
		failure.Status = apiErr.Status
		failure.Message = vendorMessage(apiErr.Body)
	}
	if failure.Message == "" {
		failure.Message = shortMessage(err.Error())
	}
	return failure
}

// vendorMessage is GitHub's own sentence about a refused token exchange,
// out of the JSON body its API answers errors with
// ({"message":"This installation has been suspended","documentation_url":...}).
//
// That sentence is the one fact about this failure no part of this
// deployment can work out for itself -- "suspended", "Integration not
// found" and "A JSON web token could not be decoded" send an operator to
// three different places -- so it is worth the unmarshal. A body that is
// not that shape yields nothing, and the caller falls back to the error
// text.
func vendorMessage(body []byte) string {
	var payload struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return shortMessage(payload.Message)
}

// maxMintMessage bounds a repeated message the way upstreamNote bounds
// the one it quotes into a git "remote:" line, and for the same reason:
// past a sentence or two this is noise wrapped around the thing an
// operator is trying to read.
const maxMintMessage = 200

// shortMessage reduces a message to something safe to repeat anywhere
// this failure is shown -- collapsed onto one line and bounded, or
// dropped entirely if it is not plain text (isPlainText's own doc
// comment: an HTML error page from something in front of GitHub is not a
// sentence).
func shortMessage(s string) string {
	s = collapseSpace(s)
	if s == "" || !isPlainText(s) {
		return ""
	}
	// By rune, so a truncation cannot leave invalid UTF-8 behind in a
	// string that is about to be JSON-encoded.
	if runes := []rune(s); len(runes) > maxMintMessage {
		s = string(runes[:maxMintMessage]) + "..."
	}
	return s
}

// AppCredential is one GitHub App installation (or multi-installation App
// when InstallationID is empty) this deployment can mint installation
// tokens for. AppID and InstallationID are kept as strings, as GitHub
// itself prints them and as the JSON below carries them, because neither is
// ever arithmetic.
type AppCredential struct {
	AppID          string
	InstallationID string
	PrivateKey     *rsa.PrivateKey
	WebhookSecret  string
}

// ParseAppPrivateKey parses the PEM-encoded RSA private key GitHub
// generates when an App's private key is created. GitHub's own download
// is PKCS#1 ("RSA PRIVATE KEY"); PKCS#8 ("PRIVATE KEY") is accepted too,
// since a key manager an operator routes the download through may
// re-encode it.
func ParseAppPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("gitproxy: no PEM block found in GitHub App private key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("gitproxy: parsing GitHub App private key: %w", err)
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("gitproxy: GitHub App private key is not an RSA key")
	}
	return rsaKey, nil
}

func base64URL(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

// appJWT signs the short-lived JSON Web Token GitHub's App-authentication
// flow exchanges for an installation access token.
//
// iat is backdated a minute -- GitHub's own docs call this out -- so
// clock drift that puts this host slightly ahead of GitHub's own clock
// cannot land iat in GitHub's future and get the JWT rejected as
// "not yet valid". exp is nine minutes out, under GitHub's ten-minute
// ceiling with a minute of margin: nothing here holds onto the JWT past
// the one mintInstallationToken call that immediately follows, so the
// gap between "signed" and "GitHub receives it" is the only clock skew
// that matters, not this token's own lifetime.
func appJWT(appID string, key *rsa.PrivateKey, now time.Time) (string, error) {
	header := base64URL([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-time.Minute).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": appID,
	})
	if err != nil {
		return "", fmt.Errorf("gitproxy: encoding GitHub App JWT claims: %w", err)
	}
	signingInput := header + "." + base64URL(claims)
	hashed := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hashed[:])
	if err != nil {
		return "", fmt.Errorf("gitproxy: signing GitHub App JWT: %w", err)
	}
	return signingInput + "." + base64URL(sig), nil
}

// mintInstallationToken exchanges cred's App JWT for a fresh installation
// access token over transport, which is the REST API of the App's own
// host -- api.github.com, or https://<host>/api/v3 on a GitHub Enterprise
// Server (CredentialSet.appTransportFor) -- and never another host's,
// since the JWT is good there for its whole ten-minute life.
//
// repo, when non-empty, narrows the token to that one repository of the
// installation (GitHub's "repositories" request field, which takes bare
// repository names -- the installation's own account is the owner).
// Without it GitHub mints a token carrying the installation's entire
// configured scope, every repository and every permission it was
// installed with (#2388), so a caller that knows which repository it is
// about to use the token against should always say so: a leaked or
// misrouted narrowed token reaches that repository and nothing else.
// A repository outside the installation makes GitHub refuse the mint
// (422), which load serves as the same anonymous-with-MintFailure a
// broken key gets -- the full-scope token could not have reached that
// repository either. Permissions are deliberately not narrowed: asking
// for one the installation was not granted fails the whole mint, and
// this package does not know which ones a given App was installed with.
func mintInstallationToken(transport github.Transport, cred AppCredential, now time.Time, repo string) (token string, expiresAt time.Time, err error) {
	if cred.InstallationID == "" {
		return "", time.Time{}, errors.New("gitproxy: no installation ID resolved for GitHub App")
	}
	jwt, err := appJWT(cred.AppID, cred.PrivateKey, now)
	if err != nil {
		return "", time.Time{}, err
	}
	headers := map[string]string{
		"Authorization": "Bearer " + jwt,
		"Accept":        "application/vnd.github+json",
	}
	var body []byte
	if repo != "" {
		body, err = json.Marshal(map[string][]string{"repositories": {repo}})
		if err != nil {
			return "", time.Time{}, fmt.Errorf("gitproxy: encoding installation token request: %w", err)
		}
		headers["Content-Type"] = "application/json"
	}
	resp, err := transport.Request(
		"POST", fmt.Sprintf("/app/installations/%s/access_tokens", cred.InstallationID),
		headers, body,
	)
	if err != nil {
		return "", time.Time{}, err
	}
	if resp.Status != 201 {
		return "", time.Time{}, &github.Error{Status: resp.Status, Body: resp.Body}
	}
	var data struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return "", time.Time{}, fmt.Errorf("gitproxy: decoding installation token response: %w", err)
	}
	expiresAt, err = time.Parse(time.RFC3339, data.ExpiresAt)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("gitproxy: parsing installation token expiry %q: %w", data.ExpiresAt, err)
	}
	return data.Token, expiresAt, nil
}

// resolveInstallationID discovers which GitHub App installation covers
// scope ("owner/repo" for a repository, "owner/" for an account-wide
// lookup, or "" for any single installation of the App) using the App's
// own RS256 JWT. Called only when <name>.app.json omits installation_id,
// so one standalone GitHub App credential can serve repositories across
// multiple organizations or accounts without pinning a single installation.
func resolveInstallationID(transport github.Transport, cred AppCredential, now time.Time, scope string) (string, error) {
	if cred.InstallationID != "" {
		return cred.InstallationID, nil
	}
	jwt, err := appJWT(cred.AppID, cred.PrivateKey, now)
	if err != nil {
		return "", err
	}
	headers := map[string]string{
		"Authorization": "Bearer " + jwt,
		"Accept":        "application/vnd.github+json",
		"User-Agent":    "grain-automation",
	}
	owner, repo, _ := strings.Cut(scope, "/")
	if owner != "" && repo != "" {
		resp, err := transport.Request(
			"GET",
			fmt.Sprintf("/repos/%s/%s/installation", url.PathEscape(owner), url.PathEscape(repo)),
			headers, nil,
		)
		if err != nil {
			return "", err
		}
		if resp.Status != http.StatusOK {
			return "", &github.Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
		}
		var data struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(resp.Body, &data); err != nil {
			return "", fmt.Errorf("gitproxy: decoding repository installation response: %w", err)
		}
		if data.ID == 0 {
			return "", errors.New("gitproxy: repository installation response carried no id")
		}
		return strconv.FormatInt(data.ID, 10), nil
	}

	resp, err := transport.Request("GET", "/app/installations?per_page=100", headers, nil)
	if err != nil {
		return "", err
	}
	if resp.Status != http.StatusOK {
		return "", &github.Error{Status: resp.Status, Body: resp.Body, URL: resp.URL}
	}
	var list []struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	if err := json.Unmarshal(resp.Body, &list); err != nil {
		return "", fmt.Errorf("gitproxy: decoding app installations response: %w", err)
	}
	if len(list) == 0 {
		return "", errors.New("GitHub App is not installed on any account yet")
	}
	if owner != "" {
		for _, inst := range list {
			if strings.EqualFold(inst.Account.Login, owner) && inst.ID != 0 {
				return strconv.FormatInt(inst.ID, 10), nil
			}
		}
		return "", fmt.Errorf("GitHub App is not installed on %s", owner)
	}
	if list[0].ID == 0 {
		return "", errors.New("gitproxy: app installation entry carried no id")
	}
	return strconv.FormatInt(list[0].ID, 10), nil
}
