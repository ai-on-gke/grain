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

async function requestJSON(path, options = {}) {
  const headers = {
    Accept: "application/json",
    ...(options.body ? { "Content-Type": "application/json" } : {}),
    ...(options.headers || {}),
  };
  const resp = await fetch(path, {
    ...options,
    headers,
  });
  let data = null;
  const text = await resp.text();
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = { error: text };
    }
  }
  if (!resp.ok) {
    const err = new Error(
      (data && (data.error || data.message)) ||
        `Request failed (${resp.status})`,
    );
    err.status = resp.status;
    err.code = data && data.code;
    err.conflict = data && data.conflict;
    err.payload = data;
    throw err;
  }
  return data;
}

export function fetchMe() {
  return requestJSON("/api/me");
}

export function fetchShapes() {
  return requestJSON("/api/shapes");
}

export function fetchStatus() {
  return requestJSON("/api/status");
}

export function fetchAdminSummary() {
  return requestJSON("/api/admin/summary");
}

export function fetchDirectory(params = {}) {
  const qs = new URLSearchParams();
  Object.entries(params).forEach(([k, v]) => {
    if (v !== undefined && v !== null && String(v).trim() !== "") {
      qs.set(k, String(v).trim());
    }
  });
  const suffix = qs.toString() ? `?${qs.toString()}` : "";
  return requestJSON(`/api/directory${suffix}`);
}

export function fetchRoute({ repo, path, includeOffline }) {
  const qs = new URLSearchParams();
  if (repo) qs.set("repo", repo.trim());
  if (path) qs.set("path", path.trim());
  if (includeOffline) qs.set("include_offline", "true");
  return requestJSON(`/api/route?${qs.toString()}`);
}

export function fetchNameAvailability(name) {
  return requestJSON(`/api/names/${encodeURIComponent(name)}`);
}

export function fetchEntries(params = {}) {
  const qs = new URLSearchParams();
  Object.entries(params).forEach(([k, v]) => {
    if (v !== undefined && v !== null && String(v).trim() !== "") {
      qs.set(k, String(v).trim());
    }
  });
  const suffix = qs.toString() ? `?${qs.toString()}` : "";
  return requestJSON(`/api/entries${suffix}`);
}

export function fetchEntry(name) {
  return requestJSON(`/api/entries/${encodeURIComponent(name)}`);
}

export function createEntry(payload) {
  return requestJSON("/api/entries", {
    method: "POST",
    body: JSON.stringify(payload),
  });
}

export function updateEntry(name, payload) {
  return requestJSON(`/api/entries/${encodeURIComponent(name)}`, {
    method: "PATCH",
    body: JSON.stringify(payload),
  });
}

export function setEntryScopes(name, scopes) {
  return requestJSON(`/api/entries/${encodeURIComponent(name)}/scopes`, {
    method: "PUT",
    body: JSON.stringify({ scopes }),
  });
}

export function rotateCredential(name, payload = {}) {
  return requestJSON(
    `/api/entries/${encodeURIComponent(name)}/credentials/rotate`,
    {
      method: "POST",
      body: JSON.stringify(payload),
    },
  );
}

export function revokeCredential(name, credId) {
  return requestJSON(
    `/api/entries/${encodeURIComponent(name)}/credentials/${encodeURIComponent(credId)}`,
    {
      method: "DELETE",
    },
  );
}

export function upgradeEntry(name, targetVersion = "") {
  return requestJSON(`/api/entries/${encodeURIComponent(name)}/upgrade`, {
    method: "POST",
    body: JSON.stringify({ target_version: targetVersion }),
  });
}

export function replaceEntry(name, payload = {}) {
  return requestJSON(`/api/entries/${encodeURIComponent(name)}/replace`, {
    method: "POST",
    body: JSON.stringify(payload),
  });
}

export function restoreEntry(name) {
  return requestJSON(`/api/entries/${encodeURIComponent(name)}/restore`, {
    method: "POST",
    body: JSON.stringify({}),
  });
}

export function stopEntry(name) {
  return requestJSON(`/api/entries/${encodeURIComponent(name)}/stop`, {
    method: "POST",
    body: JSON.stringify({}),
  });
}

export function startEntry(name) {
  return requestJSON(`/api/entries/${encodeURIComponent(name)}/start`, {
    method: "POST",
    body: JSON.stringify({}),
  });
}

export function deleteEntry(name, confirmName, deleteDisk = false) {
  const qs = new URLSearchParams({ confirm: confirmName });
  return requestJSON(
    `/api/entries/${encodeURIComponent(name)}?${qs.toString()}`,
    {
      method: "DELETE",
      body: JSON.stringify({
        confirm: confirmName,
        confirm_name: confirmName,
        delete_disk: Boolean(deleteDisk),
      }),
    },
  );
}

export function offerTransfer(name, payload) {
  return requestJSON(`/api/entries/${encodeURIComponent(name)}/transfer`, {
    method: "POST",
    body: JSON.stringify(payload),
  });
}

export function cancelTransfer(name) {
  return requestJSON(`/api/entries/${encodeURIComponent(name)}/transfer`, {
    method: "POST",
    body: JSON.stringify({ cancel: true }),
  });
}

export function acceptTransfer(name) {
  return requestJSON(
    `/api/entries/${encodeURIComponent(name)}/transfer/accept`,
    {
      method: "POST",
      body: JSON.stringify({}),
    },
  );
}

export function forceTransferEntry(name, payload) {
  return requestJSON(
    `/api/admin/entries/${encodeURIComponent(name)}/transfer`,
    {
      method: "POST",
      body: JSON.stringify({ ...payload, force: true }),
    },
  );
}

export function purgeEntry(name, payload = {}) {
  return requestJSON(`/api/admin/entries/${encodeURIComponent(name)}/purge`, {
    method: "POST",
    body: JSON.stringify(payload),
  });
}

export function setOwnerDisabled(email, disabled) {
  return requestJSON(`/api/admin/owners/${encodeURIComponent(email)}`, {
    method: "POST",
    body: JSON.stringify({ disabled: Boolean(disabled) }),
  });
}

export function setQuota(email, payload) {
  return requestJSON(`/api/admin/quotas/${encodeURIComponent(email)}`, {
    method: "PUT",
    body: JSON.stringify(payload),
  });
}

export function resetQuota(email) {
  return requestJSON(`/api/admin/quotas/${encodeURIComponent(email)}`, {
    method: "DELETE",
  });
}

export function resolveScopeConflict(payload) {
  return requestJSON("/api/admin/conflicts/resolve", {
    method: "POST",
    body: JSON.stringify(payload),
  });
}

export function setSQLInstanceDraining(name, draining) {
  return requestJSON(`/api/admin/sql-instances/${encodeURIComponent(name)}`, {
    method: "POST",
    body: JSON.stringify({ draining: Boolean(draining) }),
  });
}

export function deleteOrphanVM(zone, instanceId) {
  return requestJSON(
    `/api/admin/orphans/${encodeURIComponent(zone)}/${encodeURIComponent(instanceId)}`,
    {
      method: "DELETE",
    },
  );
}

export function disconnectTunnel(name) {
  return requestJSON(`/api/admin/tunnels/${encodeURIComponent(name)}`, {
    method: "DELETE",
  });
}

export function fetchDelegatingApps() {
  return requestJSON("/api/admin/delegating-apps");
}

export function createDelegatingApp(payload) {
  return requestJSON("/api/admin/delegating-apps", {
    method: "POST",
    body: JSON.stringify(payload),
  });
}

export function setDelegatingAppDisabled(name, disabled) {
  return requestJSON(`/api/admin/delegating-apps/${encodeURIComponent(name)}`, {
    method: "PATCH",
    body: JSON.stringify({ disabled: Boolean(disabled) }),
  });
}

export function fetchDelegationGrants(params = {}) {
  const qs = new URLSearchParams();
  if (params.includeRevoked) {
    qs.set("include_revoked", "true");
  }
  const suffix = qs.toString() ? `?${qs.toString()}` : "";
  return requestJSON(`/api/delegation-grants${suffix}`);
}

export function createDelegationGrant(payload) {
  return requestJSON("/api/delegation-grants", {
    method: "POST",
    body: JSON.stringify(payload),
  });
}

export function revokeDelegationGrant(ref) {
  return requestJSON(`/api/delegation-grants/${encodeURIComponent(ref)}`, {
    method: "DELETE",
  });
}
