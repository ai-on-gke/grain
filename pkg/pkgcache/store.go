package pkgcache

import "io"

// Store is where a Server keeps what it fetches: an object store keyed by
// ecosystem (kind) and URL, holding one body and its Meta per key.
//
// The Server takes this rather than a *Cache so that the store can be
// something other than a directory on this host -- the cluster's cache in
// GCS, or a pass-through to it (docs/design/grit-split.md#GS-1). *Cache
// is the one implementation today.
type Store interface {
	// Open returns a stored copy of url, if there is one. The caller
	// closes it. A damaged entry is reported absent, never as an error.
	Open(kind, url string) (Object, Meta, bool)
	// NewWriter starts an entry for url. An error means "forward without
	// caching", never a failed request.
	NewWriter(kind, url string) (ObjectWriter, error)
	// MaxEntryBytes is the largest single entry the store keeps, which a
	// Server names when it declines to cache something bigger.
	MaxEntryBytes() int64
}

// Object is one stored entry being read. It seeks because the Go build
// cache hashes an output before serving it (gocache.go).
type Object interface {
	io.ReadSeekCloser
	// Name says which entry it is, for a log line about a bad one.
	Name() string
}

// ObjectWriter is one entry being filled (*Writer's doc comment). Nothing
// written is visible until Commit, and Abort leaves nothing behind.
type ObjectWriter interface {
	// Write never reports an error: a store that gives up mid-entry must
	// not take the client's download with it.
	io.Writer
	// Commit makes the entry visible, reporting whether it was kept.
	Commit(meta Meta) bool
	// Abort drops the entry.
	Abort()
	// Oversize is whether the entry was dropped for exceeding
	// MaxEntryBytes rather than for an error.
	Oversize() bool
}

// Admin is the store as whoever runs it sees it: what it holds, permission
// to empty it, and its bound. It is the shape ui.PackageCache asks of the
// daemon, so the daemon's wiring holds one of these rather than a *Cache.
type Admin interface {
	// Stats is what is stored right now, one entry per ecosystem, in
	// Kinds' order.
	Stats() ([]KindStats, error)
	// Entries is how many objects are stored across every ecosystem, as a
	// running count rather than a measurement.
	Entries() int
	// Flush empties one ecosystem, or every one for kind "", and reports
	// what went.
	Flush(kind string) (entries int, freed int64, err error)
	// SetMaxBytes moves the store's bound, evicting down to it.
	SetMaxBytes(maxBytes int64)
}

var (
	_ Store        = (*Cache)(nil)
	_ Admin        = (*Cache)(nil)
	_ ObjectWriter = (*Writer)(nil)
)
