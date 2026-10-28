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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Minter mints and releases grants of one capability server; *Server is
// one.
type Minter interface {
	Mint(ctx context.Context, method, attemptID string, deadline time.Time, args map[string]any) (*MintResponse, error)
	Release(ctx context.Context, method, attemptID string) (*ReleaseResponse, error)
}

// MinterFor returns the minter serving a capability_id.
type MinterFor func(capabilityID string) (Minter, error)

// PassOptions identify the attempt a pass mints for.
type PassOptions struct {
	Deployment string
	Task       string
	Attempt    int
	// Deadline is the latest instant the attempt may still be running,
	// every extension and grace period included (§4.2.3 item 1).
	Deadline time.Time
	// ReservedSandboxPorts are guest loopback ports Grain's own agent
	// uses; a grant mapping one is refused (§5.3.1).
	ReservedSandboxPorts map[int]bool
	// Now is the clock RetryAt is read against; nil is time.Now.
	Now func() time.Time
}

// MintedGrant is one grant of a pass with its attempt_id and response.
type MintedGrant struct {
	Grant
	AttemptID string
	Response  *MintResponse
}

// Pass is every grant of one attempt, minted, validated and checked against
// one another: ready to materialize (§5). It must be released when the
// attempt ends (Release), and it holds leased secrets: never log, persist
// or export it (§3.2.5).
type Pass struct {
	Grants  []MintedGrant
	minters MinterFor
}

// RefusedError is a grant (or the pass as a whole) that cannot be
// satisfied: the run is refused and Reason posted as untrusted text.
type RefusedError struct {
	Grant  string
	Reason string
}

func (e *RefusedError) Error() string {
	if e.Grant == "" {
		return "capability grants refused: " + e.Reason
	}
	return fmt.Sprintf("capability %s refused: %s", e.Grant, e.Reason)
}

// ExhaustedError is a grant whose upstream is at capacity: the run stays
// queued until RetryAt, or Grain's default backoff when it is zero.
type ExhaustedError struct {
	Grant   string
	RetryAt time.Time
}

func (e *ExhaustedError) Error() string {
	return fmt.Sprintf("capability %s is at capacity", e.Grant)
}

// TransientError is a failed mint call (§4.2.4): the run is requeued with
// backoff.
type TransientError struct {
	Grant string
	Err   error
}

func (e *TransientError) Error() string {
	return fmt.Sprintf("capability %s: %v", e.Grant, e.Err)
}

func (e *TransientError) Unwrap() error { return e.Err }

// MintPass mints every grant in index order (§4.2.3). If any is refused, at
// capacity or fails, or the responses conflict with one another, every
// grant of the pass whose method has release_ -- the failing one included
// -- is released before the error is returned, and no sandbox may boot.
func MintPass(ctx context.Context, grants []Grant, minters MinterFor, opts PassOptions) (*Pass, error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	p := &Pass{minters: minters}
	fail := func(err error) (*Pass, error) {
		p.Release(context.WithoutCancel(ctx))
		return nil, err
	}
	for i, g := range grants {
		id, err := AttemptID(opts.Deployment, opts.Task, opts.Attempt, i)
		if err != nil {
			return fail(err)
		}
		m, err := minters(g.Ref.CapabilityID)
		if err != nil {
			return fail(&RefusedError{Grant: g.Ref.String(), Reason: err.Error()})
		}
		// Recorded before the call, so the rollback releases a grant
		// whose mint failed part-way.
		p.Grants = append(p.Grants, MintedGrant{Grant: g, AttemptID: id})
		resp, err := m.Mint(ctx, g.Ref.Method, id, opts.Deadline, g.Args)
		if err != nil {
			return fail(&TransientError{Grant: g.Ref.String(), Err: err})
		}
		switch resp.Status {
		case StatusRefused:
			return fail(&RefusedError{Grant: g.Ref.String(), Reason: resp.Reason})
		case StatusResourceExhausted:
			return fail(&ExhaustedError{Grant: g.Ref.String(), RetryAt: resp.RetryAt(now())})
		}
		for _, port := range resp.Ports {
			if opts.ReservedSandboxPorts[port.SandboxPort] {
				return fail(&RefusedError{Grant: g.Ref.String(), Reason: fmt.Sprintf("sandbox_port %d is reserved by grain", port.SandboxPort)})
			}
		}
		p.Grants[i].Response = resp
	}
	if err := p.checkConflicts(); err != nil {
		return fail(err)
	}
	return p, nil
}

// checkConflicts applies the cross-grant rules: at most one setup_root
// (§5.5.2), no two placements of one path that differ (§5.1.2), and no
// sandbox_port mapped to two host_ports (§5.3.1).
func (p *Pass) checkConflicts() error {
	var roots []string
	placed := map[string]struct {
		grant string
		p     Placement
		data  []byte
	}{}
	ports := map[int]struct {
		grant string
		host  int
	}{}
	for _, g := range p.Grants {
		name := fmt.Sprintf("%s (%s)", g.Ref, g.AttemptID)
		r := g.Response
		if r.SetupRoot != "" {
			roots = append(roots, name)
		}
		for _, pl := range r.Placements {
			data, _ := pl.Decoded()
			if have, ok := placed[pl.Path]; ok {
				if have.p.Mode != pl.Mode || have.p.Owner != pl.Owner || !bytes.Equal(have.data, data) {
					return &RefusedError{Reason: fmt.Sprintf("placement collision at %s between %s and %s", pl.Path, have.grant, name)}
				}
				continue
			}
			placed[pl.Path] = struct {
				grant string
				p     Placement
				data  []byte
			}{name, pl, data}
		}
		for _, port := range r.Ports {
			if have, ok := ports[port.SandboxPort]; ok && have.host != port.HostPort {
				return &RefusedError{Reason: fmt.Sprintf("sandbox_port %d is mapped to host_port %d by %s and to %d by %s", port.SandboxPort, have.host, have.grant, port.HostPort, name)}
			}
			ports[port.SandboxPort] = struct {
				grant string
				host  int
			}{name, port.HostPort}
		}
	}
	if len(roots) > 1 {
		return &RefusedError{Reason: "more than one grant initializes the root working directory (setup_root): " + strings.Join(roots, ", ")}
	}
	return nil
}

// GrantAudit is the audit one grant's release returned (§4.4.4): the
// grant, its attempt_id, and the records, oldest first. Refused is why
// Grain refused what the server sent, when it did.
type GrantAudit struct {
	Grant     string
	AttemptID string
	Records   []json.RawMessage
	Omitted   int
	Refused   string
}

// Release calls release_<method> for every grant of the pass whose method
// advertises it (§4.4.1), retrying each a few times with backoff
// (§4.4.2), and returns every audit a release returned (§4.4.4) and
// every error that remained.
func (p *Pass) Release(ctx context.Context) ([]GrantAudit, error) {
	var errs []error
	var audits []GrantAudit
	for _, g := range p.Grants {
		if !g.Releasable {
			continue
		}
		m, err := p.minters(g.Ref.CapabilityID)
		if err != nil {
			errs = append(errs, fmt.Errorf("releasing %s (%s): %w", g.Ref, g.AttemptID, err))
			continue
		}
		var last error
		var resp *ReleaseResponse
		for try, wait := 0, 200*time.Millisecond; try < 3; try, wait = try+1, wait*4 {
			if resp, last = m.Release(ctx, g.Ref.Method, g.AttemptID); last == nil {
				break
			}
			if try < 2 {
				select {
				case <-time.After(wait):
				case <-ctx.Done():
					try = 3
				}
			}
		}
		if last != nil {
			errs = append(errs, fmt.Errorf("releasing %s (%s): %w", g.Ref, g.AttemptID, last))
			continue
		}
		if resp != nil && (len(resp.Audit) > 0 || resp.AuditOmitted > 0 || resp.AuditRefused != "") {
			audits = append(audits, GrantAudit{
				Grant: g.Ref.String(), AttemptID: g.AttemptID,
				Records: resp.Audit, Omitted: resp.AuditOmitted, Refused: resp.AuditRefused,
			})
		}
	}
	return audits, errors.Join(errs...)
}

// Renew re-mints every grant with a later deadline (§4.3.5), reading only
// each reply's status. A grant that is not renewed must not be relied on
// past its current deadline.
func (p *Pass) Renew(ctx context.Context, deadline time.Time) error {
	for _, g := range p.Grants {
		m, err := p.minters(g.Ref.CapabilityID)
		if err != nil {
			return err
		}
		resp, err := m.Mint(ctx, g.Ref.Method, g.AttemptID, deadline, g.Args)
		if err != nil {
			return &TransientError{Grant: g.Ref.String(), Err: err}
		}
		if resp.Status != StatusOK {
			return &RefusedError{Grant: g.Ref.String(), Reason: "renewal answered " + resp.Status + ": " + resp.Reason}
		}
	}
	return nil
}

// Placements is every placement of the pass, in grant order, with
// identical placements from several grants written once (§5.1.2).
func (p *Pass) Placements() []Placement {
	var out []Placement
	seen := map[string]bool{}
	for _, g := range p.Grants {
		for _, pl := range g.Response.Placements {
			if !seen[pl.Path] {
				seen[pl.Path] = true
				out = append(out, pl)
			}
		}
	}
	return out
}

// Ports is every forward of the pass, identical mappings established once.
func (p *Pass) Ports() []Port {
	var out []Port
	seen := map[int]bool{}
	for _, g := range p.Grants {
		for _, port := range g.Response.Ports {
			if !seen[port.SandboxPort] {
				seen[port.SandboxPort] = true
				out = append(out, port)
			}
		}
	}
	return out
}

// SetupCommand is one command to run in the sandbox before the agent
// starts (§5.5).
type SetupCommand struct {
	Grant     string
	AttemptID string
	Path      string
	Root      bool
}

// SetupCommands is the order setup runs in (§5.5.3): the one setup_root,
// then each grant's setup in index order.
func (p *Pass) SetupCommands() []SetupCommand {
	var out []SetupCommand
	for _, g := range p.Grants {
		if g.Response.SetupRoot != "" {
			out = append(out, SetupCommand{Grant: g.Ref.String(), AttemptID: g.AttemptID, Path: g.Response.SetupRoot, Root: true})
		}
	}
	for _, g := range p.Grants {
		if g.Response.Setup != "" {
			out = append(out, SetupCommand{Grant: g.Ref.String(), AttemptID: g.AttemptID, Path: g.Response.Setup})
		}
	}
	return out
}

// AssignServerNames gives each granted MCP endpoint its final name
// (§5.2.3): reported holds serverInfo.name per grant index ("" for a grant
// with no endpoint). Each must match the method grammar and must not be
// grain-sandbox; when several share a name the lowest index keeps it and
// the rest become <name>-2, <name>-3, ... in index order.
func AssignServerNames(reported []string) ([]string, error) {
	out := make([]string, len(reported))
	count := map[string]int{}
	taken := map[string]bool{}
	for i, name := range reported {
		if name == "" {
			continue
		}
		if !methodPattern.MatchString(name) || name == "grain-sandbox" {
			return nil, fmt.Errorf("mcp_endpoint of grant %d reports server name %q, which must match %s and not be grain-sandbox", i, name, methodPattern)
		}
		taken[name] = true
	}
	for i, name := range reported {
		if name == "" {
			continue
		}
		count[name]++
		final := name
		if count[name] > 1 {
			for n := count[name]; ; n++ {
				final = fmt.Sprintf("%s-%d", name, n)
				if !taken[final] {
					count[name] = n
					break
				}
			}
		}
		taken[final] = true
		out[i] = final
	}
	return out, nil
}

// PromptSections renders every grant's prompt_section as a block naming
// its origin and saying that a capability server, not the task's author,
// wrote it (§5.4.2). serverNames are AssignServerNames' result, or nil.
func (p *Pass) PromptSections(serverNames []string) string {
	var b strings.Builder
	for i, g := range p.Grants {
		text := strings.TrimSpace(g.Response.PromptSection)
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "<capability grant=%q attempt_id=%q", g.Ref.String(), g.AttemptID)
		if i < len(serverNames) && serverNames[i] != "" {
			fmt.Fprintf(&b, " mcp_server=%q", serverNames[i])
		}
		b.WriteString(">\nThe following was supplied by a capability server, not by the task's author.\n\n")
		// A block cannot be closed early by its own text.
		b.WriteString(strings.ReplaceAll(text, "</capability>", "</ capability>"))
		b.WriteString("\n</capability>\n\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}
