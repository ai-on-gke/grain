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

import { useCallback, useEffect, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  MenuItem,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  TextField,
  Typography,
} from "@mui/material";
import api from "../../api.js";
import { accessRoleLabel, canAccess } from "../../access.js";
import AppIcon from "../AppIcon.jsx";
import SettingsCard from "./SettingsCard.jsx";
import { formatDateTime } from "../../time.js";
import { useTimeZone } from "../../TimeZoneContext.jsx";

// actorName is the server-reported actor (GET /api/config's actor) as a
// person reads it, without its "user:" or "serviceAccount:" kind prefix.
export function actorName(config) {
  const actor = config?.actor?.id || config?.actor || "";
  return typeof actor === "string"
    ? actor.replace(/^(user|serviceAccount):/i, "")
    : "";
}

// verifiedUserEmail returns the signed-in email when the browser authenticated
// through IAP or another registered IdentityVerifier, and "" for access-code
// or unauthenticated sessions.
export function verifiedUserEmail(config) {
  const method = config?.authMethod || "";
  if (
    !config?.authenticated ||
    !method ||
    method === "trusted" ||
    method === "none" ||
    method === "header"
  ) {
    return "";
  }
  return actorName(config);
}

const SERVICE_ACCOUNT_DOMAIN = /\.gserviceaccount\.com$/;

// isServiceAccountEntry reports whether entry is a `serviceAccount:` entry.
function isServiceAccountEntry(entry) {
  return (entry || "").startsWith("serviceAccount:");
}

// validatePrincipalEntry checks that raw is a valid `user:<email>`,
// `group:<email>` or `serviceAccount:<email>` entry (a *.gserviceaccount.com
// address), returning { canonical, error }.
export function validatePrincipalEntry(raw) {
  const trimmed = (raw || "").trim();
  if (!trimmed) {
    return {
      canonical: "",
      error: "Enter user:<email>, group:<email> or serviceAccount:<email>.",
    };
  }
  const colonIdx = trimmed.indexOf(":");
  if (colonIdx <= 0) {
    return {
      canonical: "",
      error:
        "Entry must start with user:, group: or serviceAccount: (for example, user:alice@example.com).",
    };
  }
  const prefix = trimmed.slice(0, colonIdx).trim();
  const value = trimmed
    .slice(colonIdx + 1)
    .trim()
    .toLowerCase();
  if (prefix === "domain") {
    return {
      canonical: "",
      error:
        "domain: entries are not supported — use user:<email>, group:<email> or serviceAccount:<email>.",
    };
  }
  if (prefix !== "user" && prefix !== "group" && prefix !== "serviceAccount") {
    return {
      canonical: "",
      error: `Unknown prefix "${prefix}:" — expected user:, group: or serviceAccount:.`,
    };
  }
  if (!value || !/^[^\s@/,;'"<>]+@[^\s@/,;'"<>]+$/.test(value)) {
    return {
      canonical: "",
      error: `${prefix}: entry must be a valid email address.`,
    };
  }
  const isSA = SERVICE_ACCOUNT_DOMAIN.test(value);
  if (prefix === "serviceAccount" && !isSA) {
    return {
      canonical: "",
      error:
        "serviceAccount: entries must name a service account address (*.gserviceaccount.com).",
    };
  }
  if (prefix !== "serviceAccount" && isSA) {
    return {
      canonical: "",
      error: `Service account addresses must be added as serviceAccount:${value}.`,
    };
  }
  return { canonical: `${prefix}:${value}`, error: "" };
}

// formatAddedAt prints when an entry was added in the time zone this
// browser chose under Settings → Preferences (the browser's own when it
// chose none), like every other time a page prints -- not always in UTC.
function formatAddedAt(iso, zone) {
  if (!iso) return "—";
  return formatDateTime(iso, zone) || iso;
}

function describeRemovalWarning(targetEntry, entriesCount, signedInEmail) {
  const isSelf =
    Boolean(signedInEmail) &&
    targetEntry.toLowerCase() === `user:${signedInEmail.toLowerCase()}`;
  const isLast = entriesCount === 1;
  if (isSelf && isLast) {
    return `You are removing your own account (${targetEntry}) and the last allowed entry. Verified sign-in will be refused until an entry is added again, though the access code still works on a direct connection that does not go through the identity-aware proxy (over loopback only, when IAP is required).`;
  }
  if (isSelf) {
    return `You are removing your own account (${targetEntry}). Your verified sign-in will be refused on future requests, though the access code still works on a direct connection that does not go through the identity-aware proxy (over loopback only, when IAP is required).`;
  }
  if (isLast) {
    return `You are removing the last allowed entry (${targetEntry}). No verified identities will be admitted until an entry is added again, though the access code still works on a direct connection that does not go through the identity-aware proxy (over loopback only, when IAP is required).`;
  }
  return "";
}

// AccessSection is Settings → Access: how this browser is signed in, and the
// allowed users and groups admitted through verified-identity sign-in
// (design/design/settings-panel.design.md §3.2).
//
// Who that is comes from GET /api/whoami when it has answered
// (operator-attribution spec §5.4): its label is what every change this
// browser makes is recorded under. config's own actor is the fallback for
// a daemon too old to have the route.
export default function AccessSection({
  config,
  whoami = null,
  onSaved,
  onLogout,
  showError,
}) {
  const viaIAP = (config?.authMethod || "") === "iap";
  const email = whoami
    ? whoami.verified
      ? whoami.actor?.label || whoami.actor?.id || ""
      : ""
    : verifiedUserEmail(config);
  const recordedAs = whoami?.actor?.label || "";

  const ownRole = accessRoleLabel(config);
  const canRead = canAccess(config, "access:read");
  // A verified session without access:write sees the list but none of the
  // controls that change it; one with it may still be refused a role
  // wider than its own, which the server explains.
  const canWrite = canAccess(config, "access:write");
  const [entries, setEntries] = useState([]);
  const [roles, setRoles] = useState([]);
  const [newRole, setNewRole] = useState(
    config?.access ? "grain/viewer" : "grain/operator",
  );
  const [droppedFlagEntries, setDroppedFlagEntries] = useState([]);
  const [loaded, setLoaded] = useState(false);
  const [newEntry, setNewEntry] = useState("");
  const [newReason, setNewReason] = useState("");
  const [validationError, setValidationError] = useState("");
  const [busy, setBusy] = useState(false);
  // pending is a change waiting on this person: a reason for one that
  // involves a dangerous or deny role, a confirmation before removing
  // oneself or the last entry, or -- when the server answers confirm_self
  // -- a confirmation before narrowing an entry that matches oneself.
  // {kind: "remove"|"role", entry, current, role, warn, needsReason,
  //  confirmSelf, error}
  const [pending, setPending] = useState(null);
  const [pendingReason, setPendingReason] = useState("");
  const [postRemoveWarning, setPostRemoveWarning] = useState("");
  const [history, setHistory] = useState(null);

  const loadPrincipals = useCallback(async () => {
    try {
      const data = await api("/api/access/principals");
      setEntries(Array.isArray(data?.entries) ? data.entries : []);
      setRoles(Array.isArray(data?.roles) ? data.roles : []);
      setDroppedFlagEntries(
        Array.isArray(data?.droppedFlagEntries) ? data.droppedFlagEntries : [],
      );
      setLoaded(true);
    } catch (err) {
      showError?.(err);
    }
  }, [showError]);

  // loadHistory reads the access changes config_audit recorded. A daemon
  // too old to have the route leaves the card out.
  const loadHistory = useCallback(async () => {
    try {
      const data = await api("/api/access/history");
      setHistory(Array.isArray(data) ? data : null);
    } catch {
      setHistory(null);
    }
  }, []);

  useEffect(() => {
    if (!canRead) {
      setEntries([]);
      setDroppedFlagEntries([]);
      setHistory(null);
      setLoaded(true);
      return;
    }
    loadPrincipals();
    loadHistory();
  }, [canRead, loadPrincipals, loadHistory]);

  const reload = async () => {
    let nextConfig;
    if (onSaved) {
      try {
        nextConfig = await onSaved();
      } catch {
        return;
      }
    }
    if (!canAccess(nextConfig || config, "access:read")) {
      setEntries([]);
      setDroppedFlagEntries([]);
      setHistory(null);
      setLoaded(true);
      return;
    }
    await loadPrincipals();
    await loadHistory();
  };

  const people = entries.filter((e) => !isServiceAccountEntry(e.entry));
  const serviceAccounts = entries.filter((e) => isServiceAccountEntry(e.entry));

  const handleAdd = async (e) => {
    e?.preventDefault();
    const { canonical, error } = validatePrincipalEntry(newEntry);
    if (error) {
      setValidationError(error);
      return;
    }
    const reason = newReason.trim();
    if (isServiceAccountEntry(canonical) && !reason) {
      setValidationError("Give a reason to admit a service account.");
      return;
    }
    if (roleSensitive(roles, newRole) && !reason) {
      setValidationError(
        `Give a reason: ${newRole} ${roleSensitiveWhy(roles, newRole)}, so assigning it is recorded with why.`,
      );
      return;
    }
    setValidationError("");
    setBusy(true);
    try {
      const body = { entry: canonical, role: newRole };
      if (reason) body.reason = reason;
      await api("/api/access/principals", {
        method: "POST",
        body: JSON.stringify(body),
      });
      setNewEntry("");
      setNewReason("");
      setPostRemoveWarning("");
      await reload();
    } catch (err) {
      showError?.(err);
    } finally {
      setBusy(false);
    }
  };

  const openPending = (change) => {
    setPendingReason("");
    setPending(change);
  };

  // applyChange sends change: a PATCH for a role, a DELETE for a removal.
  // A refusal is shown on the pending panel when there is one, so the
  // person reads the rule beside what they asked; confirm_self turns the
  // panel into the confirmation the server asked for.
  const applyChange = async (change, reason, confirmSelf) => {
    setBusy(true);
    try {
      const path = `/api/access/principals/${encodeURIComponent(change.entry)}`;
      if (change.kind === "remove") {
        const q = new URLSearchParams();
        if (reason) q.set("reason", reason);
        if (confirmSelf) q.set("confirmSelf", "1");
        const qs = q.toString();
        await api(qs ? `${path}?${qs}` : path, { method: "DELETE" });
        if (change.warn) setPostRemoveWarning(change.warn);
      } else {
        const body = { role: change.role };
        if (reason) body.reason = reason;
        if (confirmSelf) body.confirmSelf = true;
        await api(path, { method: "PATCH", body: JSON.stringify(body) });
      }
      setPending(null);
      setPendingReason("");
      await reload();
    } catch (err) {
      if (err?.code === "confirm_self" && !confirmSelf) {
        setPending({ ...change, confirmSelf: err.message || String(err) });
      } else if (pending || change.needsReason || change.warn) {
        const { confirmSelf: _dropped, ...rest } = change;
        setPending({ ...rest, error: err?.message || String(err) });
      } else {
        showError?.(err);
      }
    } finally {
      setBusy(false);
    }
  };

  const changeRole = (entry, current, role) => {
    if (role === current) return;
    const change = { kind: "role", entry, current, role };
    if (roleSensitive(roles, current) || roleSensitive(roles, role)) {
      openPending({ ...change, needsReason: true });
      return;
    }
    applyChange(change, "", false);
  };

  const handleRequestRemove = (entry, current) => {
    const warn = describeRemovalWarning(entry, entries.length, email);
    const change = {
      kind: "remove",
      entry,
      current,
      warn,
      needsReason: roleSensitive(roles, current),
    };
    if (warn || change.needsReason) {
      openPending(change);
      return;
    }
    applyChange(change, "", false);
  };

  const submitPending = () => {
    const reason = pendingReason.trim();
    if (pending.needsReason && !reason) {
      setPending({
        ...pending,
        error:
          "Give a reason: this change is recorded in the access history with why.",
      });
      return;
    }
    applyChange(pending, reason, Boolean(pending.confirmSelf));
  };

  return (
    <>
      <SettingsCard title="This browser" anchor="this-browser">
        {ownRole ? (
          <Typography
            variant="body2"
            color="text.secondary"
            sx={{ mt: 1 }}
            data-testid="access-own-role"
          >
            Your role: <RoleChip role={ownRole} />
            {config?.access?.full ? null : (
              <> — actions outside it are hidden or refused.</>
            )}
          </Typography>
        ) : null}
        {viaIAP ? (
          <Box sx={{ mt: 1 }}>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
              Signed in through IAP as {email || actorName(config)}.
            </Typography>
            <Button
              variant="outlined"
              color="error"
              size="small"
              startIcon={<AppIcon name="signOut" size={16} />}
              onClick={onLogout}
            >
              Log out
            </Button>
          </Box>
        ) : email ? (
          <Box sx={{ mt: 1 }}>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
              Signed in via {config.authMethod} as {email}.
            </Typography>
            {onLogout ? (
              <Button
                variant="outlined"
                color="error"
                size="small"
                startIcon={<AppIcon name="signOut" size={16} />}
                onClick={onLogout}
              >
                Log out
              </Button>
            ) : null}
          </Box>
        ) : (
          <Box sx={{ mt: 1 }}>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
              Signed in with the access code.
              {recordedAs ? (
                <>
                  {" "}
                  Changes made here are recorded as{" "}
                  <span data-testid="access-recorded-as">{recordedAs}</span>,
                  not as a person.
                </>
              ) : null}
            </Typography>
            <Button
              variant="outlined"
              color="error"
              size="small"
              startIcon={<AppIcon name="signOut" size={16} />}
              onClick={onLogout}
            >
              Log out
            </Button>
          </Box>
        )}
      </SettingsCard>

      <SettingsCard
        title="Allowed users and groups"
        anchor="allowed-principals"
      >
        <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
          Each entry has a role, an API scope set: <code>grain/operator</code>{" "}
          (everything), <code>grain/contributor</code>,{" "}
          <code>grain/viewer</code> or one this deployment declares. Someone
          matching several entries holds all their roles together. A change
          involving a role with dangerous scopes, or one that denies scopes,
          needs a reason and is kept in the access history. Signed in as a
          person, you change only entries whose old and new roles are within
          your own; you cannot widen or lift a deny on an entry that matches
          you, narrowing one asks you to confirm, and the last full admin cannot
          be removed. The access code can make any change.
        </Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
          Users (<code>user:&lt;email&gt;</code>), groups (
          <code>group:&lt;email&gt;</code>) and service accounts (
          <code>serviceAccount:&lt;email&gt;</code>, with a reason) admitted
          when signed in through IAP or another identity verifier. Changes take
          effect immediately without restarting the daemon. The access code
          still works on a direct connection that bypasses the identity-aware
          proxy (over loopback only, when IAP is required).
        </Typography>

        {droppedFlagEntries.length > 0 && (
          <Alert
            severity="warning"
            sx={{ mt: 1.5 }}
            data-testid="dropped-flag-entries-warning"
          >
            Ignored startup flag entries during one-time migration:{" "}
            <strong>{droppedFlagEntries.join(", ")}</strong>. Only{" "}
            <code>user:&lt;email&gt;</code> and <code>group:&lt;email&gt;</code>{" "}
            entries are imported. Add a service account deliberately below as{" "}
            <code>serviceAccount:&lt;email&gt;</code> with a reason, and replace
            any <code>domain:</code> entries with explicit users or groups.
          </Alert>
        )}

        {pending && (
          <Alert
            severity="warning"
            sx={{ mt: 1.5 }}
            data-testid={
              pending.kind === "remove" && pending.warn
                ? "access-remove-warning"
                : "access-change-panel"
            }
          >
            <Box data-testid="access-change-summary">
              {pending.kind === "remove" ? (
                <>
                  Remove <code>{pending.entry}</code> (role{" "}
                  <code>{pending.current}</code>).
                </>
              ) : (
                <>
                  Change <code>{pending.entry}</code> from{" "}
                  <code>{pending.current}</code> to <code>{pending.role}</code>.
                </>
              )}
              {pending.needsReason ? (
                <>
                  {" "}
                  A dangerous or deny role is involved, so give a reason; it is
                  recorded in the access history below.
                </>
              ) : null}
            </Box>
            {pending.warn ? <Box sx={{ mt: 1 }}>{pending.warn}</Box> : null}
            {pending.confirmSelf ? (
              <Box sx={{ mt: 1 }} data-testid="access-confirm-self">
                {pending.confirmSelf}
              </Box>
            ) : null}
            <TextField
              size="small"
              fullWidth
              label="Reason"
              placeholder="Why this change is made"
              value={pendingReason}
              onChange={(e) => setPendingReason(e.target.value)}
              required={Boolean(pending.needsReason)}
              disabled={busy}
              inputProps={{ "data-testid": "access-change-reason" }}
              sx={{ mt: 1.5, bgcolor: "background.paper" }}
            />
            {pending.error ? (
              <Alert
                severity="error"
                sx={{ mt: 1 }}
                data-testid="access-change-error"
              >
                {pending.error}
              </Alert>
            ) : null}
            <Stack direction="row" spacing={1} sx={{ mt: 1 }}>
              <Button
                color="warning"
                variant="contained"
                size="small"
                disabled={busy}
                data-testid="access-change-apply"
                onClick={submitPending}
              >
                {pending.confirmSelf
                  ? "Yes, narrow my own access"
                  : pending.kind === "remove"
                    ? pending.warn
                      ? "Remove anyway"
                      : "Remove"
                    : "Change role"}
              </Button>
              <Button
                color="inherit"
                size="small"
                disabled={busy}
                onClick={() => setPending(null)}
              >
                Cancel
              </Button>
            </Stack>
          </Alert>
        )}

        {!pending && postRemoveWarning && (
          <Alert
            severity="warning"
            sx={{ mt: 1.5 }}
            data-testid="access-removed-notice"
            onClose={() => setPostRemoveWarning("")}
          >
            {postRemoveWarning}
          </Alert>
        )}

        {canRead && (
          <Box sx={{ overflowX: "auto", mt: 1.5 }}>
            {loaded && entries.length === 0 ? (
              <Typography
                variant="body2"
                color="text.secondary"
                sx={{ py: 1 }}
                data-testid="allowed-principals-empty"
              >
                No allowed users or groups yet. Only the access code can sign in
                until an entry is added.
              </Typography>
            ) : people.length > 0 ? (
              <PrincipalTable
                rows={people}
                busy={busy}
                canWrite={canWrite}
                roles={roles}
                onRemove={handleRequestRemove}
                onRole={changeRole}
                testid="allowed-principals-table"
              />
            ) : null}
          </Box>
        )}

        {serviceAccounts.length > 0 && (
          <Box sx={{ mt: 2 }} data-testid="allowed-service-accounts">
            <Typography variant="subtitle2">Service accounts</Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
              Non-human callers, each added deliberately with a reason and held
              to its role. Accounts whose keys grain hands to tasks are always
              refused, whatever their role.
            </Typography>
            <Box sx={{ overflowX: "auto", mt: 1 }}>
              <PrincipalTable
                rows={serviceAccounts}
                busy={busy}
                canWrite={canWrite}
                roles={roles}
                onRemove={handleRequestRemove}
                onRole={changeRole}
                showReason
                testid="allowed-service-accounts-table"
              />
            </Box>
          </Box>
        )}

        {canWrite ? (
          <Box
            component="form"
            id="add-allowed-principal"
            onSubmit={handleAdd}
            noValidate
            sx={{ mt: 2 }}
          >
            <Stack
              direction={{ xs: "column", sm: "row" }}
              spacing={1.5}
              alignItems={{ xs: "stretch", sm: "flex-start" }}
            >
              <TextField
                size="small"
                label="User, group or service account"
                placeholder="user:alice@example.com or group:team@example.com"
                value={newEntry}
                onChange={(e) => {
                  setNewEntry(e.target.value);
                  if (validationError) setValidationError("");
                }}
                error={Boolean(validationError)}
                helperText={
                  validationError ||
                  "Prefix with user: for an individual email, group: for a Google Group or serviceAccount: for a service account."
                }
                disabled={busy}
                sx={{ flex: 1 }}
              />
              <TextField
                select
                size="small"
                label="Role"
                value={newRole}
                onChange={(e) => setNewRole(e.target.value)}
                disabled={busy}
                helperText={
                  roleSensitive(roles, newRole)
                    ? "Dangerous or deny role: a reason is needed."
                    : " "
                }
                inputProps={{ "data-testid": "access-role-input" }}
                sx={{ minWidth: 200 }}
              >
                {roleOptions(roles, newRole).map((r) => (
                  <MenuItem key={r.name} value={r.name}>
                    {r.name}
                  </MenuItem>
                ))}
              </TextField>
              <TextField
                size="small"
                label="Reason"
                placeholder="Why this entry is added"
                value={newReason}
                onChange={(e) => {
                  setNewReason(e.target.value);
                  if (validationError) setValidationError("");
                }}
                required={roleSensitive(roles, newRole)}
                helperText="Required for service accounts and dangerous or deny roles."
                disabled={busy}
                inputProps={{ "data-testid": "access-reason-input" }}
                sx={{ flex: 1 }}
              />
              <Button
                type="submit"
                variant="contained"
                disabled={busy}
                sx={{ height: 40, flexShrink: 0 }}
              >
                Add allowed principal
              </Button>
            </Stack>
          </Box>
        ) : canRead ? (
          <Typography
            variant="body2"
            color="text.secondary"
            sx={{ mt: 2 }}
            data-testid="access-read-only"
          >
            Your role does not include <code>access:write</code>, so you can see
            this list but not change it.
          </Typography>
        ) : (
          <Typography
            variant="body2"
            color="text.secondary"
            sx={{ mt: 2 }}
            data-testid="access-read-only"
          >
            Your role does not include <code>access:read</code>, so this list is
            hidden.
          </Typography>
        )}
      </SettingsCard>

      {history ? (
        <SettingsCard title="Access history" anchor="access-history">
          <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
            Every change to this list, newest first: who made it, when, the old
            and new role, and the reason given.
          </Typography>
          <AccessHistory rows={history} />
        </SettingsCard>
      ) : null}
    </>
  );
}

// AccessHistory is the config_audit rows of surface "access".
function AccessHistory({ rows }) {
  const zone = useTimeZone();
  if (rows.length === 0) {
    return (
      <Typography
        variant="body2"
        color="text.secondary"
        sx={{ py: 1 }}
        data-testid="access-history-empty"
      >
        No changes recorded yet.
      </Typography>
    );
  }
  return (
    <Box sx={{ overflowX: "auto", mt: 1 }}>
      <Table size="small" data-testid="access-history">
        <TableHead>
          <TableRow>
            <TableCell>When</TableCell>
            <TableCell>Who</TableCell>
            <TableCell>Change</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {rows.map((r) => (
            <TableRow key={r.id} data-testid={`access-history-${r.id}`}>
              <TableCell sx={{ whiteSpace: "nowrap" }}>
                {formatAddedAt(r.at, zone)}
              </TableCell>
              <TableCell sx={{ wordBreak: "break-all" }}>
                {r.actorLabel || r.actor || "—"}
              </TableCell>
              <TableCell
                sx={{ fontFamily: "monospace", wordBreak: "break-word" }}
              >
                {r.summary}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Box>
  );
}

// roleSensitive reports whether the role named name holds a dangerous
// scope or denies scopes: a change involving it needs a reason, and from a
// signed-in person a role of their own that holds all of it.
function roleSensitive(roles, name) {
  const r = roles.find((x) => x.name === name);
  return Boolean(r?.person || r?.denies);
}

// roleSensitiveWhy is what makes the role named name sensitive, in words.
function roleSensitiveWhy(roles, name) {
  const r = roles.find((x) => x.name === name);
  return r?.person ? "holds dangerous scopes" : "denies scopes";
}

// roleOptions are the roles a picker offers, with current kept even when
// it names no set in effect, so the picker still shows it.
function roleOptions(roles, current) {
  const out = roles.length ? [...roles] : [{ name: "grain/operator" }];
  if (current && !out.some((r) => r.name === current)) {
    out.push({ name: current });
  }
  return out;
}

function RoleChip({ role }) {
  return (
    <Chip
      component="span"
      size="small"
      label={role}
      variant="outlined"
      sx={{ fontFamily: "monospace" }}
    />
  );
}

// PrincipalTable lists allowlist rows with their role, who added them and
// when, and the reason they were added when showReason is set. Without
// canWrite it has no controls.
function PrincipalTable({
  rows,
  busy,
  canWrite,
  roles,
  onRemove,
  onRole,
  showReason = false,
  testid,
}) {
  const zone = useTimeZone();
  return (
    <Table size="small" data-testid={testid}>
      <TableHead>
        <TableRow>
          <TableCell>Principal</TableCell>
          {showReason ? (
            <TableCell>Reason</TableCell>
          ) : (
            <TableCell>Kind</TableCell>
          )}
          <TableCell>Role</TableCell>
          <TableCell>Added by</TableCell>
          <TableCell>Added</TableCell>
          {canWrite ? <TableCell align="right">Actions</TableCell> : null}
        </TableRow>
      </TableHead>
      <TableBody>
        {rows.map((item) => (
          <TableRow
            key={item.entry}
            data-testid={`principal-row-${item.entry}`}
          >
            <TableCell sx={{ fontFamily: "monospace", wordBreak: "break-all" }}>
              {item.entry}
            </TableCell>
            {showReason ? (
              <TableCell>{item.reason || "—"}</TableCell>
            ) : (
              <TableCell>
                <Chip
                  size="small"
                  label={item.entry.startsWith("group:") ? "Group" : "User"}
                  variant="outlined"
                />
              </TableCell>
            )}
            <TableCell>
              {canWrite ? (
                <TextField
                  select
                  size="small"
                  variant="standard"
                  value={item.role || "grain/operator"}
                  disabled={busy}
                  onChange={(e) =>
                    onRole(
                      item.entry,
                      item.role || "grain/operator",
                      e.target.value,
                    )
                  }
                  inputProps={{ "aria-label": `Role of ${item.entry}` }}
                  sx={{ minWidth: 160, fontFamily: "monospace" }}
                >
                  {roleOptions(roles, item.role || "grain/operator").map(
                    (r) => (
                      <MenuItem key={r.name} value={r.name}>
                        {r.name}
                      </MenuItem>
                    ),
                  )}
                </TextField>
              ) : (
                <RoleChip role={item.role || "grain/operator"} />
              )}
            </TableCell>
            <TableCell>{item.addedBy || "—"}</TableCell>
            <TableCell sx={{ whiteSpace: "nowrap" }}>
              {formatAddedAt(item.addedAt, zone)}
            </TableCell>
            {canWrite ? (
              <TableCell align="right">
                <Button
                  size="small"
                  color="error"
                  variant="outlined"
                  disabled={busy}
                  aria-label={`Remove ${item.entry}`}
                  onClick={() =>
                    onRemove(item.entry, item.role || "grain/operator")
                  }
                >
                  Remove
                </Button>
              </TableCell>
            ) : null}
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
