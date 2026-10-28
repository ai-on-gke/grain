package ui

// The GitHub Actions cache (pkg/actionscache) as an operator sees it:
// what it is holding, who is filling it, and the one thing there is to
// do to it besides switching it off, which is to throw it away.
//
// It is the package cache's surface next door (pkg/ui/packagecache.go),
// field for field and route for route, because it is the same kind of
// thing: a store on the controller's disk that exists only to make the
// next run faster, and that an operator occasionally needs to look at
// and empty. The two decisions about it that are configuration --
// whether CI jobs use it at all (Settings.ActionsCacheEnabled) and how
// large it may grow (Settings.ActionsCacheSizeGB) -- are on the
// Settings form with the rest of them. Neither of the routes here is a
// setting: one is a live reading and the other deletes files, which is
// why saving a form never empties a cache and reading a number never
// requires opening one.
//
// The breakdown is per repository rather than per anything else,
// because that is the only question the figures can answer that the
// total cannot: the cap is one global bound, so a busy repository
// evicts a quiet one's entries, and "who is filling this" is what
// somebody reading a full cache wants to know.
//
// Reached through an interface rather than through *actionscache.Cache,
// for the reason PackageCache is: a UI with no cache behind it (`grain
// demo`, every test in this package) is a nil field reporting itself
// unavailable.

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/ai-on-gke/grain/pkg/apierror"
)

// ActionsCacheRepoStats is one repository's share of the cache, as the
// Actions cache page shows it.
type ActionsCacheRepoStats struct {
	// Repo is "owner/name", the repository whose CI wrote these
	// entries -- each under the branch that wrote it, and read back only
	// as pkg/actionscache's Scope allows.
	Repo string `json:"repo"`
	// Bytes is how much disk that repository's entries take.
	//
	// No per-repository entry count beside it: actionscache.Stats keeps
	// bytes per repository and a count only for the whole cache, since
	// the count of *archives* is not a figure anybody sizes anything by
	// -- one buildkit layer-cache blob and two hundred `actions/cache`
	// entries can be the same gigabyte.
	Bytes int64 `json:"bytes"`
}

// ActionsCache is the half of actionscache.Cache this package needs:
// what it holds, and permission to empty it.
type ActionsCache interface {
	// ActionsCacheStats is what is stored right now: the whole cache's
	// entry count and size, the bound it is held to, and the breakdown
	// by repository.
	ActionsCacheStats() (entries int, bytes int64, maxBytes int64, repos []ActionsCacheRepoStats)
	// ActionsCacheEntryCount is how many entries are stored, and
	// nothing else. It exists for the reason
	// PackageCacheEntryCount does: the sidebar draws that one number on
	// every config poll from every open browser, so it must stay cheap
	// -- which this one is, the cache keeping its entries in memory.
	ActionsCacheEntryCount() int
	// FlushActionsCache empties one repository's entries, or every
	// repository's when repo is empty, and reports how many entries and
	// bytes went.
	FlushActionsCache(repo string) (entries int, freed int64)
}

// ActionsCacheState is what GET /api/actions-cache answers with: what
// the cache holds, and whether this deployment keeps one at all.
//
// "Enabled" rather than an error status for the second case, exactly as
// PackageCacheState has it: a deployment with no Actions cache is a
// perfectly ordinary deployment whose CI builds from cold, and the page
// that says so is showing a reading rather than reporting a fault.
//
// Note what Enabled is *not*: it is not Settings.ActionsCacheEnabled.
// This says whether this process is running a cache to report on; that
// says whether the next CI job is pointed at one. A deployment that has
// just turned the switch off still has a cache full of entries, and
// that is precisely the moment somebody wants this page's flush button.
type ActionsCacheState struct {
	// Enabled is whether this process runs a cache. False leaves the
	// rest zero -- there is nothing to report rather than nothing in it.
	Enabled bool `json:"enabled"`
	// Entries and Bytes are the whole cache, and MaxBytes the bound it
	// evicts down to.
	Entries  int   `json:"entries"`
	Bytes    int64 `json:"bytes"`
	MaxBytes int64 `json:"maxBytes"`
	// Repos is the breakdown, largest first, so the repository filling
	// the cache is the first line rather than somewhere in the list.
	Repos []ActionsCacheRepoStats `json:"repos"`
	// UsedByJobs is Settings.ActionsCacheEnabled, carried here so the
	// page can say "this is full and nothing is reading it" without a
	// second call for a setting it does not otherwise care about.
	UsedByJobs bool `json:"usedByJobs"`
}

// ActionsCacheFlush is what POST /api/actions-cache/flush answers with:
// what was thrown away, and what is left.
type ActionsCacheFlush struct {
	// Repo is the repository flushed, empty for all of them -- echoed
	// back so a caller reading a log knows which button was pressed.
	Repo string `json:"repo,omitempty"`
	// Entries and Bytes are what this flush removed.
	Entries int   `json:"entries"`
	Bytes   int64 `json:"bytes"`
	// Cache is the cache's state afterwards, so a page can redraw its
	// numbers without a second call.
	Cache ActionsCacheState `json:"cache"`
}

// actionsCacheState is the live reading, and the zero value both when
// this process runs no cache and when there is nothing in it -- the
// difference being Enabled.
func (c *Client) actionsCacheState(ctx context.Context) ActionsCacheState {
	if c.Config.ActionsCache == nil {
		return ActionsCacheState{}
	}
	entries, bytes, maxBytes, repos := c.Config.ActionsCache.ActionsCacheStats()
	state := ActionsCacheState{
		Enabled: true, Entries: entries, Bytes: bytes, MaxBytes: maxBytes, Repos: repos,
		// The stored switch, read through the same path everything else
		// reads configuration through. A store that cannot be read
		// leaves this true, which is what a deployment that has chosen
		// nothing runs (model.DefaultConfig): the page would rather
		// overstate that jobs are using a cache than tell somebody their
		// CI is cold when it is not.
		UsedByJobs: true,
	}
	if cfg, err := c.Store.GetConfig(ctx); err == nil && cfg != nil {
		state.UsedByJobs = cfg.ActionsCacheEnabled
	}
	if state.Repos == nil {
		state.Repos = []ActionsCacheRepoStats{}
	}
	return state
}

// FlushActionsCache empties the cache, or one repository's share of it.
//
// It is safe at any moment, CI jobs in flight included: the worst a
// flush can do to a job is turn a hit it has not made yet into a miss,
// which costs that job a cold build and nothing else
// (actionscache.Cache.Flush). That is why this needs no pause, no drain
// and no confirmation beyond the button.
func (c *Client) FlushActionsCache(ctx context.Context, repo string) (ActionsCacheFlush, error) {
	if c.Config.ActionsCache == nil {
		return ActionsCacheFlush{}, errors.New("this deployment does not keep an Actions cache")
	}
	repo = strings.TrimSpace(repo)
	entries, freed := c.Config.ActionsCache.FlushActionsCache(repo)
	return ActionsCacheFlush{Repo: repo, Entries: entries, Bytes: freed, Cache: c.actionsCacheState(ctx)}, nil
}

// handleGetActionsCache answers GET /api/actions-cache.
func (s *Server) handleGetActionsCache(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.tasks.actionsCacheState(r.Context()))
}

// handleFlushActionsCache answers POST /api/actions-cache/flush, with
// ?repo=owner/name to empty one repository's entries and no query to
// empty every repository's.
//
// An unknown repository is not an error: it names no entries, so the
// flush removes none and says so. There is no list of repositories the
// cache is willing to be asked about -- what it holds is whatever CI
// has run -- so refusing a name would mean inventing one.
func (s *Server) handleFlushActionsCache(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.ActionsCache == nil {
		writeError(w, http.StatusNotImplemented, apierror.Public("this deployment does not keep an Actions cache"))
		return
	}
	result, err := s.tasks.FlushActionsCache(r.Context(), r.URL.Query().Get("repo"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
