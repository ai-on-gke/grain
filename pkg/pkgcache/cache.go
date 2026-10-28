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

// Package pkgcache is this deployment's package cache: one HTTP service
// in the controller that every sandbox's apt, pip, docker, npm and go
// are pointed at, so the twentieth run that installs ripgrep -- or pulls
// debian:bookworm, or runs `npm ci` -- fetches it from the machine next
// to it rather than from the internet.
//
// It is wired the way pkg/modelproxy is -- a listener beside the git
// proxy, on the address a sandbox already reaches this deployment's own
// services at (cmd/grain's -kontur-git-proxy-host and its two siblings)
// -- because that is the one address every sandbox backend can reach,
// with no route, no DNAT and no change to any sandbox image. A sandbox
// with no network at all (model.NetworkNone) reaches it for the same
// reason it still reaches the git proxy, which means package installs
// start working in sandboxes where they used to fail outright. It is
// also why such a sandbox may only *read* through this service: no git
// push, no registry upload or delete, and a CONNECT tunnel only to a host
// that serves nothing but downloads. Every one of those would be a way
// out to the internet for a sandbox promised it had none -- carrying a
// credential the sandbox supplies itself, which is exactly what a
// prompt-injected run is handed (restrict.go).
//
// Five ecosystems, one service, because they are the same job five
// times -- each reached by whatever hook its own client already has:
//
//   - **apt**, served as an ordinary HTTP proxy. apt's own
//     Acquire::http::Proxy::<host> setting is what points it here, per
//     archive host rather than globally (sandbox.go), so a third-party
//     repository a run adds itself keeps being fetched directly and is
//     not broken by this existing.
//   - **PyPI**, served as a reverse proxy at /pypi/. pip has no proxy
//     setting that could cache anything -- it speaks HTTPS to
//     pypi.org -- so what it is given instead is an index URL of our
//     own, whose pages have had their download links rewritten to point
//     back through here (pypi.go).
//   - **Docker Hub**, served as a registry at /v2/. dockerd's
//     `registry-mirrors` names another registry to ask first for
//     anything under docker.io, which is this one; it forwards to
//     registry-1.docker.io, fetching an anonymous pull token the way the
//     docker CLI does (docker.go).
//   - **npm**, at /npm/, given to npm as a registry URL for PyPI's
//     reason -- registry.npmjs.org is HTTPS -- but with nothing
//     rewritten: npm maps a registry tarball URL onto whatever registry
//     it is configured with by itself (npm.go).
//   - **Go modules**, at /go/, which needs no rewriting either: GOPROXY
//     is a documented protocol of relative paths, so a module proxy in
//     front of a module proxy is a plain reverse proxy (goproxy.go).
//
// Two more kinds have joined them since -- GitHub release and archive
// downloads (github.go) and the Playwright browser CDNs (playwright.go)
// -- and every one of them is also reachable transparently, at its own
// upstream hostname, through the DNS interception in dns.go.
//
// What is cached is what cannot change under us: a .deb in a pool
// directory, a wheel or sdist on files.pythonhosted.org, a container
// layer addressed by the digest of its own content, a published npm
// tarball, a module zip at a version. All of them are named after the
// exact bytes they contain and are never rewritten in place, so a hit
// needs no revalidation and can never be stale. (A container layer is
// filed under its digest alone, so its bytes are hashed on the way in and
// it is stored only if they match -- see Server.serve.) Package *indexes* --
// InRelease, Packages.gz, a simple-index page, an image manifest, a
// packument, `@v/list` -- are forwarded every time and never stored,
// which costs a few kilobytes per run and removes the entire class of
// bug where a deployment installs last month's package list, or runs
// last month's `:latest`.
//
// It is a cache and nothing else. Nothing here is a credential store, an
// allowlist for anything a sandbox does, or a mirror an operator has to
// keep in sync: an empty cache behaves exactly like no cache, a
// corrupted entry is one 404 away from being refetched, and Flush
// (which is what the UI's own button calls) is allowed to throw all of
// it away at any moment, including while runs are using it.
package pkgcache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The ecosystems, as a stored entry is filed under and as Stats and
// Flush name them. They are directory names on disk, so they are short,
// lower-case and fixed.
const (
	KindAPT        = "apt"
	KindPyPI       = "pypi"
	KindDocker     = "docker"
	KindNPM        = "npm"
	KindGo         = "go"
	KindGoBuild    = "gobuild"
	KindGitHub     = "github"
	KindPlaywright = "playwright"
)

// Kinds is every ecosystem this cache holds, in the order a report
// lists them.
func Kinds() []string {
	return []string{KindAPT, KindPyPI, KindDocker, KindNPM, KindGo, KindGoBuild, KindGitHub, KindPlaywright}
}

// ValidKind reports whether kind names an ecosystem this cache has. A
// flush request naming anything else is refused rather than silently
// flushing nothing -- see Cache.Flush.
func ValidKind(kind string) bool {
	for _, k := range Kinds() {
		if k == kind {
			return true
		}
	}
	return false
}

// DefaultMaxBytes is how much disk the cache helps itself to when a
// deployment has not said. 20GiB is a few thousand .debs and wheels --
// comfortably more than the working set of a deployment installing the
// same handful of toolchains over and over, and small enough to be an
// unremarkable thing to find in a data directory.
const DefaultMaxBytes int64 = 20 << 30

// DefaultMaxEntryBytes is the largest single object worth storing.
// Anything above it is still forwarded, just never written to disk: one
// 4GiB download would otherwise evict the entire useful contents of a
// cache to hold a file nothing will ask for twice.
//
// An object over it is fetched from upstream on every single request,
// for ever, which is a thing an operator has to be able to find out
// about rather than guess at -- so the one place that discovers it says
// so once per fetch (Writer.Oversize, and its caller in server.go).
const DefaultMaxEntryBytes int64 = 2 << 30

// Meta is what is remembered about a stored object besides its bytes:
// enough to answer a request from disk without going upstream again.
type Meta struct {
	// URL is the upstream URL this is a copy of, for a human reading the
	// cache directory and for Stats. It is a public package URL, never a
	// credential-bearing one -- nothing this service forwards is
	// authenticated. It is set from the entry's own key when a caller
	// has not named one, and named separately by the caller that files an
	// entry under something other than its URL (a container layer, filed
	// under its digest).
	URL string `json:"url"`
	// ContentType is what to answer with. Stored rather than guessed
	// from the extension so a hit and a miss are byte-identical replies.
	ContentType string `json:"contentType"`
	// Size is the body length, also available from the file itself and
	// kept here so Stats need not stat both halves of every entry.
	Size int64 `json:"size"`
	// StoredAt is when the entry was written. Only ever used for a
	// report; eviction reads the file's own modification time, which is
	// what the filesystem keeps current.
	StoredAt time.Time `json:"storedAt"`
}

// Cache is the on-disk half: a content store keyed by upstream URL,
// under one directory per ecosystem, with a size bound it enforces by
// throwing away the least recently stored entries.
//
// Safe for concurrent use. The only shared mutable state is the byte
// count eviction reads, and every write to the store itself is a
// rename over a temp file in the same directory, so two runs fetching
// the same package at the same time race to a well-defined end: one
// rename wins and both clients got their bytes from upstream.
//
// That last sentence is also why the byte count is kept the way it is.
// One rename winning means the loser's bytes are *not* on disk, so a
// commit may only add what its entry actually grew the store by --
// Writer.Commit measures what it is replacing and adds the difference
// (which for a duplicate is nothing). And because no arithmetic on a
// running total survives every race there is, the count is not only
// added to and subtracted from: every pass that walks the whole store
// anyway -- evict, Flush, Stats -- ends by replacing it with what the
// walk measured. Drift is therefore bounded by the time to the next
// such pass rather than lasting for the life of the process, which
// matters because an over-count makes evict throw away live entries to
// bring an imaginary number down.
type Cache struct {
	dir      string
	maxBytes int64
	// maxEntry is DefaultMaxEntryBytes, as a field so that a test can
	// cross the limit without writing two gigabytes to do it.
	maxEntry int64

	// commit serializes the measure-and-rename at the end of
	// Writer.Commit, so that two writers finishing the same key cannot
	// both decide they are replacing nothing. It is held across four
	// metadata operations and nothing else -- never across a walk, a
	// removal loop or an eviction -- and deliberately is not mu, so that
	// Bytes and Stats (the Settings pane) never wait behind a commit.
	commit sync.Mutex

	// evicting makes eviction passes exclusive of each other -- see
	// evict, which explains why two passes at once cannot both be right.
	evicting sync.Mutex

	mu    sync.Mutex
	bytes int64
	// entries is how many objects the store holds, kept exactly the way
	// bytes above is and for a different reader: the rail's Proxy cache
	// count (pkg/ui, Sidebar.jsx), which is drawn on every config poll
	// from every open browser and so cannot afford the walk Stats does.
	// A commit adds one only when it replaced nothing, since a rename
	// over an existing entry leaves the store holding what it held.
	entries int64
	// added is the signed sum of every adjustment grew has ever applied
	// to bytes, and addedEntries the same for entries. A pass that
	// measures the store takes a copy of both before it starts and hands
	// them back to resync, which is how the commits that landed while
	// the pass was walking survive the measurement replacing the counts.
	added        int64
	addedEntries int64
}

// counts is what a pass about to measure the whole store carries from
// mark to resync: the two running totals as they stood before it began
// walking. See mark and resync.
type counts struct {
	bytes   int64
	entries int64
}

// NewCache opens (creating if need be) a cache under dir, holding at
// most maxBytes of package data. maxBytes <= 0 means DefaultMaxBytes.
//
// It walks what is already there to learn its size, which is what makes
// the bound survive a restart rather than being rediscovered only after
// the cache has grown past it again.
func NewCache(dir string, maxBytes int64) (*Cache, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("pkgcache: no directory to cache in")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	for _, kind := range Kinds() {
		if err := os.MkdirAll(filepath.Join(dir, kind), 0o755); err != nil {
			return nil, fmt.Errorf("pkgcache: creating %s: %w", filepath.Join(dir, kind), err)
		}
	}
	c := &Cache{dir: dir, maxBytes: maxBytes, maxEntry: DefaultMaxEntryBytes}
	entries, err := c.walk()
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		c.bytes += e.size
	}
	c.entries = int64(len(entries))
	return c, nil
}

// MaxBytes is the size bound this cache is holding itself to.
func (c *Cache) MaxBytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxBytes
}

// SetMaxBytes updates the size bound this cache is holding itself to,
// evicting if the cache is already larger than the new bound. maxBytes <= 0
// means DefaultMaxBytes.
func (c *Cache) SetMaxBytes(maxBytes int64) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	c.mu.Lock()
	c.maxBytes = maxBytes
	over := c.bytes > c.maxBytes
	c.mu.Unlock()
	if over {
		c.evict()
	}
}

// Dir is where the cache keeps its files, for a log line that has to
// tell an operator which directory to look in (or to clear by hand,
// when the daemon is not running to be asked).
func (c *Cache) Dir() string { return c.dir }

// key is where one upstream URL lives on disk: the SHA-256 of the URL,
// fanned out one byte deep so that a cache with tens of thousands of
// entries is not one directory with tens of thousands of files in it.
//
// Hashed rather than spelled out because a URL is not a path: it can be
// longer than any filesystem's component limit, it contains characters
// a path cannot, and the obvious sanitization of it is the kind that
// lets two different URLs collide on one file.
func (c *Cache) key(kind, url string) string {
	sum := sha256.Sum256([]byte(url))
	name := hex.EncodeToString(sum[:])
	return filepath.Join(c.dir, kind, name[:2], name)
}

// Open returns a stored copy of url, if there is one. The caller owns
// the returned file and must close it.
//
// An entry whose metadata cannot be read is treated as absent rather
// than as an error: the worst case is one extra fetch from upstream,
// and a cache that refuses to serve because part of itself is damaged
// is worse than one that quietly refills.
func (c *Cache) Open(kind, url string) (Object, Meta, bool) {
	path := c.key(kind, url)
	metaBytes, err := os.ReadFile(path + ".meta")
	if err != nil {
		return nil, Meta{}, false
	}
	var meta Meta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return nil, Meta{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, Meta{}, false
	}
	info, err := f.Stat()
	if err != nil || info.Size() != meta.Size {
		// A body that does not match the length recorded beside it is a
		// write that was interrupted, or a file somebody truncated. Do
		// not serve it: an apt or pip failure over a short read is far
		// more confusing than the refetch this causes instead.
		f.Close()
		return nil, Meta{}, false
	}
	return f, meta, true
}

// Writer is one entry being filled. It is written as a temp file beside
// its final name and renamed into place by Commit, so nothing ever
// observes a half-written entry -- including this process, after a
// crash: an uncommitted temp file is garbage that the next Flush or a
// human removes, and is never mistaken for a cached object.
type Writer struct {
	cache *Cache
	url   string
	path  string
	tmp   *os.File
	n     int64
	// dead is set when the entry has already been abandoned -- it grew
	// past the per-entry limit, or a write failed. Further writes are
	// accepted and dropped, so a caller teeing a response into this
	// never has to stop streaming to its own client because the cache
	// gave up.
	dead bool
	// oversize records that it was the per-entry limit and not an error
	// that ended the entry, so that the caller can say so. Without it an
	// object over the cap is refetched from upstream on every single
	// request with nothing anywhere admitting why -- a cache that is
	// enabled, healthy, and mysteriously never serving that one thing.
	oversize bool
}

// NewWriter starts an entry for url. A cache that cannot open a temp
// file returns an error, and every caller in this package treats that
// as "forward without caching" rather than as a failed request.
func (c *Cache) NewWriter(kind, url string) (ObjectWriter, error) {
	path := c.key(kind, url)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".partial-*")
	if err != nil {
		return nil, err
	}
	return &Writer{cache: c, url: url, path: path, tmp: tmp}, nil
}

// Write implements io.Writer. It never reports an error: this is the
// second half of an io.MultiWriter whose first half is the client's own
// connection, and a cache that fails mid-entry must not take the
// download with it.
func (w *Writer) Write(p []byte) (int, error) {
	if w.dead {
		return len(p), nil
	}
	if w.n+int64(len(p)) > w.cache.MaxEntryBytes() {
		w.oversize = true
		w.abandon()
		return len(p), nil
	}
	n, err := w.tmp.Write(p)
	w.n += int64(n)
	if err != nil {
		w.abandon()
	}
	return len(p), nil
}

// Commit finishes the entry: the metadata is written first, then the
// body renamed into place, so Open never sees a body with no metadata
// beside it. It reports whether anything was stored.
func (w *Writer) Commit(meta Meta) bool {
	if w.dead {
		return false
	}
	name := w.tmp.Name()
	if err := w.tmp.Close(); err != nil {
		os.Remove(name)
		w.dead = true
		return false
	}
	if meta.URL == "" {
		meta.URL = w.url
	}
	meta.Size = w.n
	meta.StoredAt = time.Now().UTC()
	encoded, err := json.Marshal(meta)
	if err != nil {
		os.Remove(name)
		w.dead = true
		return false
	}
	metaTmp, err := os.CreateTemp(filepath.Dir(w.path), ".partial-meta-*")
	if err != nil {
		os.Remove(name)
		w.dead = true
		return false
	}
	metaTmpName := metaTmp.Name()
	if _, err := metaTmp.Write(encoded); err != nil {
		metaTmp.Close()
		os.Remove(metaTmpName)
		os.Remove(name)
		w.dead = true
		return false
	}
	if err := metaTmp.Close(); err != nil {
		os.Remove(metaTmpName)
		os.Remove(name)
		w.dead = true
		return false
	}
	// From here to the unlock is the one part of a commit that has to be
	// serialized: what this entry replaces has to be measured by whoever
	// is about to replace it, or two runs that fetched the same package
	// at once both report having added a whole copy of it to a store
	// that holds one. See Cache.commit.
	w.cache.commit.Lock()
	replaced := storedSize(w.path)
	if err := os.Rename(metaTmpName, w.path+".meta"); err != nil {
		w.cache.commit.Unlock()
		os.Remove(metaTmpName)
		os.Remove(name)
		w.dead = true
		return false
	}
	if err := os.Rename(name, w.path); err != nil {
		w.cache.commit.Unlock()
		os.Remove(name)
		w.dead = true
		return false
	}
	w.cache.commit.Unlock()
	w.dead = true
	// replaced == 0 means there was nothing under this key, so the store
	// holds one more object than it did; a commit over an existing entry
	// changes its size and not the count.
	var added int64
	if replaced == 0 {
		added = 1
	}
	w.cache.grew(w.n+int64(len(encoded))-replaced, added)
	return true
}

// storedSize is how much disk the entry at path is occupying, body and
// .meta together, and 0 when there is nothing there. It counts an entry
// exactly the way walk does -- a .meta with no body beside it is not an
// entry and was never counted, so it is not subtracted either.
func storedSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return 0
	}
	size := info.Size()
	if metaInfo, err := os.Stat(path + ".meta"); err == nil {
		size += metaInfo.Size()
	}
	return size
}

// Abort throws the entry away. Safe to call after Commit, which is what
// lets a caller defer it unconditionally.
func (w *Writer) Abort() { w.abandon() }

// Oversize reports whether this entry was dropped for being larger than
// a single entry may be, rather than stored or lost to an error. The
// caller logs it: this package's writer has no logger of its own, and
// the caller is the half that knows the URL a human would recognise.
func (w *Writer) Oversize() bool { return w.oversize }

// MaxEntryBytes is the largest single object this cache will store.
func (c *Cache) MaxEntryBytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.maxEntry <= 0 {
		return DefaultMaxEntryBytes
	}
	return c.maxEntry
}

func (w *Writer) abandon() {
	if w.dead {
		return
	}
	w.dead = true
	name := w.tmp.Name()
	w.tmp.Close()
	os.Remove(name)
}

// grew records that the store is n bytes bigger than it was, holding
// objects more than it did -- both signed, because an entry committed
// over a larger one makes the store smaller and a removal takes an
// object away -- and evicts if that has taken the cache past its bound.
func (c *Cache) grew(n, objects int64) {
	c.mu.Lock()
	c.bytes += n
	c.added += n
	c.entries += objects
	c.addedEntries += objects
	if c.bytes < 0 {
		c.bytes = 0
	}
	if c.entries < 0 {
		c.entries = 0
	}
	over := c.bytes > c.maxBytes
	c.mu.Unlock()
	if over {
		c.evict()
	}
}

// mark is what a pass about to measure the whole store takes before it
// starts walking, and hands back to resync when it is done.
func (c *Cache) mark() counts {
	c.mu.Lock()
	defer c.mu.Unlock()
	return counts{bytes: c.added, entries: c.addedEntries}
}

// resync replaces the running counts with what the caller has just
// obtained by walking the store, plus whatever grew recorded since mark
// was taken.
//
// That second term is what makes this safe to call from a pass that
// walks and removes without holding mu: entries committed while the
// walk was in progress are not in what it measured, and dropping them
// would turn an over-count into an under-count. Nothing here is exact
// under concurrent commits, but nothing here accumulates either, which
// is the property that was missing.
func (c *Cache) resync(measured int64, measuredEntries int64, mark counts) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bytes = measured + (c.added - mark.bytes)
	if c.bytes < 0 {
		c.bytes = 0
	}
	c.entries = measuredEntries + (c.addedEntries - mark.entries)
	if c.entries < 0 {
		c.entries = 0
	}
}

// Bytes is how much disk the cache believes it is using.
func (c *Cache) Bytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}

// Entries is how many objects the cache believes it is holding, across
// every ecosystem. Stats says the same thing broken down per kind and
// exactly, by walking; this is the running count, which is what a
// caller polled every few seconds by every open browser wants.
func (c *Cache) Entries() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return int(c.entries)
}

// stored is one entry as the walk below sees it.
type stored struct {
	path    string
	size    int64
	modTime time.Time
	kind    string
}

// walk lists every entry in the cache. It counts a body and its .meta
// together, under the body's name, and ignores anything else it finds
// (a leftover .partial-* from a crashed write, a file a human dropped
// in) so that a directory with junk in it still reports sensibly.
func (c *Cache) walk() ([]stored, error) {
	var out []stored
	for _, kind := range Kinds() {
		root := filepath.Join(c.dir, kind)
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				// A directory that vanished under us is a concurrent
				// Flush, not a failure worth propagating.
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if info.IsDir() || strings.HasSuffix(path, ".meta") || strings.Contains(filepath.Base(path), ".partial-") {
				return nil
			}
			size := info.Size()
			if metaInfo, err := os.Stat(path + ".meta"); err == nil {
				size += metaInfo.Size()
			}
			out = append(out, stored{path: path, size: size, modTime: info.ModTime(), kind: kind})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("pkgcache: reading %s: %w", root, err)
		}
	}
	return out, nil
}

// evictionTarget is how far under the bound an eviction pass takes the
// cache. Evicting down to exactly the limit would mean evicting again on
// the next package downloaded, and again on the one after that; leaving
// a tenth of the cache free means a pass happens once per tenth of a
// cache filled.
const evictionTarget = 0.9

// evict removes the oldest entries until the cache is comfortably back
// under its bound.
//
// Oldest-first by modification time, which for a store nothing rewrites
// is "least recently fetched from upstream" rather than "least recently
// used". The difference matters less than it looks: what a cache like
// this holds is versioned artifacts, and an old entry is overwhelmingly
// one whose version nothing installs any more.
//
// Failures are ignored on purpose. Eviction is housekeeping, it runs
// while requests are being served, and every reason a removal fails
// (the file has already gone, a Flush is running) ends with the disk
// space this was after being free anyway.
//
// How far it goes is decided against the walk it has just done rather
// than against the running byte count, and c.mu is not held while it
// removes. Both are for the same reason: this runs in the hot path of
// every download that fills the cache, so it must neither throw away
// live entries to satisfy a number that has drifted above the truth,
// nor hold every concurrent commit and every Bytes() call -- the
// Settings pane's among them -- behind a removal loop that on a 20GiB
// cache is thousands of unlink syscalls long.
//
// Passes are exclusive of each other (c.evicting), which is not the
// same thing at all: two passes walking and removing at once each
// measure a store the other is emptying underneath it, and both then
// believe the cache holds more than it does. Serializing the passes
// costs nothing -- a second pass on the heels of the first has nothing
// left to do -- and it is what makes the measurement mean something.
func (c *Cache) evict() {
	c.evicting.Lock()
	defer c.evicting.Unlock()
	// Passes, not one pass: entries committed while this was walking are
	// on disk but not in what it measured, so the cache can still be
	// over its bound when the pass that was meant to bring it under
	// finishes. Looking again is how that is noticed rather than left
	// for whoever happens to commit next. The bound on the loop is
	// against a cache being filled faster than it can be emptied, where
	// there is nothing to be gained by holding one downloader in here
	// while the others wait: the next commit calls this again anyway.
	for pass := 0; pass < 4; pass++ {
		if !c.evictOnce() {
			return
		}
	}
}

// evictOnce is one eviction pass, and reports whether the cache is
// still over its bound afterwards.
func (c *Cache) evictOnce() (stillOver bool) {
	mark := c.mark()
	entries, err := c.walk()
	if err != nil {
		return false
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].modTime.Before(entries[j].modTime) })
	var remaining int64
	for _, e := range entries {
		remaining += e.size
	}
	remainingEntries := int64(len(entries))
	maxBytes := c.MaxBytes()
	target := int64(float64(maxBytes) * evictionTarget)
	for _, e := range entries {
		if remaining <= target {
			break
		}
		if err := os.Remove(e.path); err != nil && !os.IsNotExist(err) {
			// Still there, still occupying its bytes.
			continue
		}
		os.Remove(e.path + ".meta")
		remaining -= e.size
		remainingEntries--
	}
	c.resync(remaining, remainingEntries, mark)
	return c.Bytes() > maxBytes
}

// KindStats is what one ecosystem's half of the cache holds.
type KindStats struct {
	Kind    string `json:"kind"`
	Entries int    `json:"entries"`
	Bytes   int64  `json:"bytes"`
}

// Stats is what the cache holds right now, per ecosystem -- what the
// Settings pane shows next to its flush buttons, so that "flush" is a
// decision somebody can make about a number rather than about a word.
func (c *Cache) Stats() ([]KindStats, error) {
	mark := c.mark()
	entries, err := c.walk()
	if err != nil {
		return nil, err
	}
	byKind := map[string]*KindStats{}
	for _, kind := range Kinds() {
		byKind[kind] = &KindStats{Kind: kind}
	}
	var total int64
	for _, e := range entries {
		s := byKind[e.kind]
		s.Entries++
		s.Bytes += e.size
		total += e.size
	}
	// The walk has just measured the cache exactly, so believe it over
	// the running counts: this is where one that drifted (a file removed
	// by hand, an eviction that raced a flush) is corrected.
	c.resync(total, int64(len(entries)), mark)
	out := make([]KindStats, 0, len(byKind))
	for _, kind := range Kinds() {
		out = append(out, *byKind[kind])
	}
	return out, nil
}

// Flush empties the cache -- one ecosystem when kind names one, all of
// it when kind is empty -- and reports how many entries went and how
// many bytes that freed.
//
// It is safe to run at any time, including mid-run with a dozen
// sandboxes fetching through the cache. A request that was reading an
// entry when it was removed keeps reading the open file to the end
// (POSIX unlink semantics), a request that was about to open one gets a
// miss and fetches upstream, and an in-flight write commits into a
// directory that has just been emptied and is simply cached again. The
// one thing a flush must not do is make a package install fail, and
// nothing here can.
func (c *Cache) Flush(kind string) (entries int, freed int64, err error) {
	kinds := Kinds()
	if kind = strings.TrimSpace(kind); kind != "" {
		if !ValidKind(kind) {
			return 0, 0, fmt.Errorf("pkgcache: %q is not a package cache: this deployment caches %s",
				kind, strings.Join(Kinds(), ", "))
		}
		kinds = []string{kind}
	}
	mark := c.mark()
	all, err := c.walk()
	if err != nil {
		return 0, 0, err
	}
	wanted := map[string]bool{}
	for _, k := range kinds {
		wanted[k] = true
	}
	// What the kinds this flush is not touching hold, which is what the
	// cache is left holding when it is done. Measured rather than
	// subtracted from the running count, so that a flush cannot leave a
	// phantom balance behind -- "disk empty, count still positive" was
	// exactly what `c.bytes -= freed` did to a count that had drifted.
	var kept, keptEntries int64
	for _, e := range all {
		if !wanted[e.kind] {
			kept += e.size
			keptEntries++
			continue
		}
		if err := os.Remove(e.path); err != nil && !os.IsNotExist(err) {
			continue
		}
		os.Remove(e.path + ".meta")
		entries++
		freed += e.size
	}
	// Directories and abandoned temp files the loop above leaves behind:
	// removing the whole kind directory and recreating it is what makes
	// a flush actually empty, rather than leaving a fan-out tree of empty
	// directories and any .partial-* a crashed write left there.
	for _, k := range kinds {
		dir := filepath.Join(c.dir, k)
		if err := os.RemoveAll(dir); err != nil {
			return entries, freed, fmt.Errorf("pkgcache: emptying %s: %w", dir, err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return entries, freed, fmt.Errorf("pkgcache: recreating %s: %w", dir, err)
		}
	}
	// The directories this flushed have just been removed and recreated
	// empty, so what is left is what the other kinds held when they were
	// walked, plus anything committed since.
	c.resync(kept, keptEntries, mark)
	return entries, freed, nil
}

// copyBody streams from upstream to both the client and the cache entry
// being filled, and reports whether the whole body arrived. A truncated
// upstream response leaves nothing stored -- the client sees the short
// read and retries, and the retry must not be answered from a copy of
// the same truncated bytes.
func copyBody(dst io.Writer, entry ObjectWriter, src io.Reader, expected int64) (int64, bool) {
	var w io.Writer = dst
	if entry != nil {
		w = io.MultiWriter(dst, entry)
	}
	n, err := io.Copy(w, src)
	if err != nil {
		return n, false
	}
	if expected >= 0 && n != expected {
		return n, false
	}
	return n, true
}
