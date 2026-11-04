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
import AppIcon from "../AppIcon.jsx";
import SettingsCard from "./SettingsCard.jsx";

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
export function isServiceAccountEntry(entry) {
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

function formatAddedAt(iso) {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toISOString().replace("T", " ").slice(0, 16) + " UTC";
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

  const [entries, setEntries] = useState([]);
  const [droppedFlagEntries, setDroppedFlagEntries] = useState([]);
  const [loaded, setLoaded] = useState(false);
  const [newEntry, setNewEntry] = useState("");
  const [newReason, setNewReason] = useState("");
  const [validationError, setValidationError] = useState("");
  const [busy, setBusy] = useState(false);
  const [confirmRemove, setConfirmRemove] = useState(null);
  const [postRemoveWarning, setPostRemoveWarning] = useState("");

  const loadPrincipals = useCallback(async () => {
    try {
      const data = await api("/api/access/principals");
      setEntries(Array.isArray(data?.entries) ? data.entries : []);
      setDroppedFlagEntries(
        Array.isArray(data?.droppedFlagEntries) ? data.droppedFlagEntries : [],
      );
      setLoaded(true);
    } catch (err) {
      showError?.(err);
    }
  }, [showError]);

  useEffect(() => {
    loadPrincipals();
  }, [loadPrincipals]);

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
      setValidationError(
        "Give a reason: a service account gets full operator access.",
      );
      return;
    }
    setValidationError("");
    setBusy(true);
    try {
      await api("/api/access/principals", {
        method: "POST",
        body: JSON.stringify(
          reason ? { entry: canonical, reason } : { entry: canonical },
        ),
      });
      setNewEntry("");
      setNewReason("");
      setPostRemoveWarning("");
      await loadPrincipals();
    } catch (err) {
      showError?.(err);
    } finally {
      setBusy(false);
    }
  };

  const executeRemove = async (entry, warningAfter) => {
    setBusy(true);
    try {
      await api(`/api/access/principals/${encodeURIComponent(entry)}`, {
        method: "DELETE",
      });
      setConfirmRemove(null);
      if (warningAfter) {
        setPostRemoveWarning(warningAfter);
      }
      await loadPrincipals();
    } catch (err) {
      showError?.(err);
    } finally {
      setBusy(false);
    }
  };

  const handleRequestRemove = (entry) => {
    const warn = describeRemovalWarning(entry, entries.length, email);
    if (warn) {
      setConfirmRemove({ entry, message: warn });
      return;
    }
    executeRemove(entry, "");
  };

  return (
    <>
      <SettingsCard title="This browser" anchor="this-browser">
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

        {confirmRemove && (
          <Alert
            severity="warning"
            sx={{ mt: 1.5 }}
            data-testid="access-remove-warning"
          >
            <Box>{confirmRemove.message}</Box>
            <Stack direction="row" spacing={1} sx={{ mt: 1 }}>
              <Button
                color="warning"
                variant="contained"
                size="small"
                disabled={busy}
                onClick={() =>
                  executeRemove(confirmRemove.entry, confirmRemove.message)
                }
              >
                Remove anyway
              </Button>
              <Button
                color="inherit"
                size="small"
                disabled={busy}
                onClick={() => setConfirmRemove(null)}
              >
                Cancel
              </Button>
            </Stack>
          </Alert>
        )}

        {!confirmRemove && postRemoveWarning && (
          <Alert
            severity="warning"
            sx={{ mt: 1.5 }}
            data-testid="access-removed-notice"
            onClose={() => setPostRemoveWarning("")}
          >
            {postRemoveWarning}
          </Alert>
        )}

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
              onRemove={handleRequestRemove}
              testid="allowed-principals-table"
            />
          ) : null}
        </Box>

        {serviceAccounts.length > 0 && (
          <Box sx={{ mt: 2 }} data-testid="allowed-service-accounts">
            <Typography variant="subtitle2">Service accounts</Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
              Non-human callers with full operator access, each added
              deliberately with a reason. Accounts whose keys grain hands to
              tasks are always refused.
            </Typography>
            <Box sx={{ overflowX: "auto", mt: 1 }}>
              <PrincipalTable
                rows={serviceAccounts}
                busy={busy}
                onRemove={handleRequestRemove}
                showReason
                testid="allowed-service-accounts-table"
              />
            </Box>
          </Box>
        )}

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
              size="small"
              label="Reason"
              placeholder="Why this entry is added"
              value={newReason}
              onChange={(e) => {
                setNewReason(e.target.value);
                if (validationError) setValidationError("");
              }}
              helperText="Required for service accounts."
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
      </SettingsCard>
    </>
  );
}

// PrincipalTable lists allowlist rows with who added them and when, and the
// reason they were added when showReason is set.
function PrincipalTable({ rows, busy, onRemove, showReason = false, testid }) {
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
          <TableCell>Added by</TableCell>
          <TableCell>Added</TableCell>
          <TableCell align="right">Actions</TableCell>
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
            <TableCell>{item.addedBy || "—"}</TableCell>
            <TableCell sx={{ whiteSpace: "nowrap" }}>
              {formatAddedAt(item.addedAt)}
            </TableCell>
            <TableCell align="right">
              <Button
                size="small"
                color="error"
                variant="outlined"
                disabled={busy}
                aria-label={`Remove ${item.entry}`}
                onClick={() => onRemove(item.entry)}
              >
                Remove
              </Button>
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
