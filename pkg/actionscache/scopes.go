package actionscache

// Who a cache URL belongs to.
//
// The cache is one service holding every repository's entries, reached
// on the address every sandbox can already reach this deployment at --
// so something has to decide which repository the sandbox asking is
// asking *for*. That something is the token in the URL's first path
// segment, and this is the registry behind it: minted as a CI job's
// sandbox is built, registered against the one repository that job is
// testing, and revoked when the sandbox is released.
//
// It is gitproxy.ReadScopes' idea applied to a second service, down to
// the properties that make a second source of authority safe:
//
//   - one token names one repository. Granting the same token again
//     replaces what it named rather than adding to it.
//   - a token nobody has granted names nothing, so an unknown, stale or
//     stolen token is refused rather than being served somebody's
//     entries.
//   - a token names nothing once its job is released, because the
//     registration is gone. A token read out of the file it was written
//     to, or out of a log, is worth exactly as much as a finished job.
//
// A token names more than a repository, though: it names the branch the
// job is building and whether the job is a trusted release run, because
// that is what decides which of the repository's entries it may read
// and which it may write (Scope, and "Who may read it" in the package
// doc). Both are decided by grain when the sandbox is built, from the
// run it is building for, and nothing a job sends can change them.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
)

// Scope is what one job's token stands for: whose entries, written from
// which branch, by a run of which trust.
type Scope struct {
	// Repo is "owner/name". Nothing in one repository is ever visible
	// to another.
	Repo string
	// Ref is the branch the job is building, as the run names it
	// ("main", "grain/task-12"; a leading refs/heads/ is dropped). An
	// entry is written under its job's ref, and a job reads its own
	// ref's entries ahead of anything else.
	Ref string
	// Trusted is set for a release run and for nothing else: the one
	// event grain starts itself, after a merge or at an operator's
	// request, and the one a caller cannot ask for (ci.Request's
	// TrustedRelease). A trusted job reads only what trusted jobs
	// wrote, so nothing an ordinary branch's CI uploads can ever be
	// restored into a release.
	Trusted bool
}

// normalize trims what a caller may have padded and drops the
// refs/heads/ prefix, so that "main" and "refs/heads/main" are one ref.
func (s Scope) normalize() Scope {
	s.Repo = strings.TrimSpace(s.Repo)
	s.Ref = strings.TrimPrefix(strings.TrimSpace(s.Ref), "refs/heads/")
	return s
}

// valid is what a grant and every cache call need: a repository and a
// ref. A scope missing either names no entries rather than all of them.
func (s Scope) valid() bool { return s.Repo != "" && s.Ref != "" }

// writes answers whether an entry belongs to exactly this scope -- the
// test for adding to an upload in progress, which only the job that
// reserved it (or another job of the same ref and trust) may do.
func (s Scope) writes(e *Entry) bool {
	return e.Repo == s.Repo && e.Ref == s.Ref && e.Trusted == s.Trusted
}

// reads answers whether a job of this scope may restore e at all, and
// how much it prefers it: 2 for its own ref's entries, 1 for one it may
// fall back on, 0 for one it may not have.
//
// An ordinary job falls back on what trusted release runs wrote -- the
// GitHub rule that a branch may read its base branch's cache -- and on
// nothing any other branch wrote. A trusted job reads trusted entries
// and nothing else, of its own ref first.
func (s Scope) reads(e *Entry) int {
	switch {
	case e.Repo != s.Repo:
		return 0
	case s.Trusted && !e.Trusted:
		return 0
	case e.Ref == s.Ref && e.Trusted == s.Trusted:
		return 2
	case e.Trusted:
		return 1
	default:
		return 0
	}
}

// Scopes is the registry: which token may read and write which
// repository's entries, under which Scope, as it stands right now.
//
// Every method is safe on a nil *Scopes, which reads as an empty
// registry -- a server wired up without one serves nobody, which is the
// fail-closed direction.
type Scopes struct {
	mu     sync.RWMutex
	scopes map[string]Scope
}

// NewScopes returns an empty registry, ready for the CI service to
// grant into and the server to read from at the same time.
func NewScopes() *Scopes { return &Scopes{scopes: map[string]Scope{}} }

// MintToken returns a fresh token: 32 hex characters out of the
// system's own randomness, which is what act's cache server uses for
// the same path segment and is far more than the guessing anybody could
// do against a service only sandboxes can reach.
func MintToken() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("actionscache: minting a cache token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// Grant records that token stands for scope, and for nothing else. A
// scope with no repository or no ref is not recorded at all.
func (s *Scopes) Grant(token string, scope Scope) {
	if s == nil {
		return
	}
	token, scope = strings.TrimSpace(token), scope.normalize()
	if token == "" || !scope.valid() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scopes == nil {
		s.scopes = map[string]Scope{}
	}
	s.scopes[token] = scope
}

// Revoke drops a token. Called when the job's sandbox is released,
// whether the job passed, failed or never started a step.
func (s *Scopes) Revoke(token string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.scopes, strings.TrimSpace(token))
}

// Scope answers what a token stands for, and whether it stands for
// anything at all.
func (s *Scopes) Scope(token string) (Scope, bool) {
	if s == nil {
		return Scope{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	scope, ok := s.scopes[strings.TrimSpace(token)]
	return scope, ok
}

// Repo answers which repository a token stands for, and whether it
// stands for one at all.
func (s *Scopes) Repo(token string) (string, bool) {
	scope, ok := s.Scope(token)
	return scope.Repo, ok
}

// Granted is how many tokens are live, for a log line and for a test
// that wants to prove a release let go of one.
func (s *Scopes) Granted() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.scopes)
}
