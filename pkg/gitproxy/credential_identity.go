package gitproxy

// The commit identity that goes with any git credential, not only an
// ambient one.
//
// A credential already says who its pushes act as -- GitHub attributes
// every push and API call to the account behind the token -- so the
// commits a run pushes with it can be attributed to that same account
// without a team having to type a name and address in as well:
//
//   - a PAT (fine-grained or classic) is a person's: GET /user with the
//     token answers their login, numeric id and display name, and the
//     commit identity is that display name (or the login) with the
//     login's noreply address, <id>+<login>@users.noreply.<host>, which
//     GitHub attributes to the account exactly as it does a verified
//     address and which works whether or not the person keeps their
//     address private. /user needs no scope or permission at all, so
//     every token that can push can answer it.
//   - a GitHub App installation is the App's bot account: GET /app with
//     the App's JWT answers its slug, GET /users/<slug>[bot] the bot
//     user's id, and the identity is `<slug>[bot]
//     <<id>+<slug>[bot]@users.noreply.<host>>` -- the same shape
//     GitHub's own Apps commit as (github-actions[bot] is
//     41898282+github-actions[bot]@users.noreply.github.com), so the
//     commits show the App's avatar and link to it.
//   - an SSH key is whoever its paired API credential is (a PAT or an
//     App, sshkey.go's APICredential); an unpaired key says nothing.
//   - an ambient credential is this host's own login, answered as
//     AmbientGitIdentity always has (ambient_identity.go).
//   - a credential on a plain git host has no API to ask, and the
//     anonymous one is nobody: both answer an error, and the caller
//     falls back to grain's own default.
//
// Lookups are cached per credential and per token (or App), so a
// dispatch asks GitHub once per token rather than once per run.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// CredentialGitIdentity is the commit identity a run pushing with
// credential name commits under: the account that credential's token
// acts as, in the shapes this file's own comment lists. An error is a
// credential that names nobody GitHub can say (anonymous, an SSH key
// with no API credential paired, a plain git host) or a lookup that
// failed; the caller then falls back to grain's own default identity.
func (c *CredentialSet) CredentialGitIdentity(name string) (CommitIdentity, error) {
	return c.credentialGitIdentity(name, 0)
}

// credentialGitIdentity is CredentialGitIdentity, following at most one
// SSH key -> API credential pairing (depth guards a pairing that points
// back at an SSH key, which APICredential already refuses).
func (c *CredentialSet) credentialGitIdentity(name string, depth int) (CommitIdentity, error) {
	switch {
	case name == "" || name == "anonymous":
		return CommitIdentity{}, errors.New("an anonymous credential is nobody to commit as")
	case c.IsAmbient(name):
		return c.AmbientGitIdentity(name)
	}
	cred, ok := c.Get(name)
	if !ok {
		return CommitIdentity{}, fmt.Errorf("credential %q is not configured", name)
	}
	if cred.SSH != nil {
		api, paired := c.APICredential(cred)
		if !paired || depth > 0 {
			return CommitIdentity{}, fmt.Errorf("SSH key credential %q has no API credential paired with it to say who it is", name)
		}
		return c.credentialGitIdentity(api.Name, depth+1)
	}
	h := c.HostOf(name)
	if h.Kind == model.GitHostGit {
		return CommitIdentity{}, fmt.Errorf("credential %q is on %s, a plain git host with no API to say who it is", name, h.Host)
	}
	if appCred, ok := c.loadAppCredential(name); ok {
		return c.appGitIdentity(name, h, appCred, cred)
	}
	if cred.Token == nil || *cred.Token == "" {
		return CommitIdentity{}, fmt.Errorf("credential %q has no token", name)
	}
	return c.tokenGitIdentity(name, h, *cred.Token)
}

// credentialIdentityEntry is one CredentialGitIdentity answer, kept for
// as long as key -- the token, or the App and its host -- is unchanged.
type credentialIdentityEntry struct {
	key string
	id  CommitIdentity
}

func (c *CredentialSet) cachedCredentialIdentity(name, key string) (CommitIdentity, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.credentialIdentities[name]
	if !ok || e.key != key {
		return CommitIdentity{}, false
	}
	return e.id, true
}

func (c *CredentialSet) cacheCredentialIdentity(name, key string, id CommitIdentity) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.credentialIdentities == nil {
		c.credentialIdentities = map[string]credentialIdentityEntry{}
	}
	c.credentialIdentities[name] = credentialIdentityEntry{key: key, id: id}
}

// githubUser is the part of a /user or /users/{login} answer a commit
// identity is built from.
type githubUser struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// tokenGitIdentity asks h's API who token is (GET /user) and builds the
// commit identity from the answer, preferring the host account's own git
// config (HostGitIdentity) where available, then the display name and email
// from /user, and falling back to the login and its noreply address for
// whichever half is still empty.
func (c *CredentialSet) tokenGitIdentity(name string, h CredentialHost, token string) (CommitIdentity, error) {
	key := h.Host + "\x00" + token
	if id, ok := c.cachedCredentialIdentity(name, key); ok {
		return applyHostGitIdentity(id, c.HostGitIdentity()), nil
	}
	transport, err := c.appTransportFor(name)
	if err != nil {
		return CommitIdentity{}, err
	}
	resp, err := transport.Request("GET", "/user", map[string]string{
		"Accept":        "application/vnd.github+json",
		"User-Agent":    "grain-automation",
		"Authorization": "token " + token,
	}, nil)
	if err != nil {
		return CommitIdentity{}, fmt.Errorf("asking %s who credential %q is: %s", h.Host, name, shortMessage(err.Error()))
	}
	if resp.Status != http.StatusOK {
		return CommitIdentity{}, fmt.Errorf("asking %s who credential %q is: HTTP %d", h.Host, name, resp.Status)
	}
	var who githubUser
	if err := json.Unmarshal(resp.Body, &who); err != nil {
		return CommitIdentity{}, fmt.Errorf("reading %s's /user answer for credential %q: %v", h.Host, name, err)
	}
	if who.Login == "" {
		return CommitIdentity{}, fmt.Errorf("%s answered /user for credential %q with no login", h.Host, name)
	}
	login := AmbientIdentity{Login: who.Login, ID: who.ID, Name: strings.TrimSpace(who.Name)}
	id := CommitIdentity{Name: oneLine(login.Name), Email: oneLine(strings.TrimSpace(who.Email))}
	if id.Name == "" {
		id.Name = login.Login
	}
	if id.Email == "" {
		id.Email = login.NoreplyEmail(h.Host)
	}
	c.cacheCredentialIdentity(name, key, id)
	return applyHostGitIdentity(id, c.HostGitIdentity()), nil
}

func applyHostGitIdentity(id, host CommitIdentity) CommitIdentity {
	if host.Name != "" {
		id.Name = host.Name
	}
	if host.Email != "" {
		id.Email = host.Email
	}
	return id
}

// appGitIdentity is the commit identity of the App behind credential
// name: its bot account, `<slug>[bot]`, with that bot user's noreply
// address. The slug comes from GET /app with the App's own JWT; the bot
// user's id from GET /users/<slug>[bot], asked with the installation
// token cred carries when it has one (the JWT is good for /app endpoints
// only). A bot whose id cannot be read is still named, with no address
// -- the sandbox's default fills that half.
func (c *CredentialSet) appGitIdentity(name string, h CredentialHost, app AppCredential, cred Credential) (CommitIdentity, error) {
	key := h.Host + "\x00app\x00" + app.AppID
	if id, ok := c.cachedCredentialIdentity(name, key); ok {
		return id, nil
	}
	slug, err := c.fetchAppSlug(name, h, app)
	if err != nil {
		return CommitIdentity{}, err
	}
	bot := slug + "[bot]"
	id := CommitIdentity{Name: bot}

	transport, err := c.appTransportFor(name)
	if err != nil {
		return CommitIdentity{}, err
	}
	headers := map[string]string{
		"Accept":     "application/vnd.github+json",
		"User-Agent": "grain-automation",
	}
	if cred.Token != nil && *cred.Token != "" {
		headers["Authorization"] = "token " + *cred.Token
	}
	userResp, err := transport.Request("GET", "/users/"+url.PathEscape(bot), headers, nil)
	if err == nil && userResp.Status == http.StatusOK {
		var who githubUser
		if json.Unmarshal(userResp.Body, &who) == nil && who.ID != 0 {
			id.Email = AmbientIdentity{Login: bot, ID: who.ID}.NoreplyEmail(h.Host)
		}
	}
	if id.Email == "" {
		// Named but not cached: the address is worth asking for again
		// on the next dispatch.
		return id, nil
	}
	c.cacheCredentialIdentity(name, key, id)
	return id, nil
}

// fetchAppSlug queries GET /app signed with the App's own JWT to read the
// App's slug for credential name.
func (c *CredentialSet) fetchAppSlug(name string, h CredentialHost, app AppCredential) (string, error) {
	transport, err := c.appTransportFor(name)
	if err != nil {
		return "", err
	}
	jwt, err := appJWT(app.AppID, app.PrivateKey, c.now())
	if err != nil {
		return "", err
	}
	resp, err := transport.Request("GET", "/app", map[string]string{
		"Accept":        "application/vnd.github+json",
		"User-Agent":    "grain-automation",
		"Authorization": "Bearer " + jwt,
	}, nil)
	if err != nil {
		return "", fmt.Errorf("asking %s which App credential %q is: %s", h.Host, name, shortMessage(err.Error()))
	}
	if resp.Status != http.StatusOK {
		return "", fmt.Errorf("asking %s which App credential %q is: HTTP %d", h.Host, name, resp.Status)
	}
	var appInfo struct {
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(resp.Body, &appInfo); err != nil {
		return "", fmt.Errorf("reading %s's /app answer for credential %q: %v", h.Host, name, err)
	}
	if appInfo.Slug == "" {
		return "", fmt.Errorf("%s answered /app for credential %q with no slug", h.Host, name)
	}
	return appInfo.Slug, nil
}

// appSlug resolves and caches the GitHub App slug for AppBotLogin.
func (c *CredentialSet) appSlug(name string, h CredentialHost, app AppCredential) (string, error) {
	c.mu.Lock()
	if slug, ok := c.appSlugs[name]; ok && slug != "" {
		c.mu.Unlock()
		return slug, nil
	}
	c.mu.Unlock()

	slug, err := c.fetchAppSlug(name, h, app)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	if c.appSlugs == nil {
		c.appSlugs = map[string]string{}
	}
	c.appSlugs[name] = slug
	c.mu.Unlock()
	return slug, nil
}

// AppBotLogin returns `<slug>[bot]` when credential name (or its paired
// SSH API credential) is backed by a GitHub App, or ("", false) when name
// is not an App credential.
func (c *CredentialSet) AppBotLogin(name string) (string, bool, error) {
	return c.appBotLogin(name, 0)
}

func (c *CredentialSet) appBotLogin(name string, depth int) (string, bool, error) {
	if name == "" || name == "anonymous" || c.IsAmbient(name) || namesOneFile(name) != nil {
		return "", false, nil
	}
	if c.IsSSH(name) {
		cred, ok := c.Get(name)
		if !ok || cred.SSH == nil || depth > 0 {
			return "", false, nil
		}
		api, paired := c.APICredential(cred)
		if !paired {
			return "", false, nil
		}
		return c.appBotLogin(api.Name, depth+1)
	}
	appCred, ok := c.loadAppCredential(name)
	if !ok {
		return "", false, nil
	}
	h := c.HostOf(name)
	if h.Kind == model.GitHostGit {
		return "", false, nil
	}
	slug, err := c.appSlug(name, h, appCred)
	if err != nil {
		return "", true, err
	}
	return slug + "[bot]", true, nil
}

// credentialIdentityFlight is one CredentialGitIdentity run started by
// CredentialGitIdentityWithin, which later callers for the same name
// join.
type credentialIdentityFlight struct {
	done chan struct{}
	id   CommitIdentity
	err  error
}

// CredentialGitIdentityWithin is CredentialGitIdentity for a caller that
// answers a person -- a settings pane -- and so must not sit on a slow
// API: it waits at most wait, and otherwise reports pending while the
// lookup goes on in the background, so that asking again later finds it
// done (cached) or joins it.
func (c *CredentialSet) CredentialGitIdentityWithin(name string, wait time.Duration) (id CommitIdentity, pending bool, err error) {
	c.mu.Lock()
	fl, ok := c.credentialIdentityFlights[name]
	if !ok {
		if c.credentialIdentityFlights == nil {
			c.credentialIdentityFlights = map[string]*credentialIdentityFlight{}
		}
		fl = &credentialIdentityFlight{done: make(chan struct{})}
		c.credentialIdentityFlights[name] = fl
		go func() {
			id, err := c.CredentialGitIdentity(name)
			c.mu.Lock()
			if c.credentialIdentityFlights[name] == fl {
				delete(c.credentialIdentityFlights, name)
			}
			fl.id, fl.err = id, err
			c.mu.Unlock()
			close(fl.done)
		}()
	}
	c.mu.Unlock()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-fl.done:
		return fl.id, false, fl.err
	case <-timer.C:
		return CommitIdentity{}, true, nil
	}
}
