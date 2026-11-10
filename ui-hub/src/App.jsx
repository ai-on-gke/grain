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

import React, {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  ConfirmDialog,
  CopyBlock,
  EmptyState,
  FilterBar,
  HostKindBadge,
  PageHeader,
  SortableTable,
  StatusPill,
  ThemeModeToggle,
  formatRelativeAge,
  useThemeMode,
} from "@grain/ui-shared";
import {
  acceptTransfer,
  cancelTransfer,
  createDelegatingApp,
  createDelegationGrant,
  createEntry,
  deleteEntry,
  deleteOrphanVM,
  disconnectTunnel,
  fetchAdminSummary,
  fetchDelegatingApps,
  fetchDelegationGrants,
  fetchDirectory,
  fetchEntries,
  fetchEntry,
  fetchMe,
  fetchNameAvailability,
  fetchRoute,
  fetchShapes,
  fetchStatus,
  forceTransferEntry,
  offerTransfer,
  purgeEntry,
  replaceEntry,
  resetQuota,
  resolveScopeConflict,
  restoreEntry,
  revokeCredential,
  revokeDelegationGrant,
  rotateCredential,
  setDelegatingAppDisabled,
  setEntryScopes,
  setOwnerDisabled,
  setQuota,
  setSQLInstanceDraining,
  startEntry,
  stopEntry,
  updateEntry,
  upgradeEntry,
} from "./api.js";

const REPLACE_STEPS = ["draining", "deleting_vm", "creating_vm", "probing"];

// Hosted instances a manager can stop (POST /api/entries/{name}/stop) or
// start again (POST /api/entries/{name}/start); these mirror the states the
// registry's BeginStopEntry / BeginStartEntry accept.
const STOPPABLE_STATES = new Set(["running", "failed"]);
const STARTABLE_STATES = new Set(["stopped", "stopping"]);

function canStopEntry(e) {
  return Boolean(e) && e.kind === "instance" && STOPPABLE_STATES.has(e.state);
}

function canStartEntry(e) {
  return Boolean(e) && e.kind === "instance" && STARTABLE_STATES.has(e.state);
}

// reachabilityStatus is what the Reachability pill shows: a deleted, stopped,
// stopping, creating, replacing, or failed entry says so rather than a bare "offline".
function reachabilityStatus(e) {
  if (
    e.state === "deleted" ||
    e.state === "stopped" ||
    e.state === "stopping" ||
    e.state === "creating" ||
    e.state === "replacing" ||
    e.state === "failed"
  ) {
    return e.state;
  }
  return e.status;
}

function LifecycleBadge({ entry }) {
  let cls = "gs-badge";
  if (entry.state === "stopped") cls = "gs-badge reg-badge-stopped";
  else if (entry.state === "stopping") cls = "gs-badge gs-badge-warning";
  else if (entry.state === "failed" || entry.state === "deleted") {
    cls = "gs-badge gs-badge-danger";
  }
  return (
    <span className={cls} data-testid={`lifecycle-badge-${entry.name}`}>
      {entry.state}
      {entry.replace_step ? ` (${entry.replace_step})` : ""}
    </span>
  );
}

function StopEntryDialog({ target, onCancel, onConfirm }) {
  return (
    <ConfirmDialog
      open={Boolean(target)}
      title={`Stop ${target?.name || ""}?`}
      confirmLabel="Stop instance"
      onCancel={onCancel}
      onConfirm={onConfirm}
    >
      <p>
        Stopping <strong>{target?.name}</strong> lets running tasks drain, then
        shuts down and deletes its VM. Its database, secrets and config
        repository are kept, so starting it again (or visiting its URL, when
        auto-wake is on) boots a new VM with everything intact.
      </p>
    </ConfirmDialog>
  );
}

function parseRouteFromPathname(pathname) {
  const clean = (pathname || "/").replace(/\/+$/, "") || "/";
  if (clean === "/directory") return { page: "directory", entryName: "" };
  if (clean === "/new" || clean === "/create" || clean === "/register") {
    return { page: "new", entryName: "" };
  }
  if (clean === "/grants" || clean === "/apps") {
    return { page: "grants", entryName: "" };
  }
  if (clean === "/admin") return { page: "admin", entryName: "" };
  if (clean === "/status") return { page: "status", entryName: "" };
  if (clean.startsWith("/entries/")) {
    try {
      const name = decodeURIComponent(clean.slice("/entries/".length));
      if (name) return { page: "detail", entryName: name };
    } catch {
      return { page: "my-entries", entryName: "" };
    }
  }
  return { page: "my-entries", entryName: "" };
}

function formatTTLSeconds(sec) {
  const n = Number(sec) || 0;
  if (n <= 0) return "90d";
  if (n % 86400 === 0) return `${n / 86400}d`;
  if (n % 3600 === 0) return `${n / 3600}h`;
  if (n % 60 === 0) return `${n / 60}m`;
  return `${n}s`;
}

function formatShortTimestamp(iso) {
  if (!iso || typeof iso !== "string") return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return iso
    .replace(/\.\d+Z$/, "Z")
    .replace("T", " ")
    .replace(/:\d{2}Z$/, " UTC");
}

function DelegationStatePill({ state }) {
  const norm = String(state || "").toLowerCase();
  let cls = "gs-pill gs-pill-offline";
  if (norm === "enabled" || norm === "active") {
    cls = "gs-pill gs-pill-online";
  } else if (norm === "expired") {
    cls = "gs-pill gs-pill-degraded";
  } else if (norm === "disabled" || norm === "revoked") {
    cls = "gs-pill gs-pill-danger";
  }
  return (
    <span className={cls}>
      <span className="gs-pill-dot" aria-hidden="true" />
      <span>{norm}</span>
    </span>
  );
}

function safeHttpUrl(raw) {
  if (!raw || typeof raw !== "string") return "";
  try {
    const parsed = new URL(raw);
    if (parsed.protocol === "https:" || parsed.protocol === "http:") {
      return parsed.toString();
    }
  } catch {
    return "";
  }
  return "";
}

function safeHttpOrigin(raw) {
  if (!raw || typeof raw !== "string") return "";
  try {
    const parsed = new URL(raw);
    if (parsed.protocol === "https:" || parsed.protocol === "http:") {
      return parsed.origin;
    }
  } catch {
    return "";
  }
  return "";
}

function sanitizeSlug(raw, fallback) {
  const cleaned = String(raw || "")
    .toLowerCase()
    .replace(/[^a-z0-9.-]/g, "");
  return cleaned || fallback;
}

function makeRandomEntryName() {
  const suffix = Math.random().toString(16).slice(2, 8).padEnd(6, "a");
  return `grain-${suffix}`;
}

function buildConnectedSnippet(registryHost, instanceDomain, entryName, token) {
  const rawOrigin = registryHost
    ? registryHost.startsWith("http")
      ? registryHost
      : `https://${registryHost}`
    : typeof window !== "undefined"
      ? window.location.origin
      : "https://registry.grain.example.com";
  const regOrigin =
    safeHttpOrigin(rawOrigin) || "https://registry.grain.example.com";
  const instDom = sanitizeSlug(instanceDomain, "grain.example.com");
  const name =
    String(entryName || "")
      .toLowerCase()
      .replace(/[^a-z0-9-]/g, "") || "my-grain";
  const safeToken = String(token || "").replace(/[^A-Za-z0-9_.-]/g, "");
  const tokLine = safeToken ? `export GRAIN_HUB_TOKEN="${safeToken}"\n` : "";
  const cli =
    `${tokLine}grain daemon \\\n` +
    `  -hub-url ${regOrigin} \\\n` +
    `  -hub-entry ${name} \\\n` +
    `  -hub-connect \\\n` +
    `  -ui-require-verifier=registry \\\n` +
    `  -ui-hub-issuer grain-registry \\\n` +
    `  -ui-hub-audience https://${name}.${instDom} \\\n` +
    `  -ui-hub-keys-url ${regOrigin}/.well-known/jwks.json`;
  // The same configuration as the daemon's environment, one KEY=value per
  // line, for a systemd EnvironmentFile, docker --env-file, or a Kubernetes
  // env list: each flag above falls back to the variable named here
  // (pkg/cli/hub.go, pkg/cli/daemon.go, pkg/webauth/hub.go).
  const env =
    (safeToken ? `GRAIN_HUB_TOKEN=${safeToken}\n` : "") +
    `GRAIN_HUB_URL=${regOrigin}\n` +
    `GRAIN_HUB_ENTRY=${name}\n` +
    `GRAIN_HUB_CONNECT=true\n` +
    `GRAIN_REQUIRE_VERIFIER=registry\n` +
    `GRAIN_HUB_ISSUER=grain-registry\n` +
    `GRAIN_HUB_AUDIENCE=https://${name}.${instDom}\n` +
    `GRAIN_HUB_KEYS_URL=${regOrigin}/.well-known/jwks.json`;
  return { cli, env };
}

function DirectoryView({ onNavigateEntry }) {
  const [entries, setEntries] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [q, setQ] = useState("");
  const [repoFilter, setRepoFilter] = useState("");
  const [tagFilter, setTagFilter] = useState("");
  const [statusFilter, setStatusFilter] = useState("");

  const [routeRepo, setRouteRepo] = useState("acme/platform");
  const [routePath, setRoutePath] = useState("ui-hub/src/App.jsx");
  const [routeIncludeOffline, setRouteIncludeOffline] = useState(false);
  const [routeResult, setRouteResult] = useState(null);
  const [routeError, setRouteError] = useState("");
  const [routeLoading, setRouteLoading] = useState(false);

  const loadDirectory = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const res = await fetchDirectory({
        q,
        repo: repoFilter,
        tag: tagFilter,
        status: statusFilter,
      });
      setEntries(Array.isArray(res && res.entries) ? res.entries : []);
    } catch (err) {
      setError(err.message || "Failed to load directory");
    } finally {
      setLoading(false);
    }
  }, [q, repoFilter, tagFilter, statusFilter]);

  useEffect(() => {
    loadDirectory();
  }, [loadDirectory]);

  const handleTestRoute = async (e) => {
    if (e) e.preventDefault();
    if (!routeRepo.trim()) return;
    setRouteLoading(true);
    setRouteError("");
    try {
      const res = await fetchRoute({
        repo: routeRepo,
        path: routePath,
        includeOffline: routeIncludeOffline,
      });
      setRouteResult(res);
    } catch (err) {
      setRouteError(err.message || "Route lookup failed");
    } finally {
      setRouteLoading(false);
    }
  };

  return (
    <div>
      <PageHeader
        title="Organization Directory"
        subtitle="Discover listed grain deployments across repositories, teams, and runtime environments."
      />

      <section
        className="reg-section reg-card-subtle"
        aria-label="Find a grain for a path"
      >
        <h2 className="reg-section-title">Find a grain for a path</h2>
        <p className="reg-section-desc">
          Resolve which listed grain owns a repository and file path using
          longest-prefix scope precedence.
        </p>
        <form className="reg-route-form" onSubmit={handleTestRoute}>
          <div className="reg-field">
            <label className="reg-label" htmlFor="route-repo-input">
              Repository
            </label>
            <input
              id="route-repo-input"
              className="gs-input"
              placeholder="owner/repo (e.g. acme/platform)"
              value={routeRepo}
              onChange={(e) => setRouteRepo(e.target.value)}
            />
          </div>
          <div className="reg-field">
            <label className="reg-label" htmlFor="route-path-input">
              File or directory path
            </label>
            <input
              id="route-path-input"
              className="gs-input"
              placeholder="path/in/repo (optional)"
              value={routePath}
              onChange={(e) => setRoutePath(e.target.value)}
            />
          </div>
          <label className="reg-checkbox-row">
            <input
              type="checkbox"
              checked={routeIncludeOffline}
              onChange={(e) => setRouteIncludeOffline(e.target.checked)}
            />
            <span>Include offline</span>
          </label>
          <button
            type="submit"
            className="gs-btn gs-btn-primary"
            disabled={routeLoading || !routeRepo.trim()}
          >
            {routeLoading ? "Routing..." : "Resolve route"}
          </button>
        </form>

        {routeError ? (
          <div className="reg-banner reg-banner-danger" role="alert">
            <span>{routeError}</span>
          </div>
        ) : null}

        {routeResult ? (
          <div className="reg-stack">
            <div className="reg-meta-row">
              <span>
                Route candidates for{" "}
                <strong className="reg-mono">
                  {routeResult.repo}
                  {routeResult.path ? `/${routeResult.path}` : ""}
                </strong>
              </span>
              {routeResult.primary ? (
                <span className="gs-badge gs-badge-vm">
                  Primary match: {routeResult.primary.name}
                </span>
              ) : null}
            </div>
            <SortableTable
              emptyMessage="No matching listed entries cover that repository and path."
              rowKey={(r) => r.name}
              rows={routeResult.candidates || []}
              columns={[
                {
                  key: "name",
                  header: "Entry",
                  render: (r) => (
                    <button
                      type="button"
                      className="reg-inline-link"
                      onClick={() => onNavigateEntry(r.name)}
                    >
                      {r.display_name
                        ? `${r.display_name} (${r.name})`
                        : r.name}
                    </button>
                  ),
                },
                {
                  key: "status",
                  header: "Status",
                  render: (r) => (
                    <StatusPill
                      status={r.status}
                      ageSeconds={r.heartbeat_age_seconds}
                    />
                  ),
                },
                {
                  key: "scope",
                  header: "Matched Scope",
                  render: (r) => (
                    <span className="reg-mono">
                      {r.matched_scope?.repo}:/
                      {r.matched_scope?.path_prefix || ""} (specificity{" "}
                      {r.matched_scope?.specificity ?? 0})
                    </span>
                  ),
                },
                {
                  key: "role",
                  header: "Role & Source",
                  render: (r) => (
                    <div className="reg-badge-row">
                      <span
                        className={
                          r.matched_scope?.role === "primary"
                            ? "gs-badge gs-badge-vm"
                            : "gs-badge"
                        }
                      >
                        {r.matched_scope?.role || "primary"}
                      </span>
                      <span className="gs-badge">
                        {r.matched_scope?.source || "reported"}
                      </span>
                    </div>
                  ),
                },
                {
                  key: "open",
                  header: "Action",
                  render: (r) =>
                    safeHttpUrl(r.url) ? (
                      <a
                        className="gs-btn gs-btn-sm gs-btn-primary"
                        href={safeHttpUrl(r.url)}
                        target="_blank"
                        rel="noopener noreferrer"
                      >
                        Open grain
                      </a>
                    ) : null,
                },
              ]}
            />
          </div>
        ) : null}
      </section>

      <section className="reg-section">
        <FilterBar ariaLabel="Directory filters">
          <div className="reg-filter-item">
            <input
              className="gs-input"
              aria-label="Search directory"
              placeholder="Search name, purpose, owner, or repo..."
              value={q}
              onChange={(e) => setQ(e.target.value)}
            />
          </div>
          <div className="reg-filter-item">
            <input
              className="gs-input"
              aria-label="Filter by repository"
              placeholder="Filter by repo (e.g. acme/platform)"
              value={repoFilter}
              onChange={(e) => setRepoFilter(e.target.value)}
            />
          </div>
          <div className="reg-filter-item">
            <input
              className="gs-input"
              aria-label="Filter by tag"
              placeholder="Filter by tag (e.g. production)"
              value={tagFilter}
              onChange={(e) => setTagFilter(e.target.value)}
            />
          </div>
          <div className="reg-filter-item">
            <select
              className="gs-select"
              aria-label="Filter by status"
              value={statusFilter}
              onChange={(e) => setStatusFilter(e.target.value)}
            >
              <option value="">All statuses</option>
              <option value="online">online</option>
              <option value="degraded">degraded</option>
              <option value="offline">offline</option>
            </select>
          </div>
        </FilterBar>
      </section>

      {error ? (
        <div className="reg-banner reg-banner-danger" role="alert">
          <span>{error}</span>
        </div>
      ) : null}

      {loading ? (
        <div className="reg-card">Loading directory entries...</div>
      ) : entries.length === 0 ? (
        <EmptyState
          title="No directory entries matched"
          description="Try clearing your search or status filters."
        />
      ) : (
        <div className="reg-grid-cards">
          {entries.map((entry) => (
            <article
              key={entry.name}
              className="reg-dir-card"
              data-testid={`dir-card-${entry.name}`}
            >
              <div>
                <div className="reg-dir-card-head">
                  <div>
                    <h3 className="reg-dir-title">
                      {entry.display_name || entry.name}
                    </h3>
                    <div className="reg-dir-name">{entry.name}</div>
                  </div>
                  <StatusPill
                    status={entry.status}
                    ageSeconds={entry.heartbeat_age_seconds}
                  />
                </div>
                {entry.purpose ? (
                  <p className="reg-dir-purpose">{entry.purpose}</p>
                ) : null}
              </div>

              <div className="reg-badge-row">
                <HostKindBadge hostKind={entry.host_kind} kind={entry.kind} />
                {entry.grain_version ? (
                  <span className="gs-badge">{entry.grain_version}</span>
                ) : null}
                <span className="gs-badge">
                  {entry.active_tasks || 0} active /{" "}
                  {entry.awaiting_reply_tasks || 0} awaiting
                </span>
                {(entry.tags || []).map((t) => (
                  <span key={t} className="gs-badge">
                    #{t}
                  </span>
                ))}
              </div>

              {entry.scopes && entry.scopes.length > 0 ? (
                <div
                  className="reg-scope-list"
                  aria-label={`Scopes for ${entry.name}`}
                >
                  {entry.scopes.map((sc, idx) => (
                    <div
                      key={`${sc.repo}-${sc.path_prefix}-${idx}`}
                      className="reg-scope-item"
                    >
                      <span className="reg-mono">
                        {sc.repo}
                        {sc.path_prefix ? `/${sc.path_prefix}` : " (root)"}
                      </span>
                      <span
                        className={
                          (sc.effective_role || sc.role) === "primary"
                            ? "gs-badge gs-badge-vm"
                            : "gs-badge"
                        }
                      >
                        {sc.effective_role || sc.role}
                      </span>
                    </div>
                  ))}
                </div>
              ) : null}

              <div className="reg-meta-row">
                <span>
                  Owner: <strong>{entry.owner}</strong>
                  {entry.owner_group ? ` · ${entry.owner_group}` : ""}
                </span>
              </div>

              <div className="reg-actions-row">
                {safeHttpUrl(entry.url) ? (
                  <a
                    className="gs-btn gs-btn-sm gs-btn-primary"
                    href={safeHttpUrl(entry.url)}
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    Open grain
                  </a>
                ) : null}
                <button
                  type="button"
                  className="gs-btn gs-btn-sm"
                  onClick={() => onNavigateEntry(entry.name)}
                >
                  Details
                </button>
              </div>
            </article>
          ))}
        </div>
      )}
    </div>
  );
}

function MyEntriesView({ me, onNavigateEntry, onNavigateNew, onRefreshMe }) {
  const [entries, setEntries] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [actionNotice, setActionNotice] = useState("");
  const [deleteTarget, setDeleteTarget] = useState(null);
  const [deleteConfirmText, setDeleteConfirmText] = useState("");
  const [stopTarget, setStopTarget] = useState(null);

  const loadEntries = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      // Soft-deleted entries are hidden here; restoring one within its
      // 30-day hold is an admin action on the Admin page.
      const res = await fetchEntries({ mine: "1" });
      setEntries(
        Array.isArray(res && res.entries)
          ? res.entries.filter((e) => e && e.state !== "deleted")
          : [],
      );
    } catch (err) {
      setError(err.message || "Failed to load entries");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadEntries();
  }, [loadEntries]);

  const pendingTransfers = useMemo(
    () =>
      entries.filter(
        (e) =>
          e.pending_owner &&
          me?.email &&
          e.pending_owner.toLowerCase() === me.email.toLowerCase(),
      ),
    [entries, me],
  );

  const handleAcceptTransfer = async (name) => {
    setError("");
    try {
      await acceptTransfer(name);
      setActionNotice(`Accepted ownership transfer for ${name}.`);
      await loadEntries();
      if (onRefreshMe) onRefreshMe();
    } catch (err) {
      setError(err.message || "Failed to accept transfer");
    }
  };

  const handleQuickUpgrade = async (name) => {
    setError("");
    try {
      await upgradeEntry(name, "");
      setActionNotice(`Requested upgrade for ${name}.`);
      await loadEntries();
    } catch (err) {
      setError(err.message || "Failed to request upgrade");
    }
  };

  const handleConfirmStop = async () => {
    if (!stopTarget) return;
    const name = stopTarget.name;
    setError("");
    setStopTarget(null);
    try {
      await stopEntry(name);
      setActionNotice(`Requested stop for ${name}.`);
      await loadEntries();
    } catch (err) {
      setError(err.message || "Failed to stop instance");
    }
  };

  const handleQuickStart = async (name) => {
    setError("");
    try {
      await startEntry(name);
      setActionNotice(`Requested start for ${name}.`);
      await loadEntries();
    } catch (err) {
      setError(err.message || "Failed to start instance");
    }
  };

  const handleConfirmDelete = async () => {
    if (!deleteTarget) return;
    const name = deleteTarget.name;
    setError("");
    try {
      await deleteEntry(name, deleteConfirmText);
      setEntries((prev) => prev.filter((e) => e.name !== name));
      setActionNotice(`Deleted ${name}.`);
      setDeleteTarget(null);
      setDeleteConfirmText("");
      if (onRefreshMe) onRefreshMe();
    } catch (err) {
      setError(err.message || "Failed to delete entry");
    }
  };

  const quota = me?.quota || {
    quota_units_used: 0,
    quota_units_limit: 2,
    connected_used: 0,
    connected_limit: 20,
  };

  return (
    <div>
      <PageHeader
        title="My Entries"
        subtitle="Manage your hosted grain VMs, connected daemons, and co-owned team entries."
        actions={
          <button
            type="button"
            className="gs-btn gs-btn-primary"
            onClick={onNavigateNew}
          >
            Create / Register entry
          </button>
        }
      />

      <div className="reg-grid-stats" aria-label="Personal quota usage">
        <div className="reg-stat-card">
          <div className="reg-stat-label">Hosted VM Quota Units</div>
          <div className="reg-stat-value">
            {quota.quota_units_used} / {quota.quota_units_limit}
          </div>
          <div className="reg-stat-meta">
            Weighted VM shape units across active instances
          </div>
        </div>
        <div className="reg-stat-card">
          <div className="reg-stat-label">Connected Entries</div>
          <div className="reg-stat-value">
            {quota.connected_used} / {quota.connected_limit}
          </div>
          <div className="reg-stat-meta">
            Self-registered daemons over outbound tunnel
          </div>
        </div>
        <div className="reg-stat-card">
          <div className="reg-stat-label">Signed-in Principal</div>
          <div className="reg-stat-value">{me?.email || "anonymous"}</div>
          <div className="reg-stat-meta">
            {me?.is_registry_admin ? "Hub administrator" : "Standard creator"}
          </div>
        </div>
      </div>

      {pendingTransfers.map((tr) => (
        <div key={tr.name} className="reg-banner reg-banner-info">
          <div>
            <strong>Pending ownership transfer:</strong> {tr.owner} offered
            transfer of <span className="reg-mono">{tr.name}</span> to you (
            {tr.pending_owner}).
          </div>
          <div className="reg-actions-row">
            <button
              type="button"
              className="gs-btn gs-btn-sm gs-btn-primary"
              onClick={() => handleAcceptTransfer(tr.name)}
            >
              Accept transfer
            </button>
            <button
              type="button"
              className="gs-btn gs-btn-sm"
              onClick={() => onNavigateEntry(tr.name)}
            >
              Inspect entry
            </button>
          </div>
        </div>
      ))}

      {actionNotice ? (
        <div className="reg-banner reg-banner-success" role="status">
          <span>{actionNotice}</span>
        </div>
      ) : null}

      {error ? (
        <div className="reg-banner reg-banner-danger" role="alert">
          <span>{error}</span>
        </div>
      ) : null}

      {loading ? (
        <div className="reg-card">Loading your entries...</div>
      ) : (
        <SortableTable
          emptyMessage="You do not own or co-own any hub entries yet."
          rowKey={(e) => e.name}
          rows={entries}
          columns={[
            {
              key: "name",
              header: "Entry & Subdomain",
              sortValue: (e) => e.name,
              render: (e) => (
                <div>
                  <button
                    type="button"
                    className="reg-inline-link"
                    onClick={() => onNavigateEntry(e.name)}
                  >
                    {e.display_name ? `${e.display_name} (${e.name})` : e.name}
                  </button>
                  <div className="reg-dir-name">{e.url || e.name}</div>
                </div>
              ),
            },
            {
              key: "kind",
              header: "Kind & Lifecycle",
              sortValue: (e) => `${e.kind}-${e.state}`,
              render: (e) => (
                <div className="reg-badge-row">
                  <HostKindBadge hostKind={e.host_kind} kind={e.kind} />
                  <LifecycleBadge entry={e} />
                </div>
              ),
            },
            {
              key: "status",
              header: "Reachability",
              sortValue: (e) => e.status,
              render: (e) => (
                <div>
                  <StatusPill
                    status={reachabilityStatus(e)}
                    ageSeconds={e.heartbeat_age_seconds}
                    detail={e.status_detail}
                  />
                  {e.status_detail &&
                  (e.state === "creating" ||
                    e.state === "replacing" ||
                    e.state === "failed" ||
                    e.status === "degraded") ? (
                    <div
                      className="reg-hint reg-status-detail"
                      data-testid={`status-detail-${e.name}`}
                    >
                      {e.status_detail}
                    </div>
                  ) : null}
                </div>
              ),
            },
            {
              key: "version",
              header: "Version & Tasks",
              render: (e) => (
                <div>
                  <div className="reg-mono">{e.grain_version || "—"}</div>
                  <div className="reg-hint">
                    {e.active_tasks || 0} active · {e.awaiting_reply_tasks || 0}{" "}
                    awaiting
                  </div>
                </div>
              ),
            },
            {
              key: "visibility",
              header: "Catalog & Scopes",
              render: (e) => (
                <div className="reg-badge-row">
                  <span
                    className={e.listed ? "gs-badge gs-badge-vm" : "gs-badge"}
                  >
                    {e.listed ? "listed" : "unlisted"}
                  </span>
                  {e.scope_conflicts && e.scope_conflicts.length > 0 ? (
                    <span className="gs-badge gs-badge-warning">
                      conflict ({e.scope_conflicts[0].conflicting_entry})
                    </span>
                  ) : null}
                </div>
              ),
            },
            {
              key: "actions",
              header: "Quick Actions",
              render: (e) => (
                <div className="reg-actions-row">
                  {safeHttpUrl(e.url) && e.state !== "deleted" ? (
                    <a
                      className="gs-btn gs-btn-sm gs-btn-primary"
                      href={safeHttpUrl(e.url)}
                      target="_blank"
                      rel="noopener noreferrer"
                    >
                      Open
                    </a>
                  ) : null}
                  <button
                    type="button"
                    className="gs-btn gs-btn-sm"
                    onClick={() => onNavigateEntry(e.name)}
                  >
                    Manage
                  </button>
                  {e.kind === "instance" &&
                  e.state !== "deleted" &&
                  !canStartEntry(e) ? (
                    <>
                      <button
                        type="button"
                        className="gs-btn gs-btn-sm"
                        onClick={() => handleQuickUpgrade(e.name)}
                      >
                        Upgrade
                      </button>
                      <button
                        type="button"
                        className="gs-btn gs-btn-sm"
                        onClick={() => onNavigateEntry(e.name)}
                      >
                        Resize
                      </button>
                    </>
                  ) : null}
                  {canStopEntry(e) ? (
                    <button
                      type="button"
                      className="gs-btn gs-btn-sm"
                      aria-label={`Stop ${e.name}`}
                      onClick={() => setStopTarget(e)}
                    >
                      Stop
                    </button>
                  ) : null}
                  {canStartEntry(e) ? (
                    <button
                      type="button"
                      className="gs-btn gs-btn-sm gs-btn-primary"
                      aria-label={`Start ${e.name}`}
                      onClick={() => handleQuickStart(e.name)}
                    >
                      Start
                    </button>
                  ) : null}
                  <button
                    type="button"
                    className="gs-btn gs-btn-sm gs-btn-danger"
                    onClick={() => {
                      setDeleteTarget(e);
                      setDeleteConfirmText("");
                    }}
                  >
                    Delete
                  </button>
                </div>
              ),
            },
          ]}
        />
      )}

      <StopEntryDialog
        target={stopTarget}
        onCancel={() => setStopTarget(null)}
        onConfirm={handleConfirmStop}
      />

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        title={`Delete ${deleteTarget?.name || ""}?`}
        confirmLabel="Delete entry"
        danger
        disabled={
          !deleteTarget || deleteConfirmText.trim() !== deleteTarget.name
        }
        onCancel={() => {
          setDeleteTarget(null);
          setDeleteConfirmText("");
        }}
        onConfirm={handleConfirmDelete}
      >
        <p>
          Deleting <strong>{deleteTarget?.name}</strong> stops routing traffic
          and removes it from your entries. Its name stays reserved for you for
          30 days, and a hosted instance can still be restored (
          <code>grain hub restore</code> or a hub administrator) until its
          retained data is purged.
        </p>
        <div className="reg-field">
          <label className="reg-label" htmlFor="confirm-delete-input">
            Type <span className="reg-mono">{deleteTarget?.name}</span> to
            confirm
          </label>
          <input
            id="confirm-delete-input"
            className="gs-input"
            value={deleteConfirmText}
            onChange={(e) => setDeleteConfirmText(e.target.value)}
          />
        </div>
      </ConfirmDialog>
    </div>
  );
}

function CreateRegisterView({
  me,
  shapes,
  shapesLoaded,
  defaultShape,
  onCreated,
  onNavigateEntry,
}) {
  // Creating a hosted VM is the default; ?kind=connected selects
  // registering a self-hosted daemon instead.
  const initialKind =
    typeof window !== "undefined" &&
    new URLSearchParams(window.location.search).get("kind") === "connected"
      ? "connected"
      : "instance";
  const [kind, setKind] = useState(initialKind);
  const [name, setName] = useState(() => makeRandomEntryName());
  const [availability, setAvailability] = useState(null);
  const [displayName, setDisplayName] = useState("");
  const [purpose, setPurpose] = useState("");
  const [ownerGroup, setOwnerGroup] = useState("");
  const [listed, setListed] = useState(true);
  const [tags, setTags] = useState("");
  const [shape, setShape] = useState(defaultShape || "medium");
  const [bootDiskGb, setBootDiskGb] = useState(() => {
    const initial = (shapes || []).find(
      (s) => s.name === (defaultShape || "medium"),
    );
    return initial?.boot_disk_gb ? String(initial.boot_disk_gb) : "";
  });
  const [bootDiskDirty, setBootDiskDirty] = useState(false);
  const [hostKind, setHostKind] = useState("workstation");
  const [connectedPrincipal, setConnectedPrincipal] = useState("");
  const [scopeRepo, setScopeRepo] = useState("");
  const [scopePrefix, setScopePrefix] = useState("");
  const [scopeRole, setScopeRole] = useState("primary");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const [createdResult, setCreatedResult] = useState(null);

  useEffect(() => {
    if (!shapes || shapes.length === 0) return;
    let effectiveShape = shape;
    if (!shapes.some((s) => s.name === effectiveShape)) {
      effectiveShape =
        (defaultShape && shapes.some((s) => s.name === defaultShape)
          ? defaultShape
          : shapes[0].name) || shape;
      setShape(effectiveShape);
    }
    if (!bootDiskDirty) {
      const matched = shapes.find((s) => s.name === effectiveShape);
      if (matched?.boot_disk_gb) {
        setBootDiskGb(String(matched.boot_disk_gb));
      }
    }
  }, [shapes, defaultShape, shape, bootDiskDirty]);

  const handleShapeSelectChange = (nextShape) => {
    setShape(nextShape);
    setBootDiskDirty(false);
    const matched = (shapes || []).find((s) => s.name === nextShape);
    if (matched?.boot_disk_gb) {
      setBootDiskGb(String(matched.boot_disk_gb));
    }
  };

  // A registry with no VM shapes cannot create hosted instances, so the
  // form falls back to registering a connected daemon once shapes load.
  const fellBackRef = useRef(false);
  useEffect(() => {
    if (fellBackRef.current || !shapesLoaded) return;
    fellBackRef.current = true;
    if (kind === "instance" && (!shapes || shapes.length === 0)) {
      setKind("connected");
    }
  }, [shapesLoaded, shapes, kind]);

  useEffect(() => {
    const trimmed = name.trim().toLowerCase();
    if (!trimmed) {
      setAvailability(null);
      return undefined;
    }
    let cancelled = false;
    fetchNameAvailability(trimmed)
      .then((res) => {
        if (!cancelled) setAvailability(res);
      })
      .catch(() => {
        if (!cancelled) setAvailability(null);
      });
    return () => {
      cancelled = true;
    };
  }, [name]);

  const snippet = useMemo(
    () =>
      buildConnectedSnippet(
        me?.registry_host,
        me?.instance_domain,
        createdResult?.entry?.name || name.trim().toLowerCase(),
        createdResult?.registration_token,
      ),
    [me, createdResult, name],
  );

  const handleSubmit = async (e) => {
    e.preventDefault();
    setSubmitting(true);
    setError("");
    try {
      const tagList = tags
        .split(",")
        .map((t) => t.trim().toLowerCase())
        .filter(Boolean);
      const scopes = scopeRepo.trim()
        ? [
            {
              repo: scopeRepo.trim(),
              path_prefix: scopePrefix.trim().replace(/^\/+|\/+$/g, ""),
              role: scopeRole,
            },
          ]
        : [];
      const payload = {
        name: name.trim().toLowerCase(),
        kind,
        display_name: displayName.trim(),
        purpose: purpose.trim(),
        owner_group: ownerGroup.trim(),
        listed,
        tags: tagList,
        scopes,
      };
      if (kind === "instance") {
        payload.shape = shape || defaultShape || "medium";
        const parsedDisk = Number.parseInt(String(bootDiskGb).trim(), 10);
        if (Number.isFinite(parsedDisk) && parsedDisk > 0) {
          payload.boot_disk_gb = parsedDisk;
        }
      } else {
        payload.host_kind = hostKind;
        payload.connected_principal = connectedPrincipal.trim();
      }
      const res = await createEntry(payload);
      setCreatedResult(res);
      if (onCreated) onCreated();
    } catch (err) {
      setError(err.message || "Failed to create entry");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div>
      <PageHeader
        title="Create or Register a Grain Entry"
        subtitle="Provision a managed GCE VM instance or register a self-hosted grain daemon over an outbound tunnel."
      />

      {createdResult ? (
        <div className="reg-card reg-stack" data-testid="created-entry-banner">
          <div className="reg-banner reg-banner-success">
            <div>
              <strong>Created {createdResult.entry.name}</strong> (
              {createdResult.entry.kind}) at{" "}
              <span className="reg-mono">{createdResult.entry.url}</span>
            </div>
            <button
              type="button"
              className="gs-btn gs-btn-primary"
              onClick={() => onNavigateEntry(createdResult.entry.name)}
            >
              Open entry detail
            </button>
          </div>
          {createdResult.registration_token ? (
            <div className="reg-stack">
              <div className="reg-banner reg-banner-warning">
                <span>
                  <strong>One-time registration token (`grc_…`):</strong> Save
                  this token now; the hub stores only its SHA-256 hash and
                  cannot display it again.
                </span>
              </div>
              <CopyBlock
                label="One-time grc_… Registration Credential"
                code={createdResult.registration_token}
              />
              <CopyBlock
                label="CLI Flag Snippet (-hub-connect)"
                code={snippet.cli}
              />
              <CopyBlock
                label="Environment Snippet (systemd EnvironmentFile, docker --env-file)"
                code={snippet.env}
              />
            </div>
          ) : null}
        </div>
      ) : null}

      <div
        className="reg-mode-picker"
        role="radiogroup"
        aria-label="Entry kind"
      >
        <button
          type="button"
          className={
            kind === "instance"
              ? "reg-mode-card reg-mode-card-active"
              : "reg-mode-card"
          }
          onClick={() => setKind("instance")}
        >
          <div className="reg-mode-title">Hosted VM (instance)</div>
          <div className="reg-mode-desc">
            Provision a dedicated GCE VM and Cloud SQL database managed by the
            hub and routed over private VPC HTTP.
          </div>
        </button>
        <button
          type="button"
          className={
            kind === "connected"
              ? "reg-mode-card reg-mode-card-active"
              : "reg-mode-card"
          }
          onClick={() => setKind("connected")}
        >
          <div className="reg-mode-title">
            Self-registered grain (connected)
          </div>
          <div className="reg-mode-desc">
            Connect an existing workstation, Kubernetes pod, or server daemon
            over an outbound WebSocket-mux tunnel (<code>-hub-connect</code>).
            No inbound firewall port required.
          </div>
        </button>
      </div>

      {error ? (
        <div className="reg-banner reg-banner-danger" role="alert">
          <span>{error}</span>
        </div>
      ) : null}

      <form className="reg-card reg-stack" onSubmit={handleSubmit}>
        <div className="reg-form-grid">
          <div className="reg-field">
            <label className="reg-label" htmlFor="entry-name-input">
              Entry subdomain name
            </label>
            <div className="reg-input-with-action">
              <input
                id="entry-name-input"
                className="gs-input reg-mono"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="e.g. team-platform"
                spellCheck={false}
                autoComplete="off"
              />
              <button
                type="button"
                className="gs-btn gs-btn-sm"
                aria-label="Generate another random name"
                title="Generate another random name"
                onClick={() => setName(makeRandomEntryName())}
              >
                Regenerate
              </button>
            </div>
            <div className="reg-hint">
              URL:{" "}
              <span className="reg-mono">
                https://{name.trim().toLowerCase() || "<name>"}.
                {me?.instance_domain || "grain.example.com"}
              </span>
              {availability ? (
                <span>
                  {" "}
                  · Availability:{" "}
                  <strong data-testid="name-availability-status">
                    {availability.status}
                  </strong>
                  {availability.reason ? ` (${availability.reason})` : ""}
                </span>
              ) : null}
            </div>
          </div>

          <div className="reg-field">
            <label className="reg-label" htmlFor="display-name-input">
              Display name
            </label>
            <input
              id="display-name-input"
              className="gs-input"
              placeholder="Human-readable title in Directory"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
            />
          </div>

          <div className="reg-field">
            <label className="reg-label" htmlFor="owner-group-input">
              Co-owning group (optional)
            </label>
            <input
              id="owner-group-input"
              className="gs-input"
              placeholder="group:platform-eng@example.com"
              value={ownerGroup}
              onChange={(e) => setOwnerGroup(e.target.value)}
            />
          </div>

          <div className="reg-field">
            <label className="reg-label" htmlFor="tags-input">
              Tags (comma-separated)
            </label>
            <input
              id="tags-input"
              className="gs-input"
              placeholder="platform, production"
              value={tags}
              onChange={(e) => setTags(e.target.value)}
            />
          </div>
        </div>

        <div className="reg-field">
          <label className="reg-label" htmlFor="purpose-input">
            Purpose description
          </label>
          <textarea
            id="purpose-input"
            className="gs-textarea"
            rows={2}
            placeholder="Describe which repositories or workflows this grain serves..."
            value={purpose}
            onChange={(e) => setPurpose(e.target.value)}
          />
        </div>

        {kind === "instance" ? (
          <div className="reg-form-grid">
            <div className="reg-field">
              <label className="reg-label" htmlFor="shape-select">
                VM Shape
              </label>
              <select
                id="shape-select"
                className="gs-select"
                value={shape}
                onChange={(e) => handleShapeSelectChange(e.target.value)}
              >
                {(shapes || []).map((sh) => (
                  <option key={sh.name} value={sh.name}>
                    {sh.display_name || sh.name} — {sh.machine_type},{" "}
                    {sh.boot_disk_gb} GB disk ({sh.quota_units} quota unit
                    {sh.quota_units === 1 ? "" : "s"})
                  </option>
                ))}
              </select>
            </div>
            <div className="reg-field">
              <label className="reg-label" htmlFor="boot-disk-gb-input">
                Boot disk size (GB)
              </label>
              <input
                id="boot-disk-gb-input"
                type="number"
                min={20}
                max={4000}
                className="gs-input"
                value={bootDiskGb}
                onChange={(e) => {
                  setBootDiskGb(e.target.value);
                  setBootDiskDirty(true);
                }}
                placeholder="100"
              />
              <div className="reg-hint">
                20–4000 GB (defaults to shape&apos;s recommended size)
              </div>
            </div>
          </div>
        ) : (
          <div className="reg-form-grid">
            <div className="reg-field">
              <label className="reg-label" htmlFor="host-kind-select">
                Host environment kind
              </label>
              <select
                id="host-kind-select"
                className="gs-select"
                value={hostKind}
                onChange={(e) => setHostKind(e.target.value)}
              >
                <option value="workstation">workstation</option>
                <option value="kubernetes">kubernetes</option>
                <option value="other">other</option>
              </select>
            </div>
            <div className="reg-field">
              <label className="reg-label" htmlFor="connected-principal-input">
                Connected workload principal (optional)
              </label>
              <input
                id="connected-principal-input"
                className="gs-input"
                placeholder="service-account@project.iam.gserviceaccount.com"
                value={connectedPrincipal}
                onChange={(e) => setConnectedPrincipal(e.target.value)}
              />
            </div>
          </div>
        )}

        <div className="reg-form-grid">
          <div className="reg-field">
            <label className="reg-label" htmlFor="initial-scope-repo">
              Initial declared scope repository (optional)
            </label>
            <input
              id="initial-scope-repo"
              className="gs-input"
              placeholder="acme/platform"
              value={scopeRepo}
              onChange={(e) => setScopeRepo(e.target.value)}
            />
          </div>
          <div className="reg-field">
            <label className="reg-label" htmlFor="initial-scope-prefix">
              Scope path prefix (optional)
            </label>
            <input
              id="initial-scope-prefix"
              className="gs-input"
              placeholder="pkg/hub"
              value={scopePrefix}
              onChange={(e) => setScopePrefix(e.target.value)}
            />
          </div>
          <div className="reg-field">
            <label className="reg-label" htmlFor="initial-scope-role">
              Scope role
            </label>
            <select
              id="initial-scope-role"
              className="gs-select"
              value={scopeRole}
              onChange={(e) => setScopeRole(e.target.value)}
            >
              <option value="primary">primary</option>
              <option value="secondary">secondary</option>
            </select>
          </div>
        </div>

        <label className="reg-checkbox-row">
          <input
            type="checkbox"
            checked={listed}
            onChange={(e) => setListed(e.target.checked)}
          />
          <span>
            List in organization Directory (`/directory` and `/api/route`)
          </span>
        </label>

        {kind === "connected" ? (
          <div className="reg-stack">
            <CopyBlock
              label="Outbound Tunnel CLI Configuration Preview (-hub-connect)"
              code={snippet.cli}
            />
          </div>
        ) : null}

        <div className="reg-actions-row">
          <button
            type="submit"
            className="gs-btn gs-btn-primary"
            disabled={submitting}
          >
            {submitting
              ? "Saving..."
              : kind === "instance"
                ? "Create hosted VM"
                : "Register connected entry"}
          </button>
        </div>
      </form>
    </div>
  );
}

function EntryDetailView({
  entryName,
  me,
  shapes,
  onNavigateEntry,
  onBack,
  onRefreshMe,
}) {
  const [entry, setEntry] = useState(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [conflictBanner, setConflictBanner] = useState(null);
  const [notice, setNotice] = useState("");

  // Catalog form state
  const [displayName, setDisplayName] = useState("");
  const [purpose, setPurpose] = useState("");
  const [hostKind, setHostKind] = useState("workstation");
  const [connectedPrincipal, setConnectedPrincipal] = useState("");
  const [ownerGroup, setOwnerGroup] = useState("");
  const [tagsText, setTagsText] = useState("");
  const [listed, setListed] = useState(false);

  // Declared scopes editor state
  const [declaredRows, setDeclaredRows] = useState([]);
  const [newScopeRepo, setNewScopeRepo] = useState("");
  const [newScopePrefix, setNewScopePrefix] = useState("");
  const [newScopeRole, setNewScopeRole] = useState("primary");

  // Transfer & lifecycle state
  const [transferTo, setTransferTo] = useState("");
  const [clearOwnerGroupOnTransfer, setClearOwnerGroupOnTransfer] =
    useState(false);
  const [resizeShape, setResizeShape] = useState("");
  const [resizeDiskGb, setResizeDiskGb] = useState("");
  const [upgradeVersion, setUpgradeVersion] = useState("");
  const [rotateLabel, setRotateLabel] = useState("rotated-token");
  const [revokePrevOnRotate, setRevokePrevOnRotate] = useState(false);
  const [mintedToken, setMintedToken] = useState("");
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false);
  const [deleteConfirmText, setDeleteConfirmText] = useState("");
  const [stopDialogOpen, setStopDialogOpen] = useState(false);
  const [revokeCredTarget, setRevokeCredTarget] = useState(null);

  const syncFromEntry = (data) => {
    setEntry(data);
    setDisplayName(data.display_name || "");
    setPurpose(data.purpose || "");
    setHostKind(data.host_kind || "workstation");
    setConnectedPrincipal(data.connected_principal || "");
    setOwnerGroup(data.owner_group || "");
    setTagsText((data.tags || []).join(", "));
    setListed(Boolean(data.listed));
    const nextShape = data.shape || "medium";
    setResizeShape(nextShape);
    const shapeDefaultDisk = (shapes || []).find(
      (s) => s.name === nextShape,
    )?.boot_disk_gb;
    const effectiveDisk = data.boot_disk_gb || shapeDefaultDisk || "";
    setResizeDiskGb(effectiveDisk ? String(effectiveDisk) : "");
    setUpgradeVersion(data.target_version || "");
    const rawDeclared =
      data.declared_scopes ||
      (data.scopes || []).filter((s) => s.source === "declared");
    setDeclaredRows(
      rawDeclared.map((s) => ({
        repo: s.repo,
        path_prefix: s.path_prefix || "",
        role: s.role || "primary",
      })),
    );
  };

  const loadEntry = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const data = await fetchEntry(entryName);
      syncFromEntry(data);
    } catch (err) {
      setError(err.message || `Failed to load ${entryName}`);
    } finally {
      setLoading(false);
    }
  }, [entryName]);

  useEffect(() => {
    loadEntry();
  }, [loadEntry]);

  const handleSaveCatalog = async (e) => {
    e.preventDefault();
    setError("");
    setConflictBanner(null);
    setNotice("");
    try {
      const tags = tagsText
        .split(",")
        .map((t) => t.trim().toLowerCase())
        .filter(Boolean);
      const payload = {
        display_name: displayName.trim(),
        purpose: purpose.trim(),
        owner_group: ownerGroup.trim(),
        listed,
        tags,
      };
      if (entry.kind === "connected") {
        payload.host_kind = hostKind;
        payload.connected_principal = connectedPrincipal.trim();
      }
      const updated = await updateEntry(entry.name, payload);
      syncFromEntry(updated);
      setNotice("Saved catalog and visibility settings.");
    } catch (err) {
      if (err.status === 409 && err.conflict) {
        setConflictBanner(err.conflict);
      }
      setError(err.message || "Failed to update entry");
    }
  };

  const handleAddDeclaredScope = () => {
    if (!newScopeRepo.trim()) return;
    setDeclaredRows((prev) => [
      ...prev,
      {
        repo: newScopeRepo.trim(),
        path_prefix: newScopePrefix.trim().replace(/^\/+|\/+$/g, ""),
        role: newScopeRole,
      },
    ]);
    setNewScopeRepo("");
    setNewScopePrefix("");
  };

  const handleRemoveDeclaredScope = (idx) => {
    setDeclaredRows((prev) => prev.filter((_, i) => i !== idx));
  };

  const handleSaveScopes = async () => {
    setError("");
    setConflictBanner(null);
    setNotice("");
    try {
      await setEntryScopes(entry.name, declaredRows);
      await loadEntry();
      setNotice("Saved declared scope overrides.");
    } catch (err) {
      if (err.status === 409 && err.conflict) {
        setConflictBanner(err.conflict);
      }
      setError(err.message || "Failed to update scopes");
    }
  };

  const handleRotateCredential = async () => {
    setError("");
    setNotice("");
    try {
      const res = await rotateCredential(entry.name, {
        label: rotateLabel.trim() || "rotated",
        revoke_previous: revokePrevOnRotate,
      });
      setMintedToken(res.registration_token || "");
      await loadEntry();
      setNotice("Minted new grc_… registration credential.");
    } catch (err) {
      setError(err.message || "Failed to rotate credential");
    }
  };

  const handleConfirmRevokeCredential = async () => {
    if (!revokeCredTarget) return;
    const credId = revokeCredTarget.credential_id;
    setRevokeCredTarget(null);
    setError("");
    setNotice("");
    try {
      await revokeCredential(entry.name, credId);
      await loadEntry();
      setNotice(`Revoked credential ${credId}.`);
    } catch (err) {
      setError(err.message || "Failed to revoke credential");
    }
  };

  const handleOfferTransfer = async (e) => {
    e.preventDefault();
    if (!transferTo.trim()) return;
    setError("");
    setNotice("");
    try {
      const updated = await offerTransfer(entry.name, {
        to: transferTo.trim(),
        clear_owner_group: clearOwnerGroupOnTransfer,
      });
      syncFromEntry(updated);
      setTransferTo("");
      setClearOwnerGroupOnTransfer(false);
      setNotice(`Offered ownership transfer to ${updated.pending_owner}.`);
    } catch (err) {
      setError(err.message || "Failed to offer transfer");
    }
  };

  const handleCancelTransfer = async () => {
    setError("");
    setNotice("");
    try {
      const updated = await cancelTransfer(entry.name);
      syncFromEntry(updated);
      setNotice("Cancelled pending ownership transfer offer.");
    } catch (err) {
      setError(err.message || "Failed to cancel transfer offer");
    }
  };

  const handleAcceptTransfer = async () => {
    setError("");
    setNotice("");
    try {
      const updated = await acceptTransfer(entry.name);
      syncFromEntry(updated);
      setNotice(`Accepted ownership transfer for ${updated.name}.`);
      if (onRefreshMe) onRefreshMe();
    } catch (err) {
      setError(err.message || "Failed to accept transfer");
    }
  };

  const handleResizeOrReplace = async () => {
    setError("");
    setNotice("");
    try {
      const payload = {
        shape: resizeShape,
        target_version: upgradeVersion.trim(),
      };
      const parsedDisk = Number.parseInt(String(resizeDiskGb).trim(), 10);
      if (Number.isFinite(parsedDisk) && parsedDisk > 0) {
        payload.boot_disk_gb = parsedDisk;
      }
      const updated = await replaceEntry(entry.name, payload);
      syncFromEntry(updated);
      setNotice(
        `Initiated VM replacement (shape=${updated.shape}${
          updated.boot_disk_gb ? `, disk=${updated.boot_disk_gb} GB` : ""
        }).`,
      );
    } catch (err) {
      setError(err.message || "Failed to replace instance");
    }
  };

  const handleUpgrade = async () => {
    setError("");
    setNotice("");
    try {
      const updated = await upgradeEntry(entry.name, upgradeVersion.trim());
      syncFromEntry(updated);
      setNotice(`Requested upgrade to ${updated.target_version || "latest"}.`);
    } catch (err) {
      setError(err.message || "Failed to upgrade instance");
    }
  };

  const handleStopConfirm = async () => {
    setError("");
    setNotice("");
    setStopDialogOpen(false);
    try {
      const updated = await stopEntry(entry.name);
      syncFromEntry(updated);
      setNotice(`Requested stop for ${updated.name}.`);
    } catch (err) {
      setError(err.message || "Failed to stop instance");
    }
  };

  const handleStart = async () => {
    setError("");
    setNotice("");
    try {
      const updated = await startEntry(entry.name);
      syncFromEntry(updated);
      setNotice(`Requested start for ${updated.name}.`);
    } catch (err) {
      setError(err.message || "Failed to start instance");
    }
  };

  const handleRestore = async () => {
    setError("");
    setNotice("");
    try {
      const updated = await restoreEntry(entry.name);
      syncFromEntry(updated);
      setNotice(`Restored ${updated.name}.`);
      if (onRefreshMe) onRefreshMe();
    } catch (err) {
      setError(err.message || "Failed to restore entry");
    }
  };

  const handleDeleteConfirm = async () => {
    setError("");
    try {
      await deleteEntry(entry.name, deleteConfirmText);
      setDeleteDialogOpen(false);
      await loadEntry();
      setNotice(`Soft-deleted ${entry.name}.`);
      if (onRefreshMe) onRefreshMe();
    } catch (err) {
      setError(err.message || "Failed to delete entry");
    }
  };

  if (loading) {
    return <div className="reg-card">Loading entry {entryName}...</div>;
  }
  if (!entry) {
    return (
      <EmptyState
        title={`Entry "${entryName}" not found`}
        description={error || "The requested hub entry does not exist."}
        action={
          <button type="button" className="gs-btn" onClick={onBack}>
            Back to entries
          </button>
        }
      />
    );
  }

  const reportedScopes =
    entry.reported_scopes ||
    (entry.scopes || []).filter((s) => s.source === "reported");
  const snippet = buildConnectedSnippet(
    me?.registry_host,
    me?.instance_domain,
    entry.name,
    mintedToken,
  );

  return (
    <div>
      <PageHeader
        title={
          entry.display_name
            ? `${entry.display_name} (${entry.name})`
            : entry.name
        }
        subtitle={entry.url || `Entry kind: ${entry.kind}`}
        actions={
          <>
            <button type="button" className="gs-btn" onClick={onBack}>
              Back
            </button>
            {canStopEntry(entry) ? (
              <button
                type="button"
                className="gs-btn"
                onClick={() => setStopDialogOpen(true)}
              >
                Stop instance
              </button>
            ) : null}
            {canStartEntry(entry) ? (
              <button
                type="button"
                className="gs-btn gs-btn-primary"
                onClick={handleStart}
              >
                Start instance
              </button>
            ) : null}
            {safeHttpUrl(entry.url) && entry.state !== "deleted" ? (
              <a
                className="gs-btn gs-btn-primary"
                href={safeHttpUrl(entry.url)}
                target="_blank"
                rel="noopener noreferrer"
              >
                Open grain
              </a>
            ) : null}
          </>
        }
      />

      {notice ? (
        <div className="reg-banner reg-banner-success" role="status">
          <span>{notice}</span>
        </div>
      ) : null}

      {error ? (
        <div className="reg-banner reg-banner-danger" role="alert">
          <span>{error}</span>
        </div>
      ) : null}

      {conflictBanner ? (
        <div className="reg-banner reg-banner-warning" role="alert">
          <div>
            <strong>Primary scope conflict on {conflictBanner.repo}:</strong>{" "}
            Listed entry{" "}
            <button
              type="button"
              className="reg-inline-link"
              onClick={() => onNavigateEntry(conflictBanner.conflicting_entry)}
            >
              {conflictBanner.conflicting_entry}
            </button>{" "}
            already holds primary claim on{" "}
            <span className="reg-mono">
              /{conflictBanner.conflicting_prefix || ""}
            </span>
            . Demote that scope to <code>secondary</code> or narrow your path
            prefix before listing.
          </div>
        </div>
      ) : null}

      {entry.scope_conflicts && entry.scope_conflicts.length > 0 ? (
        <div
          className="reg-banner reg-banner-warning"
          data-testid="scope-conflict-banner"
        >
          <div>
            <strong>Active scope conflict:</strong>{" "}
            {entry.scope_conflicts.map((cf, i) => (
              <span key={`${cf.repo}-${i}`}>
                Primary claim on{" "}
                <span className="reg-mono">
                  {cf.repo}/{cf.path_prefix || ""}
                </span>{" "}
                is inactive because listed entry{" "}
                <button
                  type="button"
                  className="reg-inline-link"
                  onClick={() => onNavigateEntry(cf.conflicting_entry)}
                >
                  {cf.conflicting_entry}
                </button>{" "}
                holds an overlapping primary claim.
              </span>
            ))}
          </div>
        </div>
      ) : null}

      {entry.kind === "instance" &&
      (entry.state === "stopped" || entry.state === "stopping") ? (
        <div
          className="reg-banner reg-banner-info"
          data-testid="stopped-banner"
        >
          <div>
            <strong>
              {entry.state === "stopped"
                ? "This instance is stopped."
                : "This instance is stopping."}
            </strong>{" "}
            {entry.state === "stopped"
              ? "No VM is running; its database, secrets and config repository are kept."
              : "Running tasks are draining before its VM is shut down; its state is kept."}{" "}
            {entry.auto_wake
              ? "Start it here, or visit its URL to wake it."
              : "Auto-wake is off, so start it here to bring it back."}
          </div>
        </div>
      ) : null}

      {entry.status_detail &&
      (entry.state === "creating" ||
        entry.state === "replacing" ||
        entry.state === "failed") ? (
        <div
          className={
            entry.state === "failed"
              ? "reg-banner reg-banner-danger"
              : "reg-banner reg-banner-info"
          }
          data-testid="lifecycle-status-banner"
          role={entry.state === "failed" ? "alert" : "status"}
        >
          <div className="reg-banner-body">
            <strong>
              {entry.state === "creating"
                ? "Provisioning instance:"
                : entry.state === "replacing"
                  ? "Replacing instance:"
                  : "Instance provisioning failed:"}
            </strong>{" "}
            {entry.status_detail}
          </div>
        </div>
      ) : null}

      {entry.pending_owner ? (
        <div
          className="reg-banner reg-banner-info"
          data-testid="pending-transfer-banner"
        >
          <div>
            <strong>Pending ownership transfer to {entry.pending_owner}</strong>
            {entry.transfer_expires_at ? (
              <span className="reg-hint">
                {" "}
                · expires {entry.transfer_expires_at}
              </span>
            ) : null}
          </div>
          <div className="reg-actions-row">
            {me?.email &&
            entry.pending_owner.toLowerCase() === me.email.toLowerCase() ? (
              <button
                type="button"
                className="gs-btn gs-btn-sm gs-btn-primary"
                onClick={handleAcceptTransfer}
              >
                Accept transfer
              </button>
            ) : null}
            <button
              type="button"
              className="gs-btn gs-btn-sm"
              onClick={handleCancelTransfer}
            >
              Cancel transfer offer
            </button>
          </div>
        </div>
      ) : null}

      {/* Live runtime & tunnel status section */}
      <section className="reg-section reg-card">
        <h2 className="reg-section-title">
          Runtime, Heartbeat & Tunnel Status
        </h2>
        <div className="reg-kv-grid">
          <div className="reg-kv-item">
            <div className="reg-kv-label">Reachability Status</div>
            <div className="reg-kv-val">
              <StatusPill
                status={reachabilityStatus(entry)}
                ageSeconds={entry.heartbeat_age_seconds}
              />
            </div>
          </div>
          <div className="reg-kv-item">
            <div className="reg-kv-label">Kind & Host Environment</div>
            <div className="reg-kv-val">
              <HostKindBadge hostKind={entry.host_kind} kind={entry.kind} /> (
              {entry.kind})
            </div>
          </div>
          <div className="reg-kv-item">
            <div className="reg-kv-label">Lifecycle State</div>
            <div className="reg-kv-val">
              <LifecycleBadge entry={entry} />
            </div>
          </div>
          {entry.kind === "connected" ? (
            <div className="reg-kv-item">
              <div className="reg-kv-label">Outbound Tunnel</div>
              <div className="reg-kv-val" data-testid="tunnel-status-badge">
                <span
                  className={
                    entry.tunnel_connected
                      ? "gs-pill gs-pill-online"
                      : "gs-pill gs-pill-offline"
                  }
                >
                  <span className="gs-pill-dot" />
                  {entry.tunnel_connected ? "connected" : "disconnected"}
                </span>
              </div>
            </div>
          ) : (
            <div className="reg-kv-item">
              <div className="reg-kv-label">VM Shape & Zone</div>
              <div className="reg-kv-val">
                {entry.shape || "default"} {entry.zone ? `(${entry.zone})` : ""}
                {entry.boot_disk_gb ? (
                  <div className="reg-hint">
                    {entry.boot_disk_gb} GB boot disk
                  </div>
                ) : null}
              </div>
            </div>
          )}
          <div className="reg-kv-item">
            <div className="reg-kv-label">Last Seen / Heartbeat</div>
            <div className="reg-kv-val" data-testid="entry-last-seen">
              {entry.last_seen_at
                ? formatRelativeAge(
                    entry.heartbeat_age_seconds ?? entry.last_seen_at,
                  )
                : "never"}
              {entry.last_seen_at ? (
                <div className="reg-hint reg-mono">{entry.last_seen_at}</div>
              ) : null}
            </div>
          </div>
          <div className="reg-kv-item">
            <div className="reg-kv-label">Grain Version & Tasks</div>
            <div className="reg-kv-val">
              <span className="reg-mono">
                {entry.grain_version || "unreported"}
              </span>
              {entry.grain_commit ? ` (${entry.grain_commit})` : ""}
              <div className="reg-hint">
                {entry.active_tasks || 0} active ·{" "}
                {entry.awaiting_reply_tasks || 0} awaiting reply
              </div>
            </div>
          </div>
        </div>

        {entry.status_detail ? (
          <div
            className="reg-hint reg-detail-line"
            data-testid="entry-status-detail"
          >
            Status detail: <strong>{entry.status_detail}</strong>
          </div>
        ) : null}

        {entry.health_detail && entry.health_detail !== entry.status_detail ? (
          <div
            className="reg-hint reg-detail-line"
            data-testid="entry-health-detail"
          >
            Health detail: <strong>{entry.health_detail}</strong>
          </div>
        ) : null}

        {entry.kind === "instance" &&
        (entry.state === "creating" || entry.state === "replacing") ? (
          <div
            className="reg-stepper"
            aria-label="VM lifecycle replacement progress"
          >
            {REPLACE_STEPS.map((st) => {
              const currentIdx = REPLACE_STEPS.indexOf(
                entry.replace_step || "creating_vm",
              );
              const idx = REPLACE_STEPS.indexOf(st);
              let cls = "reg-step";
              if (idx < currentIdx) cls = "reg-step reg-step-done";
              else if (idx === currentIdx) cls = "reg-step reg-step-active";
              return (
                <span key={st} className={cls}>
                  {st}
                </span>
              );
            })}
          </div>
        ) : null}
      </section>

      {/* Catalog & ownership editor */}
      <section className="reg-section reg-card">
        <h2 className="reg-section-title">Catalog & Ownership</h2>
        <form className="reg-stack" onSubmit={handleSaveCatalog}>
          <div className="reg-form-grid">
            <div className="reg-field">
              <label className="reg-label" htmlFor="detail-display-name">
                Display name
              </label>
              <input
                id="detail-display-name"
                className="gs-input"
                value={displayName}
                onChange={(e) => setDisplayName(e.target.value)}
              />
            </div>
            <div className="reg-field">
              <label className="reg-label" htmlFor="detail-owner-group">
                Co-owning group (owner_group)
              </label>
              <input
                id="detail-owner-group"
                className="gs-input"
                value={ownerGroup}
                onChange={(e) => setOwnerGroup(e.target.value)}
              />
            </div>
            <div className="reg-field">
              <label className="reg-label" htmlFor="detail-tags">
                Tags (comma-separated)
              </label>
              <input
                id="detail-tags"
                className="gs-input"
                value={tagsText}
                onChange={(e) => setTagsText(e.target.value)}
              />
            </div>
            {entry.kind === "connected" ? (
              <>
                <div className="reg-field">
                  <label className="reg-label" htmlFor="detail-host-kind">
                    Host kind
                  </label>
                  <select
                    id="detail-host-kind"
                    className="gs-select"
                    value={hostKind}
                    onChange={(e) => setHostKind(e.target.value)}
                  >
                    <option value="workstation">workstation</option>
                    <option value="kubernetes">kubernetes</option>
                    <option value="other">other</option>
                  </select>
                </div>
                <div className="reg-field">
                  <label
                    className="reg-label"
                    htmlFor="detail-connected-principal"
                  >
                    Connected principal
                  </label>
                  <input
                    id="detail-connected-principal"
                    className="gs-input"
                    value={connectedPrincipal}
                    onChange={(e) => setConnectedPrincipal(e.target.value)}
                  />
                </div>
              </>
            ) : null}
          </div>
          <div className="reg-field">
            <label className="reg-label" htmlFor="detail-purpose">
              Purpose
            </label>
            <textarea
              id="detail-purpose"
              className="gs-textarea"
              rows={2}
              value={purpose}
              onChange={(e) => setPurpose(e.target.value)}
            />
          </div>
          <label className="reg-checkbox-row">
            <input
              type="checkbox"
              checked={listed}
              onChange={(e) => setListed(e.target.checked)}
            />
            <span>Listed in organization Directory</span>
          </label>
          <div className="reg-actions-row">
            <button type="submit" className="gs-btn gs-btn-primary">
              Save catalog metadata
            </button>
          </div>
        </form>

        <hr className="gs-table" />
        <form className="reg-actions-row" onSubmit={handleOfferTransfer}>
          <span className="reg-label">
            Accountable owner: <strong>{entry.owner}</strong>
          </span>
          <input
            className="gs-input reg-filter-item"
            aria-label="New owner email"
            placeholder="Transfer ownership to email (e.g. bob@example.com)"
            value={transferTo}
            onChange={(e) => setTransferTo(e.target.value)}
          />
          {entry.owner_group ? (
            <label className="reg-checkbox-row">
              <input
                type="checkbox"
                checked={clearOwnerGroupOnTransfer}
                onChange={(e) => setClearOwnerGroupOnTransfer(e.target.checked)}
              />
              <span>Clear co-owning group ({entry.owner_group})</span>
            </label>
          ) : null}
          <button type="submit" className="gs-btn">
            Offer ownership transfer
          </button>
        </form>
      </section>

      {/* Scopes panel */}
      <section className="reg-section reg-card">
        <h2 className="reg-section-title">Repository & Path Scopes</h2>
        <p className="reg-section-desc">
          Self-reported scopes from the running daemon are combined with
          declared scope overrides. Declared overrides can narrow a path prefix
          or demote a primary scope to secondary.
        </p>

        <div className="reg-stack">
          <div>
            <div className="reg-badge-row">
              <strong>Self-reported scopes</strong>
              <span className="gs-badge gs-badge-vm">
                Reported by grain from repo config (read-only)
              </span>
            </div>
            <SortableTable
              emptyMessage="No scopes reported by the running grain daemon yet."
              rowKey={(s, idx) => `${s.repo}-${s.path_prefix}-${idx}`}
              rows={reportedScopes}
              columns={[
                {
                  key: "repo",
                  header: "Repository & Prefix",
                  render: (s) => (
                    <span className="reg-mono">
                      {s.repo}
                      {s.path_prefix ? `/${s.path_prefix}` : " (repo root)"}
                    </span>
                  ),
                },
                {
                  key: "role",
                  header: "Reported Role",
                  render: (s) => <span className="gs-badge">{s.role}</span>,
                },
                {
                  key: "branch",
                  header: "Branch",
                  render: (s) => (
                    <span className="reg-mono">{s.branch || "—"}</span>
                  ),
                },
                {
                  key: "active",
                  header: "Effective Status",
                  render: (s) => (
                    <div className="reg-badge-row">
                      <span
                        className={
                          s.active
                            ? "gs-badge gs-badge-workstation"
                            : "gs-badge gs-badge-warning"
                        }
                      >
                        {s.active ? "active" : s.inactive_reason || "inactive"}
                      </span>
                      {s.conflict_with ? (
                        <button
                          type="button"
                          className="reg-inline-link"
                          onClick={() => onNavigateEntry(s.conflict_with)}
                        >
                          Conflict with {s.conflict_with}
                        </button>
                      ) : null}
                    </div>
                  ),
                },
              ]}
            />
          </div>

          <div>
            <strong>
              Declared scope overrides (`PUT /api/entries/{entry.name}/scopes`)
            </strong>
            <SortableTable
              emptyMessage="No declared scope overrides configured."
              rowKey={(s, idx) => `${s.repo}-${s.path_prefix}-${idx}`}
              rows={declaredRows}
              columns={[
                {
                  key: "repo",
                  header: "Repository",
                  render: (s) => <span className="reg-mono">{s.repo}</span>,
                },
                {
                  key: "path_prefix",
                  header: "Path Prefix",
                  render: (s) => (
                    <span className="reg-mono">
                      {s.path_prefix || "(repo root)"}
                    </span>
                  ),
                },
                {
                  key: "role",
                  header: "Declared Role",
                  render: (s) => (
                    <span
                      className={
                        s.role === "primary"
                          ? "gs-badge gs-badge-vm"
                          : "gs-badge"
                      }
                    >
                      {s.role}
                    </span>
                  ),
                },
                {
                  key: "remove",
                  header: "Action",
                  render: (_, idx) => (
                    <button
                      type="button"
                      className="gs-btn gs-btn-sm gs-btn-danger"
                      onClick={() =>
                        handleRemoveDeclaredScope(declaredRows.indexOf(_))
                      }
                    >
                      Remove
                    </button>
                  ),
                },
              ]}
            />
            <div className="reg-route-form">
              <div className="reg-field">
                <label className="reg-label" htmlFor="add-scope-repo">
                  Repository
                </label>
                <input
                  id="add-scope-repo"
                  className="gs-input"
                  placeholder="acme/platform"
                  value={newScopeRepo}
                  onChange={(e) => setNewScopeRepo(e.target.value)}
                />
              </div>
              <div className="reg-field">
                <label className="reg-label" htmlFor="add-scope-prefix">
                  Path prefix
                </label>
                <input
                  id="add-scope-prefix"
                  className="gs-input"
                  placeholder="sub/directory"
                  value={newScopePrefix}
                  onChange={(e) => setNewScopePrefix(e.target.value)}
                />
              </div>
              <div className="reg-field">
                <label className="reg-label" htmlFor="add-scope-role">
                  Role
                </label>
                <select
                  id="add-scope-role"
                  className="gs-select"
                  value={newScopeRole}
                  onChange={(e) => setNewScopeRole(e.target.value)}
                >
                  <option value="primary">primary</option>
                  <option value="secondary">secondary</option>
                </select>
              </div>
              <button
                type="button"
                className="gs-btn"
                onClick={handleAddDeclaredScope}
              >
                Add scope row
              </button>
            </div>
            <div className="reg-actions-row">
              <button
                type="button"
                className="gs-btn gs-btn-primary"
                onClick={handleSaveScopes}
              >
                Save declared scopes
              </button>
            </div>
          </div>
        </div>
      </section>

      {/* Credentials & Lifecycle actions */}
      <section className="reg-section reg-card">
        <h2 className="reg-section-title">Credentials & Lifecycle Actions</h2>

        {entry.kind === "connected" ? (
          <div className="reg-stack">
            <div className="reg-actions-row">
              <input
                className="gs-input reg-filter-item"
                aria-label="Credential label"
                placeholder="Credential label"
                value={rotateLabel}
                onChange={(e) => setRotateLabel(e.target.value)}
              />
              <label className="reg-checkbox-row">
                <input
                  type="checkbox"
                  checked={revokePrevOnRotate}
                  onChange={(e) => setRevokePrevOnRotate(e.target.checked)}
                />
                <span>Revoke previous active credentials immediately</span>
              </label>
              <button
                type="button"
                className="gs-btn gs-btn-primary"
                onClick={handleRotateCredential}
              >
                Rotate token
              </button>
            </div>

            {mintedToken ? (
              <div className="reg-stack" data-testid="rotated-token-banner">
                <CopyBlock
                  label="Newly Rotated grc_… Registration Token (shown once)"
                  code={mintedToken}
                />
                <CopyBlock
                  label="Outbound Tunnel CLI Snippet (-hub-connect)"
                  code={snippet.cli}
                />
              </div>
            ) : null}

            <SortableTable
              emptyMessage="No registration credentials recorded."
              rowKey={(c) => c.credential_id}
              rows={entry.credentials || []}
              columns={[
                {
                  key: "id",
                  header: "Credential ID",
                  render: (c) => (
                    <span className="reg-mono">{c.credential_id}</span>
                  ),
                },
                {
                  key: "label",
                  header: "Label & Creator",
                  render: (c) => (
                    <span>
                      {c.label || "default"}{" "}
                      {c.created_by ? `(${c.created_by})` : ""}
                    </span>
                  ),
                },
                {
                  key: "last_used",
                  header: "Last Used",
                  render: (c) => (
                    <span>
                      {c.last_used_at
                        ? formatRelativeAge(c.last_used_at)
                        : "never"}
                    </span>
                  ),
                },
                {
                  key: "state",
                  header: "Status",
                  render: (c) => (
                    <span
                      className={
                        c.revoked_at
                          ? "gs-badge gs-badge-danger"
                          : "gs-badge gs-badge-workstation"
                      }
                    >
                      {c.revoked_at ? "revoked" : "active"}
                    </span>
                  ),
                },
                {
                  key: "revoke",
                  header: "Action",
                  render: (c) =>
                    !c.revoked_at ? (
                      <button
                        type="button"
                        className="gs-btn gs-btn-sm gs-btn-danger"
                        onClick={() => setRevokeCredTarget(c)}
                      >
                        Revoke
                      </button>
                    ) : null,
                },
              ]}
            />
          </div>
        ) : (
          <div className="reg-stack">
            <div className="reg-form-grid">
              <div className="reg-field">
                <label className="reg-label" htmlFor="detail-resize-shape">
                  Resize VM Shape
                </label>
                <select
                  id="detail-resize-shape"
                  className="gs-select"
                  value={resizeShape}
                  onChange={(e) => {
                    const nextShape = e.target.value;
                    setResizeShape(nextShape);
                    const matched = (shapes || []).find(
                      (s) => s.name === nextShape,
                    );
                    if (matched?.boot_disk_gb) {
                      setResizeDiskGb(String(matched.boot_disk_gb));
                    }
                  }}
                >
                  {(shapes || []).map((sh) => (
                    <option key={sh.name} value={sh.name}>
                      {sh.display_name || sh.name} ({sh.quota_units} units,{" "}
                      {sh.boot_disk_gb} GB)
                    </option>
                  ))}
                </select>
              </div>
              <div className="reg-field">
                <label className="reg-label" htmlFor="detail-resize-disk-gb">
                  Boot Disk Size (GB)
                </label>
                <input
                  id="detail-resize-disk-gb"
                  type="number"
                  min={20}
                  max={4000}
                  className="gs-input"
                  value={resizeDiskGb}
                  onChange={(e) => setResizeDiskGb(e.target.value)}
                  placeholder="100"
                />
              </div>
              <div className="reg-field">
                <label className="reg-label" htmlFor="detail-upgrade-version">
                  Target Grain Version
                </label>
                <input
                  id="detail-upgrade-version"
                  className="gs-input"
                  placeholder="v0.14.2"
                  value={upgradeVersion}
                  onChange={(e) => setUpgradeVersion(e.target.value)}
                />
              </div>
            </div>
            <div className="reg-actions-row">
              <button type="button" className="gs-btn" onClick={handleUpgrade}>
                Upgrade version
              </button>
              <button
                type="button"
                className="gs-btn"
                onClick={handleResizeOrReplace}
              >
                Resize / Replace VM
              </button>
            </div>
          </div>
        )}

        <div className="reg-actions-row">
          {entry.state === "deleted" ? (
            <button
              type="button"
              className="gs-btn gs-btn-primary"
              onClick={handleRestore}
            >
              Restore deleted entry
            </button>
          ) : (
            <button
              type="button"
              className="gs-btn gs-btn-danger"
              onClick={() => {
                setDeleteConfirmText("");
                setDeleteDialogOpen(true);
              }}
            >
              Delete entry...
            </button>
          )}
        </div>
      </section>

      <StopEntryDialog
        target={stopDialogOpen ? entry : null}
        onCancel={() => setStopDialogOpen(false)}
        onConfirm={handleStopConfirm}
      />

      <ConfirmDialog
        open={Boolean(revokeCredTarget)}
        title="Revoke registration credential?"
        confirmLabel="Revoke credential"
        danger
        onCancel={() => setRevokeCredTarget(null)}
        onConfirm={handleConfirmRevokeCredential}
      >
        <p>
          Revoking credential{" "}
          <span className="reg-mono">{revokeCredTarget?.credential_id}</span> (
          {revokeCredTarget?.label || "default"}) on{" "}
          <strong>{entry.name}</strong> immediately disconnects any outbound
          tunnel using it and cannot be undone.
        </p>
      </ConfirmDialog>

      <ConfirmDialog
        open={deleteDialogOpen}
        title={`Delete ${entry.name}?`}
        confirmLabel="Delete entry"
        danger
        disabled={deleteConfirmText.trim() !== entry.name}
        onCancel={() => {
          setDeleteDialogOpen(false);
          setDeleteConfirmText("");
        }}
        onConfirm={handleDeleteConfirm}
      >
        <p>
          Deleting <strong>{entry.name}</strong> revokes active credentials,
          closes any active tunnel or hosted VM, and holds the name in a 30-day
          reservation window.
        </p>
        <div className="reg-field">
          <label className="reg-label" htmlFor="detail-confirm-delete-input">
            Type <span className="reg-mono">{entry.name}</span> to confirm
            deletion
          </label>
          <input
            id="detail-confirm-delete-input"
            className="gs-input"
            value={deleteConfirmText}
            onChange={(e) => setDeleteConfirmText(e.target.value)}
          />
        </div>
      </ConfirmDialog>
    </div>
  );
}

function AdminView({ onNavigateEntry }) {
  const [summary, setSummary] = useState(null);
  const [allEntries, setAllEntries] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  // Quota editor state
  const [quotaEmail, setQuotaEmail] = useState("");
  const [quotaMaxUnits, setQuotaMaxUnits] = useState("10");
  const [quotaMaxConnected, setQuotaMaxConnected] = useState("20");

  // Force transfer state
  const [forceTransferEntryName, setForceTransferEntryName] = useState("");
  const [forceTransferTo, setForceTransferTo] = useState("");
  const [forceClearGroup, setForceClearGroup] = useState(false);

  // Hard purge confirmation state
  const [purgeTarget, setPurgeTarget] = useState(null);
  const [purgeReleaseReservation, setPurgeReleaseReservation] = useState(true);

  // Delegating apps state
  const [delegatingApps, setDelegatingApps] = useState([]);
  const [newAppName, setNewAppName] = useState("");
  const [newAppPrincipal, setNewAppPrincipal] = useState("");
  const [newAppDisplayName, setNewAppDisplayName] = useState("");
  const [newAppDescription, setNewAppDescription] = useState("");
  const [newAppHomepage, setNewAppHomepage] = useState("");
  const [newAppOwner, setNewAppOwner] = useState("");
  const [newAppOwnerGroup, setNewAppOwnerGroup] = useState("");
  const [newAppMaxAccess, setNewAppMaxAccess] = useState("write");
  const [newAppMaxTTLDays, setNewAppMaxTTLDays] = useState("90");
  const [newAppRequireSubjectToken, setNewAppRequireSubjectToken] =
    useState(false);
  const [creatingApp, setCreatingApp] = useState(false);

  const loadAdmin = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const [sumData, entData, appsData] = await Promise.all([
        fetchAdminSummary(),
        fetchEntries({ all: "1", include_deleted: "true" }),
        fetchDelegatingApps(),
      ]);
      setSummary(sumData);
      setAllEntries(
        Array.isArray(entData && entData.entries) ? entData.entries : [],
      );
      setDelegatingApps(
        Array.isArray(appsData && appsData.apps) ? appsData.apps : [],
      );
    } catch (err) {
      setError(err.message || "Failed to load admin summary");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadAdmin();
  }, [loadAdmin]);

  const handleToggleOwnerDisabled = async (email, nextDisabled) => {
    setError("");
    setNotice("");
    try {
      await setOwnerDisabled(email, nextDisabled);
      await loadAdmin();
      setNotice(`${nextDisabled ? "Disabled" : "Enabled"} owner ${email}.`);
    } catch (err) {
      setError(err.message || "Failed to update owner disabled state");
    }
  };

  const handleSaveQuota = async (e) => {
    e.preventDefault();
    if (!quotaEmail.trim()) return;
    setError("");
    setNotice("");
    try {
      await setQuota(quotaEmail.trim(), {
        max_instance_units: Number(quotaMaxUnits) || 10,
        max_connected: Number(quotaMaxConnected) || 20,
      });
      await loadAdmin();
      setNotice(`Saved custom quota override for ${quotaEmail.trim()}.`);
    } catch (err) {
      setError(err.message || "Failed to update quota");
    }
  };

  const handleResetQuota = async (email) => {
    setError("");
    setNotice("");
    try {
      await resetQuota(email);
      await loadAdmin();
      setNotice(`Reset quota override for ${email} to default limits.`);
    } catch (err) {
      setError(err.message || "Failed to reset quota");
    }
  };

  const handleResolveConflict = async (cf, resolution) => {
    setError("");
    setNotice("");
    try {
      await resolveScopeConflict({
        entry_name: cf.entry_name,
        repo: cf.repo,
        path_prefix: cf.path_prefix || "",
        resolution,
      });
      await loadAdmin();
      setNotice(
        `Resolved conflict on ${cf.repo}/${cf.path_prefix || ""} (${resolution} ${cf.entry_name}).`,
      );
    } catch (err) {
      setError(err.message || "Failed to resolve scope conflict");
    }
  };

  const handleToggleSQLDraining = async (name, nextDraining) => {
    setError("");
    setNotice("");
    try {
      await setSQLInstanceDraining(name, nextDraining);
      await loadAdmin();
      setNotice(
        `Set Cloud SQL instance ${name} draining=${String(nextDraining)}.`,
      );
    } catch (err) {
      setError(err.message || "Failed to update SQL draining state");
    }
  };

  const handleDeleteOrphan = async (zone, instanceId) => {
    setError("");
    setNotice("");
    try {
      await deleteOrphanVM(zone, instanceId);
      await loadAdmin();
      setNotice(`Deleted orphan VM ${instanceId} in ${zone}.`);
    } catch (err) {
      setError(err.message || "Failed to delete orphan VM");
    }
  };

  const handleDisconnectTunnel = async (entryName) => {
    setError("");
    setNotice("");
    try {
      await disconnectTunnel(entryName);
      await loadAdmin();
      setNotice(`Disconnected active tunnel for ${entryName}.`);
    } catch (err) {
      setError(err.message || "Failed to disconnect tunnel");
    }
  };

  const handleRestoreDeleted = async (entryName) => {
    setError("");
    setNotice("");
    try {
      await restoreEntry(entryName);
      await loadAdmin();
      setNotice(`Restored soft-deleted entry ${entryName}.`);
    } catch (err) {
      setError(err.message || "Failed to restore entry");
    }
  };

  const handleConfirmHardPurge = async () => {
    if (!purgeTarget) return;
    const entryName = purgeTarget.name;
    const releaseReservation = purgeReleaseReservation;
    setPurgeTarget(null);
    setError("");
    setNotice("");
    try {
      await purgeEntry(entryName, {
        confirm: entryName,
        force: true,
        release_reservation: releaseReservation,
      });
      await loadAdmin();
      setNotice(
        `Hard-purged retained resources for ${entryName}${releaseReservation ? " and released 30-day name reservation" : ""}.`,
      );
    } catch (err) {
      setError(err.message || "Failed to hard-purge entry");
    }
  };

  const handleForceTransfer = async (e) => {
    e.preventDefault();
    if (!forceTransferEntryName.trim() || !forceTransferTo.trim()) return;
    setError("");
    setNotice("");
    try {
      await forceTransferEntry(forceTransferEntryName.trim(), {
        to: forceTransferTo.trim(),
        clear_owner_group: forceClearGroup,
      });
      await loadAdmin();
      setNotice(
        `Force-transferred ${forceTransferEntryName.trim()} to ${forceTransferTo.trim()}.`,
      );
      setForceTransferEntryName("");
      setForceTransferTo("");
      setForceClearGroup(false);
    } catch (err) {
      setError(err.message || "Failed to force-transfer entry");
    }
  };

  const handleCreateDelegatingApp = async (e) => {
    e.preventDefault();
    const trimmedName = newAppName.trim().toLowerCase();
    const trimmedPrincipal = newAppPrincipal.trim();
    if (!trimmedName || !trimmedPrincipal) return;
    setError("");
    setNotice("");
    setCreatingApp(true);
    try {
      const ttlDaysNum = Number(newAppMaxTTLDays);
      const ttlSec =
        Number.isFinite(ttlDaysNum) && ttlDaysNum > 0
          ? Math.round(ttlDaysNum * 86400)
          : 0;
      const body = {
        name: trimmedName,
        principal: trimmedPrincipal,
        max_access: newAppMaxAccess,
        max_grant_ttl_seconds: ttlSec,
        require_subject_token: newAppRequireSubjectToken,
      };
      if (newAppDisplayName.trim()) {
        body.display_name = newAppDisplayName.trim();
      }
      if (newAppDescription.trim()) {
        body.description = newAppDescription.trim();
      }
      if (newAppHomepage.trim()) {
        body.homepage = newAppHomepage.trim();
      }
      if (newAppOwner.trim()) {
        body.owner = newAppOwner.trim();
      }
      if (newAppOwnerGroup.trim()) {
        body.owner_group = newAppOwnerGroup.trim();
      }
      const created = await createDelegatingApp(body);
      await loadAdmin();
      setNotice(
        `Registered delegating app ${created?.name || trimmedName} (${created?.principal || trimmedPrincipal}).`,
      );
      setNewAppName("");
      setNewAppPrincipal("");
      setNewAppDisplayName("");
      setNewAppDescription("");
      setNewAppHomepage("");
      setNewAppOwner("");
      setNewAppOwnerGroup("");
      setNewAppMaxAccess("write");
      setNewAppMaxTTLDays("90");
      setNewAppRequireSubjectToken(false);
    } catch (err) {
      setError(err.message || "Failed to register delegating app");
    } finally {
      setCreatingApp(false);
    }
  };

  const handleToggleDelegatingApp = async (name, nextDisabled) => {
    setError("");
    setNotice("");
    try {
      await setDelegatingAppDisabled(name, nextDisabled);
      await loadAdmin();
      setNotice(
        `${nextDisabled ? "Disabled" : "Enabled"} delegating app ${name}.`,
      );
    } catch (err) {
      setError(err.message || "Failed to update delegating app state");
    }
  };

  if (loading && !summary) {
    return (
      <div className="reg-card">Loading hub administration overview...</div>
    );
  }

  const lease = summary?.reconciler_lease;

  return (
    <div>
      <PageHeader
        title="Hub Administration"
        subtitle="Inspect active outbound tunnels per replica, organization-wide entries, owners & quotas, scope conflicts, shared Cloud SQL pool, orphan VMs, retention recovery, delegating apps, and control-plane audit logs."
      />

      {notice ? (
        <div className="reg-banner reg-banner-success" role="status">
          <span>{notice}</span>
        </div>
      ) : null}

      {error ? (
        <div className="reg-banner reg-banner-danger" role="alert">
          <span>{error}</span>
        </div>
      ) : null}

      <section className="reg-section">
        <h2 className="reg-section-title">
          Active Outbound Tunnels per Replica
        </h2>
        <div className="reg-grid-stats">
          {(summary?.replicas || []).map((rep) => (
            <div key={rep.replica_id} className="reg-stat-card">
              <div className="reg-stat-label">{rep.replica_id}</div>
              <div className="reg-stat-value">
                {rep.active_tunnels} active tunnels
              </div>
              <div className="reg-stat-meta">
                Peer:{" "}
                <span className="reg-mono">{rep.peer_addr || "local"}</span> ·
                Heartbeat {formatRelativeAge(rep.heartbeat_at)}
              </div>
            </div>
          ))}
          {lease ? (
            <div className="reg-stat-card" data-testid="reconciler-lease-card">
              <div className="reg-stat-label">Reconciler Leader Lease</div>
              <div className="reg-stat-value">
                <span className="reg-mono">{lease.holder_id || "none"}</span>
              </div>
              <div className="reg-stat-meta">
                {lease.active ? "Active leader" : "Lease expired"} · lag{" "}
                {lease.lag_seconds ?? 0}s
              </div>
            </div>
          ) : null}
        </div>

        <SortableTable
          emptyMessage="No active outbound tunnel sessions connected."
          rowKey={(t) => `${t.entry_name}-${t.session_epoch}`}
          rows={summary?.tunnel_sessions || []}
          columns={[
            {
              key: "entry_name",
              header: "Entry",
              sortValue: (t) => t.entry_name,
              render: (t) => (
                <button
                  type="button"
                  className="reg-inline-link"
                  onClick={() => onNavigateEntry(t.entry_name)}
                >
                  {t.entry_name}
                </button>
              ),
            },
            {
              key: "replica_id",
              header: "Replica & Peer",
              sortValue: (t) => t.replica_id,
              render: (t) => (
                <div>
                  <div className="reg-mono">{t.replica_id}</div>
                  <div className="reg-hint reg-mono">{t.peer_addr}</div>
                </div>
              ),
            },
            {
              key: "remote_addr",
              header: "Remote Address",
              render: (t) => <span className="reg-mono">{t.remote_addr}</span>,
            },
            {
              key: "epoch",
              header: "Epoch & RTT",
              render: (t) => (
                <span>
                  epoch {t.session_epoch} · {t.rtt_ms ?? 0} ms
                </span>
              ),
            },
            {
              key: "last_seen",
              header: "Last Seen",
              render: (t) => <span>{formatRelativeAge(t.last_seen_at)}</span>,
            },
            {
              key: "action",
              header: "Action",
              render: (t) => (
                <button
                  type="button"
                  className="gs-btn gs-btn-sm gs-btn-danger"
                  onClick={() => handleDisconnectTunnel(t.entry_name)}
                >
                  Disconnect
                </button>
              ),
            },
          ]}
        />
      </section>

      {/* Owners & Quotas section */}
      <section className="reg-section reg-card">
        <h2 className="reg-section-title">Owners & Quotas</h2>
        <SortableTable
          emptyMessage="No registered entry owners."
          rowKey={(o) => o.email}
          rows={summary?.owners || []}
          columns={[
            {
              key: "email",
              header: "Owner Email",
              sortValue: (o) => o.email,
              render: (o) => (
                <div className="reg-badge-row">
                  <span className="reg-mono">{o.email}</span>
                  {o.disabled ? (
                    <span className="gs-badge gs-badge-danger">disabled</span>
                  ) : null}
                  {o.has_override ? (
                    <span className="gs-badge gs-badge-vm">custom quota</span>
                  ) : null}
                </div>
              ),
            },
            {
              key: "instance_units",
              header: "VM Quota Units",
              render: (o) => (
                <span>
                  {o.used_instance_units} / {o.max_instance_units}
                </span>
              ),
            },
            {
              key: "connected",
              header: "Connected Cap",
              render: (o) => (
                <span>
                  {o.used_connected} / {o.max_connected}
                </span>
              ),
            },
            {
              key: "actions",
              header: "Actions",
              render: (o) => (
                <div className="reg-actions-row">
                  <button
                    type="button"
                    className="gs-btn gs-btn-sm"
                    onClick={() => {
                      setQuotaEmail(o.email);
                      setQuotaMaxUnits(String(o.max_instance_units));
                      setQuotaMaxConnected(String(o.max_connected));
                    }}
                  >
                    Edit quota
                  </button>
                  {o.has_override ? (
                    <button
                      type="button"
                      className="gs-btn gs-btn-sm"
                      onClick={() => handleResetQuota(o.email)}
                    >
                      Reset quota
                    </button>
                  ) : null}
                  <button
                    type="button"
                    className={
                      o.disabled
                        ? "gs-btn gs-btn-sm gs-btn-primary"
                        : "gs-btn gs-btn-sm gs-btn-danger"
                    }
                    onClick={() =>
                      handleToggleOwnerDisabled(o.email, !o.disabled)
                    }
                  >
                    {o.disabled ? "Enable owner" : "Disable owner"}
                  </button>
                </div>
              ),
            },
          ]}
        />
        <form className="reg-actions-row" onSubmit={handleSaveQuota}>
          <input
            className="gs-input reg-filter-item"
            aria-label="Quota owner email"
            placeholder="Owner email (e.g. alice@example.com)"
            value={quotaEmail}
            onChange={(e) => setQuotaEmail(e.target.value)}
          />
          <input
            className="gs-input"
            aria-label="Max VM quota units"
            type="number"
            min="1"
            placeholder="Max VM units"
            value={quotaMaxUnits}
            onChange={(e) => setQuotaMaxUnits(e.target.value)}
          />
          <input
            className="gs-input"
            aria-label="Max connected entries"
            type="number"
            min="1"
            placeholder="Max connected"
            value={quotaMaxConnected}
            onChange={(e) => setQuotaMaxConnected(e.target.value)}
          />
          <button type="submit" className="gs-btn gs-btn-primary">
            Save quota override
          </button>
        </form>
      </section>

      {/* Active Scope Conflicts section */}
      <section className="reg-section reg-card">
        <h2 className="reg-section-title">Active Scope Conflicts</h2>
        <SortableTable
          emptyMessage="No active primary-prefix scope conflicts across listed entries."
          rowKey={(cf, idx) =>
            `${cf.entry_name}-${cf.repo}-${cf.path_prefix}-${idx}`
          }
          rows={summary?.scope_conflicts || []}
          columns={[
            {
              key: "entry",
              header: "Contender Entry",
              render: (cf) => (
                <div>
                  <button
                    type="button"
                    className="reg-inline-link"
                    onClick={() => onNavigateEntry(cf.entry_name)}
                  >
                    {cf.entry_name}
                  </button>
                  <div className="reg-hint">{cf.owner}</div>
                </div>
              ),
            },
            {
              key: "scope",
              header: "Repo & Prefix",
              render: (cf) => (
                <span className="reg-mono">
                  {cf.repo}
                  {cf.path_prefix ? `/${cf.path_prefix}` : " (repo root)"}
                </span>
              ),
            },
            {
              key: "incumbent",
              header: "Incumbent Primary",
              render: (cf) => (
                <div>
                  <button
                    type="button"
                    className="reg-inline-link"
                    onClick={() => onNavigateEntry(cf.conflict_with)}
                  >
                    {cf.conflict_with}
                  </button>
                  {cf.incumbent_owner ? (
                    <div className="reg-hint">{cf.incumbent_owner}</div>
                  ) : null}
                </div>
              ),
            },
            {
              key: "actions",
              header: "Resolution",
              render: (cf) => (
                <div className="reg-actions-row">
                  <button
                    type="button"
                    className="gs-btn gs-btn-sm"
                    onClick={() => handleResolveConflict(cf, "demote")}
                  >
                    Demote to secondary
                  </button>
                  <button
                    type="button"
                    className="gs-btn gs-btn-sm gs-btn-danger"
                    onClick={() => handleResolveConflict(cf, "remove")}
                  >
                    Remove scope
                  </button>
                </div>
              ),
            },
          ]}
        />
      </section>

      {/* Shared Cloud SQL Pool & Orphan VMs section */}
      <section className="reg-section reg-card">
        <h2 className="reg-section-title">
          Shared Cloud SQL Pool & Orphan GCE VMs
        </h2>
        <div className="reg-stack">
          <SortableTable
            emptyMessage="No shared Cloud SQL instances registered."
            rowKey={(sql) => sql.name}
            rows={summary?.sql_instances || []}
            columns={[
              {
                key: "name",
                header: "SQL Instance",
                render: (sql) => (
                  <div>
                    <div className="reg-mono">{sql.name}</div>
                    <div className="reg-hint reg-mono">
                      {sql.connection_name} ({sql.private_ip})
                    </div>
                  </div>
                ),
              },
              {
                key: "utilization",
                header: "Databases (Active / Retained / Cap)",
                render: (sql) => (
                  <span>
                    {sql.used_databases} / {sql.max_databases} (
                    {sql.active_databases} active · {sql.retained_databases}{" "}
                    retained)
                  </span>
                ),
              },
              {
                key: "state",
                header: "Placement Status",
                render: (sql) => (
                  <span
                    className={
                      sql.draining
                        ? "gs-badge gs-badge-warning"
                        : "gs-badge gs-badge-workstation"
                    }
                  >
                    {sql.draining ? "draining" : "accepting"}
                  </span>
                ),
              },
              {
                key: "action",
                header: "Action",
                render: (sql) => (
                  <button
                    type="button"
                    className="gs-btn gs-btn-sm"
                    onClick={() =>
                      handleToggleSQLDraining(sql.name, !sql.draining)
                    }
                  >
                    {sql.draining ? "Resume placement" : "Drain instance"}
                  </button>
                ),
              },
            ]}
          />

          <SortableTable
            emptyMessage="No orphan GCE VMs detected."
            rowKey={(o) => `${o.zone}-${o.instance_id}`}
            rows={summary?.orphans || []}
            columns={[
              {
                key: "instance_id",
                header: "Orphan GCE VM",
                render: (o) => (
                  <span className="reg-mono">{o.instance_id}</span>
                ),
              },
              {
                key: "zone",
                header: "Zone & IP",
                render: (o) => (
                  <span className="reg-mono">
                    {o.zone} {o.internal_ip ? `(${o.internal_ip})` : ""}
                  </span>
                ),
              },
              {
                key: "entry",
                header: "Label Entry & State",
                render: (o) => (
                  <span>
                    {o.entry_name || "unlabeled"} · {o.state}
                  </span>
                ),
              },
              {
                key: "action",
                header: "Action",
                render: (o) => (
                  <button
                    type="button"
                    className="gs-btn gs-btn-sm gs-btn-danger"
                    onClick={() => handleDeleteOrphan(o.zone, o.instance_id)}
                  >
                    Delete orphan VM
                  </button>
                ),
              },
            ]}
          />
        </div>
      </section>

      <section className="reg-section reg-card">
        <h2 className="reg-section-title">
          All Hub Entries & Retention Recovery ({allEntries.length})
        </h2>
        <SortableTable
          emptyMessage="No entries in the hub."
          rowKey={(e) => e.name}
          rows={allEntries}
          columns={[
            {
              key: "name",
              header: "Name",
              sortValue: (e) => e.name,
              render: (e) => (
                <button
                  type="button"
                  className="reg-inline-link"
                  onClick={() => onNavigateEntry(e.name)}
                >
                  {e.name}
                </button>
              ),
            },
            {
              key: "kind",
              header: "Kind",
              render: (e) => (
                <HostKindBadge hostKind={e.host_kind} kind={e.kind} />
              ),
            },
            {
              key: "status",
              header: "Status",
              render: (e) => (
                <div>
                  <StatusPill
                    status={reachabilityStatus(e)}
                    ageSeconds={e.heartbeat_age_seconds}
                  />
                  {e.purged_at ? (
                    <div className="reg-hint">hard-purged</div>
                  ) : e.db_purge_after ? (
                    <div className="reg-hint">
                      DB retention until {e.db_purge_after}
                    </div>
                  ) : null}
                </div>
              ),
            },
            {
              key: "owner",
              header: "Owner & Group",
              render: (e) => (
                <span>
                  {e.owner}
                  {e.owner_group ? ` (${e.owner_group})` : ""}
                </span>
              ),
            },
            {
              key: "version",
              header: "Version",
              render: (e) => (
                <span className="reg-mono">{e.grain_version || "—"}</span>
              ),
            },
            {
              key: "admin_actions",
              header: "Admin Recovery",
              render: (e) => (
                <div className="reg-actions-row">
                  {e.state === "deleted" && !e.purged_at ? (
                    <button
                      type="button"
                      className="gs-btn gs-btn-sm gs-btn-primary"
                      onClick={() => handleRestoreDeleted(e.name)}
                    >
                      Restore
                    </button>
                  ) : null}
                  {e.state === "deleted" && !e.purged_at ? (
                    <button
                      type="button"
                      className="gs-btn gs-btn-sm gs-btn-danger"
                      onClick={() => {
                        setPurgeTarget(e);
                        setPurgeReleaseReservation(true);
                      }}
                    >
                      Hard purge
                    </button>
                  ) : null}
                  <button
                    type="button"
                    className="gs-btn gs-btn-sm"
                    onClick={() => {
                      setForceTransferEntryName(e.name);
                      setForceTransferTo(e.owner || "");
                    }}
                  >
                    Force transfer
                  </button>
                </div>
              ),
            },
          ]}
        />
        <form className="reg-actions-row" onSubmit={handleForceTransfer}>
          <input
            className="gs-input"
            aria-label="Force transfer entry name"
            placeholder="Entry name"
            value={forceTransferEntryName}
            onChange={(e) => setForceTransferEntryName(e.target.value)}
          />
          <input
            className="gs-input reg-filter-item"
            aria-label="Force transfer new owner email"
            placeholder="New accountable owner email"
            value={forceTransferTo}
            onChange={(e) => setForceTransferTo(e.target.value)}
          />
          <label className="reg-checkbox-row">
            <input
              type="checkbox"
              checked={forceClearGroup}
              onChange={(e) => setForceClearGroup(e.target.checked)}
            />
            <span>Clear owner_group</span>
          </label>
          <button type="submit" className="gs-btn gs-btn-danger">
            Execute force transfer
          </button>
        </form>
      </section>

      <section
        id="delegating-apps"
        className="reg-section reg-card reg-stack"
        data-testid="admin-delegating-apps-section"
      >
        <div className="reg-section-head">
          <h2 className="reg-section-title">Delegating Apps</h2>
          <span className="gs-badge">delegation_policy: consent</span>
        </div>
        <p className="reg-section-desc">
          Register and govern machine service accounts permitted to act on
          behalf of consenting users via <code>X-Grain-Act-As</code>. Enter a
          bare service-account email; the hub normalizes and stores{" "}
          <code>serviceAccount:&lt;email&gt;</code>.
        </p>

        <SortableTable
          emptyMessage="No delegating apps registered."
          rowKey={(app) => app.name}
          rows={delegatingApps}
          columns={[
            {
              key: "name",
              header: "App",
              sortValue: (app) => app.name,
              render: (app) => {
                const safeHome = safeHttpUrl(app.homepage);
                return (
                  <div>
                    <div className="reg-badge-row">
                      <strong className="reg-mono">{app.name}</strong>
                      {app.display_name && app.display_name !== app.name ? (
                        <span>· {app.display_name}</span>
                      ) : null}
                      {app.require_subject_token ? (
                        <span className="gs-badge gs-badge-warning">
                          subject token
                        </span>
                      ) : null}
                    </div>
                    {app.description ? (
                      <div className="reg-hint">{app.description}</div>
                    ) : null}
                    {safeHome ? (
                      <div className="reg-hint">
                        <a
                          href={safeHome}
                          target="_blank"
                          rel="noreferrer"
                          className="reg-inline-link"
                        >
                          {safeHome}
                        </a>
                      </div>
                    ) : null}
                  </div>
                );
              },
            },
            {
              key: "principal",
              header: "Service Account Principal",
              sortValue: (app) => app.principal,
              render: (app) => (
                <span className="reg-mono">{app.principal}</span>
              ),
            },
            {
              key: "ceiling",
              header: "Owner & Ceiling",
              render: (app) => (
                <div>
                  <div>
                    {app.owner || "—"}
                    {app.owner_group ? ` (${app.owner_group})` : ""}
                  </div>
                  <div className="reg-badge-row reg-meta-badges">
                    <span
                      className={
                        app.max_access === "write"
                          ? "gs-badge gs-badge-vm"
                          : "gs-badge gs-badge-workstation"
                      }
                    >
                      max {app.max_access}
                    </span>
                    <span className="gs-badge">
                      TTL ≤ {formatTTLSeconds(app.max_grant_ttl_seconds)}
                    </span>
                  </div>
                </div>
              ),
            },
            {
              key: "status",
              header: "Status",
              render: (app) => (
                <div>
                  <DelegationStatePill
                    state={app.disabled_at ? "disabled" : "enabled"}
                  />
                  {app.disabled_at ? (
                    <div className="reg-hint">
                      {app.disabled_by ? `by ${app.disabled_by}` : "disabled"}
                    </div>
                  ) : null}
                </div>
              ),
            },
            {
              key: "action",
              header: "Action",
              render: (app) => {
                const isDisabled = Boolean(app.disabled_at);
                return (
                  <button
                    type="button"
                    className={
                      isDisabled
                        ? "gs-btn gs-btn-sm gs-btn-primary"
                        : "gs-btn gs-btn-sm gs-btn-danger"
                    }
                    onClick={() =>
                      handleToggleDelegatingApp(app.name, !isDisabled)
                    }
                  >
                    {isDisabled ? "Enable" : "Disable"}
                  </button>
                );
              },
            },
          ]}
        />

        <form
          className="reg-subcard reg-stack"
          onSubmit={handleCreateDelegatingApp}
        >
          <h3 className="reg-subcard-title">Register a Delegating App</h3>
          <div className="reg-form-grid">
            <div className="reg-field">
              <label className="reg-label" htmlFor="admin-app-name">
                App name
              </label>
              <input
                id="admin-app-name"
                className="gs-input reg-mono"
                placeholder="planner"
                value={newAppName}
                onChange={(e) => setNewAppName(e.target.value)}
                required
              />
              <span className="reg-hint">
                3–63 lowercase letters, digits, or hyphens (starts with a
                letter).
              </span>
            </div>

            <div className="reg-field">
              <label className="reg-label" htmlFor="admin-app-principal">
                Service account email
              </label>
              <input
                id="admin-app-principal"
                className="gs-input reg-mono"
                placeholder="planner@acme-prod.iam.gserviceaccount.com"
                value={newAppPrincipal}
                onChange={(e) => setNewAppPrincipal(e.target.value)}
                required
              />
              <span className="reg-hint">
                Bare service-account email; stored as{" "}
                <code>serviceAccount:&lt;email&gt;</code>.
              </span>
            </div>

            <div className="reg-field">
              <label className="reg-label" htmlFor="admin-app-display-name">
                Display name
              </label>
              <input
                id="admin-app-display-name"
                className="gs-input"
                placeholder="Task Planner"
                value={newAppDisplayName}
                onChange={(e) => setNewAppDisplayName(e.target.value)}
              />
            </div>

            <div className="reg-field">
              <label className="reg-label" htmlFor="admin-app-owner">
                Accountable owner email
              </label>
              <input
                id="admin-app-owner"
                className="gs-input"
                placeholder="Defaults to signed-in admin"
                value={newAppOwner}
                onChange={(e) => setNewAppOwner(e.target.value)}
              />
            </div>

            <div className="reg-field">
              <label className="reg-label" htmlFor="admin-app-owner-group">
                Owner group (optional)
              </label>
              <input
                id="admin-app-owner-group"
                className="gs-input"
                placeholder="platform-team@example.com"
                value={newAppOwnerGroup}
                onChange={(e) => setNewAppOwnerGroup(e.target.value)}
              />
            </div>

            <div className="reg-field">
              <label className="reg-label" htmlFor="admin-app-homepage">
                Homepage URL (optional)
              </label>
              <input
                id="admin-app-homepage"
                className="gs-input"
                placeholder="https://planner.example.com"
                value={newAppHomepage}
                onChange={(e) => setNewAppHomepage(e.target.value)}
              />
            </div>

            <div className="reg-field">
              <label className="reg-label" htmlFor="admin-app-max-access">
                Max access ceiling
              </label>
              <select
                id="admin-app-max-access"
                className="gs-select"
                value={newAppMaxAccess}
                onChange={(e) => setNewAppMaxAccess(e.target.value)}
              >
                <option value="write">write</option>
                <option value="read">read</option>
              </select>
            </div>

            <div className="reg-field">
              <label className="reg-label" htmlFor="admin-app-max-ttl-days">
                Max grant TTL (days)
              </label>
              <input
                id="admin-app-max-ttl-days"
                type="number"
                min="1"
                className="gs-input"
                value={newAppMaxTTLDays}
                onChange={(e) => setNewAppMaxTTLDays(e.target.value)}
              />
            </div>

            <div className="reg-field">
              <label className="reg-label" htmlFor="admin-app-description">
                Description (optional)
              </label>
              <input
                id="admin-app-description"
                className="gs-input"
                placeholder="Files and dispatches tasks across your sandboxes."
                value={newAppDescription}
                onChange={(e) => setNewAppDescription(e.target.value)}
              />
            </div>
          </div>

          <div className="reg-actions-row">
            <label className="reg-checkbox-row">
              <input
                type="checkbox"
                checked={newAppRequireSubjectToken}
                onChange={(e) => setNewAppRequireSubjectToken(e.target.checked)}
              />
              <span>Require user subject token alongside delegation grant</span>
            </label>
            <button
              type="submit"
              className="gs-btn gs-btn-primary"
              disabled={creatingApp}
            >
              {creatingApp ? "Registering..." : "Register delegating app"}
            </button>
          </div>
        </form>
      </section>

      <section className="reg-section">
        <h2 className="reg-section-title">Control-Plane Audit Log</h2>
        <SortableTable
          emptyMessage="No audit events recorded."
          rowKey={(a) => String(a.id)}
          rows={summary?.audit_log || []}
          columns={[
            {
              key: "at",
              header: "When",
              render: (a) => <span>{formatRelativeAge(a.at)}</span>,
            },
            {
              key: "actor",
              header: "Actor & Authority",
              render: (a) => (
                <div>
                  <span className="reg-mono">{a.actor_email}</span>
                  {a.via ? (
                    <span className="reg-hint">
                      {" "}
                      ({a.via}
                      {a.grant_id ? `: ${a.grant_id}` : ""})
                    </span>
                  ) : null}
                </div>
              ),
            },
            {
              key: "action",
              header: "Action",
              render: (a) => <span className="gs-badge">{a.action}</span>,
            },
            {
              key: "entry",
              header: "Entry",
              render: (a) =>
                a.entry_name ? (
                  <button
                    type="button"
                    className="reg-inline-link"
                    onClick={() => onNavigateEntry(a.entry_name)}
                  >
                    {a.entry_name}
                  </button>
                ) : (
                  "—"
                ),
            },
          ]}
        />
      </section>

      <ConfirmDialog
        open={Boolean(purgeTarget)}
        title={`Hard purge ${purgeTarget?.name || ""}?`}
        confirmLabel="Hard purge"
        danger
        onCancel={() => setPurgeTarget(null)}
        onConfirm={handleConfirmHardPurge}
      >
        <p>
          Hard-purging <strong>{purgeTarget?.name}</strong> permanently destroys
          its retained database, Secret Manager secrets, storage prefix, and
          configuration repository. Once purged, the entry can no longer be
          restored.
        </p>
        <label className="reg-checkbox-row">
          <input
            type="checkbox"
            checked={purgeReleaseReservation}
            onChange={(e) => setPurgeReleaseReservation(e.target.checked)}
          />
          <span>
            Release the 30-day name reservation so another owner can claim{" "}
            <span className="reg-mono">{purgeTarget?.name}</span>
          </span>
        </label>
      </ConfirmDialog>
    </div>
  );
}

function AppsActingForYouView({ onNavigateEntry }) {
  const [grants, setGrants] = useState([]);
  const [includeRevoked, setIncludeRevoked] = useState(false);
  const [myEntries, setMyEntries] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  // Grant consent form state
  const [appInput, setAppInput] = useState("");
  const [entriesInput, setEntriesInput] = useState("");
  const [accessInput, setAccessInput] = useState("");
  const [ttlValue, setTtlValue] = useState("30");
  const [ttlUnit, setTtlUnit] = useState("days");
  const [submittingGrant, setSubmittingGrant] = useState(false);
  const [revokingId, setRevokingId] = useState("");
  const [revokeGrantTarget, setRevokeGrantTarget] = useState(null);

  const loadGrants = useCallback(async (withRevoked) => {
    setLoading(true);
    setError("");
    try {
      const res = await fetchDelegationGrants({ includeRevoked: withRevoked });
      setGrants(Array.isArray(res && res.grants) ? res.grants : []);
    } catch (err) {
      setError(err.message || "Failed to load consent grants");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadGrants(includeRevoked);
  }, [includeRevoked, loadGrants]);

  useEffect(() => {
    let cancelled = false;
    fetchEntries()
      .then((res) => {
        if (cancelled) return;
        const rows = Array.isArray(res && res.entries) ? res.entries : [];
        setMyEntries(rows.filter((e) => e && e.state !== "deleted" && e.name));
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);

  const handleAddEntryToken = (token) => {
    if (token === "*") {
      setEntriesInput("*");
      return;
    }
    const existing = entriesInput
      .split(/[\s,]+/)
      .map((s) => s.trim().toLowerCase())
      .filter((s) => s && s !== "*");
    if (!existing.includes(token)) {
      existing.push(token);
    }
    setEntriesInput(existing.join(", "));
  };

  const handlePrefillFromGrant = (grant) => {
    setAppInput(grant.app || "");
    setEntriesInput(
      Array.isArray(grant.entries) ? grant.entries.join(", ") : "",
    );
    setAccessInput(grant.access || "");
    setError("");
    setNotice(
      `Loaded ${grant.app} into the consent form above — adjust scope or TTL and click Grant consent.`,
    );
  };

  const handleGrantConsent = async (e) => {
    e.preventDefault();
    const app = appInput.trim().toLowerCase();
    const entries = Array.from(
      new Set(
        entriesInput
          .split(/[\s,]+/)
          .map((s) => s.trim().toLowerCase())
          .filter(Boolean),
      ),
    );
    if (!app || entries.length === 0) {
      setError(
        "Enter both a delegating app name and at least one entry (or *).",
      );
      return;
    }
    const rawNum = Number(ttlValue);
    const mult = ttlUnit === "days" ? 86400 : ttlUnit === "hours" ? 3600 : 1;
    const ttlSeconds =
      Number.isFinite(rawNum) && rawNum > 0 ? Math.round(rawNum * mult) : 0;

    setError("");
    setNotice("");
    setSubmittingGrant(true);
    try {
      const created = await createDelegationGrant({
        app,
        entries,
        access: accessInput,
        ttl_seconds: ttlSeconds,
      });
      await loadGrants(includeRevoked);
      setNotice(
        `Granted ${created?.access || accessInput || "app-ceiling"} consent to ${created?.app || app} (${created?.id || "saved"}).`,
      );
      setAppInput("");
      setEntriesInput("");
      setAccessInput("");
      setTtlValue("30");
      setTtlUnit("days");
    } catch (err) {
      setError(err.message || "Failed to grant consent");
    } finally {
      setSubmittingGrant(false);
    }
  };

  const handleConfirmRevokeGrant = async () => {
    if (!revokeGrantTarget) return;
    const grant = revokeGrantTarget;
    setRevokeGrantTarget(null);
    setError("");
    setNotice("");
    setRevokingId(grant.id);
    try {
      await revokeDelegationGrant(grant.id);
      await loadGrants(includeRevoked);
      setNotice(`Revoked consent grant ${grant.id} for ${grant.app}.`);
    } catch (err) {
      setError(err.message || "Failed to revoke consent grant");
    } finally {
      setRevokingId("");
    }
  };

  return (
    <div>
      <PageHeader
        title="Apps acting for you"
        subtitle="Inspect, grant, and revoke personal consent allowing registered delegating apps to reach entries with delegation_policy: consent on your behalf."
      />

      {notice ? (
        <div className="reg-banner reg-banner-success" role="status">
          <span>{notice}</span>
        </div>
      ) : null}

      {error ? (
        <div className="reg-banner reg-banner-danger" role="alert">
          <span>{error}</span>
        </div>
      ) : null}

      <section
        className="reg-section reg-card reg-stack"
        data-testid="grant-consent-card"
      >
        <div>
          <h2 className="reg-section-title">Grant Consent to an App</h2>
          <p className="reg-section-desc">
            Authorizes a registered delegating app to forward requests as your
            identity to the entries you select. Re-submitting for the same app
            replaces your active grant in place.
          </p>
        </div>

        <form className="reg-stack" onSubmit={handleGrantConsent}>
          <div className="reg-form-grid">
            <div className="reg-field">
              <label className="reg-label" htmlFor="grant-app-input">
                Delegating app name
              </label>
              <input
                id="grant-app-input"
                className="gs-input reg-mono"
                placeholder="planner"
                value={appInput}
                onChange={(e) => setAppInput(e.target.value)}
                required
              />
              <span className="reg-hint">
                Short app name registered by a hub administrator.
              </span>
            </div>

            <div className="reg-field">
              <label className="reg-label" htmlFor="grant-entries-input">
                Entries
              </label>
              <input
                id="grant-entries-input"
                className="gs-input reg-mono"
                placeholder="platform-vm, alice-workstation or *"
                value={entriesInput}
                onChange={(e) => setEntriesInput(e.target.value)}
                required
              />
              <span className="reg-hint">
                Comma-separated entry names, or <code>*</code> for every entry
                you can reach.
              </span>
              <div className="reg-chip-row">
                <button
                  type="button"
                  className="gs-btn gs-btn-sm"
                  onClick={() => handleAddEntryToken("*")}
                >
                  All reachable (*)
                </button>
                {myEntries.slice(0, 6).map((ent) => (
                  <button
                    key={ent.name}
                    type="button"
                    className="gs-btn gs-btn-sm"
                    onClick={() => handleAddEntryToken(ent.name)}
                  >
                    + {ent.name}
                  </button>
                ))}
              </div>
            </div>

            <div className="reg-field">
              <label className="reg-label" htmlFor="grant-access-select">
                Access level
              </label>
              <select
                id="grant-access-select"
                className="gs-select"
                value={accessInput}
                onChange={(e) => setAccessInput(e.target.value)}
              >
                <option value="">App ceiling (default)</option>
                <option value="read">read</option>
                <option value="write">write</option>
              </select>
              <span className="reg-hint">
                Bounded by the app&apos;s registered max_access ceiling.
              </span>
            </div>

            <div className="reg-field">
              <label className="reg-label" htmlFor="grant-ttl-value">
                Grant TTL
              </label>
              <div className="reg-ttl-row">
                <input
                  id="grant-ttl-value"
                  type="number"
                  min="0"
                  className="gs-input"
                  aria-label="Grant TTL value"
                  value={ttlValue}
                  onChange={(e) => setTtlValue(e.target.value)}
                />
                <select
                  id="grant-ttl-unit"
                  className="gs-select"
                  aria-label="Grant TTL unit"
                  value={ttlUnit}
                  onChange={(e) => setTtlUnit(e.target.value)}
                >
                  <option value="days">days</option>
                  <option value="hours">hours</option>
                  <option value="seconds">seconds</option>
                </select>
              </div>
              <span className="reg-hint">
                Set 0 to inherit the app&apos;s maximum grant TTL.
              </span>
            </div>
          </div>

          <div className="reg-actions-row">
            <button
              type="submit"
              className="gs-btn gs-btn-primary"
              disabled={submittingGrant}
            >
              {submittingGrant ? "Granting..." : "Grant consent"}
            </button>
          </div>
        </form>
      </section>

      <section
        className="reg-section reg-card reg-stack"
        data-testid="person-grants-section"
      >
        <div className="reg-section-head">
          <h2 className="reg-section-title">Your Consent Grants</h2>
          <label className="reg-checkbox-row">
            <input
              type="checkbox"
              checked={includeRevoked}
              onChange={(e) => setIncludeRevoked(e.target.checked)}
            />
            <span>Include revoked grants</span>
          </label>
        </div>

        {loading && grants.length === 0 ? (
          <div className="reg-hint">Loading your consent grants...</div>
        ) : (
          <SortableTable
            emptyMessage="No active consent grants. Apps cannot act as you on consent-gated entries until you grant consent above."
            rowKey={(g) => g.id}
            rows={grants}
            columns={[
              {
                key: "app",
                header: "App & Grant ID",
                sortValue: (g) => g.app,
                render: (g) => (
                  <div>
                    <strong className="reg-mono">{g.app}</strong>
                    <div className="reg-hint reg-mono">{g.id}</div>
                  </div>
                ),
              },
              {
                key: "entries",
                header: "Entries",
                render: (g) => {
                  const list = Array.isArray(g.entries) ? g.entries : [];
                  if (list.includes("*")) {
                    return (
                      <span className="gs-badge gs-badge-vm">
                        * (all reachable entries)
                      </span>
                    );
                  }
                  return (
                    <div className="reg-badge-row">
                      {list.map((name) => (
                        <button
                          key={name}
                          type="button"
                          className="reg-inline-link reg-mono"
                          onClick={() => onNavigateEntry(name)}
                        >
                          {name}
                        </button>
                      ))}
                    </div>
                  );
                },
              },
              {
                key: "access",
                header: "Access",
                sortValue: (g) => g.access,
                render: (g) => (
                  <span
                    className={
                      g.access === "write"
                        ? "gs-badge gs-badge-vm"
                        : "gs-badge gs-badge-workstation"
                    }
                  >
                    {g.access}
                  </span>
                ),
              },
              {
                key: "status",
                header: "Status & Expiry",
                render: (g) => {
                  const isRevoked = Boolean(g.revoked_at);
                  const isExpired =
                    !isRevoked &&
                    Boolean(g.expires_at) &&
                    Date.parse(g.expires_at) <= Date.now();
                  const state = isRevoked
                    ? "revoked"
                    : isExpired
                      ? "expired"
                      : "active";
                  return (
                    <div>
                      <DelegationStatePill state={state} />
                      {isRevoked ? (
                        <div className="reg-hint">
                          Revoked {formatShortTimestamp(g.revoked_at)}
                        </div>
                      ) : g.expires_at ? (
                        <div className="reg-hint">
                          Expires {formatShortTimestamp(g.expires_at)}
                        </div>
                      ) : null}
                    </div>
                  );
                },
              },
              {
                key: "last_used",
                header: "Last Used",
                render: (g) => <span>{formatRelativeAge(g.last_used_at)}</span>,
              },
              {
                key: "actions",
                header: "Actions",
                render: (g) => {
                  const isRevoked = Boolean(g.revoked_at);
                  return (
                    <div className="reg-actions-row">
                      <button
                        type="button"
                        className="gs-btn gs-btn-sm"
                        onClick={() => handlePrefillFromGrant(g)}
                      >
                        {isRevoked ? "Re-grant" : "Update"}
                      </button>
                      {!isRevoked ? (
                        <button
                          type="button"
                          className="gs-btn gs-btn-sm gs-btn-danger"
                          disabled={revokingId === g.id}
                          onClick={() => setRevokeGrantTarget(g)}
                        >
                          {revokingId === g.id ? "Revoking..." : "Revoke"}
                        </button>
                      ) : null}
                    </div>
                  );
                },
              },
            ]}
          />
        )}
      </section>

      <ConfirmDialog
        open={Boolean(revokeGrantTarget)}
        title={`Revoke consent for ${revokeGrantTarget?.app || ""}?`}
        confirmLabel="Revoke grant"
        danger
        onCancel={() => setRevokeGrantTarget(null)}
        onConfirm={handleConfirmRevokeGrant}
      >
        <p>
          Revoking consent grant{" "}
          <span className="reg-mono">{revokeGrantTarget?.id}</span> immediately
          stops <strong>{revokeGrantTarget?.app}</strong> from acting on your
          behalf across{" "}
          <span className="reg-mono">
            {(revokeGrantTarget?.entries || []).join(", ") || "your entries"}
          </span>
          .
        </p>
      </ConfirmDialog>
    </div>
  );
}

function StatusView() {
  const [statusData, setStatusData] = useState(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    fetchStatus()
      .then((res) => {
        if (!cancelled) setStatusData(res);
      })
      .catch((err) => {
        if (!cancelled) setError(err.message || "Failed to load status");
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  if (loading) {
    return <div className="reg-card">Loading service status...</div>;
  }

  const versionRows = Object.entries(statusData?.version_counts || {}).map(
    ([ver, count]) => ({
      version: ver,
      count,
    }),
  );

  return (
    <div>
      <PageHeader
        title="Hub Service Status"
        subtitle="Control-plane health, active replica heartbeats, fleet version distribution, and VM shape catalog."
      />

      {error ? (
        <div className="reg-banner reg-banner-danger" role="alert">
          <span>{error}</span>
        </div>
      ) : null}

      <div className="reg-grid-stats">
        <div className="reg-stat-card">
          <div className="reg-stat-label">Service Health</div>
          <div className="reg-stat-value">
            <StatusPill status={statusData?.ok ? "online" : "degraded"} />
          </div>
          <div className="reg-stat-meta">
            Release {statusData?.release || "dev"}{" "}
            {statusData?.revision ? `(${statusData.revision})` : ""}
          </div>
        </div>
        <div className="reg-stat-card">
          <div className="reg-stat-label">Active Entries</div>
          <div className="reg-stat-value">{statusData?.total_entries ?? 0}</div>
          <div className="reg-stat-meta">
            {statusData?.online_entries ?? 0} online ·{" "}
            {statusData?.degraded_entries ?? 0} degraded ·{" "}
            {statusData?.offline_entries ?? 0} offline
          </div>
        </div>
        <div className="reg-stat-card">
          <div className="reg-stat-label">Outbound Tunnels</div>
          <div className="reg-stat-value">
            {statusData?.active_tunnels ?? 0}
          </div>
          <div className="reg-stat-meta">
            Across {(statusData?.replicas || []).length} active hub replicas
          </div>
        </div>
        <div className="reg-stat-card">
          <div className="reg-stat-label">Instance Domain</div>
          <div className="reg-stat-value reg-mono">
            *.{statusData?.instance_domain || "grain.example.com"}
          </div>
          <div className="reg-stat-meta">Host: {statusData?.host || "hub"}</div>
        </div>
      </div>

      <section className="reg-section">
        <h2 className="reg-section-title">Hub Replicas</h2>
        <SortableTable
          emptyMessage="No replicas registered."
          rowKey={(r) => r.replica_id}
          rows={statusData?.replicas || []}
          columns={[
            {
              key: "replica_id",
              header: "Replica ID",
              render: (r) => <span className="reg-mono">{r.replica_id}</span>,
            },
            {
              key: "healthy",
              header: "Health",
              render: (r) => (
                <StatusPill status={r.healthy ? "online" : "offline"} />
              ),
            },
            {
              key: "tunnels",
              header: "Active Tunnels",
              render: (r) => <strong>{r.active_tunnels}</strong>,
            },
            {
              key: "heartbeat",
              header: "Last Heartbeat",
              render: (r) => <span>{formatRelativeAge(r.heartbeat_at)}</span>,
            },
          ]}
        />
      </section>

      <section className="reg-section">
        <h2 className="reg-section-title">Configured Hosted VM Shapes</h2>
        <SortableTable
          emptyMessage="No VM shapes configured."
          rowKey={(s) => s.name}
          rows={statusData?.shapes || []}
          columns={[
            {
              key: "name",
              header: "Shape",
              render: (s) => (
                <div className="reg-badge-row">
                  <strong className="reg-mono">{s.name}</strong>
                  {s.default ? (
                    <span className="gs-badge gs-badge-vm">default</span>
                  ) : null}
                </div>
              ),
            },
            {
              key: "machine_type",
              header: "Machine Type",
              render: (s) => <span className="reg-mono">{s.machine_type}</span>,
            },
            {
              key: "boot_disk_gb",
              header: "Boot Disk",
              render: (s) => <span>{s.boot_disk_gb} GB</span>,
            },
            {
              key: "quota_units",
              header: "Quota Units",
              render: (s) => <span>{s.quota_units}</span>,
            },
          ]}
        />
      </section>

      <section className="reg-section">
        <h2 className="reg-section-title">Fleet Grain Versions</h2>
        <SortableTable
          emptyMessage="No fleet versions reported."
          rowKey={(v) => v.version}
          rows={versionRows}
          columns={[
            {
              key: "version",
              header: "Grain Version",
              render: (v) => <span className="reg-mono">{v.version}</span>,
            },
            {
              key: "count",
              header: "Entries",
              render: (v) => <strong>{v.count}</strong>,
            },
          ]}
        />
      </section>
    </div>
  );
}

// HubMark is the grain mark, the same still files the grain instance UI
// draws in its sidebar and tab (ui/public/grain-mark-*.svg, served here
// too through vite.config.js's publicDir). It follows the resolved theme:
// bronze on a light ground, wheat on a dark one, and swaps the favicon
// hrefs in index.html to match, as ui/src/AppThemeProvider.jsx does.
function HubMark() {
  const { resolvedMode } = useThemeMode();
  const mode = resolvedMode === "dark" ? "dark" : "light";
  useEffect(() => {
    const svg = document.getElementById("favicon");
    if (svg) svg.href = `/grain-mark-${mode}.svg`;
    const png = document.getElementById("favicon-png");
    if (png) png.href = `/grain-mark-${mode}.png`;
  }, [mode]);
  return (
    <img
      className="reg-brand-mark"
      src={`/grain-mark-${mode}.svg`}
      alt=""
      aria-hidden="true"
      width={28}
      height={28}
    />
  );
}

export default function App() {
  const [route, setRoute] = useState(() =>
    parseRouteFromPathname(
      typeof window !== "undefined" ? window.location.pathname : "/",
    ),
  );
  const [me, setMe] = useState(null);
  const [shapes, setShapes] = useState([]);
  const [defaultShape, setDefaultShape] = useState("medium");
  const [shapesLoaded, setShapesLoaded] = useState(false);

  const navigate = useCallback((path) => {
    if (typeof window !== "undefined") {
      window.history.pushState(null, "", path);
    }
    setRoute(parseRouteFromPathname(path));
  }, []);

  useEffect(() => {
    const onPop = () =>
      setRoute(parseRouteFromPathname(window.location.pathname));
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  const loadMeAndShapes = useCallback(() => {
    fetchMe()
      .then((res) => setMe(res))
      .catch(() => {});
    fetchShapes()
      .then((res) => {
        if (res && Array.isArray(res.shapes)) {
          setShapes(res.shapes);
        }
        if (res && res.default_shape) {
          setDefaultShape(res.default_shape);
        }
      })
      .catch(() => {})
      .finally(() => setShapesLoaded(true));
  }, []);

  useEffect(() => {
    loadMeAndShapes();
  }, [loadMeAndShapes]);

  const onNavigateEntry = useCallback(
    (name) => navigate(`/entries/${encodeURIComponent(name)}`),
    [navigate],
  );

  return (
    <div className="reg-shell">
      <header className="reg-topbar">
        <div className="reg-topbar-inner">
          <div className="reg-brand-group">
            <button
              type="button"
              className="reg-brand"
              onClick={() => navigate("/")}
            >
              <HubMark />
              <span>Grain Hub</span>
            </button>
            <nav className="reg-nav" aria-label="Hub navigation">
              <button
                type="button"
                className={
                  route.page === "directory"
                    ? "reg-nav-link reg-nav-link-active"
                    : "reg-nav-link"
                }
                onClick={() => navigate("/directory")}
              >
                Directory
              </button>
              <button
                type="button"
                className={
                  route.page === "my-entries"
                    ? "reg-nav-link reg-nav-link-active"
                    : "reg-nav-link"
                }
                onClick={() => navigate("/")}
              >
                My entries
              </button>
              <button
                type="button"
                className={
                  route.page === "new"
                    ? "reg-nav-link reg-nav-link-active"
                    : "reg-nav-link"
                }
                onClick={() => navigate("/new")}
              >
                Create / Register
              </button>
              <button
                type="button"
                className={
                  route.page === "grants"
                    ? "reg-nav-link reg-nav-link-active"
                    : "reg-nav-link"
                }
                onClick={() => navigate("/grants")}
              >
                Apps acting for you
              </button>
              <button
                type="button"
                className={
                  route.page === "status"
                    ? "reg-nav-link reg-nav-link-active"
                    : "reg-nav-link"
                }
                onClick={() => navigate("/status")}
              >
                Status
              </button>
              {Boolean(me && (me.is_admin || me.is_registry_admin)) ? (
                <button
                  type="button"
                  className={
                    route.page === "admin"
                      ? "reg-nav-link reg-nav-link-active"
                      : "reg-nav-link"
                  }
                  onClick={() => navigate("/admin")}
                >
                  Admin
                </button>
              ) : null}
            </nav>
          </div>

          <div className="reg-user-bar">
            {me?.email ? <span className="gs-badge">{me.email}</span> : null}
            <ThemeModeToggle />
          </div>
        </div>
      </header>

      <main className="reg-main">
        {route.page === "directory" ? (
          <DirectoryView onNavigateEntry={onNavigateEntry} />
        ) : route.page === "new" ? (
          <CreateRegisterView
            me={me}
            shapes={shapes}
            shapesLoaded={shapesLoaded}
            defaultShape={defaultShape}
            onCreated={loadMeAndShapes}
            onNavigateEntry={onNavigateEntry}
          />
        ) : route.page === "detail" ? (
          <EntryDetailView
            entryName={route.entryName}
            me={me}
            shapes={shapes}
            onNavigateEntry={onNavigateEntry}
            onBack={() => navigate("/")}
            onRefreshMe={loadMeAndShapes}
          />
        ) : route.page === "grants" ? (
          <AppsActingForYouView onNavigateEntry={onNavigateEntry} />
        ) : route.page === "admin" ? (
          <AdminView onNavigateEntry={onNavigateEntry} />
        ) : route.page === "status" ? (
          <StatusView />
        ) : (
          <MyEntriesView
            me={me}
            onNavigateEntry={onNavigateEntry}
            onNavigateNew={() => navigate("/new")}
            onRefreshMe={loadMeAndShapes}
          />
        )}
      </main>
    </div>
  );
}
