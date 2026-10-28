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

// Which repositories a named credential can reach, asked of GitHub rather
// than discovered by a clone failing: what lets a task be refused at the
// moment somebody files it against a repository its team's credential
// cannot see, rather than failing at dispatch (grain/task-1850), and what
// the New task pickers offer a team with no repositories attached.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
)

// ReachableRepo is one repository a credential can reach, as GitHub
// spells it, and whether the credential may push there.
type ReachableRepo struct {
	Repo string `json:"repo"`
	Push bool   `json:"push"`
}

// DefaultReachTTL is how long Reach keeps a credential's list. Short,
// because the list is exactly what an operator changes when they grant
// the credential a repository and then try again; a changed token
// refreshes it at once anyway (Reach.Repos keys on the token).
const DefaultReachTTL = 2 * time.Minute

// maxReachPages bounds the listing: 100 repositories a page, so a
// credential reaching more than this many is answered with the first
// 3000 and Check treats anything past them as unknown, never refused.
const maxReachPages = 30

// Reach lists, and caches, the repositories each credential in
// Credentials can reach, through API (the GitHub REST API). The zero
// value is not usable; a nil *Reach answers every question "unknown".
type Reach struct {
	Credentials *CredentialSet
	API         github.Transport
	// Host is the git host API is the REST API of (the daemon's
	// -github-host). A credential recorded on any other host is never
	// listed against API: its token is that host's, not this one's.
	// Empty checks only the kind (a plain git host is never listed).
	Host string
	// Transports maps non-default git hosts to their github.Transport,
	// allowing Reach to talk to GitHub Enterprise instances.
	Transports map[string]github.Transport
	// TTL is how long a listing is kept; zero is DefaultReachTTL.
	TTL time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu    sync.Mutex
	cache map[string]reachEntry
}

type reachEntry struct {
	repos     []ReachableRepo
	truncated bool
	at        time.Time
}

// Reachability is what Check concludes about one repository.
type Reachability int

const (
	// ReachUnknown is every answer Check cannot vouch for: no listing
	// available (no API, an anonymous credential, GitHub erroring), or a
	// listing too long to have been read to the end. A caller refuses
	// nothing on it.
	ReachUnknown Reachability = iota
	// Reachable: the credential can do what was asked.
	Reachable
	// Unreachable: GitHub listed what the credential reaches and the
	// repository is not on it, or is on it without the push asked for.
	Unreachable
)

// Repos is the list of repositories credential name reaches, and false
// when no list can be had: no API to ask, no such credential, an
// anonymous one, or GitHub refusing or failing the listing -- each of
// which err says, for a caller that wants to report it.
func (r *Reach) Repos(name string) ([]ReachableRepo, bool, error) {
	entry, err := r.entry(name)
	if err != nil || entry == nil {
		return nil, false, err
	}
	return append([]ReachableRepo(nil), entry.repos...), true, nil
}

// ReposForHost is the list of repositories credential name reaches on host,
// and false when no list can be had: no API to ask, no such credential,
// an anonymous one, or GitHub refusing or failing the listing.
func (r *Reach) ReposForHost(name, host string) ([]ReachableRepo, bool, error) {
	entry, err := r.entryForHost(name, host)
	if err != nil || entry == nil {
		return nil, false, err
	}
	return append([]ReachableRepo(nil), entry.repos...), true, nil
}

// Check is whether credential name can reach owner/repo -- push access
// when push is true, read otherwise.
func (r *Reach) Check(name, owner, repo string, push bool) (Reachability, error) {
	entry, err := r.entry(name)
	if err != nil || entry == nil {
		return ReachUnknown, err
	}
	want := strings.ToLower(owner + "/" + repo)
	var hostWant string
	if h := r.Credentials.HostOf(name); h.Host != "" && !model.SameGitHost(h.Host, r.Host, model.DefaultGitHost) {
		hostWant = strings.ToLower(h.Host + "/" + owner + "/" + repo)
	}
	for _, got := range entry.repos {
		gotLower := strings.ToLower(got.Repo)
		if gotLower == want || (hostWant != "" && gotLower == hostWant) {
			if push && !got.Push {
				return Unreachable, nil
			}
			return Reachable, nil
		}
	}
	if entry.truncated {
		return ReachUnknown, nil
	}
	return Unreachable, nil
}

func (r *Reach) apiFor(name string, h CredentialHost) (github.Transport, error) {
	if r.Credentials.ForgeViaCommand(name) && (r.Transports == nil || r.Transports[h.Host] == nil) {
		// Only the host's own tool reaches it, whichever host the
		// deployment's own API is on.
		return r.Credentials.ambientTransportFor(name, h.Host)
	}
	if !h.Recorded || r.Host == "" || model.SameGitHost(h.Host, r.Host, model.DefaultGitHost) {
		return r.API, nil
	}
	if r.Transports != nil && r.Transports[h.Host] != nil {
		return r.Transports[h.Host], nil
	}
	if r.Credentials.IsAmbient(name) {
		return r.Credentials.ambientTransportFor(name, h.Host)
	}
	if r.Credentials.AppTransport != nil {
		return r.Credentials.AppTransport, nil
	}
	if _, isReal := r.API.(*github.RealTransport); isReal || r.API == nil {
		apiHost, prefix, ok := h.APIBase()
		if !ok {
			return nil, nil
		}
		realTr := github.NewRealTransport(apiHost)
		realTr.Host = apiHost
		realTr.PathPrefix = prefix
		realTr.UseTLS = h.useTLS()
		realTr.Client = h.HTTPClient(appMintTimeout)
		return realTr, nil
	}
	return nil, nil
}

// entry is the cached listing for name, fetched if it is missing or
// stale. nil with a nil error is "no listing to be had" without anything
// having gone wrong: no API, an anonymous credential.
func (r *Reach) entry(name string) (*reachEntry, error) {
	if r == nil || r.Credentials == nil || name == "" {
		return nil, nil
	}
	h := r.Credentials.HostOf(name)
	return r.entryForHost(name, h.Host)
}

func (r *Reach) entryForHost(name, host string) (*reachEntry, error) {
	if r == nil || r.Credentials == nil || name == "" {
		return nil, nil
	}
	h := r.Credentials.HostOf(name)
	if h.Kind == model.GitHostGit {
		return nil, nil
	}
	normHost, err := model.NormalizeGitHost(host)
	if err != nil || normHost == "" {
		normHost = h.Host
	}
	if normHost == "" {
		normHost = r.Host
	}
	if normHost == "" {
		normHost = model.DefaultGitHost
	}
	normHost, _ = model.NormalizeGitHost(normHost)

	if h.Recorded && h.Host != "" && !model.SameGitHost(h.Host, normHost, model.DefaultGitHost) {
		return nil, nil
	}
	h.Host = normHost

	api, err := r.apiFor(name, h)
	if err != nil {
		return nil, err
	}
	if api == nil {
		return nil, nil
	}

	cred, ok := r.Credentials.Get(name)
	if !ok {
		return nil, fmt.Errorf("no credential named %q is configured", name)
	}
	if cred.Token == nil {
		if cred.MintFailure != nil {
			return nil, fmt.Errorf("no installation token could be minted for the %q GitHub App credential: %s",
				name, cred.MintFailure.Describe())
		}
		return nil, nil
	}

	sum := sha256.Sum256([]byte(*cred.Token))
	key := normHost + "\x00" + name + "\x00" + hex.EncodeToString(sum[:8])
	now := r.now()
	ttl := r.TTL
	if ttl <= 0 {
		ttl = DefaultReachTTL
	}
	r.mu.Lock()
	cached, ok := r.cache[key]
	r.mu.Unlock()
	if ok && now.Sub(cached.at) < ttl {
		return &cached, nil
	}

	repos, truncated, err := listReachable(api, *cred.Token, r.Credentials.IsApp(name))
	if err != nil {
		return nil, fmt.Errorf("listing the repositories the %q credential can reach: %w", name, err)
	}
	if normHost != "" && r.Host != "" && !model.SameGitHost(normHost, r.Host, model.DefaultGitHost) {
		prefix := normHost + "/"
		for i := range repos {
			if !strings.HasPrefix(repos[i].Repo, prefix) {
				repos[i].Repo = prefix + repos[i].Repo
			}
		}
	}
	fresh := reachEntry{repos: repos, truncated: truncated, at: now}
	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]reachEntry{}
	}
	for k := range r.cache {
		if strings.HasPrefix(k, normHost+"\x00"+name+"\x00") {
			delete(r.cache, k)
		}
	}
	r.cache[key] = fresh
	r.mu.Unlock()
	return &fresh, nil
}

func (r *Reach) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// listReachable asks GitHub what token reaches: an App installation's
// repositories, or the repositories a personal access token's user can
// see (which, for a fine-grained token, is the repositories it was
// given). An installation listing that carries no permissions is read as
// push, since an App's access is uniform across its installation and a
// listing that cannot say is not grounds to refuse anything.
func listReachable(api github.Transport, token string, app bool) ([]ReachableRepo, bool, error) {
	path := "/user/repos?per_page=100&page=%d"
	if app {
		path = "/installation/repositories?per_page=100&page=%d"
	}
	headers := map[string]string{
		"Accept":               "application/vnd.github+json",
		"User-Agent":           "grain-automation",
		"X-GitHub-Api-Version": "2022-11-28",
		"Authorization":        "token " + token,
	}
	type repoJSON struct {
		FullName    string           `json:"full_name"`
		Permissions *map[string]bool `json:"permissions"`
	}
	var out []ReachableRepo
	for page := 1; page <= maxReachPages; page++ {
		resp, err := api.Request("GET", fmt.Sprintf(path, page), headers, nil)
		if err != nil {
			if login, ok := AsLoginRedirect(err); ok {
				return nil, false, login
			}
			return nil, false, err
		}
		if resp.Status >= 300 && resp.Status < 400 {
			location, _ := headerValue(resp.Headers, "Location")
			if login, ok := NewLoginRedirect("", location); ok {
				return nil, false, login
			}
		}
		if resp.Status != 200 {
			return nil, false, fmt.Errorf("GitHub answered %d", resp.Status)
		}
		var list []repoJSON
		if app {
			var wrapped struct {
				Repositories []repoJSON `json:"repositories"`
			}
			err = json.Unmarshal(resp.Body, &wrapped)
			list = wrapped.Repositories
		} else {
			err = json.Unmarshal(resp.Body, &list)
		}
		if err != nil {
			return nil, false, fmt.Errorf("reading GitHub's answer: %w", err)
		}
		for _, repo := range list {
			push := app
			if repo.Permissions != nil {
				perms := *repo.Permissions
				push = perms["push"] || perms["admin"] || perms["maintain"]
			}
			out = append(out, ReachableRepo{Repo: repo.FullName, Push: push})
		}
		if len(list) < 100 {
			return out, false, nil
		}
	}
	return out, true, nil
}
