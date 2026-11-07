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

import React, { useCallback, useEffect, useMemo, useState } from "react";
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
} from "@grain/ui-shared";
import {
  acceptTransfer,
  createEntry,
  deleteEntry,
  fetchAdminSummary,
  fetchDirectory,
  fetchEntries,
  fetchEntry,
  fetchMe,
  fetchNameAvailability,
  fetchRoute,
  fetchShapes,
  fetchStatus,
  offerTransfer,
  replaceEntry,
  restoreEntry,
  revokeCredential,
  rotateCredential,
  setEntryScopes,
  updateEntry,
  upgradeEntry,
} from "./api.js";

const REPLACE_STEPS = ["draining", "deleting_vm", "creating_vm", "probing"];

function parseRouteFromPathname(pathname) {
  const clean = (pathname || "/").replace(/\/+$/, "") || "/";
  if (clean === "/directory") return { page: "directory", entryName: "" };
  if (clean === "/new" || clean === "/create" || clean === "/register") {
    return { page: "new", entryName: "" };
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
  const regOrigin = (
    safeHttpUrl(rawOrigin) || "https://registry.grain.example.com"
  ).replace(/\/+$/, "");
  const instDom = sanitizeSlug(instanceDomain, "grain.example.com");
  const name =
    String(entryName || "")
      .toLowerCase()
      .replace(/[^a-z0-9-]/g, "") || "my-grain";
  const safeToken = String(token || "").replace(/[^A-Za-z0-9_.-]/g, "");
  const tokLine = safeToken
    ? `export GRAIN_REGISTRY_TOKEN="${safeToken}"\n`
    : "";
  const cli =
    `${tokLine}grain daemon \\\n` +
    `  -registry-url ${regOrigin} \\\n` +
    `  -registry-entry ${name} \\\n` +
    `  -registry-connect \\\n` +
    `  -ui-require-verifier=registry \\\n` +
    `  -ui-registry-issuer ${regOrigin} \\\n` +
    `  -ui-registry-audience https://${name}.${instDom} \\\n` +
    `  -ui-registry-keys-url ${regOrigin}/.well-known/jwks.json`;
  const yaml =
    `registry:\n` +
    `  url: ${regOrigin}\n` +
    `  entry: ${name}\n` +
    `  connect: true\n` +
    `  auth_mode: token\n` +
    `auth:\n` +
    `  require_verifier: registry\n` +
    `  registry_issuer: ${regOrigin}\n` +
    `  registry_audience: https://${name}.${instDom}\n` +
    `  registry_keys_url: ${regOrigin}/.well-known/jwks.json`;
  return { cli, yaml };
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
  const [routePath, setRoutePath] = useState("ui-registry/src/App.jsx");
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

  const loadEntries = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const res = await fetchEntries({ mine: "1", include_deleted: "true" });
      setEntries(Array.isArray(res && res.entries) ? res.entries : []);
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

  const handleQuickRestore = async (name) => {
    setError("");
    try {
      await restoreEntry(name);
      setActionNotice(`Restored ${name}.`);
      await loadEntries();
      if (onRefreshMe) onRefreshMe();
    } catch (err) {
      setError(err.message || "Failed to restore entry");
    }
  };

  const handleConfirmDelete = async () => {
    if (!deleteTarget) return;
    setError("");
    try {
      await deleteEntry(deleteTarget.name, deleteConfirmText);
      setActionNotice(`Deleted ${deleteTarget.name}.`);
      setDeleteTarget(null);
      setDeleteConfirmText("");
      await loadEntries();
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
            {me?.is_registry_admin
              ? "Registry administrator"
              : "Standard creator"}
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
          emptyMessage="You do not own or co-own any registry entries yet."
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
                  <span className="gs-badge">
                    {e.state}
                    {e.replace_step ? ` (${e.replace_step})` : ""}
                  </span>
                </div>
              ),
            },
            {
              key: "status",
              header: "Reachability",
              sortValue: (e) => e.status,
              render: (e) => (
                <StatusPill
                  status={e.state === "deleted" ? "deleted" : e.status}
                  ageSeconds={e.heartbeat_age_seconds}
                  detail={e.status_detail}
                />
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
                  {e.kind === "instance" && e.state !== "deleted" ? (
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
                  {e.state === "deleted" ? (
                    <button
                      type="button"
                      className="gs-btn gs-btn-sm gs-btn-primary"
                      onClick={() => handleQuickRestore(e.name)}
                    >
                      Restore
                    </button>
                  ) : (
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
                  )}
                </div>
              ),
            },
          ]}
        />
      )}

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
          and reserves the name for 30 days so it can be restored before
          permanent purge.
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
  defaultShape,
  onCreated,
  onNavigateEntry,
}) {
  const [kind, setKind] = useState("connected");
  const [useCustomName, setUseCustomName] = useState(true);
  const [name, setName] = useState(() => makeRandomEntryName());
  const [availability, setAvailability] = useState(null);
  const [displayName, setDisplayName] = useState("");
  const [purpose, setPurpose] = useState("");
  const [ownerGroup, setOwnerGroup] = useState("");
  const [listed, setListed] = useState(true);
  const [tags, setTags] = useState("");
  const [shape, setShape] = useState(defaultShape || "medium");
  const [hostKind, setHostKind] = useState("workstation");
  const [connectedPrincipal, setConnectedPrincipal] = useState("");
  const [scopeRepo, setScopeRepo] = useState("");
  const [scopePrefix, setScopePrefix] = useState("");
  const [scopeRole, setScopeRole] = useState("primary");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const [createdResult, setCreatedResult] = useState(null);

  const handleKindChange = (nextKind) => {
    setKind(nextKind);
    if (nextKind === "instance") {
      setUseCustomName(false);
      setName(makeRandomEntryName());
    } else {
      setUseCustomName(true);
    }
  };

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
                  this token now; the registry stores only its SHA-256 hash and
                  cannot display it again.
                </span>
              </div>
              <CopyBlock
                label="One-time grc_… Registration Credential"
                code={createdResult.registration_token}
              />
              <CopyBlock
                label="CLI Flag Snippet (-registry-connect)"
                code={snippet.cli}
              />
              <CopyBlock label="daemon.yaml Snippet" code={snippet.yaml} />
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
            kind === "connected"
              ? "reg-mode-card reg-mode-card-active"
              : "reg-mode-card"
          }
          onClick={() => handleKindChange("connected")}
        >
          <div className="reg-mode-title">
            Self-registered grain (connected)
          </div>
          <div className="reg-mode-desc">
            Connect an existing workstation, Kubernetes pod, or server daemon
            over an outbound WebSocket-mux tunnel (
            <code>-registry-connect</code>). No inbound firewall port required.
          </div>
        </button>
        <button
          type="button"
          className={
            kind === "instance"
              ? "reg-mode-card reg-mode-card-active"
              : "reg-mode-card"
          }
          onClick={() => handleKindChange("instance")}
        >
          <div className="reg-mode-title">Hosted VM (instance)</div>
          <div className="reg-mode-desc">
            Provision a dedicated GCE VM and Cloud SQL database managed by
            grain-registry and routed over private VPC HTTP.
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
            {kind === "instance" && !useCustomName ? (
              <div className="reg-actions-row">
                <span className="gs-badge gs-badge-vm reg-mono">{name}</span>
                <button
                  type="button"
                  className="gs-btn gs-btn-sm"
                  onClick={() => setName(makeRandomEntryName())}
                >
                  Generate another random name
                </button>
                <button
                  type="button"
                  className="gs-btn gs-btn-sm"
                  onClick={() => setUseCustomName(true)}
                >
                  Choose custom name
                </button>
              </div>
            ) : (
              <input
                id="entry-name-input"
                className="gs-input"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="e.g. team-platform"
              />
            )}
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
          <div className="reg-field">
            <label className="reg-label" htmlFor="shape-select">
              VM Shape
            </label>
            <select
              id="shape-select"
              className="gs-select"
              value={shape}
              onChange={(e) => setShape(e.target.value)}
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
              placeholder="pkg/registry"
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
              label="Outbound Tunnel CLI Configuration Preview (-registry-connect)"
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
  const [resizeShape, setResizeShape] = useState("");
  const [upgradeVersion, setUpgradeVersion] = useState("");
  const [rotateLabel, setRotateLabel] = useState("rotated-token");
  const [revokePrevOnRotate, setRevokePrevOnRotate] = useState(false);
  const [mintedToken, setMintedToken] = useState("");
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false);
  const [deleteConfirmText, setDeleteConfirmText] = useState("");

  const syncFromEntry = (data) => {
    setEntry(data);
    setDisplayName(data.display_name || "");
    setPurpose(data.purpose || "");
    setHostKind(data.host_kind || "workstation");
    setConnectedPrincipal(data.connected_principal || "");
    setOwnerGroup(data.owner_group || "");
    setTagsText((data.tags || []).join(", "));
    setListed(Boolean(data.listed));
    setResizeShape(data.shape || "medium");
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

  const handleRevokeCredential = async (credId) => {
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
      });
      syncFromEntry(updated);
      setTransferTo("");
      setNotice(`Offered ownership transfer to ${updated.pending_owner}.`);
    } catch (err) {
      setError(err.message || "Failed to offer transfer");
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
      const updated = await replaceEntry(entry.name, {
        shape: resizeShape,
        target_version: upgradeVersion.trim(),
      });
      syncFromEntry(updated);
      setNotice(`Initiated VM replacement (shape=${updated.shape}).`);
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
        description={error || "The requested registry entry does not exist."}
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

      {entry.pending_owner ? (
        <div className="reg-banner reg-banner-info">
          <div>
            <strong>Pending ownership transfer to {entry.pending_owner}</strong>
          </div>
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
                status={entry.state === "deleted" ? "deleted" : entry.status}
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
              {entry.state}
              {entry.replace_step ? ` · ${entry.replace_step}` : ""}
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

        {entry.health_detail || entry.status_detail ? (
          <div className="reg-hint">
            Health detail:{" "}
            <strong>{entry.health_detail || entry.status_detail}</strong>
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
                  label="Outbound Tunnel CLI Snippet (-registry-connect)"
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
                        onClick={() => handleRevokeCredential(c.credential_id)}
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
                  onChange={(e) => setResizeShape(e.target.value)}
                >
                  {(shapes || []).map((sh) => (
                    <option key={sh.name} value={sh.name}>
                      {sh.display_name || sh.name} ({sh.quota_units} units)
                    </option>
                  ))}
                </select>
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

      <ConfirmDialog
        open={deleteDialogOpen}
        title={`Delete ${entry.name}?`}
        confirmLabel="Delete entry"
        danger
        disabled={deleteConfirmText.trim() !== entry.name}
        onCancel={() => setDeleteDialogOpen(false)}
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

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    Promise.all([
      fetchAdminSummary(),
      fetchEntries({ all: "1", include_deleted: "true" }),
    ])
      .then(([sumData, entData]) => {
        if (cancelled) return;
        setSummary(sumData);
        setAllEntries(
          Array.isArray(entData && entData.entries) ? entData.entries : [],
        );
      })
      .catch((err) => {
        if (!cancelled) setError(err.message || "Failed to load admin summary");
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  if (loading) {
    return (
      <div className="reg-card">
        Loading registry administration overview...
      </div>
    );
  }

  return (
    <div>
      <PageHeader
        title="Registry Administration"
        subtitle="Inspect active outbound tunnels per replica, organization-wide entries, scope conflicts, and control-plane audit logs."
      />

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
          ]}
        />
      </section>

      <section className="reg-section">
        <h2 className="reg-section-title">
          All Registry Entries ({allEntries.length})
        </h2>
        <SortableTable
          emptyMessage="No entries in registry."
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
                <StatusPill
                  status={e.state === "deleted" ? "deleted" : e.status}
                  ageSeconds={e.heartbeat_age_seconds}
                />
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
          ]}
        />
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
              header: "Actor",
              render: (a) => <span className="reg-mono">{a.actor_email}</span>,
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
        title="Registry Service Status"
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
            Across {(statusData?.replicas || []).length} active registry
            replicas
          </div>
        </div>
        <div className="reg-stat-card">
          <div className="reg-stat-label">Instance Domain</div>
          <div className="reg-stat-value reg-mono">
            *.{statusData?.instance_domain || "grain.example.com"}
          </div>
          <div className="reg-stat-meta">
            Host: {statusData?.host || "registry"}
          </div>
        </div>
      </div>

      <section className="reg-section">
        <h2 className="reg-section-title">Registry Replicas</h2>
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

export default function App() {
  const [route, setRoute] = useState(() =>
    parseRouteFromPathname(
      typeof window !== "undefined" ? window.location.pathname : "/",
    ),
  );
  const [me, setMe] = useState(null);
  const [shapes, setShapes] = useState([]);
  const [defaultShape, setDefaultShape] = useState("medium");

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
      .catch(() => {});
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
              <span className="reg-brand-mark" aria-hidden="true">
                G
              </span>
              <span>Grain Registry</span>
            </button>
            <nav className="reg-nav" aria-label="Registry navigation">
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
