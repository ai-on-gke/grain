package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// API is the slice of GitHub's REST surface this command needs: the
// packages API for the versions of a container package, and the repos
// API for which branches still exist.
//
// It talks through pkg/github's Transport rather than net/http directly,
// for the seam that package documents -- a test drives the whole command
// against scripted responses without opening a socket, and the real
// transport is the one every other GitHub call in this repository goes
// through.
type API struct {
	Transport github.Transport
	Token     string
	// Owner is the account the packages belong to (`bwsalmon` in
	// ghcr.io/bwsalmon/grain/guest), and Repo the repository they are
	// scoped to -- which is also the first path segment of every package
	// name here, since GHCR names a package for the image path under the
	// owner: ghcr.io/bwsalmon/grain/guest is the package "grain/guest".
	Owner string
	Repo  string

	// Sleep waits out a rate limit between one attempt at a call and the
	// next; nil is time.Sleep. Now is the clock a reset time is read
	// against; nil is time.Now. Both are here for the tests, which have
	// to drive the retry in do() without spending a minute of wall clock
	// on it.
	Sleep func(time.Duration)
	Now   func() time.Time

	// ownerPath is "/orgs/{owner}" or "/users/{owner}", resolved once by
	// the first call that needs it. The packages API has no single path
	// that serves both, and nothing in a workflow's environment says
	// which kind of account it is running under, so this asks: an
	// account that is not an organization answers 404 on the orgs path.
	ownerPath string
}

// How long one call will wait for a rate limit to clear, and how many
// times.
//
// The bound is the job's own timeout: prune-packages.yml gives this 20
// minutes, and a primary limit that has just been exhausted resets up to
// an hour later, so waiting one out is not something this run can do --
// it can only say so. A secondary limit, which is the one a burst of
// deletions actually provokes, asks for a minute or so in Retry-After
// and is worth taking: three of those is under five minutes of a
// twenty-minute job, and the alternative is a red run that deleted half
// its plan.
const (
	maxRateLimitPause = 90 * time.Second
	rateLimitRetries  = 3
	networkRetries    = 3
	networkRetryDelay = 500 * time.Millisecond
	// What to wait when GitHub says "rate limit" in the body and puts no
	// number anywhere in the headers. Secondary-limit documentation says
	// to wait at least a minute.
	defaultRateLimitPause = 60 * time.Second
)

func (a *API) headers() map[string]string {
	h := map[string]string{
		"Accept":               "application/vnd.github+json",
		"User-Agent":           "grain-ghcr-prune",
		"X-GitHub-Api-Version": "2022-11-28",
	}
	if a.Token != "" {
		h["Authorization"] = "token " + a.Token
	}
	return h
}

// do sends one call, waiting out a rate limit and trying again if that
// is what came back.
//
// The retry is here rather than at the call sites because every path in
// this file can meet one: a run of this command lists every version of
// three packages a hundred at a time and then deletes up to -max-deletes
// of them, which is hundreds of requests inside one job against a
// GITHUB_TOKEN whose primary limit is a thousand an hour for the whole
// repository, and a burst of DELETEs against a secondary limit that
// counts mutations far more heavily than reads.
//
// A response this gives up on is handed back as it stands, still
// carrying the headers that say what it was -- so DeleteVersion below
// can tell a limit it waited out and lost from the permission failure it
// must not be confused with.
func (a *API) do(method, path string) (github.ApiResponse, error) {
	resp, _, err := a.send(method, path)
	return resp, err
}

// send is do, also reporting whether any attempt before the one that
// answered failed in transport. That is the case in which an earlier
// attempt may have reached GitHub and done its work with only the
// response lost -- which for a DELETE means the retry finds the version
// already gone, and DeleteVersion needs to know that is what a 404 is.
//
// A call pkg/github's own rate-limit gate declined to send
// (*github.RateLimitError) is not a network failure and is not retried
// as one: the gate stays shut until a stated instant, and three retries
// half a second apart would all meet it. It is waited out like a 403 or
// 429 the API answered with, against the same bounds.
func (a *API) send(method, path string) (resp github.ApiResponse, lost bool, err error) {
	networkAttempts, limitAttempts := 0, 0
	for {
		resp, err := a.Transport.Request(method, path, a.headers(), nil)
		if err != nil {
			var gated *github.RateLimitError
			if errors.As(err, &gated) {
				pause := gated.Until.Sub(a.now())
				if pause < 0 {
					pause = 0
				}
				if limitAttempts >= rateLimitRetries || pause > maxRateLimitPause {
					return resp, lost, err
				}
				limitAttempts++
				a.sleep(pause)
				continue
			}
			lost = true
			if networkAttempts < networkRetries {
				networkAttempts++
				a.sleep(time.Duration(networkAttempts) * networkRetryDelay)
				continue
			}
			return resp, lost, err
		}
		pause, limited := rateLimitPause(resp, a.now())
		if !limited || limitAttempts >= rateLimitRetries || pause > maxRateLimitPause {
			return resp, lost, nil
		}
		limitAttempts++
		a.sleep(pause)
	}
}

func (a *API) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *API) sleep(d time.Duration) {
	if a.Sleep != nil {
		a.Sleep(d)
		return
	}
	time.Sleep(d)
}

// header reads one response header whatever it is capitalised as.
// RealTransport keys these by net/http's canonical spelling
// ("X-Ratelimit-Remaining"), GitHub sends them lowercase over HTTP/2,
// and a test writes whichever it finds most readable; none of that
// should decide whether a rate limit is recognised.
func header(resp github.ApiResponse, name string) string {
	for k, v := range resp.Headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// rateLimitPause reports how long GitHub is asking this client to wait
// before repeating a call, and whether the response is a rate limit at
// all.
//
// It exists because 403 is not one condition, which pkg/github's own
// IsPermissionDenied spells out at length: GitHub answers 403 to a
// credential that may never do this -- the failure prune-packages wants
// loud, since clearing it means a person setting GHCR_PRUNE_TOKEN -- and
// equally to both of its rate limits, which clear on their own and which
// nobody should be sent to make a personal access token over. Telling
// them apart is the difference between "wait and try again" and an
// afternoon spent on a token that was never the problem.
//
// Positively identifying a limit is what is required, in the direction
// that package argues for: anything not recognised stays whatever it
// already was, so a permission failure is never read as transient.
func rateLimitPause(resp github.ApiResponse, now time.Time) (time.Duration, bool) {
	// 429 is what the secondary limit increasingly answers with; 403 is
	// what both have historically used and what the primary one still
	// does. Nothing else is a rate limit whatever its body says.
	if resp.Status != 403 && resp.Status != 429 {
		return 0, false
	}
	// Retry-After, when it is there, is GitHub telling this client the
	// number outright -- the secondary limit's own instruction, and the
	// one to obey ahead of any reset time.
	if v := header(resp, "Retry-After"); v != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second, true
		}
	}
	// The primary limit: a budget spent to nothing, and the wall-clock
	// second it refills at.
	if header(resp, "X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(strings.TrimSpace(header(resp, "X-RateLimit-Reset")), 10, 64); err == nil {
			pause := time.Unix(reset, 0).Sub(now)
			if pause < 0 {
				pause = 0
			}
			return pause, true
		}
		return defaultRateLimitPause, true
	}
	// Status 429 is always Too Many Requests (rate limit).
	if resp.Status == 429 {
		return defaultRateLimitPause, true
	}
	// For 403, neither header was present, so the body is all there is. GitHub words both
	// limits with "rate limit" in the message ("You have exceeded a
	// secondary rate limit"), and a permission refusal never does.
	var body struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(resp.Body, &body); err == nil && strings.Contains(strings.ToLower(body.Message), "rate limit") {
		return defaultRateLimitPause, true
	}
	return 0, false
}

// ErrRateLimited is a call GitHub is refusing for rate rather than for
// permission, after do() has waited as long as one job can afford to.
// It is separate from github.Error so the message a person reads names
// the thing that is actually wrong and the time it clears at, instead of
// sending them to the packages settings page.
type ErrRateLimited struct {
	Status int
	Body   string
	Pause  time.Duration
}

func (e *ErrRateLimited) Error() string {
	return fmt.Sprintf("GitHub rate-limited this call (%d) and wants %s, which is longer than this job can wait -- this is a rate limit and not a permission problem, so the next scheduled run picks up where this one stopped; if it keeps happening, the run is asking for more requests an hour than GITHUB_TOKEN has (1,000 per repository) and wants either a smaller -max-deletes or the GHCR_PRUNE_TOKEN secret, whose own budget is larger: %s",
		e.Status, e.Pause.Round(time.Second), e.Body)
}

// rateLimitError turns a response do() gave up on into an ErrRateLimited,
// or returns nil when the response was never a rate limit.
func (a *API) rateLimitError(resp github.ApiResponse) error {
	pause, limited := rateLimitPause(resp, a.now())
	if !limited {
		return nil
	}
	body := string(resp.Body)
	if len(body) > 200 {
		body = body[:200]
	}
	return &ErrRateLimited{Status: resp.Status, Body: body, Pause: pause}
}

// apiError is how a response this job cannot use is reported: a
// github.Error, so a caller and the tests can still tell one apart from
// a refusal or a rate limit, rendered as GitHub's own sentence.
//
// Every error here is printed straight at a person -- run() returns it,
// main prints it, and where it lands is a workflow log somebody is
// reading because their images did not get pruned. github.Error's own
// rendering quotes the response body escaped, so a token without
// `packages: write` currently reports itself as `GitHub API error 403:
// "{\"message\":\"Resource not accessible by personal access
// token\",\"documentation_url\":...`, with the sentence naming the fix
// buried in it and the remedy this file appends after it pushed off the
// line. github.Readable puts GitHub's own words where the reader is
// looking and keeps the *github.Error underneath for errors.As.
func apiError(resp github.ApiResponse) error {
	return github.Readable(&github.Error{Status: resp.Status, Body: resp.Body})
}

// packagePath is the versions collection for one package under whichever
// owner kind this account turned out to be. The package name goes
// through url.PathEscape because it contains a slash -- "grain/guest" is
// one path segment, not two, and GitHub answers 404 to the unescaped
// spelling.
func (a *API) packagePath(pkg string) (string, error) {
	if a.ownerPath == "" {
		if err := a.resolveOwnerKind(pkg); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%s/packages/container/%s/versions", a.ownerPath, url.PathEscape(a.qualified(pkg))), nil
}

// qualified turns the short name a caller uses ("guest") into the
// package name GHCR knows ("grain/guest"). A name that already carries
// the repository is left alone, so a caller may pass either.
func (a *API) qualified(pkg string) string {
	if strings.Contains(pkg, "/") {
		return pkg
	}
	return a.Repo + "/" + pkg
}

func (a *API) resolveOwnerKind(pkg string) error {
	escaped := url.PathEscape(a.qualified(pkg))
	for _, candidate := range []string{"/orgs/" + a.Owner, "/users/" + a.Owner} {
		resp, err := a.do("GET", fmt.Sprintf("%s/packages/container/%s/versions?per_page=1", candidate, escaped))
		if err != nil {
			return err
		}
		if resp.Status == 200 {
			a.ownerPath = candidate
			return nil
		}
		if resp.Status != 404 {
			if err := a.rateLimitError(resp); err != nil {
				return err
			}
			return apiError(resp)
		}
	}
	return fmt.Errorf("no container package %q under %s as either an organization or a user -- check the name and that this token may read packages", a.qualified(pkg), a.Owner)
}

type versionJSON struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	Metadata  struct {
		Container struct {
			Tags []string `json:"tags"`
		} `json:"container"`
	} `json:"metadata"`
}

// ListVersions returns every version of one package, newest first as
// GitHub orders them, following the Link header to the last page.
func (a *API) ListVersions(pkg string) ([]Version, error) {
	base, err := a.packagePath(pkg)
	if err != nil {
		return nil, err
	}
	path := base + "?per_page=100"
	var out []Version
	for path != "" {
		resp, err := a.do("GET", path)
		if err != nil {
			return nil, err
		}
		if resp.Status != 200 {
			if err := a.rateLimitError(resp); err != nil {
				return nil, fmt.Errorf("listing versions of %s: %w", a.qualified(pkg), err)
			}
			return nil, fmt.Errorf("listing versions of %s: %w", a.qualified(pkg), apiError(resp))
		}
		var items []versionJSON
		if err := json.Unmarshal(resp.Body, &items); err != nil {
			return nil, fmt.Errorf("decoding versions of %s: %w", a.qualified(pkg), err)
		}
		for _, item := range items {
			created, err := time.Parse(time.RFC3339, item.CreatedAt)
			if err != nil {
				return nil, fmt.Errorf("version %d of %s has an unparseable created_at %q: %w", item.ID, a.qualified(pkg), item.CreatedAt, err)
			}
			out = append(out, Version{
				Package: pkg,
				ID:      item.ID,
				Digest:  item.Name,
				Tags:    item.Metadata.Container.Tags,
				Created: created,
			})
		}
		path = nextPagePath(header(resp, "Link"))
	}
	return out, nil
}

// ErrRefused is a deletion GitHub will refuse however often it is
// retried, but that says nothing about this command's credentials: the
// 400 a public package version with more than 5,000 downloads answers
// with. One of those must not fail a scheduled run for ever, so it is
// reported and stepped over, unlike a 401/403/404 -- which say the token
// cannot delete packages at all, and which should be loud.
type ErrRefused struct {
	Status int
	Body   string
}

func (e *ErrRefused) Error() string {
	return fmt.Sprintf("GitHub refused the deletion (%d): %s", e.Status, e.Body)
}

// ErrAlreadyGone is a retried deletion answered with 404: the version
// is not there to delete, almost certainly because the attempt whose
// response was lost deleted it. prune counts it rather than stopping.
type ErrAlreadyGone struct {
	Body string
}

func (e *ErrAlreadyGone) Error() string {
	return fmt.Sprintf("version already gone (404 on a retried DELETE): %s", e.Body)
}

// DeleteVersion removes one version, and with it every tag on it.
func (a *API) DeleteVersion(pkg string, id int64) error {
	base, err := a.packagePath(pkg)
	if err != nil {
		return err
	}
	resp, lost, err := a.send("DELETE", fmt.Sprintf("%s/%d", base, id))
	if err != nil {
		return err
	}
	// A 403 is two failures wearing the same number, and this is the call
	// that meets both: a run deleting up to -max-deletes versions in a
	// tight loop is a burst of mutations, which is precisely what a
	// secondary rate limit is for (429 is the other number it uses).
	// Saying "your token may not delete packages" to a limit that clears
	// in a minute sends somebody to make a personal access token that
	// changes nothing, so the limit is named first and separately.
	if err := a.rateLimitError(resp); err != nil {
		return err
	}
	switch {
	case resp.Status == 204 || resp.Status == 200:
		return nil
	case resp.Status == 404 && lost:
		// An earlier attempt failed in transport, and a timeout says
		// nothing about whether the request got there: this is most
		// likely that attempt's deletion, with only its answer lost.
		// A 404 to a DELETE that was sent once is still an error below
		// -- a version listed moments ago does not vanish by itself.
		return &ErrAlreadyGone{Body: string(resp.Body)}
	case resp.Status == 400 || resp.Status == 422:
		return &ErrRefused{Status: resp.Status, Body: string(resp.Body)}
	case resp.Status == 401 || resp.Status == 403:
		// What is left of a 403 once the limits above are ruled out is
		// the failure whose fix is a person doing something, so it says
		// what. Listing versions of a public package needs no permission
		// worth mentioning; deleting one is where a credential that is
		// not enough finds out.
		return fmt.Errorf("%w -- deleting a package version needs a credential that may: GITHUB_TOKEN with `packages: write` covers a package scoped to this repository, and the GHCR_PRUNE_TOKEN repository secret (a PAT with delete:packages) is what prune-packages.yml uses instead when it does not", apiError(resp))
	default:
		return apiError(resp)
	}
}

// ListBranchTags returns the branches this repository still has, in the
// spelling a docker tag uses: pkg/upgrade's TagForBranch, called rather
// than restated so that this cannot drift from what an upgrade resolves,
// and the substitution CI's publishers make when they push.
func (a *API) ListBranchTags() (map[string]bool, error) {
	path := fmt.Sprintf("/repos/%s/%s/branches?per_page=100", a.Owner, a.Repo)
	tags := map[string]bool{}
	for path != "" {
		resp, err := a.do("GET", path)
		if err != nil {
			return nil, err
		}
		if resp.Status != 200 {
			if err := a.rateLimitError(resp); err != nil {
				return nil, fmt.Errorf("listing branches of %s/%s: %w", a.Owner, a.Repo, err)
			}
			return nil, fmt.Errorf("listing branches of %s/%s: %w", a.Owner, a.Repo, apiError(resp))
		}
		var items []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(resp.Body, &items); err != nil {
			return nil, err
		}
		for _, item := range items {
			tags[upgrade.TagForBranch(item.Name)] = true
		}
		path = nextPagePath(header(resp, "Link"))
	}
	return tags, nil
}

// nextPagePath is the path+query of the rel="next" link, or "" on the
// last page -- the same Link-header convention every GitHub list
// endpoint uses, and the same reading pkg/github does of it. Written out
// again here rather than exported from there: it is ten lines, this
// command is not worth widening that package's surface for, and it needs
// one thing that package's copy does not (see EscapedPath below).
func nextPagePath(link string) string {
	for _, part := range strings.Split(link, ",") {
		segment := strings.TrimSpace(part)
		if !strings.Contains(segment, `rel="next"`) {
			continue
		}
		start, end := strings.Index(segment, "<"), strings.Index(segment, ">")
		if start == -1 || end == -1 || end < start {
			continue
		}
		u, err := url.Parse(segment[start+1 : end])
		if err != nil {
			continue
		}
		// EscapedPath, not Path: these paths carry a package name with
		// a slash in it ("grain/guest"), escaped to %2F, and handing
		// the decoded form back to the API is a 404 on every page after
		// the first. pkg/github's own copy of this uses Path, which is
		// right for the paths it builds and wrong here.
		if u.RawQuery != "" {
			return u.EscapedPath() + "?" + u.RawQuery
		}
		return u.EscapedPath()
	}
	return ""
}
