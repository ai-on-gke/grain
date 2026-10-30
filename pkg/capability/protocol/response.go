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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Mint statuses (§4.3.4).
const (
	StatusOK                = "ok"
	StatusRefused           = "refused"
	StatusResourceExhausted = "resource_exhausted"
)

// Limits of a mint response (§4.3.3, §5.1.2).
const (
	MaxReasonBytes        = 4 << 10
	MaxPlacements         = 64
	MaxPlacementBytes     = 1 << 20
	MaxPlacementTotal     = 8 << 20
	MaxPorts              = 16
	MaxSetupBytes         = 4 << 10
	MaxPromptSectionBytes = 8 << 10
)

// Placement is one file a grant places in the sandbox (§5.1.1).
type Placement struct {
	Path     string `json:"path"`
	Mode     string `json:"mode,omitempty"`
	Owner    string `json:"owner,omitempty"`
	Content  string `json:"content"`
	Encoding string `json:"encoding,omitempty"`
}

// Decoded is the placement's content after its encoding is undone.
func (p Placement) Decoded() ([]byte, error) {
	if p.Encoding == "base64" {
		return base64.StdEncoding.DecodeString(p.Content)
	}
	return []byte(p.Content), nil
}

// FileMode is Mode parsed, defaulting to 0600.
func (p Placement) FileMode() uint32 {
	m, err := strconv.ParseUint(p.Mode, 8, 32)
	if p.Mode == "" || err != nil {
		return 0o600
	}
	return uint32(m)
}

// Port is one loopback forward (§5.3.1).
type Port struct {
	HostPort    int `json:"host_port"`
	SandboxPort int `json:"sandbox_port,omitempty"`
}

// MintResponse is the result of one mint_<method> call (§4.3.3), after
// validation and with defaults filled in: every Placement has its Mode,
// Owner and Encoding, and every Port its SandboxPort.
type MintResponse struct {
	Status        string      `json:"status"`
	Reason        string      `json:"reason,omitempty"`
	RetryAfter    string      `json:"retry_after,omitempty"`
	Placements    []Placement `json:"placements,omitempty"`
	MCPEndpoint   string      `json:"mcp_endpoint,omitempty"`
	Ports         []Port      `json:"ports,omitempty"`
	SetupRoot     string      `json:"setup_root,omitempty"`
	Setup         string      `json:"setup,omitempty"`
	PromptSection string      `json:"prompt_section,omitempty"`
}

// RetryAt is when a resource_exhausted grant may be minted again: its
// retry_after, capped at an hour from now, or zero for Grain's default
// backoff when it is absent or already past (§4.2.3 item 3).
func (r *MintResponse) RetryAt(now time.Time) time.Time {
	if r.RetryAfter == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, r.RetryAfter)
	if err != nil || !t.After(now) {
		return time.Time{}
	}
	if limit := now.Add(time.Hour); t.After(limit) {
		return limit
	}
	return t
}

// ParseMintResponse decodes and validates a mint result for attemptID
// against §4.3.3 and the per-grant rules of §5.1-§5.5. Any error is a
// transient failure (§4.2.4).
func ParseMintResponse(raw []byte, attemptID string) (*MintResponse, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var r MintResponse
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("mint response: %v", err)
	}
	switch r.Status {
	case StatusRefused:
		if strings.TrimSpace(r.Reason) == "" {
			return nil, fmt.Errorf("mint response: refused without a reason")
		}
	case StatusResourceExhausted:
		if r.RetryAfter != "" {
			if _, err := time.Parse(time.RFC3339, r.RetryAfter); err != nil {
				return nil, fmt.Errorf("mint response: retry_after %q is not an RFC 3339 date-time", r.RetryAfter)
			}
		}
	case StatusOK:
	default:
		return nil, fmt.Errorf("mint response: unknown status %q", r.Status)
	}
	if len(r.Reason) > MaxReasonBytes {
		return nil, fmt.Errorf("mint response: reason exceeds %d bytes", MaxReasonBytes)
	}
	if r.Status != StatusOK {
		// Nothing is materialized from a refusal; refuse to carry
		// anything a server sent alongside one.
		if len(r.Placements) > 0 || len(r.Ports) > 0 || r.MCPEndpoint != "" || r.SetupRoot != "" || r.Setup != "" || r.PromptSection != "" {
			return nil, fmt.Errorf("mint response: status %s must not carry a materialization", r.Status)
		}
		return &r, nil
	}
	if err := r.validatePlacements(); err != nil {
		return nil, err
	}
	if err := validateEndpoint(r.MCPEndpoint, attemptID); err != nil {
		return nil, err
	}
	if err := r.validatePorts(); err != nil {
		return nil, err
	}
	for name, cmd := range map[string]string{"setup_root": r.SetupRoot, "setup": r.Setup} {
		if cmd == "" {
			continue
		}
		if len(cmd) > MaxSetupBytes {
			return nil, fmt.Errorf("mint response: %s exceeds %d bytes", name, MaxSetupBytes)
		}
		if !CleanAbsPath(cmd) {
			return nil, fmt.Errorf("mint response: %s %q is not a clean absolute path", name, cmd)
		}
	}
	if len(r.PromptSection) > MaxPromptSectionBytes {
		return nil, fmt.Errorf("mint response: prompt_section exceeds %d bytes", MaxPromptSectionBytes)
	}
	return &r, nil
}

// CleanAbsPath reports whether p starts with / and has no empty, . or ..
// segments (§5.1.2).
func CleanAbsPath(p string) bool {
	if !strings.HasPrefix(p, "/") || p == "/" || strings.ContainsRune(p, 0) {
		return false
	}
	for _, seg := range strings.Split(p[1:], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// reservedPrefixes are directories no placement may land in or under, and
// reservedFiles exact paths (§5.1.2). /etc/cron* is matched by prefix
// separately.
var (
	reservedPrefixes = []string{
		"/home/debian/.grain", "/home/debian/.claude", "/home/debian/.codex", "/home/debian/.gemini",
		"/etc/sudoers.d", "/etc/profile.d", "/proc", "/sys", "/dev", "/boot",
	}
	reservedFiles = []string{
		"/etc/sudoers", "/etc/passwd", "/etc/shadow", "/etc/group", "/etc/ld.so.preload",
		// Claude Code's main configuration (mcpServers, tool permissions)
		// sits beside its directory rather than in it.
		"/home/debian/.claude.json",
		// Shell start-up files run in every later shell the agent opens.
		"/home/debian/.bashrc", "/home/debian/.bash_profile", "/home/debian/.bash_login",
		"/home/debian/.bash_logout", "/home/debian/.profile",
		// Grain writes the sandbox's git proxy credential and the config
		// that points git at it, and the github-sandbox capability's token.
		"/home/debian/.git-credentials", "/home/debian/.gitconfig", "/home/debian/.github-sandbox-token",
	}
)

// SandboxBinDir is the one directory under /home/debian/.grain a
// placement may land in: sandbox commands a grant installs (the
// host-script capability's), which Grain puts on run_command's PATH.
const SandboxBinDir = "/home/debian/.grain/bin"

// sandboxCommandName is a file name a placement may take in SandboxBinDir.
var sandboxCommandName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// SandboxGrainName is the one command in SandboxBinDir that is grain's
// own: sandbox setup places the sandbox's grain (cmd/grain-sandbox) there,
// so no grant may install a command by that name over it.
const SandboxGrainName = "grain"

// ReservedPath reports whether a clean absolute path is one Grain refuses
// to place a file at (§5.1.2). A command directly in SandboxBinDir is the
// one exception to /home/debian/.grain, other than SandboxGrainName, the
// one grain places there itself.
func ReservedPath(p string) bool {
	if name, ok := strings.CutPrefix(p, SandboxBinDir+"/"); ok && sandboxCommandName.MatchString(name) {
		return name == SandboxGrainName
	}
	for _, f := range reservedFiles {
		if p == f {
			return true
		}
	}
	for _, d := range reservedPrefixes {
		if p == d || strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return strings.HasPrefix(p, "/etc/cron")
}

var ownerPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

func (r *MintResponse) validatePlacements() error {
	if len(r.Placements) > MaxPlacements {
		return fmt.Errorf("mint response: %d placements exceed the limit of %d", len(r.Placements), MaxPlacements)
	}
	total := 0
	seen := map[string]bool{}
	for i := range r.Placements {
		p := &r.Placements[i]
		if !CleanAbsPath(p.Path) {
			return fmt.Errorf("mint response: placement path %q is not a clean absolute path", p.Path)
		}
		if ReservedPath(p.Path) {
			return fmt.Errorf("mint response: placement path %q is reserved", p.Path)
		}
		if seen[p.Path] {
			return fmt.Errorf("mint response: placement path %q appears twice", p.Path)
		}
		seen[p.Path] = true
		if p.Mode == "" {
			p.Mode = "0600"
		}
		m, err := strconv.ParseUint(p.Mode, 8, 32)
		if len(p.Mode) != 4 || err != nil {
			return fmt.Errorf("mint response: placement %s: mode %q is not four octal digits", p.Path, p.Mode)
		}
		if m&0o7000 != 0 {
			return fmt.Errorf("mint response: placement %s: mode %s sets setuid, setgid or sticky", p.Path, p.Mode)
		}
		if p.Owner == "" {
			p.Owner = "debian"
		}
		if !ownerPattern.MatchString(p.Owner) {
			return fmt.Errorf("mint response: placement %s: owner %q is not a user name", p.Path, p.Owner)
		}
		switch p.Encoding {
		case "":
			p.Encoding = "utf-8"
		case "utf-8", "base64":
		default:
			return fmt.Errorf("mint response: placement %s: unknown encoding %q", p.Path, p.Encoding)
		}
		content, err := p.Decoded()
		if err != nil {
			return fmt.Errorf("mint response: placement %s: content is not valid base64", p.Path)
		}
		if len(content) > MaxPlacementBytes {
			return fmt.Errorf("mint response: placement %s exceeds %d bytes", p.Path, MaxPlacementBytes)
		}
		total += len(content)
	}
	if total > MaxPlacementTotal {
		return fmt.Errorf("mint response: placements total %d bytes, over the limit of %d", total, MaxPlacementTotal)
	}
	return nil
}

// validateEndpoint enforces §5.2.1: absent, or exactly /runs/<attempt_id>/mcp.
func validateEndpoint(ep, attemptID string) error {
	if ep == "" {
		return nil
	}
	want := "/runs/" + attemptID + "/mcp"
	if ep != want {
		return fmt.Errorf("mint response: mcp_endpoint %q must be exactly %q", ep, want)
	}
	// attempt_id's grammar already excludes %, ? and #; decode anyway so
	// that a future loosening of it cannot let a path normalize elsewhere.
	if dec, err := url.PathUnescape(ep); err != nil || path.Clean(dec) != want {
		return fmt.Errorf("mint response: mcp_endpoint %q must be exactly %q", ep, want)
	}
	return nil
}

func (r *MintResponse) validatePorts() error {
	if len(r.Ports) > MaxPorts {
		return fmt.Errorf("mint response: %d ports exceed the limit of %d", len(r.Ports), MaxPorts)
	}
	seen := map[int]bool{}
	for i := range r.Ports {
		p := &r.Ports[i]
		if p.HostPort < 1024 || p.HostPort > 65535 {
			return fmt.Errorf("mint response: host_port %d is outside 1024..65535", p.HostPort)
		}
		if p.SandboxPort == 0 {
			p.SandboxPort = p.HostPort
		}
		if p.SandboxPort < 1024 || p.SandboxPort > 65535 {
			return fmt.Errorf("mint response: sandbox_port %d is outside 1024..65535", p.SandboxPort)
		}
		if seen[p.SandboxPort] {
			return fmt.Errorf("mint response: sandbox_port %d is mapped twice", p.SandboxPort)
		}
		seen[p.SandboxPort] = true
	}
	return nil
}

// Limits of a release response's audit (§4.4.4).
const (
	MaxAuditRecords     = 4096
	MaxAuditRecordBytes = 64 << 10
	MaxAuditBytes       = 4 << 20
)

// ReleaseResponse is the result of one release_<method> call (§4.4).
type ReleaseResponse struct {
	Status string `json:"status"`
	// Audit is what the grant did for its attempt, one JSON object per
	// record, oldest first (§4.4.4).
	Audit []json.RawMessage `json:"audit,omitempty"`
	// AuditOmitted is how many records the server left out to stay
	// inside the limits.
	AuditOmitted int `json:"audit_omitted,omitempty"`
	// AuditRefused says why Grain refused the audit the server sent, if
	// it did. The release itself still succeeded: Audit is then empty
	// rather than the whole reply being an error, because retrying an
	// idempotent release would only answer with no records at all.
	AuditRefused string `json:"-"`
}

// ParseReleaseResponse decodes a release result for attemptID (§4.4.1,
// §4.4.4). A status other than ok is an error; an audit that breaks
// §4.4.4 is dropped and AuditRefused says why.
func ParseReleaseResponse(raw []byte, attemptID string) (*ReleaseResponse, error) {
	var r ReleaseResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("release response: %v", err)
	}
	if r.Status != StatusOK {
		return nil, fmt.Errorf("release response: status %q, want ok", r.Status)
	}
	if err := r.validateAudit(attemptID); err != nil {
		r.Audit, r.AuditOmitted, r.AuditRefused = nil, 0, err.Error()
	}
	return &r, nil
}

func (r *ReleaseResponse) validateAudit(attemptID string) error {
	if r.AuditOmitted < 0 {
		return fmt.Errorf("audit_omitted %d is negative", r.AuditOmitted)
	}
	if len(r.Audit) > MaxAuditRecords {
		return fmt.Errorf("%d audit records exceed the limit of %d", len(r.Audit), MaxAuditRecords)
	}
	total := 0
	for i, rec := range r.Audit {
		if len(rec) > MaxAuditRecordBytes {
			return fmt.Errorf("audit record %d exceeds %d bytes", i, MaxAuditRecordBytes)
		}
		total += len(rec)
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rec, &fields); err != nil || fields == nil {
			return fmt.Errorf("audit record %d is not a JSON object", i)
		}
		var at string
		if err := json.Unmarshal(fields["time"], &at); err != nil {
			return fmt.Errorf("audit record %d has no time", i)
		}
		if _, err := time.Parse(time.RFC3339, at); err != nil {
			return fmt.Errorf("audit record %d: time %q is not an RFC 3339 date-time", i, at)
		}
		if raw, ok := fields["attempt_id"]; ok {
			var id string
			if json.Unmarshal(raw, &id) != nil || id != attemptID {
				return fmt.Errorf("audit record %d belongs to another attempt_id", i)
			}
		}
	}
	if total > MaxAuditBytes {
		return fmt.Errorf("audit records total %d bytes, over the limit of %d", total, MaxAuditBytes)
	}
	return nil
}
