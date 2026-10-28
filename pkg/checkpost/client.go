package checkpost

// The read side: grain asking this service how a commit's checks ended,
// instead of asking GitHub every thirty seconds.
//
// It is in this package rather than in pkg/forge because the wire format
// and the signing scheme are this package's, and a client that lived
// with its caller would be a second place they are written down. What
// pkg/forge holds is the interface (forge.Verdicts); this satisfies it.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/forge"
	"github.com/bwsalmon/grain/pkg/github"
)

// DefaultTimeout bounds one read.
//
// Short, because this is on the reconcile cycle's critical path and the
// whole point of it is to be cheaper than the GitHub call it replaces. A
// service that cannot answer in five seconds is one grain should stop
// waiting for and ask GitHub instead -- which is exactly what a timeout
// here causes, since forge.CheckRuns treats an error as "ask the forge".
const DefaultTimeout = 5 * time.Second

// Client reads verdicts for a set of repositories that report here.
//
// # Expect, and why there has to be one
//
// A reporting service holds verdicts for checks that have finished.
// Three jobs into a twelve-job build it holds three, all green, and
// nothing about that answer distinguishes it from a finished build with
// three checks. Counting cannot tell them apart, and neither can
// freshness -- a build that is still running is not stale.
//
// So the deployment says what a complete answer looks like: per
// repository, the names of the *reporting* checks it expects to see. A
// reporting check is the one each workflow posts last, after all its
// other jobs have finished, so its presence is that workflow saying "I
// am done, and everything I cover is here". When every expected name is
// present, the answer is complete; until then grain asks GitHub, exactly
// as it always did.
//
// A repository with no entry reports nothing here as far as this client
// is concerned, and every read for it is incomplete -- which makes the
// whole mechanism opt-in per repository rather than a switch that
// silently changes how every repo is gated.
type Client struct {
	// BaseURL is where the service is.
	BaseURL string
	// Secret signs every request, and is the same one the service
	// verifies with.
	Secret string
	// Expect maps "owner/name" to the reporting-check names that
	// together make an answer complete. A repository absent from this
	// map is never answered from here.
	Expect map[string][]string
	// HTTP is the client used, DefaultTimeout if nil.
	HTTP *http.Client
	// Now is the clock the signature is stamped with; time.Now if nil.
	Now func() time.Time
}

// compile-time check that this is what pkg/forge asks for.
var _ forge.Verdicts = (*Client)(nil)

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: DefaultTimeout}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// For is forge.Verdicts: the checks reported for this commit, and
// whether they are the whole answer.
//
// A repository this deployment does not expect reports from is answered
// (nil, false, nil) without a request being made at all -- not an error,
// because it is not a fault: it is a repository whose checks come from
// the forge, which is most of them.
func (c *Client) For(ref forge.Ref, headSHA string) ([]github.CheckRun, bool, error) {
	repo := ref.Owner + "/" + ref.Repo
	expected := c.Expect[repo]
	if len(expected) == 0 {
		return nil, false, nil
	}
	if c.BaseURL == "" || c.Secret == "" {
		return nil, false, fmt.Errorf("checkpost: %s is expected to report here, but this "+
			"deployment has no service url or secret configured", repo)
	}

	verdicts, err := c.fetch(repo, headSHA)
	if err != nil {
		return nil, false, err
	}

	// Complete only when every reporting check the deployment expects
	// has arrived. Missing one is a workflow that has not finished (or
	// one whose reporting job failed), and either way the answer is
	// somebody else's to give.
	present := make(map[string]bool, len(verdicts))
	for _, v := range verdicts {
		present[v.Name] = true
	}
	for _, name := range expected {
		if !present[name] {
			return nil, false, nil
		}
	}

	checks := make([]github.CheckRun, 0, len(verdicts))
	for _, v := range verdicts {
		conclusion := v.Conclusion
		checks = append(checks, github.CheckRun{
			Name: v.Name,
			// Every verdict here has by definition finished -- there is
			// no way to report one that has not (Verdict's own doc
			// comment) -- so this is always "completed", which is what
			// makes the answer judgeable by exactly the same code that
			// judges GitHub's own.
			Status:     "completed",
			Conclusion: &conclusion,
			HTMLURL:    v.URL,
		})
	}
	return checks, true, nil
}

// fetch performs the signed GET.
func (c *Client) fetch(repo, headSHA string) ([]Verdict, error) {
	owner, name, ok := Repo(repo)
	if !ok {
		return nil, fmt.Errorf("checkpost: %q is not owner/name", repo)
	}
	path := fmt.Sprintf("/v1/verdicts/%s/%s/%s", owner, name, headSHA)
	req, err := http.NewRequest(http.MethodGet, strings.TrimSuffix(c.BaseURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	// The GET has no body, so it is the method and path in the signature
	// that tie it to this one repository and commit: without them every
	// read in the same second would carry the same signature, and one
	// captured read would answer for any repository (sign.go). The path
	// signed is the route, not BaseURL's prefix in front of it -- the
	// route is what the server sees.
	ts := c.now()
	req.Header.Set(TimestampHeader, fmt.Sprint(ts.Unix()))
	req.Header.Set(SignatureHeader, Sign(c.Secret, ts, http.MethodGet, path, nil))

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("checkpost: the service answered %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out []Verdict
	if err := json.Unmarshal(bytes.TrimSpace(body), &out); err != nil {
		return nil, fmt.Errorf("checkpost: the service's answer did not parse: %w", err)
	}
	return out, nil
}

// ParseExpect reads the deployment's per-repository reporting-check
// configuration out of one string, so it can travel as a flag or an
// environment variable:
//
//	owner/name=tests,build-artifacts;other/repo=ci
//
// Semicolons between repositories, commas between the check names that
// together make that repository's answer complete. Whitespace around any
// part is ignored, because this is typed by hand into a unit file.
//
// An empty string is no repository reporting here, which is the default
// and leaves every read falling through to GitHub.
func ParseExpect(s string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, entry := range strings.Split(s, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		repo, names, ok := strings.Cut(entry, "=")
		repo = strings.TrimSpace(repo)
		if !ok || repo == "" {
			return nil, fmt.Errorf("checkpost: %q is not owner/name=check,check", entry)
		}
		if _, _, ok := Repo(repo); !ok {
			return nil, fmt.Errorf("checkpost: %q is not owner/name", repo)
		}
		var expect []string
		for _, n := range strings.Split(names, ",") {
			if n = strings.TrimSpace(n); n != "" {
				expect = append(expect, n)
			}
		}
		if len(expect) == 0 {
			// Refused rather than read as "expect nothing", which would
			// make every read for this repository complete the moment a
			// single verdict arrived -- the exact failure the whole
			// Expect mechanism exists to prevent.
			return nil, fmt.Errorf("checkpost: %s names no reporting checks, so nothing could "+
				"ever make its answer complete", repo)
		}
		out[repo] = expect
	}
	return out, nil
}
