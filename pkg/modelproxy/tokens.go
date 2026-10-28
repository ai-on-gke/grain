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

package modelproxy

// Who this proxy will answer for: one token per run, minted as that
// run's capability is materialized and dropped when its lease is
// revoked.
//
// The registry outlives the daemon that minted it. It used to be held in
// memory only, on the argument that a token is minted for a run that a
// restarted daemon has already lost (orchestrator.RecoverOrphanedRuns),
// so the registry's lifetime and a run's lifetime ended together anyway.
// That stops being true once a restarted daemon adopts the runs it finds
// still going (docs/non-disruptive-upgrade.md, piece 5): an adopted run
// authenticates with the token the previous daemon minted, so a registry
// that died with that daemon would refuse every model call the run makes
// for the rest of its life. A Tokens given a file (PersistTo) writes
// itself there on every mint and revoke, and reads it back at startup.
//
// What it writes is not a credential. A token is two halves,
// "<id>.<secret>"; the file holds the id, a SHA-256 of the secret, the
// owner and the expiry, which is everything Authenticate needs to check a
// presented token and nothing that can be presented as one -- plus the
// team connection the token forwards with, which is an id and no more. So unlike
// gitproxy's sandbox-tokens.json, which holds tokens it hands back out
// and has to be sealed, this file needs no key: the one usable copy of a
// token is still the file inside the sandbox itself. It is written 0600
// all the same, under the data directory's secrets/, and never into the
// store or the state repository.
//
// Only the id is ever recorded anywhere else: it is what a capability's
// Lease carries as its Resource, which is a row in grain's store and
// therefore a file in the state repository grain exports itself to
// (pkg/staterepo). A lease naming the token itself would publish a live
// credential to a git repository on the next sync; a lease naming its id
// revokes exactly as well and says nothing.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultLease bounds how long a minted token is honoured for, whatever
// happens to the run that minted it. Revocation is the ordinary end of
// one (Endpoint.Revoke, called from the capability's own Revoke as the
// run's slot frees); this is the backstop for a run that ends without one.
//
// It is also only the ceiling for a caller that cannot say better. One
// that knows when the minting run will be over whatever happens to it --
// every run grain dispatches does (model.CapabilityContext.Deadline) --
// mints through MintUntil instead, so a token nobody revoked is dead once
// its run could no longer be using it, not up to a day later.
//
// Nothing extends a token once minted: a run's token is held to that
// run's own deadline, which in a deployment is hours rather than a day,
// so there is no run left alive for a refresh to keep authenticating.
// A deployment whose MaxRunRuntime came near a day would need this, and
// the capability's own MaxLease, raised with it.
//
// There is no model.Reaper to go with it and no external resource to sweep:
// this registry (and the file it persists to) is the resource's only
// home, so an unrevoked token dies with this bound at the latest. Expired
// entries are dropped from the file the next time it is written or read.
const DefaultLease = 24 * time.Hour

// ErrNoProxy is what minting reports when there is no proxy to mint
// against at all -- a nil Endpoint, which is what a deployment that
// never started one has.
var ErrNoProxy = errors.New("modelproxy: this deployment is not running a model proxy")

// Tokens is the registry: which tokens this proxy will answer for right
// now, and whose each one is.
//
// Every method is safe on a nil *Tokens, which reads as an empty
// registry -- it mints nothing and authenticates nobody, so a handler
// wired up without one refuses every request rather than serving the
// deployment's credential to whoever asks.
type Tokens struct {
	// Lease overrides DefaultLease. Zero means DefaultLease.
	Lease time.Duration
	// Now is the clock, for tests. nil means time.Now.
	Now func() time.Time

	mu   sync.RWMutex
	byID map[string]record
	// path is the file the registry persists to, empty for one held in
	// memory only. Set by PersistTo.
	path string
}

// record is one live token: a hash of the half of it that is a secret,
// who it was minted for, the team connection it forwards with, and when
// it stops being honoured.
//
// until is the hard stop MintUntil was given, zero where it was given
// none: expires never moves past it.
type record struct {
	secretHash [sha256.Size]byte
	owner      string
	connection Connection
	expires    time.Time
	until      time.Time
}

// hashSecret is what a record keeps of a token's secret half.
func hashSecret(secret string) [sha256.Size]byte { return sha256.Sum256([]byte(secret)) }

// expiry is when a token whose lease starts at now stops being honoured:
// a lease out, or until if that comes first.
func (r record) expiry(now time.Time, lease time.Duration) time.Time {
	expires := now.Add(lease)
	if !r.until.IsZero() && r.until.Before(expires) {
		return r.until
	}
	return expires
}

// NewTokens returns an empty registry, ready to mint from and
// authenticate against at the same time.
func NewTokens() *Tokens { return &Tokens{byID: map[string]record{}} }

func (t *Tokens) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Tokens) lease() time.Duration {
	if t.Lease > 0 {
		return t.Lease
	}
	return DefaultLease
}

// Mint records a fresh token for owner -- a run id, in a deployment --
// forwarding with connection, and returns both the id to revoke it by
// and the token itself, which is the only time the token exists outside
// the sandbox it is placed in.
//
// connection is decided here, once, rather than per request: it is the
// team connection the run's own agent resolved to, or its team's other
// connection of this vendor (Endpoint.ConnectionFor), and a run keeps
// spending the one it was minted with for as long as it lasts. A zero
// connection is a run whose team has none for this vendor; the token
// still authenticates, and every request it makes is refused naming
// that (Proxy.ServeHTTP), rather than forwarded with anybody else's key.
//
// Expired entries are dropped on the way through, so a long-lived
// process that minted for runs nobody revoked does not accumulate them
// indefinitely. There is no other sweep: minting is the only thing that
// grows this map.
//
// The token is honoured for the registry's lease (DefaultLease), which is
// a guess made without knowing anything about owner. A caller that knows
// when owner will be over mints through MintUntil.
func (t *Tokens) Mint(owner string, connection Connection) (id, token string, err error) {
	return t.MintUntil(owner, connection, time.Time{})
}

// MintUntil is Mint for an owner whose end is known: the token is never
// honoured past until, so a copy
// that outlives its run -- because the Revoke that should have ended it
// never came -- is worthless once that run could no longer be alive,
// rather than for the rest of a flat lease. A zero until is no bound
// beyond the lease, which is exactly Mint.
//
// An until already past still mints, a token that authenticates nobody:
// the run asking for it is out of time, and it is the run's own deadline
// that says so rather than a refusal here.
func (t *Tokens) MintUntil(owner string, connection Connection, until time.Time) (id, token string, err error) {
	if t == nil {
		return "", "", ErrNoProxy
	}
	id, err = randomHex(8)
	if err != nil {
		return "", "", fmt.Errorf("modelproxy: minting a token id: %w", err)
	}
	secret, err := randomHex(32)
	if err != nil {
		return "", "", fmt.Errorf("modelproxy: minting a token: %w", err)
	}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.byID == nil {
		t.byID = map[string]record{}
	}
	for existing, rec := range t.byID {
		if now.After(rec.expires) {
			delete(t.byID, existing)
		}
	}
	rec := record{secretHash: hashSecret(secret), owner: owner, connection: connection.normalized(), until: until}
	rec.expires = rec.expiry(now, t.lease())
	t.byID[id] = rec
	if err := t.saveLocked(); err != nil {
		delete(t.byID, id)
		return "", "", err
	}
	return id, id + "." + secret, nil
}

// Revoke drops the token id names. Safe to call for an id that holds
// none: a run that failed before it minted one has nothing to revoke,
// and revoking twice is what a retried cleanup does.
func (t *Tokens) Revoke(id string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.byID[id]; !ok {
		return
	}
	delete(t.byID, id)
	// Best effort: a revoke that could not reach the disk still holds in
	// this process, and the token's own expiry bounds what a restart
	// would bring back.
	_ = t.saveLocked()
}

// Authenticate answers who token belongs to, and false where it belongs
// to nobody -- unknown, revoked, expired, or the right id with the wrong
// secret. Lookup is the same answer with the token's connection.
func (t *Tokens) Authenticate(token string) (owner string, ok bool) {
	owner, _, ok = t.Lookup(token)
	return owner, ok
}

// Lookup answers who token belongs to and the connection it was minted
// to forward with (Mint), and false where it belongs to nobody.
//
// The secret's hash is compared in constant time. The id half is a map
// lookup and is not, which is the same trade every id-plus-secret
// credential makes: the id is a handle this proxy hands out and a lease
// row records in plain text, and only the secret is material.
func (t *Tokens) Lookup(token string) (owner string, connection Connection, ok bool) {
	if t == nil {
		return "", Connection{}, false
	}
	id, secret, found := strings.Cut(token, ".")
	if !found || id == "" || secret == "" {
		return "", Connection{}, false
	}
	t.mu.RLock()
	rec, known := t.byID[id]
	t.mu.RUnlock()
	if !known {
		return "", Connection{}, false
	}
	if t.now().After(rec.expires) {
		return "", Connection{}, false
	}
	presented := hashSecret(secret)
	if subtle.ConstantTimeCompare(rec.secretHash[:], presented[:]) != 1 {
		return "", Connection{}, false
	}
	return rec.owner, rec.connection, true
}

// Len is how many tokens are recorded right now, expired ones included
// until the next Mint sweeps them -- for a test, and for anything that
// wants to say how many runs hold one.
func (t *Tokens) Len() int {
	if t == nil {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.byID)
}

// Owners is every recorded token's id and the owner it was minted for,
// as a copy: what a restarted daemon reads to revoke the tokens of runs
// its startup recovery has just finished (daemon.revokeFinishedRunTokens).
func (t *Tokens) Owners() map[string]string {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[string]string, len(t.byID))
	for id, rec := range t.byID {
		out[id] = rec.owner
	}
	return out
}

// storedToken is one record as the persisted file holds it.
type storedToken struct {
	ID         string `json:"id"`
	SecretHash string `json:"secret_sha256"`
	Owner      string `json:"owner"`
	// Connection is the token's Connection. A file written by a build
	// from before tokens carried one has none, and its tokens are
	// refused as having no connection. A `team` a file written before
	// stage 4c of the single-tenant reset carries is ignored.
	Connection string    `json:"connection,omitempty"`
	Expires    time.Time `json:"expires"`
	Until      time.Time `json:"until,omitzero"`
}

// tokenFileMode is the persisted file's mode: what it holds cannot be
// presented as a token, but it names which runs hold one and is nobody
// else's business.
const tokenFileMode = 0o600

// PersistTo makes path the registry's file: whatever unexpired tokens it
// already holds -- the ones a previous daemon minted for runs this one
// may adopt -- are loaded into the registry, and every later Mint and
// Revoke rewrites it. A missing file is an empty registry. A file that
// will not parse is reported and left where it is, so that the operator
// can look at it, and the registry starts empty: its runs' model calls
// are refused, which is what they would have been before tokens were
// persisted at all.
func (t *Tokens) PersistTo(path string) error {
	if t == nil {
		return ErrNoProxy
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.byID == nil {
		t.byID = map[string]record{}
	}
	loaded, err := readTokenFile(path, t.now())
	if err != nil {
		return err
	}
	for id, rec := range loaded {
		if _, ok := t.byID[id]; !ok {
			t.byID[id] = rec
		}
	}
	t.path = path
	return t.saveLocked()
}

func readTokenFile(path string, now time.Time) (map[string]record, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("modelproxy: reading %s: %w", path, err)
	}
	var stored []storedToken
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("modelproxy: reading %s: %w", path, err)
	}
	out := make(map[string]record, len(stored))
	for _, st := range stored {
		raw, err := hex.DecodeString(st.SecretHash)
		if err != nil || len(raw) != sha256.Size || st.ID == "" {
			continue
		}
		rec := record{owner: st.Owner, connection: Connection{ID: st.Connection}.normalized(), expires: st.Expires, until: st.Until}
		copy(rec.secretHash[:], raw)
		if now.After(rec.expires) {
			continue
		}
		out[st.ID] = rec
	}
	return out, nil
}

// saveLocked writes the registry to its file, when it has one: written
// aside with tokenFileMode and renamed into place, so a daemon killed
// mid-write leaves the previous file rather than half of one. Expired
// records are left out. t.mu must be held.
func (t *Tokens) saveLocked() error {
	if t.path == "" {
		return nil
	}
	now := t.now()
	stored := make([]storedToken, 0, len(t.byID))
	for id, rec := range t.byID {
		if now.After(rec.expires) {
			continue
		}
		stored = append(stored, storedToken{
			ID:         id,
			SecretHash: hex.EncodeToString(rec.secretHash[:]),
			Owner:      rec.owner,
			Connection: rec.connection.ID,
			Expires:    rec.expires.UTC(),
			Until:      rec.until.UTC(),
		})
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return fmt.Errorf("modelproxy: encoding tokens: %w", err)
	}
	dir := filepath.Dir(t.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("modelproxy: writing %s: %w", t.path, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(t.path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("modelproxy: writing %s: %w", t.path, err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(tokenFileMode); err != nil {
		tmp.Close()
		return fmt.Errorf("modelproxy: writing %s: %w", t.path, err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("modelproxy: writing %s: %w", t.path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("modelproxy: writing %s: %w", t.path, err)
	}
	if err := os.Rename(tmp.Name(), t.path); err != nil {
		return fmt.Errorf("modelproxy: writing %s: %w", t.path, err)
	}
	return nil
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
