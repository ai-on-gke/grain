package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/modelproxy"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// modelProxies is this process's model proxies, keyed by vendor name, as
// everything that is not an HTTP server sees them: the address each is
// reachable at, the tokens it will answer for, and the credential it
// forwards with.
//
// Package-level for the reason ciReadScopes is (pkg/cli/ci.go): the
// capability providers that mint into these are built by
// capabilityProviders, out of a config value alone, while the listeners
// they advertise are started by runDaemon -- so the two ends have to
// meet somewhere that is neither, and an endpoint with no URL yet is a
// perfectly good thing for a provider to hold (modelproxy.Endpoint's own
// doc comment). A process that never starts a proxy -- every test that
// builds providers, and `grain demo` -- leaves them with no URL, and the
// capabilities refuse cleanly rather than placing an address nothing
// answers on.
//
// It is this process's endpoints, and it is passed to the two functions
// that use them rather than read from inside them: startModelProxies and
// capabilityProviders each take the endpoints they are wiring, so that
// the one thing a package-level value would otherwise cost -- a test
// binary in which every test shares one set of mutable endpoints, and
// two parallel tests write the same one -- is something a caller can opt
// out of by handing over a set of its own. Every test does
// (startTestModelProxies); the daemon hands over this one.
var ModelProxies = NewModelProxies()

func NewModelProxies() map[string]*modelproxy.Endpoint {
	out := make(map[string]*modelproxy.Endpoint)
	for _, vendor := range modelproxy.Vendors() {
		out[vendor.Name] = modelproxy.NewEndpoint()
	}
	return out
}

// startModelProxies binds one proxy per vendor and publishes where each
// is, into the endpoints it is given -- the daemon's own modelProxies
// above, or a set a test made for itself.
//
// store, cfg and secretStore are what the credential every forwarded
// request is authenticated with is resolved from: the requesting run's
// own team connection for that vendor (modelProxyConnection), and that
// connection's own key -- the same one an agent run on it authenticates
// with (backendCredential) -- resolved per request rather than read
// here, so a key replaced in Team settings reaches the next call rather
// than the next restart. There is no deployment-wide fallback: the
// legacy -*-file flags and vendor secrets reach the proxy only as the
// default team's connections they were migrated into
// (ui.Client.MigrateAgentBackendKeys), as they reach agent runs.
//
// Usage limits are shared with agent runs through agentPause, keyed by
// the same connection (modelProxyPause).
//
// A vendor whose listener will not bind is reported in the returned
// error and left with no URL, while the others serve: these are three
// independent capabilities, and one port refusing to bind is no reason
// for a deployment to stop offering the other two. stop shuts down
// whatever did bind.
func StartModelProxies(cfg Config, fabric Fabric, proxies map[string]*modelproxy.Endpoint, secretStore *secrets.Store, store *model.Store) (stop func(context.Context) error, err error) {
	var stops []func(context.Context) error
	var failures []error
	for _, vendor := range modelproxy.Vendors() {
		stopServer, startErr := startModelProxy(vendor, proxies[vendor.Name], cfg, fabric, secretStore, store)
		if startErr != nil {
			failures = append(failures, fmt.Errorf("%s: %w", vendor.Name, startErr))
			continue
		}
		stops = append(stops, stopServer)
	}
	stop = func(ctx context.Context) error {
		var errs []error
		for _, stopServer := range stops {
			errs = append(errs, stopServer(ctx))
		}
		for _, vendor := range modelproxy.Vendors() {
			if ep := proxies[vendor.Name]; ep != nil {
				ep.SetURL("")
			}
		}
		return errors.Join(errs...)
	}
	return stop, errors.Join(failures...)
}

// startModelProxy is one vendor's half of the above: wire how its
// connections and their credentials resolve, and publish it where a
// sandbox reaches it.
func startModelProxy(vendor modelproxy.Vendor, endpoint *modelproxy.Endpoint, cfg Config, fabric Fabric, secretStore *secrets.Store, store *model.Store) (func(context.Context) error, error) {
	if path := modelProxyTokenFile(cfg.DataDir, vendor); path != "" {
		// Before the listener: a request from a run the previous daemon
		// minted for must not reach a registry that has not read the file
		// yet.
		if err := endpoint.Tokens.PersistTo(path); err != nil {
			log.Printf("grain daemon: %s proxy: %v; runs that held a token before this restart will be refused until they are given another", vendor.Name, err)
		}
	}
	WireModelProxy(vendor, endpoint, cfg, store, secretStore, agentPause)
	proxy := &modelproxy.Proxy{
		Vendor:        vendor,
		Endpoint:      endpoint,
		Unrestricted:  cfg.ModelProxyUnrestricted,
		MaxConcurrent: cfg.ModelProxyMaxConcurrent,
		SlotWait:      cfg.ModelProxySlotWait,
		Audit:         modelProxyAudit(store),
	}

	// Through the fabric, like every other sandbox-facing service. This
	// used to read cfg.granuleGitProxyHost directly, for both the bind
	// and the URL, which is only that deployment's answer: nothing ever
	// copies -container-git-proxy-host or -k8s-git-proxy-host into that
	// field, so under either of those backends the model proxies bound
	// loopback and advertised loopback while the git proxy and both
	// caches -- which asked sandboxAdvertiseHost -- were on the address
	// sandboxes can actually route to. An agent could clone and not
	// reach a model. See sandboxnet.go's own header.
	served, stopServer, err := fabric.Serve(vendor.Name+" proxy", proxy)
	if err != nil {
		return nil, err
	}
	endpoint.SetURL(served.URL)
	return stopServer, nil
}

// modelProxyAudit is what vendor proxies do with each request's
// modelproxy.Entry: refusals only. A forwarded request is the ordinary
// case and there is one per model call a run makes -- several a minute,
// for a run driving a model in a loop -- which is a lot of journal to
// say nothing an operator wants. A refusal is the opposite: it is the
// only evidence that something in a sandbox tried to use this proxy and
// could not, and it names the run that tried, and the connection it
// would have spent, without carrying a token, a body or a query string.
// So is a usage limit the vendor answered, which paused that connection.
// Each goes to the journal and to the audit trail
// (recordModelProxyTeamAudit).
func modelProxyAudit(store *model.Store) func(modelproxy.Entry) {
	return func(entry modelproxy.Entry) {
		switch entry.Outcome {
		case modelproxy.OutcomeForwarded, modelproxy.OutcomeAnswered:
			return
		}
		recordModelProxyTeamAudit(store, entry)
		switch entry.Outcome {
		case modelproxy.OutcomeUsageLimit:
			log.Printf("grain daemon: %s proxy: %s %s for run %q met a usage limit on connection %q",
				entry.Vendor, entry.Method, entry.Path, entry.Owner, entry.Connection)
			return
		}
		log.Printf("grain daemon: %s proxy: refused %s %s for run %q (connection %q): %s",
			entry.Vendor, entry.Method, entry.Path, entry.Owner, entry.Connection, entry.Outcome)
	}
}

// modelProxyTeamAuditTimeout bounds the store write one audited entry
// costs, so a slow database delays a refusal by at most this much -- the
// same bound the git proxy's team audit uses.
const modelProxyTeamAuditTimeout = 5 * time.Second

// recordModelProxyTeamAudit is the stored copy of a model proxy entry
// the journal logs: a refusal, or a usage limit that paused a connection,
// naming the run and the connection it would have spent, where
// GET /api/deployment/audit-logs reads it back beside the git proxy's.
// An entry with no token
// behind it -- or one this proxy never minted -- has no run to name and
// stays in the journal alone, and an audit failure never fails the
// request it describes.
func recordModelProxyTeamAudit(store *model.Store, entry modelproxy.Entry) {
	if store == nil || strings.TrimSpace(entry.Owner) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), modelProxyTeamAuditTimeout)
	defer cancel()
	if err := store.RecordCredentialAccess(ctx, model.CredentialAccessEntry{
		Timestamp:  entry.Time,
		RunID:      entry.Owner,
		Kind:       model.CredentialAccessModelProxy,
		Resource:   entry.Vendor + " " + entry.Path,
		Action:     entry.Method,
		Credential: entry.Connection,
		Outcome:    entry.Outcome,
	}); err != nil {
		log.Printf("grain daemon: %s proxy: recording the audit entry: %v", entry.Vendor, err)
	}
}

// modelProxyGapService is what a restart's gap calls vendor's proxy
// (upgrade.GapRecorder).
func modelProxyGapService(vendor modelproxy.Vendor) string {
	return upgrade.ServiceModelProxy + " (" + vendor.Name + ")"
}

// revokeFinishedRunTokens revokes every token in proxies whose owner is a
// run the store says has finished. Run once at startup, after
// orchestrator.RecoverOrphanedRuns: a run the previous process left live
// -- killed outright, or cut off mid-drain -- never reached the Revoke its
// own teardown makes (orchestrator.RunDispatch), and since tokens persist
// across a restart (Tokens.PersistTo) its token would otherwise go on
// forwarding with its team's credential until its deadline, for a run
// recovery has just finished as interrupted. An owner that names no run
// (a custom backend's token, owned by its task) is left to its own
// expiry, and so is a run still live, which a later build may adopt.
func revokeFinishedRunTokens(ctx context.Context, store *model.Store, proxies map[string]*modelproxy.Endpoint) {
	for _, vendor := range modelproxy.Vendors() {
		endpoint := proxies[vendor.Name]
		if endpoint == nil {
			continue
		}
		revoked := 0
		for id, owner := range endpoint.Tokens.Owners() {
			finished, found, err := store.RunFinished(ctx, owner)
			if err != nil {
				log.Printf("grain daemon: %s proxy: checking whether run %s has finished: %v", vendor.Name, owner, err)
				continue
			}
			if found && finished {
				endpoint.Revoke(id)
				revoked++
			}
		}
		if revoked > 0 {
			log.Printf("grain daemon: %s proxy: revoked %d token(s) held by run(s) that ended with the previous process", vendor.Name, revoked)
		}
	}
}

// modelProxyTokenFile is where vendor's token registry persists itself
// (modelproxy.Tokens.PersistTo), so that a run adopted across a restart
// is still answered: under the data directory's secrets/, beside
// gitproxy's sandbox-tokens.json, and never in the store or the state
// repository. Empty for a process with no data directory, which keeps its
// tokens in memory.
func modelProxyTokenFile(dataDir string, vendor modelproxy.Vendor) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "secrets", "modelproxy-"+vendor.Name+"-tokens.json")
}

// WireModelProxy publishes into endpoint how vendor's proxy resolves a
// run's team connection when its token is minted
// (modelProxyConnection), that connection's credential per request
// (modelProxyCredential) -- or, for a Claude connection on Vertex AI, the
// project, region and access token its requests are translated for
// (modelProxyVertex) -- and the usage-limit pause it shares with agent
// runs (pause, keyed by the same connection). Exported for the tests
// that build endpoints of their own.
func WireModelProxy(vendor modelproxy.Vendor, endpoint *modelproxy.Endpoint, cfg Config, store *model.Store, secretStore *secrets.Store, pause *orchestrator.Pause) {
	endpoint.SetConnections(func(ctx context.Context, own string) (modelproxy.Connection, error) {
		live := DispatchConfig(ctx, store, cfg)
		return modelProxyConnection(live.AgentBackends, vendor, own, pause, time.Now()), nil
	})
	// A pause is keyed by the connection and the team it is listed under
	// (model.ConnectionTeam), as agent runs on it key theirs, so proxy
	// traffic and agent runs on one key stop and resume together.
	connectionTeam := func(connection modelproxy.Connection) string {
		live := DispatchConfig(context.Background(), store, cfg)
		if backend := findBackendByID(live.AgentBackends, connection.ID); backend != nil {
			return model.ConnectionTeam(*backend)
		}
		return model.DefaultTeamID
	}
	endpoint.SetCredential(func(ctx context.Context, connection modelproxy.Connection) (string, error) {
		live := DispatchConfig(ctx, store, cfg)
		return modelProxyCredential(ctx, live.AgentBackends, vendor, connection, secretStore)
	})
	vertexTokens := &vertexTokenSources{}
	endpoint.SetVertex(func(ctx context.Context, connection modelproxy.Connection) (*modelproxy.VertexTarget, error) {
		live := DispatchConfig(ctx, store, cfg)
		return modelProxyVertex(ctx, live.AgentBackends, vendor, connection, secretStore, vertexTokens)
	})
	endpoint.SetPause(
		func(connection modelproxy.Connection, now time.Time) bool {
			return pause.ConnectionPaused(connection.ID, connectionTeam(connection), now)
		},
		func(connection modelproxy.Connection, limit *agent.UsageLimitError) {
			pause.Begin(time.Now(), orchestrator.PauseScope{
				Connection: connection.ID, Team: connectionTeam(connection), Framework: vendor.Framework,
			}, limit)
		})
}

// modelProxyConnection is the connection a run whose own agent spends
// own's credential (model.AgentBackend.CredentialOwnerID, "" for none)
// forwards with through vendor's proxy: own itself where it is one of
// the deployment's connections of vendor's framework -- so a run spends
// the same key through the proxy as its agent does -- and otherwise the
// first of the deployment's enabled connections of that framework whose
// credential is not paused, or failing that the first of them. What it
// names is always the credential's owner, so a connection borrowing a
// key by CredentialBackendID is recorded as its source, the connection a
// usage limit pauses. A deployment with none answers no ID.
func modelProxyConnection(backends []model.AgentBackend, vendor modelproxy.Vendor, own string, pause *orchestrator.Pause, now time.Time) modelproxy.Connection {
	var connection modelproxy.Connection
	var candidates []model.AgentBackend
	for _, backend := range model.DeploymentAgentBackends(backends, true) {
		if model.NormalizeAgentFrameworkName(backend.Framework) != vendor.Framework {
			continue
		}
		if own != "" && (backend.ID == own || backend.CredentialOwnerID() == own) {
			connection.ID = backend.CredentialOwnerID()
			return connection
		}
		if !backend.Disabled {
			candidates = append(candidates, backend)
		}
	}
	for _, backend := range candidates {
		if !pause.ConnectionPaused(backend.CredentialOwnerID(), model.ConnectionTeam(backend), now) {
			connection.ID = backend.CredentialOwnerID()
			return connection
		}
	}
	if len(candidates) > 0 {
		connection.ID = candidates[0].CredentialOwnerID()
	}
	return connection
}

// modelProxyCredential is the credential vendor's proxy forwards with on
// connection: that connection's own key, exactly as an agent run on it
// reads it (backendCredential), for a connection that authenticates with
// one -- an API key, or for Claude an OAuth token, which
// credcheck.AuthorizeAnthropic presents the way the claude CLI does.
//
// A connection that authenticates any other way holds no key this proxy
// can present -- Claude on Vertex AI, which the proxy reaches by
// translating instead (modelProxyVertex), and which is refused here only
// for a proxy with no Vertex resolver wired; a codex subscription login, whose tokens are
// for ChatGPT's backend rather than api.openai.com; an ambient login on
// the host -- and is refused with a sentence saying so
// (modelproxy.CredentialRefusal) rather than forwarded with some other
// key. So is a connection that is gone, or is no longer the team's.
func modelProxyCredential(ctx context.Context, backends []model.AgentBackend, vendor modelproxy.Vendor, connection modelproxy.Connection, secretStore *secrets.Store) (string, error) {
	backend := findBackendByID(backends, connection.ID)
	// Whichever team the run is for, any of the deployment's connections
	// may carry it (docs/single-tenant-reset.md, stage 1): only one that
	// is gone is refused.
	if backend == nil {
		return "", &modelproxy.CredentialRefusal{Reason: "connection " + connection.ID + " is no longer one of this " +
			"deployment's connections, so this grain proxy has no credential to forward with on it; a run " +
			"started after the connections changed is minted with the current one."}
	}
	refuse := func(how string) (string, error) {
		return "", &modelproxy.CredentialRefusal{Reason: "connection " + connection.ID +
			" authenticates " + how + ", which gives this grain " + vendor.Display + " proxy no key to forward " +
			"with. An admin adds a connection that holds an API key" + oauthClause(vendor) +
			" in Settings -> Models."}
	}
	switch mode := backend.AuthMode; {
	case mode == model.AntigravityAuthAmbient:
		return refuse("with the host's own ambient login")
	case vendor.Name == modelproxy.Claude.Name && (mode == model.ClaudeAuthVertex || os.Getenv("CLAUDE_CODE_USE_VERTEX") == "1"):
		return refuse("through Vertex AI")
	case vendor.Name == modelproxy.OpenAI.Name && mode == model.CodexAuthSubscription:
		return refuse("with a ChatGPT subscription login")
	}
	return backendCredential(ctx, Config{AgentBackendID: connection.ID}, secretStore, vendor.Secret, "")
}

// oauthClause is what else, beside an API key, vendor's proxy forwards
// with.
func oauthClause(vendor modelproxy.Vendor) string {
	if vendor.Name == modelproxy.Claude.Name {
		return " or a Claude Code OAuth token"
	}
	return ""
}
