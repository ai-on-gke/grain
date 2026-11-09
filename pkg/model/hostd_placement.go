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

package model

// Two rules about where host work runs (docs/host-placement.md):
//
//   - A connection's credentials are stored (a key or file grain holds)
//     or ambient (the agent CLI's own login on the machine it runs on).
//     Ambient on a CLI that starts through grain hostd is only accepted
//     on a connection placed on a host (AgentBackend.Hostd): grain stores
//     no secret for it, so which machine's login a run spends has to be
//     said, not implied by the default host.
//   - Host work nothing places -- a run: host capability server with no
//     hostd, a connection whose framework runs on the host because
//     Settings -> Models says "Host" or the daemon's -<cli>-run flag
//     (GRAIN_*_RUN=host) does -- goes to the deployment's default host.
//     When that is the only host there is (SoleHostd), it is written
//     down: each of them is placed on it explicitly
//     (PlaceImplicitHostWork), on the daemon's start and on every
//     grain/config import, so the placement reads the same in Settings,
//     in grain/config and at run time.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// A connection's credentials (AgentBackend.Credentials).
const (
	// CredentialsStored is a key, token or auth.json grain holds for the
	// connection (or reuses from another one).
	CredentialsStored = "stored"
	// CredentialsAmbient is the agent CLI's own login on the machine the
	// connection's CLI runs on (AuthMode "ambient"): grain holds nothing.
	CredentialsAmbient = "ambient"
)

// Credentials is CredentialsAmbient for a connection whose authentication
// is the CLI's own login on its host -- AuthMode "ambient", directly or
// through the connection it reuses authentication from (all, by
// CredentialBackendID) -- and CredentialsStored for every other one.
//
// A codex "subscription" connection is stored: grain holds its auth.json.
// (A run of one on a host that has a codex login of its own still links
// that login, which is why a usage limit on it is keyed to the host the
// way an ambient one is; that is the daemon's ambientCredential, not
// this.)
func (b AgentBackend) Credentials(all []AgentBackend) string {
	mode := b.AuthMode
	if b.CredentialBackendID != "" {
		mode = ""
		for _, source := range all {
			if source.ID == b.CredentialBackendID {
				mode = source.AuthMode
				break
			}
		}
	}
	if mode == AntigravityAuthAmbient {
		return CredentialsAmbient
	}
	return CredentialsStored
}

// AmbientCredentialsNeedHost is why an ambient connection with no host
// placement is refused.
const AmbientCredentialsNeedHost = "ambient credentials are the agent CLI's own login on the machine it runs on, " +
	"and grain stores no secret for them: choose the host it runs on (Run on host) first, or use stored credentials"

// CheckAmbientPlacement refuses each connection in backends whose
// credentials are ambient, that names no host, and whose CLI would start
// through grain hostd (needsHost, by framework) -- so through the default
// host, by implication. A CLI the daemon starts as its own child (in its
// container, or on the bare machine of a development daemon) has one
// login it can mean, the daemon's own, and is not refused; nor is any
// connection when needsHost is nil, for a caller that cannot say where a
// CLI runs. A connection stored that way already is not refused either:
// one saved before the rule keeps running through the default host until
// it is changed (refused on change, never on an unrelated save). The
// answer names the first one refused.
func CheckAmbientPlacement(backends, stored []AgentBackend, needsHost func(framework string) bool) error {
	if needsHost == nil {
		return nil
	}
	for i, b := range backends {
		if b.IsRemovedCustomConnection() || strings.TrimSpace(b.Hostd) != "" || b.Credentials(backends) != CredentialsAmbient ||
			!needsHost(NormalizeAgentFrameworkName(b.Framework)) {
			continue
		}
		var prev *AgentBackend
		for j := range stored {
			if stored[j].ID == b.ID {
				prev = &stored[j]
				break
			}
		}
		if prev != nil && strings.TrimSpace(prev.Hostd) == "" && prev.Credentials(stored) == CredentialsAmbient {
			continue
		}
		name := b.Name
		if name == "" {
			name = b.ID
		}
		return fmt.Errorf("agentBackends[%d] (%s): %s", i, name, AmbientCredentialsNeedHost)
	}
	return nil
}

// ImplicitHostPlacement is what the daemon knows about where unplaced
// host work goes, which the rows of the database do not say.
type ImplicitHostPlacement struct {
	// Default is the host host work goes to when nothing names one
	// (daemon.Config.DefaultHostd); "" when that is whichever registered
	// host is connected.
	Default string
	// LocalUsable is whether LocalHostd, the co-located grain hostd,
	// takes host work on this deployment. Its registry row is always
	// there; on a deployment with no co-located hostd (Kubernetes, or a
	// development daemon on the bare machine, whose host runs are its own
	// children) it is not a host anything went to.
	LocalUsable bool
	// DaemonHost are the frameworks whose CLI runs on the host when
	// Settings -> Models sets no location for it: the daemon's
	// -<cli>-run flag (GRAIN_*_RUN=host) or its built-in default says so.
	DaemonHost map[string]bool
}

// SoleHostd is the one host unplaced host work can go to, or "" when
// there is not exactly one: the registry's unrevoked hosts (LocalHostd
// only where p.LocalUsable), which must also be p.Default when that is
// set -- a default host outside the registry (the deprecated shared
// token's) is a second host.
func SoleHostd(rows []Hostd, p ImplicitHostPlacement) string {
	var names []string
	for _, h := range rows {
		if h.Revoked() || (h.Name == LocalHostd && !p.LocalUsable) {
			continue
		}
		names = append(names, h.Name)
	}
	if len(names) != 1 {
		return ""
	}
	if p.Default != "" && p.Default != names[0] {
		return ""
	}
	return names[0]
}

// PlaceImplicitHostWork writes host work that goes to the default host
// by implication down as going to hostd, which must be that host
// (SoleHostd):
//
//   - a run: host (or run: hostd) capability server with no hostd
//     becomes run: hostd, hostd: <hostd>;
//   - a connection with no hostd whose framework runs on the host --
//     Settings -> Models says "Host" (cliRun), or says nothing and the
//     daemon's flag or default does (p.DaemonHost) -- gets hostd: <hostd>.
//     That includes an ambient connection saved before ambient needed a
//     host (CheckAmbientPlacement): its login is that host's. One whose
//     CLI runs in the daemon's container (or detached) is left where it
//     is: placing it would move it onto the host.
//
// It changes backends and servers in place and returns one line per
// change, sorted, for the log. A connection grain disabled for good (a
// retired custom command one) is left as it is.
func PlaceImplicitHostWork(backends []AgentBackend, cliRun map[string]CLIRunSetting, servers map[string]protocol.ServerConfig,
	hostd string, p ImplicitHostPlacement) []string {
	if hostd == "" {
		return nil
	}
	var changes []string
	for i := range backends {
		b := &backends[i]
		if b.IsRemovedCustomConnection() || strings.TrimSpace(b.Hostd) != "" {
			continue
		}
		fw := NormalizeAgentFrameworkName(b.Framework)
		location := cliRun[fw].Location
		onHost := location == CLIRunHost || (location == "" && p.DaemonHost[fw])
		why := ""
		switch {
		case onHost && location != "":
			why = "Settings -> Models runs " + fw + " on the host"
		case onHost:
			why = "the daemon runs " + fw + " on the host (-" + fw + "-run, GRAIN_*_RUN)"
		default:
			continue
		}
		b.Hostd = hostd
		changes = append(changes, fmt.Sprintf("model connection %q now runs on host %s: %s", b.ID, hostd, why))
	}
	for id, s := range servers {
		if !s.OnHost() || s.Hostd != "" {
			continue
		}
		s.Run = protocol.RunHostd
		s.Hostd = protocol.HostdTarget(hostd)
		servers[id] = s
		changes = append(changes, fmt.Sprintf("capability server %s now runs on host %s (was run: host, the default host)", id, hostd))
	}
	sort.Strings(changes)
	return changes
}

// MigrateImplicitHostPlacement is PlaceImplicitHostWork over the
// grain_config row in tx, against the hostd registry in tx, when that
// registry holds exactly one host unplaced work could go to
// (SoleHostd). It is idempotent, and does nothing at all when there are
// several hosts (or none): which one unplaced work meant is then only
// the daemon's to know, and stays implicit. A stored value it cannot
// read is left for the reader that reports it.
func MigrateImplicitHostPlacement(ctx context.Context, tx *sql.Tx, p ImplicitHostPlacement) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT `name`, `revoked_at` FROM `hostd`")
	if err != nil {
		return nil, fmt.Errorf("hostd: reading the registry for host placement: %w", err)
	}
	var hosts []Hostd
	for rows.Next() {
		var h Hostd
		var revoked sql.NullTime
		if err := rows.Scan(&h.Name, &revoked); err != nil {
			rows.Close()
			return nil, fmt.Errorf("hostd: scanning the registry for host placement: %w", err)
		}
		if revoked.Valid {
			t := revoked.Time
			h.RevokedAt = &t
		}
		hosts = append(hosts, h)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	sole := SoleHostd(hosts, p)
	if sole == "" {
		return nil, nil
	}
	var backendsText, cliRunText, serversText string
	err = tx.QueryRowContext(ctx, "SELECT `agent_backends`, `cli_run`, `mcp_servers` FROM `grain_config` WHERE `id` = 1").
		Scan(&backendsText, &cliRunText, &serversText)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("grain_config: reading for host placement: %w", err)
	}
	var backends []AgentBackend
	if strings.TrimSpace(backendsText) != "" {
		if err := json.Unmarshal([]byte(backendsText), &backends); err != nil {
			backends = nil
		}
	}
	cliRun, err := DecodeCLIRun(cliRunText)
	if err != nil {
		cliRun = nil
	}
	servers, err := DecodeMCPServers(serversText)
	if err != nil {
		servers = nil
	}
	changes := PlaceImplicitHostWork(backends, cliRun, servers, sole, p)
	if len(changes) == 0 {
		return nil, nil
	}
	if backends != nil {
		encoded, err := EncodeAgentBackends(backends)
		if err != nil {
			return nil, err
		}
		if encoded != backendsText {
			if _, err := tx.ExecContext(ctx, "UPDATE `grain_config` SET `agent_backends` = ? WHERE `id` = 1", encoded); err != nil {
				return nil, fmt.Errorf("grain_config.agent_backends: placing on host %s: %w", sole, err)
			}
		}
	}
	if servers != nil {
		encoded, err := EncodeMCPServers(servers)
		if err != nil {
			return nil, err
		}
		if encoded != serversText {
			if _, err := tx.ExecContext(ctx, "UPDATE `grain_config` SET `mcp_servers` = ? WHERE `id` = 1", encoded); err != nil {
				return nil, fmt.Errorf("grain_config.mcp_servers: placing on host %s: %w", sole, err)
			}
		}
	}
	return changes, nil
}

// MigrateImplicitHostPlacement is the transaction-level migration of the
// same name over s, logging each change. The daemon runs it on start,
// once the registry holds LocalHostd.
func (s *Store) MigrateImplicitHostPlacement(ctx context.Context, p ImplicitHostPlacement) ([]string, error) {
	var changes []string
	err := s.write(ctx, "place implicit host work", func(tx *sql.Tx) error {
		var err error
		changes, err = MigrateImplicitHostPlacement(ctx, tx, p)
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, c := range changes {
		log.Printf("host placement: %s", c)
	}
	return changes, nil
}
