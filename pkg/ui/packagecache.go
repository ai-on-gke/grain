package ui

// The proxy cache (pkg/pkgcache) as an operator sees it: what it is
// holding, and the one thing there is to do to it besides switching it
// off, which is to throw it away. "Proxy cache" is what every reader
// sees it called since grain/task-328; the routes, the keys and the
// package keep the package-cache name they have always had, because
// those are an API and a state repository rather than prose.
//
// Two routes, no more. The switch itself is an ordinary setting
// (Settings.PackageCacheEnabled), as is the bound on how large the
// cache may grow (Settings.PackageCacheSizeGB): both are decisions
// about what future runs are configured with, and belong on the form
// with the rest of them. Neither of these routes is a setting at all --
// one is a live reading and the other deletes files -- which is why
// they are routes and why what they report is not on the Settings
// response. Saving a form should never empty a cache as a side effect,
// and reading a number should never require opening one.
//
// The cache is reached through an interface rather than through
// *pkgcache.Cache so that a UI with no cache behind it (`grain demo`,
// every test in this package) is a nil field reporting itself
// unavailable, which is the same nil-means-unavailable contract Secrets
// and Reboot already have here.

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/ai-on-gke/grain/pkg/apierror"
)

// PackageCacheStats is one ecosystem's share of the cache, as the
// System pane's Sandbox health tab shows it.
type PackageCacheStats struct {
	// Kind is the ecosystem -- "apt", "pypi", "docker", "npm", "go", "github"
	// (pkgcache.Kinds).
	Kind string `json:"kind"`
	// Entries is how many packages are stored, and Bytes how much disk
	// they take, the metadata beside each one included.
	Entries int   `json:"entries"`
	Bytes   int64 `json:"bytes"`
}

// PackageCache is the half of pkgcache.Cache this package needs: what
// it holds, and permission to empty it.
type PackageCache interface {
	// PackageCacheStats is what is stored right now, one entry per
	// ecosystem, in a stable order.
	PackageCacheStats() ([]PackageCacheStats, error)
	// PackageCacheEntryCount is how many packages are stored across
	// every ecosystem, and nothing else -- no per-kind breakdown and no
	// error. It exists because the sidebar draws that one number beside
	// its Proxy cache entry on every config poll, from every open
	// browser, and PackageCacheStats measures the cache by walking the
	// whole directory: cheap for a pane that polls it at five seconds,
	// not something to put on the call every view makes. The cache keeps
	// this one as a running count instead (pkgcache.Cache.Entries), so
	// it can be off by whatever has committed since its last walk --
	// which for a figure abbreviated to "2.5k" is no difference at all.
	PackageCacheEntryCount() int
	// FlushPackageCache empties one ecosystem, or all of them when kind
	// is empty, and reports what went.
	FlushPackageCache(kind string) (entries int, freed int64, err error)
}

// packageCacheStats is what a flush reports as the state afterwards,
// and nil both when this process runs no cache and when the cache
// cannot be read. The second case is deliberately as quiet as the
// first: a flush that did its work but then answered with an error
// because the directory it had just emptied could not be re-read is
// worse than one that answers with no numbers.
//
// It rode on the settings response until grain/task-266, when what the
// cache holds moved off the Settings form to the host pane and became
// GET /api/package-cache's own business -- a live figure, re-read as
// that pane polls, rather than one fixed at the moment a form loaded.
func (c *Client) packageCacheStats() []PackageCacheStats {
	if c.Config.PackageCache == nil {
		return nil
	}
	stats, err := c.Config.PackageCache.PackageCacheStats()
	if err != nil {
		return nil
	}
	return stats
}

// PackageCacheFlush is what POST /api/package-cache/flush answers with:
// what was thrown away, and what is left.
type PackageCacheFlush struct {
	// Kind is the ecosystem flushed, empty for all of them -- echoed
	// back so a caller reading a log knows which button was pressed.
	Kind string `json:"kind,omitempty"`
	// Entries and Bytes are what this flush removed.
	Entries int   `json:"entries"`
	Bytes   int64 `json:"bytes"`
	// Cache is the cache's state afterwards, so a pane can redraw its
	// numbers without a second call.
	Cache []PackageCacheStats `json:"cache"`
}

// FlushPackageCache empties the cache, or one ecosystem of it.
//
// It is safe at any moment, runs in flight included: an entry being
// read when it is removed goes on being readable to whoever has it
// open, and everything else is one fetch from upstream away. That is
// why this needs no pause, no drain and no confirmation beyond the
// button.
func (c *Client) FlushPackageCache(ctx context.Context, kind string) (PackageCacheFlush, error) {
	if c.Config.PackageCache == nil {
		return PackageCacheFlush{}, errors.New("this deployment does not run a proxy cache")
	}
	kind = strings.TrimSpace(kind)
	entries, freed, err := c.Config.PackageCache.FlushPackageCache(kind)
	if err != nil {
		return PackageCacheFlush{}, err
	}
	return PackageCacheFlush{Kind: kind, Entries: entries, Bytes: freed, Cache: c.packageCacheStats()}, nil
}

// PackageCacheState is what GET /api/package-cache answers with: what
// the cache holds, and whether this deployment runs one at all.
//
// "Enabled" rather than an error status for the second case, which is
// the convention GET /api/sandboxes already set for the panes that draw
// this deployment's own machinery: a deployment with no cache is a
// perfectly ordinary deployment, and the page that says so is showing a
// reading rather than reporting a fault. The route answered 501 to it
// until the cache got a page of its own (grain/task-321) -- fine while
// the only caller was a button, and wrong for a pane whose whole job is
// to say what the cache is doing, which would have had to render an
// error banner to say "nothing, and that is fine".
type PackageCacheState struct {
	// Enabled is whether this process runs a cache. False leaves Cache
	// nil -- there is nothing to report rather than nothing in it.
	Enabled bool `json:"enabled"`
	// Cache is what is stored right now, one entry per ecosystem.
	Cache []PackageCacheStats `json:"cache"`
}

// handleGetPackageCache answers GET /api/package-cache.
func (s *Server) handleGetPackageCache(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.PackageCache == nil {
		writeJSON(w, http.StatusOK, PackageCacheState{})
		return
	}
	stats, err := s.tasks.Config.PackageCache.PackageCacheStats()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, PackageCacheState{Enabled: true, Cache: stats})
}

// handleFlushPackageCache answers POST /api/package-cache/flush, with
// ?kind=apt (or pypi, docker, npm, go) to empty one ecosystem and no
// query to empty all of them.
func (s *Server) handleFlushPackageCache(w http.ResponseWriter, r *http.Request) {
	if s.tasks.Config.PackageCache == nil {
		writeError(w, http.StatusNotImplemented, apierror.Public("this deployment does not run a proxy cache"))
		return
	}
	result, err := s.tasks.FlushPackageCache(r.Context(), r.URL.Query().Get("kind"))
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
