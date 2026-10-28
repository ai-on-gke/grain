package daemon

import (
	"context"
	"log"

	"github.com/bwsalmon/grain/pkg/capability/gcpkey"
	"github.com/bwsalmon/grain/pkg/capability/intree"
	"github.com/bwsalmon/grain/pkg/capability/protocol"
	"github.com/bwsalmon/grain/pkg/capability/servers"
	"github.com/bwsalmon/grain/pkg/hostd"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/secrets"
	"github.com/bwsalmon/grain/pkg/staterepo"
	"github.com/bwsalmon/grain/pkg/ui"
)

// capabilityServerPrincipal is who a capability server's secret
// resolutions are audited as: the daemon, attaching the server, rather
// than any run.
const capabilityServerPrincipal = "capability-servers"

// startCapabilityServers builds the registry of the deployment's
// attached capability servers (pkg/capability/servers,
// docs/capabilities-spec.md §3.1-§3.2) and starts the loop that keeps it
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
// Config.capabilityHostd), and refused anywhere else.
//
// The first configuration is handed over from the stored row here; after
// that LiveConfig.Refresh hands over each one it reads, so a merged
// grain/config change or a Settings save reaches it on the next tick.
// Each one is handed over with grain's in-tree servers registered in it
// (registerBuiltinCapabilityServers), and the first is written back.
//
// gcpPool is the accounts the in-process gcp-key provider mints from
// (gcpkey.PoolEmails of the deployment's gcp settings): Test on a gcp
// server warns when one of its permission sets shares them.
func startCapabilityServers(ctx context.Context, store *model.Store, secretStore *secrets.Store, dataDir string, hostdClient *hostd.Client, gcpPool []string) *servers.Registry {
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
		ProbeWarnings: func(_ string, cfg protocol.ServerConfig) []string {
			if cfg.Builtin != "gcp" && intree.InTreeName(cfg) != "gcp" {
				return nil
			}
			return intree.GCPOverlapWarnings(cfg, gcpPool)
		},
	}
	if store != nil {
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
	if store != nil {
		if stored, err := store.GetConfig(ctx); err != nil {
			log.Printf("grain daemon: reading capability servers: %v", err)
		} else {
			var configured map[string]protocol.ServerConfig
			if stored != nil {
				configured = stored.MCPServers
			}
			reg.Update(registerBuiltinCapabilityServers(ctx, store, dataDir, configured))
		}
	}
	go reg.Run(ctx, 0)
	if store != nil {
		// Task workspaces any agent may create (RegisterWorkspaceJanitorTools)
		// are deleted once their tasks have closed and the server's retention has passed.
		go runWorkspaceJanitor(ctx, reg, storeClosedAt(store), workspaceJanitorInterval)
	}
	return reg
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

// gcpKeyPool is the accounts the in-process gcp-key provider mints from,
// or none where it is not registered.
func gcpKeyPool(cfg Config) []string {
	if len(missingCapabilityProviderConfig(cfg, "gcp-key")) > 0 {
		return nil
	}
	return gcpkey.PoolEmails(cfg.GcpServiceAccountEmail, cfg.GcpServiceAccountCount, cfg.GcpProject)
}

// capabilityServerStatuses is live's registry as the UI reads it: a nil
// interface, not a typed nil, when there is none.
func capabilityServerStatuses(live *LiveConfig) ui.CapabilityServerStatuses {
	if reg := live.CapabilityServers(); reg != nil {
		return reg
	}
	return nil
}

// capabilityServerLookup is live's registry as admission mints through
// it: a nil interface, not a typed nil, when there is none.
func capabilityServerLookup(live *LiveConfig) orchestrator.CapabilityServers {
	if reg := live.CapabilityServers(); reg != nil {
		return reg
	}
	return nil
}
