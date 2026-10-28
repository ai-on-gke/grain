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

package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"
)

// DefaultGrace is how long after its deadline a lease is kept before the
// server expires it itself (§4.3.5).
const DefaultGrace = 5 * time.Minute

// MethodSpec is a capability method written in Go: what NewMethod serves as
// mint_<Name> and, when Release is set, release_<Name>.
type MethodSpec struct {
	Name        string
	Description string
	// Args is the schema of the method's with: arguments (§4.1.3); nil
	// means the method takes none.
	Args map[string]any
	// Mint allocates for one attempt_id. It is called at most once per
	// attempt_id while that lease lives; repeats are answered from the
	// stored response. It may return a refused or resource_exhausted
	// response, which is not stored.
	Mint func(ctx context.Context, attemptID string, deadline time.Time, args map[string]any) (*MintResponse, error)
	// Release revokes everything Mint created for attemptID. Nil makes
	// the method stateless: no release_ tool is advertised.
	Release func(ctx context.Context, attemptID string) error
	// Audit, when set, is what release_<Name> returns about attemptID
	// (§4.4.4), called once Release has revoked it so that whatever a
	// call cut short by the release recorded is included: records oldest
	// first, each marshalled to a JSON object carrying a time, and how
	// many the method already left out. Records past the limits of
	// §4.4.4 are left out here too, and counted.
	Audit func(attemptID string) (records []any, omitted int)
	// Grace is how long past deadline a lease outlives it; zero is
	// DefaultGrace.
	Grace time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

type lease struct {
	args     map[string]any
	deadline time.Time
	resp     []byte
}

// Method is a served capability method: leases keyed by attempt_id,
// idempotent mints, renewals and expiry (§4.3.5, §4.4.2).
type Method struct {
	spec MethodSpec

	mu      sync.Mutex
	leases  map[string]*lease
	pending map[string]*sync.Mutex
	// released is each audited release's reply, kept for Grace so that a
	// release_ Grain repeats -- because it timed out on the first --
	// answers with the same records rather than none (§4.4.4).
	released map[string]*releasedReply
}

type releasedReply struct {
	resp []byte
	at   time.Time
}

// NewMethod serves spec. Its Name must be a conforming method name.
func NewMethod(spec MethodSpec) (*Method, error) {
	if !ValidMethod(spec.Name) {
		return nil, fmt.Errorf("method %q does not match %s", spec.Name, methodPattern)
	}
	if spec.Mint == nil {
		return nil, fmt.Errorf("method %s has no Mint", spec.Name)
	}
	if spec.Args != nil {
		// Normalized once here so the schema advertised and the one
		// handleMint enforces are the same JSON, however it was written.
		args, err := NormalizeArgsSchema(spec.Args)
		if err != nil {
			return nil, fmt.Errorf("method %s: args %v", spec.Name, err)
		}
		spec.Args = args
	}
	if spec.Grace == 0 {
		spec.Grace = DefaultGrace
	}
	if spec.Now == nil {
		spec.Now = time.Now
	}
	return &Method{spec: spec, leases: map[string]*lease{}, pending: map[string]*sync.Mutex{}, released: map[string]*releasedReply{}}, nil
}

// handler answers one tools/call with the result's JSON, or an error that
// is reported as isError.
type handler func(ctx context.Context, args map[string]any) ([]byte, error)

// servedTool is a Tool plus what answers it.
type servedTool struct {
	Tool
	handle handler
}

// tools is the method's mint_ tool, and its release_ tool when it is
// stateful.
func (m *Method) tools() []servedTool {
	args := m.spec.Args
	if args == nil {
		args = defaultArgsSchema
	}
	tools := []servedTool{{Tool: Tool{
		Name:        mintPrefix + m.spec.Name,
		Description: m.spec.Description,
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"attempt_id", "deadline", "args"},
			"properties": map[string]any{
				"attempt_id": map[string]any{"type": "string", "pattern": attemptIDPattern.String()},
				"deadline":   map[string]any{"type": "string", "format": "date-time"},
				"args":       args,
			},
		},
	}, handle: m.handleMint}}
	if m.spec.Release != nil {
		tools = append(tools, servedTool{Tool: Tool{
			Name:        releasePrefix + m.spec.Name,
			Description: "Release what " + mintPrefix + m.spec.Name + " minted for an attempt_id.",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"attempt_id"},
				"properties":           map[string]any{"attempt_id": map[string]any{"type": "string"}},
			},
		}, handle: m.handleRelease})
	}
	return tools
}

func refused(reason string) ([]byte, error) {
	return json.Marshal(MintResponse{Status: StatusRefused, Reason: reason})
}

var releasedOK = []byte(`{"status":"ok"}`)

// attemptLock serializes calls for one attempt_id.
func (m *Method) attemptLock(id string) func() {
	m.mu.Lock()
	l := m.pending[id]
	if l == nil {
		l = &sync.Mutex{}
		m.pending[id] = l
	}
	m.mu.Unlock()
	l.Lock()
	return l.Unlock
}

func (m *Method) handleMint(ctx context.Context, in map[string]any) ([]byte, error) {
	id, _ := in["attempt_id"].(string)
	if !ValidAttemptID(id) {
		return refused(fmt.Sprintf("attempt_id must match %s", attemptIDPattern))
	}
	ds, _ := in["deadline"].(string)
	deadline, err := time.Parse(time.RFC3339, ds)
	if err != nil {
		return refused("deadline must be an RFC 3339 date-time")
	}
	if !deadline.After(m.spec.Now()) {
		return refused("deadline has already passed")
	}
	rawArgs, ok := in["args"].(map[string]any)
	if !ok && in["args"] != nil {
		return refused("args must be an object")
	}
	// Re-validated here whatever Grain checked (§4.3.2).
	args, err := ValidateArgs(m.spec.Args, rawArgs)
	if err != nil {
		return refused(err.Error())
	}

	defer m.attemptLock(id)()
	m.mu.Lock()
	l := m.leases[id]
	m.mu.Unlock()
	if l != nil {
		if !reflect.DeepEqual(l.args, args) {
			return refused("attempt_id " + id + " was already minted with different args")
		}
		m.mu.Lock()
		if deadline.After(l.deadline) {
			l.deadline = deadline
		}
		resp := l.resp
		m.mu.Unlock()
		return resp, nil
	}

	resp, err := m.spec.Mint(ctx, id, deadline, args)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Status == "" {
		return nil, fmt.Errorf("mint_%s returned no status", m.spec.Name)
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return nil, err
	}
	if resp.Status == StatusOK {
		m.mu.Lock()
		m.leases[id] = &lease{args: args, deadline: deadline, resp: b}
		m.mu.Unlock()
	}
	return b, nil
}

func (m *Method) handleRelease(ctx context.Context, in map[string]any) ([]byte, error) {
	id, _ := in["attempt_id"].(string)
	if !ValidAttemptID(id) {
		// Unknown attempt_ids release successfully (§4.4.2); one that
		// cannot be an attempt_id was never minted.
		return releasedOK, nil
	}
	return m.release(ctx, id)
}

// release revokes id's lease and returns the release_ reply: its audit,
// when the method keeps one, or a repeat of the reply an earlier release
// of id gave.
func (m *Method) release(ctx context.Context, id string) ([]byte, error) {
	defer m.attemptLock(id)()
	m.mu.Lock()
	_, ok := m.leases[id]
	prior := m.released[id]
	m.mu.Unlock()
	if !ok {
		if prior != nil {
			return prior.resp, nil
		}
		return releasedOK, nil
	}
	if m.spec.Release != nil {
		if err := m.spec.Release(ctx, id); err != nil {
			return nil, err
		}
	}
	resp := releasedOK
	if m.spec.Audit != nil {
		resp = auditReply(m.spec.Audit(id))
	}
	m.mu.Lock()
	delete(m.leases, id)
	delete(m.pending, id)
	if m.spec.Audit != nil {
		m.released[id] = &releasedReply{resp: resp, at: m.spec.Now()}
	}
	m.mu.Unlock()
	return resp, nil
}

// auditReply is a release_ reply carrying records, as many of them as
// fit §4.4.4's limits in order, the rest counted in audit_omitted.
func auditReply(records []any, omitted int) []byte {
	reply := ReleaseResponse{Status: StatusOK, AuditOmitted: omitted}
	total := 0
	for _, rec := range records {
		b, err := json.Marshal(rec)
		if err != nil || len(b) > MaxAuditRecordBytes || len(reply.Audit) == MaxAuditRecords || total+len(b) > MaxAuditBytes {
			reply.AuditOmitted++
			continue
		}
		total += len(b)
		reply.Audit = append(reply.Audit, b)
	}
	b, err := json.Marshal(reply)
	if err != nil {
		return releasedOK
	}
	return b
}

// Expire releases every lease whose deadline plus grace has passed and
// returns the attempt_ids it released. Call it periodically (RunExpiry).
func (m *Method) Expire(ctx context.Context) []string {
	now := m.spec.Now()
	m.mu.Lock()
	for id, r := range m.released {
		if now.After(r.at.Add(m.spec.Grace)) {
			delete(m.released, id)
		}
	}
	var due []string
	for id, l := range m.leases {
		if now.After(l.deadline.Add(m.spec.Grace)) {
			due = append(due, id)
		}
	}
	m.mu.Unlock()
	var done []string
	for _, id := range due {
		if _, err := m.release(ctx, id); err == nil {
			done = append(done, id)
		}
	}
	return done
}

// ReleaseAll releases every lease, deadline or not, and returns the
// attempt_ids whose release failed. A server whose leases live only in its
// own memory calls it on shutdown: once it exits nothing could expire
// them.
func (m *Method) ReleaseAll(ctx context.Context) []string {
	m.mu.Lock()
	ids := make([]string, 0, len(m.leases))
	for id := range m.leases {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	var failed []string
	for _, id := range ids {
		if _, err := m.release(ctx, id); err != nil {
			failed = append(failed, id)
		}
	}
	return failed
}

// Name is the method's name, without the mint_/release_ prefix.
func (m *Method) Name() string { return m.spec.Name }

// RunExpiry calls Expire every interval until ctx is done.
func (m *Method) RunExpiry(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Expire(ctx)
		}
	}
}

// Leases is how many attempt_ids currently hold a lease.
func (m *Method) Leases() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.leases)
}
