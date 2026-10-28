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

package gitproxy

// Git credential material: the token, App, SSH key or ambient login
// behind each credential name.
//
//	/data/secrets/github/
//	  bot.token            # a PAT
//	  ci-app.app.json      # a GitHub App installation, instead of a PAT
//
// Which credential a repository is reached with is not decided here: the
// deployment's git connections (model.TeamGitConnection, resolved by
// model.Store.GitConnectionOn) name a credential, and this set loads it.
// The secrets/github/credentials.json pattern ladder that used to make
// that choice is gone (docs/single-tenant-reset.md, stage 2); a headless
// install seeds connections with `grain connections import` instead.
//
// Unlike Authorizer (authorize.go), which now reads the live task model,
// credentials are not hot-reloaded and are not the model's concern: which
// *secret* backs a repo is an operator decision recorded in files under
// /data/secrets, loaded once at construction, matching docs/design.md's
// "replace a file under /data/secrets and restart the one service that
// reads it." A *.app.json credential is the one exception to "loaded
// once": what's on disk is loaded once same as a *.token file, but its
// Token is re-minted from that App's own private key roughly every hour
// rather than read as-is -- see apptoken.go and load below.
//
// grain/task-137 asked whether this ladder should also resolve a
// credential out of the SQLite secrets store pkg/secrets keeps (secret
// "github", key <name>) -- the store the UI's own Secrets pane writes --
// so that "where secrets live" had one answer. It deliberately does not,
// and SetToken/Remove below are the other half of that decision: the UI
// writes *these files*, rather than the proxy learning to read a second
// store. Three reasons, recorded here because the question will come
// back:
//
//   - A GitHub App credential is three values with their own file shape
//     (*.app.json, loadAppCredential below). Backing it with a secrets
//     row would mean inventing a second encoding of the same thing, and
//     then keeping the two in step forever.
//   - This ladder is loaded by more than the daemon: `grain mcp-server`
//     builds one too. Reading credentials out of the secrets database
//     would put a second process on that SQLite file, for material a
//     0600 file already holds just as well.
//   - Two stores that can both answer "what is credential X" is a state
//     an operator has to reason about the moment they disagree. One
//     store cannot.
//
// So the file tree is the mechanism, everywhere: scripts/setup.sh seeds
// it, terraform/gcp/deploy/push-secrets.sh feeds that, the bootstrap
// playbook (pkg/capability/bootstrap/playbooks/github-connections.md)
// walks a human through it, and pkg/ui's Settings pane now writes it
// through SetToken instead of asking an operator for shell access.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Credential is a named credential. A nil Token means anonymous -- no
// Authorization header at all, which is what a public repo needs and is a
// deliberate credential shape, not an error case.
type Credential struct {
	Name  string
	Token *string
	// MintFailure is set only on the one anonymous credential that is
	// *not* that deliberate shape: a *.app.json credential whose
	// installation token could not be minted, which load serves with a
	// nil Token so a public repo keeps working (see MintFailure's own doc
	// comment). It travels on the credential rather than being looked up
	// separately so that everything already handed a Credential -- the
	// proxy's refusal messages, the named-token check, the Settings pane
	// -- can tell that anonymous apart from the intended one without
	// asking the ladder a second question.
	MintFailure *MintFailure
	// WrongHost is set, with a nil Token, on a credential recorded for a
	// git host other than the one the request it was loaded for goes to
	// (hosts.go): the sentence a refusal says, since such a credential is
	// never sent.
	WrongHost string
	// SSH is set on an SSH key credential (<name>.ssh.json, sshkey.go),
	// with a nil Token: its requests are run over SSH on its own host
	// rather than forwarded over HTTPS (sshforward.go).
	SSH *SSHCredential
	// Unusable is set, with a nil Token, on a credential whose file is
	// there but cannot be used -- an SSH key that will not parse, or pins
	// no host key -- and is the sentence the request is refused with.
	// Unlike an App that cannot mint, nothing is sent anonymously in its
	// place: an SSH credential has no anonymous HTTPS equivalent to fall
	// back to on the host it names.
	Unusable string
	// Ambient is set on a credential loaded from <name>.ambient.json
	// (ambient.go): its Token was asked of this host's own gh or git
	// login rather than read from a file, and is dropped on a 401
	// (Refused) so a re-login is followed.
	Ambient *AmbientCredential
}

// appTokenRefreshSkew is how much of an App installation token's own
// remaining lifetime load treats as "already expired" -- re-minting a
// little before GitHub's own expires_at rather than exactly at it, so a
// request that reads the cache and then takes a moment to actually use
// the token (a git push's own upload, an in-flight REST call) never finds
// GitHub has invalidated it out from under that same request.
const appTokenRefreshSkew = 2 * time.Minute

// appMintFailureRetryDelay is how long load caches a failed mint attempt
// before trying again, rather than calling GitHub's token-exchange
// endpoint on every single request a broken App credential is asked to
// serve -- a wrong or revoked private key does not get less wrong by
// retrying immediately, and every sandbox's git traffic can reach this
// concurrently (TestSelectAndGetAreSafeForConcurrentCallers).
const appMintFailureRetryDelay = 30 * time.Second

// cacheEntry is one load result. expiresAt is the zero time for a static
// *.token credential (or "anonymous"), which load reads as "never
// expires" -- the cache behavior every credential had before App-backed
// ones existed. refreshEarly marks a successfully minted App token,
// whose expiresAt is when GitHub itself invalidates it -- appTokenRefreshSkew
// only applies there, never to a mint-failure entry's own short
// expiresAt (appMintFailureRetryDelay out): applying the same multi-minute
// skew to a thirty-second backoff would put its deadline in the past the
// instant it's set, and load would retry on every call instead of waiting
// out the backoff at all.
type cacheEntry struct {
	cred         Credential
	expiresAt    time.Time
	refreshEarly bool
}

// CredentialSet is the git credential material in a secrets directory.
//
// Select and Get are called from GitProxy.Handle, which NewHandler wires
// straight into net/http -- one goroutine per inbound request, so every
// sandbox's concurrent git traffic reaches load's cache at once. mu
// guards it for exactly that reason.
type CredentialSet struct {
	dir string
	// connections is how Select learns which credential covers a
	// repository: the deployment's git connections (SetConnections). nil
	// answers nothing. Under mu.
	connections ConnectionResolver

	// AppTransport, when set, is where every *.app.json credential's
	// installation token gets minted -- a test's github.FakeTransport, the
	// same way live_test.go overrides RealForwarder. nil mints each
	// credential against its own host's REST API (appTransportFor in
	// hosts.go): api.github.com, or <host>/api/v3 on GitHub Enterprise.
	AppTransport github.Transport
	// ForwardHost is the host[:port] the git proxy attaches this set's
	// tokens to (BuildConfig.ForwardHost), or "" for a set that does not
	// know. It is the default host of a credential with no host record,
	// and a set that knows it never hands out a credential recorded for
	// another host (wrongHost in hosts.go, Credential.WrongHost).
	ForwardHost string
	// ForwardInsecure is set when ForwardHost is reached over plain http
	// (BuildConfig.ForwardTLS false: a test's fake server), which a
	// credential with no host record then is too.
	ForwardInsecure bool
	// Now is load's clock, overridable so a test can advance past an App
	// token's expiry without an actual hour passing. nil means time.Now.
	Now func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
	// identities is AmbientIdentityOf's per-token cache (ambient.go).
	identities map[string]ambientIdentityEntry
	// hostIdentity is HostGitIdentity's cache (ambient_identity.go).
	hostIdentity *hostGitIdentityEntry
	// flights are the ambient token fetches running now, by cache key,
	// and generations counts forget's calls per name, so a fetch that
	// finishes after its credential was changed is not cached
	// (loadAmbientLocked). identityFlights are AmbientIdentityWithin's.
	flights         map[string]*ambientFlight
	generations     map[string]uint64
	identityFlights map[string]*identityFlight
	// credentialIdentities and credentialIdentityFlights are
	// CredentialGitIdentity's cache and CredentialGitIdentityWithin's
	// lookups in flight (credential_identity.go).
	credentialIdentities      map[string]credentialIdentityEntry
	credentialIdentityFlights map[string]*credentialIdentityFlight
	// installations caches dynamically discovered installation IDs for
	// <name>.app.json files that omit installation_id, keyed by loadOn's
	// cache key; appSlugs caches GET /app slugs keyed by credential name.
	installations map[string]string
	appSlugs      map[string]string
}

// LoadCredentialSet loads the credential material under secretsDir: one
// <name>.token, <name>.app.json, <name>.ssh.json or <name>.ambient.json
// per credential. Which credential a repository uses is the deployment's
// git connections' answer (SetConnections), never a file in this directory:
// the credentials.json ladder that used to say so is gone
// (docs/single-tenant-reset.md, stage 2).
func LoadCredentialSet(secretsDir string) (*CredentialSet, error) {
	return &CredentialSet{
		dir:   secretsDir,
		cache: map[string]cacheEntry{},
	}, nil
}

// ConnectionResolver names the credential the deployment's git
// connections give (owner, repo) on the default host, false when none
// covers it (model.Store.GitConnectionOn).
type ConnectionResolver func(owner, repo string) (string, bool)

// SetConnections gives Select the deployment's git connections to ask.
// Safe to call while requests are in flight.
func (c *CredentialSet) SetConnections(r ConnectionResolver) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connections = r
}

// Dir is the secrets directory this set was loaded from -- for a
// caller that has to *name* it to a human (pkg/ui's Settings pane,
// explaining where the token it just wrote landed). It is a path, and
// says nothing about what is in it.
func (c *CredentialSet) Dir() string { return c.dir }

// Select returns the credential the deployment's git connections give
// (owner, repo), full scope. The second return is false if no connection
// covers it, or if this set was given no Connections to ask -- a
// distinct, fail-closed condition from "not allowed at all." Its callers
// name packages rather than repositories (PackageCredential, the package
// cache's registry pulls), which is why it is not narrowed the way
// GetForRepo is.
func (c *CredentialSet) Select(owner, repo string) (Credential, bool) {
	name, ok := c.selectName(owner, repo)
	if !ok {
		return Credential{}, false
	}
	if appCred, ok := c.loadAppCredential(name); ok && appCred.InstallationID == "" && owner != "" {
		cOwner, _ := canonicalizeRepo(owner, repo)
		if _, bareOwner, hasSlash := strings.Cut(cOwner, "/"); hasSlash {
			cOwner = bareOwner
		}
		if cOwner != "" {
			return c.load(name, cOwner+"/"), true
		}
	}
	return c.load(name, ""), true
}

// PackageToken returns the bearer/basic token Select resolves for (owner, repo),
// satisfying pkgcache.RegistryCredentials without pkg/pkgcache importing this
// package. Safe to call on a nil *CredentialSet.
func (c *CredentialSet) PackageToken(owner, repo string) (string, bool) {
	if c == nil {
		return "", false
	}
	cred, ok := c.Select(owner, repo)
	if !ok || cred.Token == nil || *cred.Token == "" {
		return "", false
	}
	return *cred.Token, true
}

func (c *CredentialSet) selectName(owner, repo string) (string, bool) {
	c.mu.Lock()
	connections := c.connections
	c.mu.Unlock()
	if connections == nil {
		return "", false
	}
	cOwner, cRepo := canonicalizeRepo(owner, repo)
	name, ok := connections(cOwner, cRepo)
	if (!ok || name == "") && strings.Contains(cOwner, "/") {
		host, bareOwner, _ := strings.Cut(cOwner, "/")
		if n, bok := connections(bareOwner, cRepo); bok && n != "" && c.exists(n) {
			credHost := c.HostOf(n).Host
			if f, aok := c.AmbientOf(n); aok && f.Host != "" {
				credHost = f.Host
			}
			if model.SameGitHost(credHost, host, model.DefaultGitHost) {
				name, ok = n, true
			}
		}
	}
	if !ok || name == "" || !c.exists(name) {
		return "", false
	}
	return name, true
}

// GetForRepo is Get narrowed the way SelectForRepo narrows Select: the
// named credential, with an App-backed one's token minted for (owner,
// repo) alone.
func (c *CredentialSet) GetForRepo(name, owner, repo string) (Credential, bool) {
	return c.GetForRepoOn(name, "", owner, repo)
}

// GetForRepoOn is GetForRepo for a repository on host (empty: the set's
// default host): a credential recorded for any other host comes back
// with no token and a WrongHost refusal, and nothing -- an App's JWT
// included -- is sent anywhere to find that out.
func (c *CredentialSet) GetForRepoOn(name, host, owner, repo string) (Credential, bool) {
	if !c.exists(name) {
		return Credential{}, false
	}
	return c.loadOn(name, host, repoScope(canonicalizeRepo(owner, repo))), true
}

// repoScope is the cache-key and mint scope for a canonicalized (owner,
// repo): "owner/name", with anything past the repository's own name
// dropped, or "" -- full
// scope -- when there is no repository to narrow to.
func repoScope(owner, repo string) string {
	if i := strings.Index(repo, "/"); i >= 0 {
		repo = repo[:i]
	}
	if owner == "" || repo == "" {
		return ""
	}
	return owner + "/" + repo
}

// Get returns a named credential directly, bypassing the owner/repo
// pattern ladder Select uses -- bwsalmon/agents#52's per-task
// grain-github-<name> label, for a task that needs a scope the default
// ladder deliberately withholds (docs/design.md, "scopes to withhold").
// The second return is false if no such credential is configured
// (neither <name>.token nor <name>.app.json exists, and name isn't the
// literal "anonymous").
func (c *CredentialSet) Get(name string) (Credential, bool) {
	if !c.exists(name) {
		return Credential{}, false
	}
	return c.load(name, ""), true
}

// exists is Get's "is there such a credential at all" check: a
// <name>.token, <name>.app.json or <name>.ssh.json file, or the literal
// "anonymous".
//
// The names Get and GetForRepo are handed come from task grants and team
// config (model.Store.GitCredentialOverride), not from this directory, so
// one that does not name a single file in it -- "../x" -- does not exist,
// rather than being joined onto c.dir and read from wherever it points.
// namesOneFile, not ValidCredentialName: a name already on disk in a
// shape SetToken would no longer write is still a credential.
func (c *CredentialSet) exists(name string) bool {
	if name == "anonymous" {
		return true
	}
	if namesOneFile(name) != nil {
		return false
	}
	_, tokenErr := os.Stat(filepath.Join(c.dir, name+".token"))
	_, appErr := os.Stat(filepath.Join(c.dir, name+".app.json"))
	_, sshErr := os.Stat(c.sshFile(name))
	_, ambientErr := os.Stat(c.ambientFile(name))
	return tokenErr == nil || appErr == nil || sshErr == nil || ambientErr == nil
}

// Names is every credential this deployment has configured: one per
// <name>.token, <name>.app.json or <name>.ssh.json file in the secrets
// directory, sorted, with a name that has several counted once (load
// prefers the SSH key, then the App, so it is one credential either way).
//
// Read from the directory rather than from credentials.json, because the
// two answer different questions: the pattern file says which credential
// a repo falls back to, while this says which credentials exist at all
// -- including the extra named tokens an operator drops in for Get above
// to reach, which no pattern ever names (ExtraNames below).
//
// Unlike load's own cache this re-reads the directory on every call: it
// is called at startup, not per request, and a listing is cheap next to
// the "restart the one service that reads it" contract the rest of this
// file keeps.
func (c *CredentialSet) Names() []string {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		// A secrets directory that cannot be read is the same "no
		// credentials configured" this file already treats a missing
		// credentials.json as -- every Select then fails closed with
		// "no credential configured," which is the failure worth
		// surfacing, and it is surfaced where a request can see it.
		return nil
	}
	seen := map[string]bool{}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name, ok := credentialFileName(e.Name())
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ExtraNames is every named credential a task can ask for by name, which
// is exactly the set that becomes a capability of its own
// (model.GitCredentialCapability, pkg/capability/githubtoken).
func (c *CredentialSet) ExtraNames() []string {
	var out []string
	for _, name := range c.Names() {
		// An ambient credential is never a capability a task can name:
		// it acts as the host's own login, and only the connections that
		// name it use it (ambient.go).
		if c.IsAmbient(name) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// IsApp reports whether name is backed by a <name>.app.json file -- a
// GitHub App installation whose token load re-mints, rather than a
// *.token file read as-is. Callers that offer to *edit* a credential
// need this: SetToken below refuses to write over an App credential, and
// a UI is better off saying so up front than after the fact.
func (c *CredentialSet) IsApp(name string) bool {
	if namesOneFile(name) != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(c.dir, name+".app.json"))
	return err == nil
}

// ValidCredentialName reports whether name may be used for a credential
// written through SetToken: it becomes a filename in the secrets
// directory, and it becomes half of a capability id
// (model.GitCredentialCapability), so it is held to a conservative shape
// rather than to whatever the filesystem happens to tolerate. A name
// already on disk that does not match this is still read as it always
// was -- this gates writing, not loading.
//
// "anonymous" is refused because load reads it as a credential shape of
// its own (no Authorization header at all); a file of that name would be
// written, listed, and then never used.
func ValidCredentialName(name string) error {
	if err := namesOneFile(name); err != nil {
		return err
	}
	if name == "anonymous" {
		return fmt.Errorf("%q is reserved: it already names the no-credential-at-all case", name)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_':
		default:
			return fmt.Errorf(
				"%q is not a usable credential name: letters, digits, %q and %q only", name, "-", "_")
		}
	}
	return nil
}

// namesOneFile is the weaker check Remove holds to: enough that
// name+suffix cannot escape the secrets directory or address something
// other than a file in it, and nothing more. Removing is not writing --
// a credential already on disk under a name ValidCredentialName would
// refuse today (a dot in it, say, from before this package wrote any of
// these files itself) is still a credential an operator must be able to
// delete from the same list it appears in.
func namesOneFile(name string) error {
	if name == "" {
		return fmt.Errorf("a credential name is required")
	}
	if name == "." || name == ".." || strings.HasPrefix(name, ".") ||
		strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("%q is not a usable credential name: it must name one file in the secrets directory", name)
	}
	return nil
}

// SetToken writes token as name's credential material -- name.token in
// the secrets directory, mode 0600, replacing whatever was there.
//
// Written to a temporary file and renamed into place, so a reader that
// catches this mid-write (the git proxy in another process, serving a
// push right now) sees either the old token or the new one, never half
// of one. The temporary file's own name deliberately does not end in
// .token, so a crash between create and rename cannot leave something
// Names would report as a credential.
//
// Refused for a name already backed by name.app.json: load prefers the
// App credential, so the file this would write would be loaded by
// nothing, and silently doing nothing is the worst answer available.
//
// The value takes effect for a process that has not loaded it yet;
// everything already running keeps what it cached (this file's own doc
// comment on why the ladder is not hot-reloaded). Callers that offer
// this to a human should say so.
func (c *CredentialSet) SetToken(name, token string) error {
	if err := ValidCredentialName(name); err != nil {
		return err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("a token value is required")
	}
	if c.IsApp(name) {
		return fmt.Errorf(
			"%q is a GitHub App credential (%s.app.json): replace that file to change it", name, name)
	}
	if c.IsSSH(name) {
		return fmt.Errorf("%q is an SSH key credential (%s.ssh.json): save it as a token to replace it", name, name)
	}
	if c.IsAmbient(name) {
		return fmt.Errorf("%q is an ambient credential (%s.ambient.json): save it as a token to replace it", name, name)
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return fmt.Errorf("creating the GitHub secrets directory: %w", err)
	}
	tmp, err := os.CreateTemp(c.dir, "."+name+".token.tmp")
	if err != nil {
		return fmt.Errorf("writing credential %q: %w", name, err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("writing credential %q: %w", name, err)
	}
	if _, err := tmp.WriteString(token + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("writing credential %q: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing credential %q: %w", name, err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(c.dir, name+".token")); err != nil {
		return fmt.Errorf("writing credential %q: %w", name, err)
	}
	c.forget(name)
	return nil
}

// Remove deletes the files backing name -- both forms, since a name with
// a *.token and a *.app.json is one credential either way (Names' own
// doc comment). It reports an error when nothing of that name is
// configured, so a caller can tell "removed" from "was never there."
//
// It says nothing about credentials.json: a pattern naming a credential
// that no longer exists is a real, and deliberately visible, failure
// (Select's own fail-closed "no credential configured"). PatternsFor
// above is how a caller checks before asking for this.
func (c *CredentialSet) Remove(name string) error {
	if err := namesOneFile(name); err != nil {
		return err
	}
	var removed bool
	for _, suffix := range []string{".token", ".app.json", ".ssh.json", ".ambient.json"} {
		err := os.Remove(filepath.Join(c.dir, name+suffix))
		switch {
		case err == nil:
			removed = true
		case os.IsNotExist(err):
		default:
			return fmt.Errorf("removing credential %q: %w", name, err)
		}
	}
	if !removed {
		return fmt.Errorf("no credential named %q is configured", name)
	}
	if err := os.Remove(c.hostFile(name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing credential %q's host record: %w", name, err)
	}
	c.forget(name)
	return nil
}

// forget drops name from load's cache, so this CredentialSet's own next
// read of it goes back to the files SetToken/Remove just changed. It
// does nothing for the *other* CredentialSets a deployment has loaded
// (the git proxy's, the REST client's, the MCP server's) -- those are in
// other processes or were built separately, and the restart this file's
// doc comment describes is still what makes a change reach them.
func (c *CredentialSet) forget(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.cache {
		if key == name || strings.HasPrefix(key, name+"\x00") {
			delete(c.cache, key)
		}
	}
	for key := range c.flights {
		if key == name || strings.HasPrefix(key, name+"\x00") {
			delete(c.flights, key)
		}
	}
	if c.generations == nil {
		c.generations = map[string]uint64{}
	}
	c.generations[name]++
	delete(c.identities, name)
	delete(c.credentialIdentities, name)
	delete(c.appSlugs, name)
	for key := range c.installations {
		if key == name || strings.HasPrefix(key, name+"\x00") {
			delete(c.installations, key)
		}
	}
}

// credentialFileName maps a file in the secrets directory back to the
// credential it backs -- "bot.token" and "bot.app.json" are both the
// "bot" credential (load's own two forms) -- and reports false for
// anything else there, credentials.json itself included.
func credentialFileName(file string) (string, bool) {
	for _, suffix := range []string{".token", ".app.json", ".ssh.json", ".ambient.json"} {
		if name, ok := strings.CutSuffix(file, suffix); ok && name != "" {
			return name, true
		}
	}
	return "", false
}

func (c *CredentialSet) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// entryIsFresh reports whether entry can still be served without
// recomputing it. A zero expiresAt (a static *.token credential, or
// "anonymous") never expires; anything else is fresh until its own
// deadline -- entry.expiresAt itself for a mint-failure backoff,
// appTokenRefreshSkew before it for a real App token (cacheEntry's own
// doc comment on why those two cases apply the skew differently).
func entryIsFresh(entry cacheEntry, now time.Time) bool {
	if entry.expiresAt.IsZero() {
		return true
	}
	deadline := entry.expiresAt
	if entry.refreshEarly {
		deadline = deadline.Add(-appTokenRefreshSkew)
	}
	return now.Before(deadline)
}

// appMintTimeout bounds one token-exchange call. load holds mu across
// it, so a mint that never answers holds up every credential lookup in
// the process behind it -- every sandbox's git traffic through the
// proxy, and, since AppMintFailure below, a settings pane somebody has
// just opened. An unbounded http.Client (the default) makes that wait as
// long as the far end cares to keep the connection open; a mint that has
// taken this long has failed as surely as one GitHub refused, and
// failing it hits appMintFailureRetryDelay's own backoff.
const appMintTimeout = 20 * time.Second

// AppMintFailure reports whether name is an App-backed credential that
// cannot be turned into an installation token right now, and what GitHub
// said about it -- the answer a pane showing that credential needs
// before it can report anything better than "present" (grain/task-158).
//
// It resolves the credential exactly as a request would, through load
// and its cache, rather than reporting only what this process happened
// to have hit already: the callers are a human's own read of the
// credential, and a set that has not been asked for this name yet knows
// nothing to tell them. So this can mint -- one token exchange, then
// cached for the token's own hour, or for appMintFailureRetryDelay if it
// failed -- and it is bounded by appMintTimeout above.
//
// The answer is this process's ladder, which is not the git proxy's own
// (see forget's doc comment: a deployment holds several). It is the same
// files, the same App and the same GitHub, so a failure here is the
// failure a push would hit; what it cannot do is report the exact
// moment the proxy last tried.
func (c *CredentialSet) AppMintFailure(name string) (MintFailure, bool) {
	if !c.IsApp(name) {
		return MintFailure{}, false
	}
	cred := c.load(name, "")
	if cred.MintFailure == nil {
		return MintFailure{}, false
	}
	return *cred.MintFailure, true
}

// load resolves name to a Credential, consulting the cache first. A
// cached entry is used as-is when it never expires (a *.token credential,
// or "anonymous") or hasn't yet crossed into appTokenRefreshSkew of its
// own expiry (an App-backed one); otherwise it is (re)computed and the
// cache updated before returning, so the next call -- including one
// racing this one, both blocked on mu -- gets the same answer without
// minting twice. An ambient credential is the exception to holding mu
// throughout: its host-tool run happens with mu released, shared by
// every load of it meanwhile (loadAmbientLocked).
//
// scope is repoScope's "owner/repo" for a token that will only be used
// against that repository, or "" for full scope. An App-backed token is
// minted for exactly that scope and cached under (name, scope), so a
// narrowed token is never served to a caller that needs the whole
// installation and a full-scope one is never served where a narrowed one
// was asked for (#2388). Every other credential kind ignores scope.
func (c *CredentialSet) load(name, scope string) Credential {
	return c.loadOn(name, "", scope)
}

// loadOn is load for a request to host (empty: the set's default host).
// A credential recorded for another host is answered with its WrongHost
// refusal before anything is read or minted, and that answer is not
// cached: it is a fact about the request, not about the credential.
func (c *CredentialSet) loadOn(name, host, scope string) Credential {
	if refusal := c.wrongHost(name, host); refusal != "" {
		return Credential{Name: name, WrongHost: refusal}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	key := name
	if scope != "" {
		key = name + "\x00" + scope
	}
	if entry, ok := c.cache[key]; ok && entryIsFresh(entry, now) {
		return entry.cred
	}
	if name == "anonymous" {
		cred := Credential{Name: name}
		c.cache[key] = cacheEntry{cred: cred}
		return cred
	}
	if err := namesOneFile(name); err != nil {
		// The same fail-soft-to-anonymous a missing file gets, without
		// reading whatever a name like "../x" would point at outside c.dir.
		log.Printf("gitproxy: not loading credential: %v -- serving it as anonymous", err)
		return Credential{Name: name}
	}
	if sshCred, ok, err := c.loadSSHCredential(name); ok {
		cred := Credential{Name: name, SSH: sshCred}
		if err != nil {
			log.Printf("gitproxy: not using SSH credential %q: %v", name, err)
			cred = Credential{Name: name, Unusable: fmt.Sprintf("SSH credential %q cannot be used: %v", name, err)}
		}
		c.cache[key] = cacheEntry{cred: cred}
		return cred
	}
	if f, ok, err := c.loadAmbientFile(name); ok {
		return c.loadAmbientLocked(name, key, f, err, now)
	}
	if appCred, ok := c.loadAppCredential(name); ok {
		transport, err := c.appTransportFor(name)
		if err != nil {
			log.Printf("gitproxy: not minting an installation token for GitHub App credential %q: %v -- "+
				"serving it as anonymous", name, err)
			cred := Credential{Name: name, MintFailure: &MintFailure{Time: now, NoExchange: true, Message: shortMessage(err.Error())}}
			c.cache[key] = cacheEntry{cred: cred}
			return cred
		}
		dynamicInstall := appCred.InstallationID == ""
		if dynamicInstall {
			instID, cached := c.installations[key]
			if !cached {
				instID, err = resolveInstallationID(transport, appCred, now, scope)
				if err != nil {
					log.Printf("gitproxy: resolving installation ID for GitHub App credential %q%s: %v -- "+
						"serving it as anonymous until the next retry", name, scopeSuffix(scope), err)
					cred := Credential{Name: name, MintFailure: newMintFailure(err, now)}
					c.cache[key] = cacheEntry{cred: cred, expiresAt: now.Add(appMintFailureRetryDelay)}
					return cred
				}
				if c.installations == nil {
					c.installations = map[string]string{}
				}
				c.installations[key] = instID
			}
			appCred.InstallationID = instID
		}
		repo := ""
		if scope != "" {
			_, repo, _ = strings.Cut(scope, "/")
		}
		token, expiresAt, err := mintInstallationToken(transport, appCred, now, repo)
		if err != nil {
			if dynamicInstall {
				var apiErr *github.Error
				if errors.As(err, &apiErr) && apiErr.Status == 404 {
					delete(c.installations, key)
				}
			}
			log.Printf("gitproxy: minting an installation token for GitHub App credential %q%s: %v -- "+
				"serving it as anonymous until the next retry", name, scopeSuffix(scope), err)
			// The failure rides along on the credential for the whole
			// backoff window, so every request served anonymously in it
			// can say why it was (MintFailure's own doc comment) rather
			// than leaving this log line as the only trace.
			cred := Credential{Name: name, MintFailure: newMintFailure(err, now)}
			c.cache[key] = cacheEntry{cred: cred, expiresAt: now.Add(appMintFailureRetryDelay)}
			return cred
		}
		cred := Credential{Name: name, Token: &token}
		c.cache[key] = cacheEntry{cred: cred, expiresAt: expiresAt, refreshEarly: true}
		return cred
	}
	var token *string
	data, err := os.ReadFile(filepath.Join(c.dir, name+".token"))
	if err == nil {
		t := strings.TrimSpace(string(data))
		token = &t
	}
	cred := Credential{Name: name, Token: token}
	c.cache[key] = cacheEntry{cred: cred}
	return cred
}

// scopeSuffix is load's log-line rendering of a mint scope.
func scopeSuffix(scope string) string {
	if scope == "" {
		return ""
	}
	return " (narrowed to " + scope + ")"
}

// loadAppCredential reads name.app.json, if it exists: app_id,
// installation_id and private_key (that App's own PEM-encoded key,
// exactly as GitHub hands it out -- ParseAppPrivateKey's own doc
// comment). Any problem with the file (missing, malformed JSON, a key
// that won't parse) is reported here as "no such App credential" rather
// than propagated, the same fail-soft-to-anonymous contract a missing
// *.token file already gets from load above -- logged either way, so an
// operator's typo in the file does not read as a silent, permanent
// "nothing is auto-merging" the way #483 was before it had a log line to
// point at.
func (c *CredentialSet) loadAppCredential(name string) (AppCredential, bool) {
	data, err := os.ReadFile(filepath.Join(c.dir, name+".app.json"))
	if err != nil {
		return AppCredential{}, false
	}
	var raw struct {
		AppID          string `json:"app_id"`
		InstallationID string `json:"installation_id"`
		PrivateKey     string `json:"private_key"`
		WebhookSecret  string `json:"webhook_secret"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		log.Printf("gitproxy: %s.app.json is not valid JSON: %v", name, err)
		return AppCredential{}, false
	}
	key, err := ParseAppPrivateKey([]byte(raw.PrivateKey))
	if err != nil {
		log.Printf("gitproxy: %s.app.json: %v", name, err)
		return AppCredential{}, false
	}
	return AppCredential{
		AppID:          strings.TrimSpace(raw.AppID),
		InstallationID: strings.TrimSpace(raw.InstallationID),
		PrivateKey:     key,
		WebhookSecret:  raw.WebhookSecret,
	}, true
}

// AppWebhookSecret returns the webhook_secret stored in name.app.json, if
// name is a GitHub App credential carrying one.
func (c *CredentialSet) AppWebhookSecret(name string) (string, bool) {
	if namesOneFile(name) != nil {
		return "", false
	}
	appCred, ok := c.loadAppCredential(name)
	if !ok || appCred.WebhookSecret == "" {
		return "", false
	}
	return appCred.WebhookSecret, true
}

// canonicalizeRepo is a case-insensitive, .git-suffix-insensitive identity
// for a repo -- GitHub itself treats owner/repo case-insensitively, and a
// client may or may not include the .git suffix.
func canonicalizeRepo(owner, repo string) (string, string) {
	repo = strings.TrimSuffix(repo, ".git")
	return strings.ToLower(owner), strings.ToLower(repo)
}
