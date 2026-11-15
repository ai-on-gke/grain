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

package daemon

import (
	"context"
	"fmt"
	"github.com/ai-on-gke/grain/pkg/change"
	"io"
	"log"
	"regexp"
	"strings"

	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/capability/servers"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/staterepo"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// capabilityServerPrincipal is who a capability server's secret
// resolutions are audited as: the daemon, attaching the server, rather
// than any run.
const capabilityServerPrincipal = "capability-servers"

// startCapabilityServers builds the registry of the deployment's
// attached capability servers (pkg/capability/servers,
// design/spec/capabilities/capabilities.spec.md §3.1-§3.2) and starts the loop that keeps it
// in step with grain_config.mcp_servers until ctx ends.
//
// A server whose configure schema declares grain's identity fields
// (protocol.DeploymentIDConfig, protocol.InstanceIDConfig) is sent this
// deployment's id and installation id with every configure, so the gcp
// server names and owns its service accounts by the same deployment id
// as the branches; a rename in Settings re-attaches it.
//
// Relative stdio commands are resolved under the checked-out grain/config
// (StateRepoDir). $secret:<name> references are resolved from the
// encrypted secrets store at attach time and held only in the spawned
// process's environment or the HTTP client's headers. A stdio server's
// stderr is discarded rather than logged, since nothing stops a server
// printing the credential it was handed there.
//
// A stdio server set to run: host is started through grain hostd where
// that is this deployment's host executor (hostdClient non-nil,
// Config.capabilityHostd), and refused anywhere else. A run: hostd server
// that names a hostd of its own (protocol.ServerConfig.Hostd) is started
// on that hostd instead, through the hostdCapabilityServers returned
// beside the registry, which LiveConfig.Refresh keeps in step with it.
//
// The first configuration is handed over from the stored row here; after
// that LiveConfig.Refresh hands over each one it reads, so a merged
// grain/config change or a Settings save reaches it on the next tick.
// Each one is handed over with grain's in-tree servers registered in it
// (registerBuiltinCapabilityServers), and the first is written back.
func startCapabilityServers(ctx context.Context, store *model.Store, secretStore *secrets.Store, dataDir string, hostdClient *hostd.Client) (*servers.Registry, *hostdCapabilityServers) {
	var lookup func(name string) (string, bool)
	if secretStore != nil {
		auditCtx := secrets.WithPrincipal(context.Background(), capabilityServerPrincipal)
		lookup = func(name string) (string, bool) {
			v, err := secretStore.Resolve(auditCtx, name)
			return v, err == nil
		}
	}
	logf := func(format string, args ...any) {
		log.Printf("grain daemon: "+format, args...)
	}
	opts := servers.Options{
		Secrets:    lookup,
		ConfigRoot: StateRepoDir(dataDir),
		Hostd:      hostdClient,
		Logf:       logf,
		// A stdio server's own log is this one's: the gcp server's
		// "gcp: audit: ..." lines, the only record of each grant and token
		// it mints, were discarded here (2026-11-08 gcp token-mode walk).
		Stderr: capabilityServerStderr,
	}
	if store != nil {
		// A server that falls out of use says so where people look, not
		// only in this log: the 2026-11-03 role-ceiling refusal sat in
		// needs-config with nobody told.
		raiseDrop, clearDrop := capabilityServerAlerts(ctx, store)
		opts.OnDrop = func(st servers.Status) {
			raiseDrop(st)
			// Runs holding a grant it minted are told, rather than left
			// to infer it from permissions that never arrive.
			reason := st.Reason
			if reason == "" {
				reason = st.Error
			}
			orchestrator.NoteCapabilityServerDropped(ctx, store, st.ID, string(st.State), reason, st.ConfigRefused)
		}
		opts.OnAttach = func(id string) {
			clearDrop(id)
			// Grants whose release found their server out of use are
			// released now it is back (orchestrator.CapabilityPasses).
			go orchestrator.RetryCapabilityReleases(ctx, store)
		}
		opts.OnClear = clearDrop
		// A server declaring grain's identity fields is sent the same
		// deployment id the branch names carry (§4.5.7).
		opts.Identity = servers.StoreIdentity(store, logf)
	}
	if secretStore != nil {
		// A data-plane token is generated here rather than asked of
		// the operator: only grain and the server ever read it.
		opts.Managed = secretStore
	}
	reg := servers.New(opts)
	onHostd := newHostdCapabilityServers(ctx, reg, opts, store)
	if store != nil {
		if stored, err := store.GetConfig(ctx); err != nil {
			log.Printf("grain daemon: reading capability servers: %v", err)
		} else {
			var configured map[string]protocol.ServerConfig
			if stored != nil {
				configured = stored.MCPServers
			}
			configured = registerBuiltinCapabilityServers(ctx, store, dataDir, configured)
			reg.Update(deploymentCapabilityServers(configured))
			onHostd.update(configured)
		}
	}
	go reg.Run(ctx, 0)
	if store != nil {
		combined := placedStatusRegistry{Registry: reg, placed: onHostd}
		// Task workspaces any agent may create (RegisterWorkspaceJanitorTools)
		// are deleted once their tasks have closed and the server's retention has passed.
		go runWorkspaceJanitor(ctx, combined, storeClosedAt(store), workspaceJanitorInterval)
		// Non-PR change providers (pkg/change: workspace changelists) reach their
		// capability server's daemon-only control tools through combined, and
		// a kind blocked on expired credentials is checked again every
		// 5 minutes until it clears.
		change.Bind(changeControl{reg: combined})
		go change.Watch(ctx, store)
		// Every live CL is handed to its provider's poller once a minute,
		// for the task view's presubmit badge (changelists.md §3).
		go orchestrator.PollChanges(ctx, store)
	}
	return reg, onHostd
}

// registerBuiltinCapabilityServers is configured with an entry for every
// in-tree capability server (intree.EnsureBuiltins): what the registry is
// handed on start and on every reload, so each built-in is attached --
// or reported as needing configuring -- whatever grain_config says.
//
// When that adds or adopts an entry it is also written back to
// grain_config, and from there to the state repo like any other entry,
// so grain/config stays the source of truth. Where grain/config is an
// external repository the daemon cannot write, the entries are only
// applied here, and appear in grain/config once an operator saves one.
// Registering a server grants nothing: a grant still has to name it.
func registerBuiltinCapabilityServers(ctx context.Context, store *model.Store, dataDir string, configured map[string]protocol.ServerConfig) map[string]protocol.ServerConfig {
	ensured, changed := intree.EnsureBuiltins(configured)
	if !changed || store == nil || externalConfigRepo(dataDir) {
		return ensured
	}
	err := store.UpdateConfig(ctx, func(c *model.Config) error {
		c.MCPServers, _ = intree.EnsureBuiltins(c.MCPServers)
		return nil
	})
	if err != nil {
		log.Printf("grain daemon: registering the built-in capability servers: %v", err)
	}
	return ensured
}

// externalConfigRepo reports whether grain/config is an external
// repository (staterepo.Settings.Remote), whose settings the daemon does
// not write itself.
func externalConfigRepo(dataDir string) bool {
	if dataDir == "" {
		return false
	}
	settings, err := staterepo.LoadSettings(dataDir)
	return err == nil && settings.Remote != ""
}

// capabilityServerStatuses is live's registry as the UI reads it: a nil
// interface, not a typed nil, when there is none.
func capabilityServerStatuses(live *LiveConfig) ui.CapabilityServerStatuses {
	if reg := live.CapabilityServers(); reg != nil {
		// With the rows of the servers placed on hostds of their own.
		if live.hostdCapabilityServers != nil {
			return placedStatusRegistry{Registry: reg, placed: live.hostdCapabilityServers}
		}
		return reg
	}
	return nil
}

// capabilityServerLookup is live's registry as admission mints through
// it: a nil interface, not a typed nil, when there is none.
func capabilityServerLookup(live *LiveConfig) orchestrator.CapabilityServers {
	reg := live.CapabilityServers()
	if reg == nil {
		return nil
	}
	// Each run's run: host servers on the hostd it is routed to.
	if live.hostdCapabilityServers != nil {
		return live.hostdCapabilityServers
	}
	return reg
}

// capabilityServerAlerts raises model.FamilyCapabilityServerDown for a
// server the registry reports dropped (servers.Options.OnDrop), naming it
// and why, and clears it when the server attaches again.
func capabilityServerAlerts(ctx context.Context, store *model.Store) (func(servers.Status), func(string)) {
	return capabilityServerAlertsOnHostd(ctx, store, "")
}

// capabilityServerAlertsOnHostd is capabilityServerAlerts scoped to one
// named grain hostd when hostdName is non-empty, so a secondary hostd's
// registry cannot raise over or clear the deployment registry's own alert.
func capabilityServerAlertsOnHostd(ctx context.Context, store *model.Store, hostdName string) (func(servers.Status), func(string)) {
	subjectFor := func(id string) string {
		if hostdName == "" {
			return model.NotificationSubject(model.FamilyCapabilityServerDown, id)
		}
		return model.NotificationSubject(model.FamilyCapabilityServerDown, id+"@"+hostdName)
	}
	labelFor := func(id string) string {
		if hostdName == "" {
			return id
		}
		return id + " on host " + hostdName
	}
	onDrop := func(st servers.Status) {
		reason := st.Reason
		if reason == "" {
			reason = st.Error
		}
		label := labelFor(st.ID)
		title := fmt.Sprintf("Capability server %s is %s", label, st.State)
		body := fmt.Sprintf("Capability server %s is no longer attached (%s): %s\n\n", label, st.State, reason)
		fix := "Fix it in Settings → Capability servers; this clears itself once the server attaches."
		if st.ConfigRefused {
			title = fmt.Sprintf("Capability server %s refused its configuration", label)
			body += "Its configuration was saved but the server will not run under it. "
		} else if w := servers.WaitingLabel(st); w != "" {
			// Nothing in Settings is wrong: the server waits on something
			// outside grain -- a host credential to be renewed -- and is
			// retried every minute until it attaches.
			title = fmt.Sprintf("Capability server %s is %s", label, w)
			fix = "grain tries it again every minute, so this clears itself once that is done and the server attaches; nothing in Settings needs changing."
		}
		body += "No new grant can be minted from it until it attaches again, and grants it issued earlier cannot be released meanwhile. " + fix
		if _, err := store.RaiseNotification(ctx, model.Notification{
			Subject:  subjectFor(st.ID),
			Severity: model.NotificationWarning,
			Title:    title,
			Body:     body,
			RaisedBy: "automation:" + capabilityServerPrincipal,
		}); err != nil {
			log.Printf("grain daemon: raising capability server %s's drop: %v", label, err)
		}
	}
	onAttach := func(id string) {
		subject := subjectFor(id)
		if n, err := store.OpenNotification(ctx, subject); err != nil || n == nil {
			return
		}
		if _, err := store.ClearNotification(ctx, subject, "automation:"+capabilityServerPrincipal); err != nil {
			log.Printf("grain daemon: clearing capability server %s's drop: %v", labelFor(id), err)
		}
	}
	return onDrop, onAttach
}

// serverLogStamp is the date and time a server's own log.Logger puts
// before each line, which this log already puts before it.
var serverLogStamp = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(\.\d+)? `)

// capabilityServerStderr is servers.Options.Stderr: each line a stdio
// capability server writes to its stderr, already scrubbed of its
// secrets, logged as the server's.
func capabilityServerStderr(id string) io.Writer {
	return capabilityServerLog{id: id, logf: log.Printf}
}

type capabilityServerLog struct {
	id   string
	logf func(format string, args ...any)
}

func (l capabilityServerLog) Write(p []byte) (int, error) {
	line := serverLogStamp.ReplaceAllString(strings.TrimRight(string(p), "\r\n"), "")
	l.logf("grain daemon: capability server %s: %s", l.id, line)
	return len(p), nil
}
