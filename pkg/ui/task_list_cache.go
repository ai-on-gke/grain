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

package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
)

// What GET /api/tasks does when it is asked the same question again.
//
// This route is what every open tab polls, every three seconds, in every
// view (ui/src/App.jsx's POLL_INTERVAL_MS). On a store with real history
// that is 1.7 MB of JSON built out of a scan of most of the database --
// 197ms to 3.0s of it, median around a second -- and nearly every one of
// those answers is identical to the one three seconds earlier
// (the 2026-09-07 panel-dashboard hand run, section 5, which
// measured it and left this as the thing it could not fix from the
// dashboard's end). A deployment three months older pays five times as
// much for the same nothing.
//
// So the tick that has nothing to say is made to cost nothing to say it,
// at both ends of the wire and in three separate places:
//
//   - **the store is not read.** The response is keyed on
//     model.Store.ChangeToken, which answers "has anything at all been
//     committed?" out of SQLite's own header counter in microseconds. An
//     unchanged token means the bytes already built are still the right
//     bytes.
//   - **the JSON is not rebuilt.** The marshalled body is what is
//     cached, so a cache hit is a memcpy rather than an encode of nine
//     hundred tasks.
//   - **the bytes are not sent.** Every response carries an ETag over
//     the body, so a caller that sends If-None-Match gets a 304 with no
//     body at all -- which is also what lets the browser skip parsing
//     and comparing a megabyte of JSON it already has
//     (ui/src/poll.js).
//
// Measured on a store of 929 tasks of about the length this deployment's
// really are, in the sandbox this was written in on 2026-09-08: a poll
// that had to build cost 272ms and 2,170,035 bytes; the same poll
// answered out of the cache cost 53us and no body at all. The tick that
// did change costs what it always did.
//
// # Why this cannot serve a stale answer
//
// Server's own doc comment used to be able to say this package "caches
// nothing, so there is nowhere here for a stale value to hide", and that
// was worth something. What replaces it is an argument in three parts,
// each of which has to hold:
//
//  1. **The answer is a pure function of committed database state.**
//     Client.ListTasks reads rows and projects them (taskFrom takes no
//     clock, and the states it reads come from the task_state view,
//     which is a derivation over stored columns rather than a comparison
//     against now). There is no "and the time is now 14:03" in this
//     response for a cache to freeze.
//  2. **The token moves for every commit, from any process.** That is
//     ChangeToken's own contract and its own tests -- including the trap
//     that the daemon writing and the UI reading share one pool.
//  3. **The token is read *before* the response is built, never
//     after.** A write that lands while a build is in flight therefore
//     invalidates the entry that build produces, rather than being
//     swallowed by it. Read afterwards, the same race would cache a body
//     under the version that superseded it and serve it forever, which
//     is the one way this could have gone wrong.
//
// Anything the store cannot answer for -- an error from ChangeToken, an
// engine with no such pragma -- disables the cache for that request and
// falls back to building the response, which is exactly what this route
// did before. The ETag is still computed and still honoured in that
// case: it is derived from the body rather than from any version, so it
// keeps working when nothing else here does, across a restart included.
//
// # Why the build is not serialised
//
// Two tabs polling at once can both miss and both build, and neither
// takes a lock the other waits on. Serialising them would save one build
// in that moment and cost every caller of this route a queue behind
// whatever the slowest one is doing; the concurrent-build case is rare
// (a poll is three seconds apart and this is not) and its cost is
// exactly today's cost. Two builds racing to store their result is safe
// in either order: each entry carries the token it was built at, so the
// loser's entry is simply missed by the next request and rebuilt, and a
// stale body can never be read as a fresh one.
type taskListCache struct {
	mu    sync.Mutex
	token string // the ChangeToken everything below was built at
	// tasks is the whole list the body below was encoded from.
	tasks []Task
	// bodies holds the answers built at token: the whole list under
	// taskListKey, and each ?repo= narrowing of it under taskListRepoKey.
	bodies map[string]taskListBody

	// builds counts the times this went to the store, for the test that
	// pins that a quiet tick does not (export_test.go).
	builds atomic.Int64
}

type taskListBody struct {
	body []byte
	etag string
}

// taskListKey is the key the whole list is kept under.
const taskListKey = "*"

// taskListRepoKey is the key the list narrowed to one repository is kept
// under. Repository names compare case-insensitively, as an agent's
// repository grant does (model.APIGrantSet.Allows).
func taskListRepoKey(repo string) string {
	return "repo:" + strings.ToLower(repo)
}

// response is the body and ETag for one GET /api/tasks,
// from the cache when the store says nothing has been committed since it
// was built and from the store otherwise. A non-empty repo narrows the
// answer to that repository's tasks: the agent API gate admits a
// repository-scoped caller on the strength of the ?repo= it names
// (agentCallRepo, externalAgentCallRepo), so the list must hold no task
// outside it.
func (c *taskListCache) response(ctx context.Context, client *Client, repo string) ([]byte, string, error) {
	// Before the build, never after -- see the type's own doc comment for
	// what reading it afterwards would cost.
	token := ""
	if client.Store != nil {
		if t, err := client.Store.ChangeToken(ctx); err == nil {
			token = t
		}
		// An error is deliberately not reported to the caller: this is a
		// cheap optimisation over an answer the store can still give
		// perfectly well, and a request has no business failing because
		// an optimisation could not be applied. It is not logged either,
		// because it would be logged on every poll of every tab for as
		// long as the condition lasted.
	}
	key := taskListKey
	if repo != "" {
		key = taskListRepoKey(repo)
	}
	var tasks []Task
	if token != "" {
		var body []byte
		var etag string
		var ok bool
		if body, etag, tasks, ok = c.hit(token, key); ok {
			return body, etag, nil
		}
	}
	if tasks == nil {
		c.builds.Add(1)
		all, err := client.ListTasks(ctx)
		if err != nil {
			return nil, "", err
		}
		tasks = all
	}
	visible := tasks
	if repo != "" {
		visible = nil
		for _, t := range tasks {
			if strings.EqualFold(t.Repo, repo) {
				visible = append(visible, t)
			}
		}
	}
	// An empty list is [], never null: the UI maps over the answer, and a
	// deployment with no tasks in it yet would otherwise fail to draw at
	// all.
	if visible == nil {
		visible = []Task{}
	}
	body, err := json.Marshal(visible)
	if err != nil {
		return nil, "", err
	}
	etag := etagOf(body)
	if token != "" {
		c.mu.Lock()
		if c.token != token || c.bodies == nil {
			if c.token != token {
				c.token, c.tasks = token, tasks
			}
			c.bodies = map[string]taskListBody{}
		}
		c.bodies[key] = taskListBody{body: body, etag: etag}
		c.mu.Unlock()
	}
	return body, etag, nil
}

// hit is the cached body for key at token, or -- when only the list
// itself is still current -- that list, for the caller to cut its own
// body from.
func (c *taskListCache) hit(token, key string) ([]byte, string, []Task, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == "" || c.token != token {
		return nil, "", nil, false
	}
	if b, ok := c.bodies[key]; ok {
		return b.body, b.etag, nil, true
	}
	return nil, "", c.tasks, false
}

// etagOf is a strong validator over the exact bytes served: the same
// list is the same ETag, whoever built it and whenever. That is what
// makes a restarted daemon still answer 304 to a tab that has been open
// across it -- a version counter, which is the other way this could have
// been done, would have started again from somewhere else and sent every
// open tab a megabyte it already had.
//
// Half a SHA-256 rather than all of it: 128 bits is far past the point
// where a collision between two task lists is a thing anybody could
// arrange, and the header is sent on every poll.
func etagOf(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// etagMatches reports whether an If-None-Match header names this ETag.
//
// The header is a comma-separated list, each entry optionally weak
// ("W/"). Weakness is dropped on both sides before comparing, which is
// what RFC 9110 calls the weak comparison and is the right one for a
// conditional GET: a caller that only wants to know whether the bytes
// say the same thing does not care whether the server considers them
// byte-identical or merely equivalent. "*" matches anything the server
// has.
func etagMatches(header, etag string) bool {
	if header == "" || etag == "" {
		return false
	}
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		if strings.TrimPrefix(candidate, "W/") == strings.TrimPrefix(etag, "W/") {
			return true
		}
	}
	return false
}
