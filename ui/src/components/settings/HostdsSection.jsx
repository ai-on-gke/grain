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

import { useCallback, useEffect, useRef, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  Paper,
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
import CopyButton from "../CopyButton.jsx";
import SettingsCard from "./SettingsCard.jsx";
import { formatDateTime } from "../../time.js";
import { useTimeZone } from "../../TimeZoneContext.jsx";

// formatLabels is a host's labels as the label field takes them:
// "k=v,k2=v2", sorted.
export function formatLabels(labels) {
  return Object.keys(labels || {})
    .sort()
    .map((k) => `${k}=${labels[k]}`)
    .join(",");
}

// parseLabels reads formatLabels' form back, or throws saying what is
// wrong with it.
export function parseLabels(text) {
  const out = {};
  for (const part of (text || "").split(",")) {
    const p = part.trim();
    if (!p) continue;
    const eq = p.indexOf("=");
    if (eq <= 0) throw new Error(`label "${p}" must be key=value`);
    out[p.slice(0, eq).trim()] = p.slice(eq + 1).trim();
  }
  return out;
}

// parsePrincipals splits a comma-separated list of allowed principal rules.
export function parsePrincipals(text) {
  return (text || "")
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
}

// parseNonNegativeInt reads a whole number >= 0, returning emptyVal when
// blank or throwing saying what is wrong with it.
export function parseNonNegativeInt(text, label, emptyVal = undefined) {
  const s = String(text ?? "").trim();
  if (!s) return emptyVal;
  if (!/^\d+$/.test(s)) {
    throw new Error(`${label} "${s}" must be a non-negative whole number`);
  }
  const n = Number(s);
  if (!Number.isSafeInteger(n)) {
    throw new Error(`${label} "${s}" is out of range`);
  }
  return n;
}

// installCommand is the legacy per-host token installer line from this
// daemon's /api/hostd/install.sh.
export function installCommand(origin, name, install) {
  let cmd = `sudo sh -c "$(curl -fsSL '${origin}/api/hostd/install.sh')" grain-hostd-install --name ${name}`;
  cmd += install?.release ? ` --release ${install.release}` : " --any-version";
  if (install?.image) cmd += ` --image ${install.image}`;
  return cmd;
}

// installLines is what installs a legacy per-host token host on its machine.
export function installLines(origin, name, install) {
  if (Array.isArray(install?.lines) && install.lines.length > 0) {
    return install.lines;
  }
  return [installCommand(origin, name, install)];
}

// inviteLines is the shared enrollment snippet for Invite hosts: built from
// the daemon's RegisterHostdInstall seam when supplied, or the standard
// /hostd/install --join command.
export function inviteLines(origin, codeView) {
  if (
    Array.isArray(codeView?.install?.lines) &&
    codeView.install.lines.length > 0
  ) {
    return codeView.install.lines;
  }
  if (!codeView?.code) return [];
  return [
    `curl -fsSL ${origin}/hostd/install | sh -s -- --join ${codeView.code}`,
  ];
}

function InstallInstructions({ name, install, intro }) {
  const lines = installLines(window.location.origin, name, install);
  const text = lines.join("\n");
  const note = install?.note;
  return (
    <Box sx={{ mt: 1 }} data-testid="hostd-install-command">
      {intro ??
        (note
          ? "To install it on another machine, run this there:"
          : "To install it on another machine, run this there and paste the token when it asks:")}
      <Stack
        direction="row"
        alignItems="flex-start"
        spacing={0.5}
        sx={{ mt: 0.5 }}
      >
        <Box
          component="pre"
          data-testid="hostd-install-lines"
          sx={{
            flex: 1,
            minWidth: 0,
            m: 0,
            fontFamily: "monospace",
            fontSize: "0.85em",
            whiteSpace: "pre-wrap",
            wordBreak: "break-all",
          }}
        >
          {text}
        </Box>
        <CopyButton
          text={text}
          label={
            lines.length > 1 ? "Copy install commands" : "Copy install command"
          }
        />
      </Stack>
      {note ? (
        <Typography
          variant="body2"
          sx={{ mt: 0.5, wordBreak: "normal" }}
          data-testid="hostd-install-note"
        >
          {note}
        </Typography>
      ) : null}
    </Box>
  );
}

// connectionText says whether a host is connected, as the status column shows it.
export function connectionText(h) {
  if (h.revoked_at) return "Revoked";
  if (h.name === "local" && !h.shared) return "Co-located (socket)";
  const c = h.connection;
  if (!c) return "Never connected";
  if (c.connected) {
    return `Connected${c.host ? ` from ${c.host}` : ""}${c.principal ? ` as ${c.principal}` : ""}${c.build ? ` (${c.build})` : ""}`;
  }
  return "Disconnected";
}

function isForbiddenError(err) {
  if (!err) return false;
  if (err.status === 403) return true;
  const msg = String(err.message || err);
  return /\b403\b|deployment owner|forbidden/i.test(msg);
}

function shortFingerprint(fp) {
  if (!fp) return "";
  if (fp.length <= 24) return fp;
  return `${fp.slice(0, 20)}…`;
}

// deletedLines says what deleting each hostd in deleted did, one line
// each: its name, and the usage-limit pauses cleared with it.
export function deletedLines(deleted) {
  return deleted.map((d) => {
    let line = `Deleted host ${d.name}.`;
    if (d.clearedPauses?.length) {
      line += ` Cleared its usage-limit pauses: ${d.clearedPauses.join(", ")}.`;
    }
    return line;
  });
}

// HostdsSection is Settings → Hosts: shared --join enrollment codes ("Invite
// hosts"), pending join requests ("Pending"), the host registry
// ("Hosts"), and legacy per-host token registration.
export default function HostdsSection({ showError, dismissError }) {
  const zone = useTimeZone();
  const [hostds, setHostds] = useState(null);
  const [codes, setCodes] = useState([]);
  const [selectedCodeName, setSelectedCodeName] = useState("");
  const [pending, setPending] = useState([]);
  const [ownerAccess, setOwnerAccess] = useState(true);

  const raisedRef = useRef(null);
  const raise = useCallback(
    (err) => {
      const message = err?.message || String(err);
      raisedRef.current = String(message);
      showError?.(err);
    },
    [showError],
  );
  const settle = useCallback(() => {
    if (raisedRef.current === null) return;
    dismissError?.(raisedRef.current);
    raisedRef.current = null;
  }, [dismissError]);

  // New enrollment code form state.
  const [showNewCode, setShowNewCode] = useState(false);
  const [codeName, setCodeName] = useState("");
  const [codeExpiresIn, setCodeExpiresIn] = useState("");
  const [codeMaxUses, setCodeMaxUses] = useState("");
  const [codeCapacity, setCodeCapacity] = useState("");
  const [codeLabels, setCodeLabels] = useState("");
  const [codePrincipals, setCodePrincipals] = useState("");

  // Per-pending-request editable overrides keyed by request ID.
  const [pendingDrafts, setPendingDrafts] = useState({});

  // Legacy per-host token creation state.
  const [name, setName] = useState("");
  const [owner, setOwner] = useState("");
  const [labels, setLabels] = useState("");
  const [issued, setIssued] = useState(null);
  const [shown, setShown] = useState(null);

  // Inline edit & rename state on existing hosts.
  const [editing, setEditing] = useState(null);
  const [renaming, setRenaming] = useState(null);
  const [busy, setBusy] = useState(false);
  // notice is what the last delete did: { severity, lines }.
  const [notice, setNotice] = useState(null);

  const load = useCallback(async () => {
    try {
      const hostList = await api("/api/hostds");
      const [codesRes, pendingRes] = await Promise.allSettled([
        api("/api/hostds/enrollment-codes"),
        api("/api/hostds/pending"),
      ]);
      if (
        (codesRes.status === "rejected" && isForbiddenError(codesRes.reason)) ||
        (pendingRes.status === "rejected" &&
          isForbiddenError(pendingRes.reason))
      ) {
        setOwnerAccess(false);
        setCodes([]);
        setPending([]);
      } else {
        setOwnerAccess(true);
        if (codesRes.status === "fulfilled" && Array.isArray(codesRes.value)) {
          setCodes(codesRes.value);
        }
        if (
          pendingRes.status === "fulfilled" &&
          Array.isArray(pendingRes.value)
        ) {
          setPending(pendingRes.value);
        }
      }
      setHostds(Array.isArray(hostList) ? hostList : []);
    } catch (err) {
      showError?.(err);
    }
  }, [showError]);

  useEffect(() => {
    load();
  }, [load]);

  const act = async (fn) => {
    setBusy(true);
    try {
      await fn();
      settle();
      await load();
    } catch (err) {
      raise(err);
    } finally {
      setBusy(false);
    }
  };

  const activeCodes = (codes || []).filter((c) => !c.revoked_at && c.code);
  const selectedCode =
    codes.find((c) => c.name === selectedCodeName) ||
    activeCodes.find((c) => c.name === "default") ||
    activeCodes[0] ||
    codes[0] ||
    null;

  const createCode = () =>
    act(async () => {
      const body = {
        name: codeName.trim(),
        default_labels: parseLabels(codeLabels),
        allowed_principals: parsePrincipals(codePrincipals),
      };
      const maxUses = parseNonNegativeInt(codeMaxUses, "max uses");
      const defaultCapacity = parseNonNegativeInt(
        codeCapacity,
        "default capacity",
      );
      if (codeExpiresIn.trim()) body.expires_in = codeExpiresIn.trim();
      if (maxUses !== undefined) body.max_uses = maxUses;
      if (defaultCapacity !== undefined)
        body.default_capacity = defaultCapacity;
      const created = await api("/api/hostds/enrollment-codes", {
        method: "POST",
        body: JSON.stringify(body),
      });
      if (created?.name) setSelectedCodeName(created.name);
      setCodeName("");
      setCodeExpiresIn("");
      setCodeMaxUses("");
      setCodeCapacity("");
      setCodeLabels("");
      setCodePrincipals("");
      setShowNewCode(false);
    });

  const rotateCode = (c) =>
    act(async () => {
      await api(
        `/api/hostds/enrollment-codes/${encodeURIComponent(c.name)}/rotate`,
        { method: "POST" },
      );
    });

  const revokeCode = (c) =>
    act(async () => {
      await api(
        `/api/hostds/enrollment-codes/${encodeURIComponent(c.name)}/revoke`,
        { method: "POST" },
      );
    });

  const draftFor = (p) =>
    pendingDrafts[p.id] || {
      name: p.suggested_name || "",
      labels: formatLabels(p.default_labels),
      capacity: p.default_capacity ? String(p.default_capacity) : "",
    };

  const updateDraft = (p, patch) => {
    const cur = draftFor(p);
    setPendingDrafts((prev) => ({ ...prev, [p.id]: { ...cur, ...patch } }));
  };

  const acceptPending = (p) =>
    act(async () => {
      const d = draftFor(p);
      const body = {
        name: d.name.trim(),
        labels: parseLabels(d.labels),
        capacity: parseNonNegativeInt(d.capacity, "host capacity", 0),
      };
      await api(`/api/hostds/pending/${encodeURIComponent(p.id)}/accept`, {
        method: "POST",
        body: JSON.stringify(body),
      });
    });

  const rejectPending = (p) =>
    act(async () => {
      await api(`/api/hostds/pending/${encodeURIComponent(p.id)}/reject`, {
        method: "POST",
      });
    });

  const create = () =>
    act(async () => {
      const body = {
        name: name.trim(),
        owner: owner.trim(),
        labels: parseLabels(labels),
      };
      const out = await api("/api/hostds", {
        method: "POST",
        body: JSON.stringify(body),
      });
      setIssued(out);
      setShown(null);
      setName("");
      setOwner("");
      setLabels("");
    });

  const rotate = (h) =>
    act(async () => {
      setShown(null);
      setIssued(
        await api(`/api/hostds/${encodeURIComponent(h.name)}/rotate`, {
          method: "POST",
        }),
      );
    });

  const showInstall = async (h) => {
    try {
      const install = await api(
        `/api/hostds/${encodeURIComponent(h.name)}/install`,
      );
      settle();
      setShown({ name: h.name, install });
    } catch (err) {
      raise(err);
    }
  };

  const revoke = (h) =>
    act(async () => {
      await api(`/api/hostds/${encodeURIComponent(h.name)}/revoke`, {
        method: "POST",
      });
      if (issued?.hostd?.name === h.name) setIssued(null);
      if (shown?.name === h.name) setShown(null);
    });
  // remove deletes a revoked hostd from the registry, with its tokens
  // (DELETE /api/hostds/{name}). The daemon refuses one a model
  // connection or capability server still names, listing them.
  const remove = (h) => {
    if (
      !window.confirm(
        `Delete host ${h.name} from the registry? Its tokens go with it and the name can be registered again; past runs keep showing it, marked deleted.`,
      )
    ) {
      return;
    }
    act(async () => {
      setNotice(null);
      const out = await api(`/api/hostds/${encodeURIComponent(h.name)}`, {
        method: "DELETE",
      });
      if (issued?.hostd?.name === h.name) setIssued(null);
      if (shown?.name === h.name) setShown(null);
      setNotice({ severity: "success", lines: deletedLines([out]) });
    });
  };
  // removeAllRevoked deletes every revoked hostd nothing still names
  // (POST /api/hostds/delete-revoked) and says which it skipped.
  const removeAllRevoked = () => {
    if (
      !window.confirm(
        "Delete every revoked host from the registry? One a model connection or capability server still names is skipped.",
      )
    ) {
      return;
    }
    act(async () => {
      setNotice(null);
      const out = await api("/api/hostds/delete-revoked", { method: "POST" });
      const lines = deletedLines(out.deleted || []);
      for (const sk of out.skipped || []) {
        lines.push(`Skipped ${sk.name}: ${sk.reason}`);
      }
      if (lines.length === 0) lines.push("No revoked hosts to delete.");
      setNotice({
        severity: (out.skipped || []).length ? "warning" : "success",
        lines,
      });
    });
  };
  const anyRevoked = (hostds || []).some(
    (h) => h.revoked_at && h.name !== "local" && !h.shared,
  );
  const save = () =>
    act(async () => {
      const body = {
        owner: editing.owner.trim(),
        labels: parseLabels(editing.labels),
      };
      if (editing.capacity !== undefined) {
        body.capacity = parseNonNegativeInt(editing.capacity, "capacity", 0);
      }
      await api(`/api/hostds/${encodeURIComponent(editing.name)}`, {
        method: "PATCH",
        body: JSON.stringify(body),
      });
      setEditing(null);
    });

  const saveRename = () =>
    act(async () => {
      await api(`/api/hostds/${encodeURIComponent(renaming.oldName)}/rename`, {
        method: "POST",
        body: JSON.stringify({ name: renaming.newName.trim() }),
      });
      setRenaming(null);
    });

  const snippetLines = selectedCode
    ? inviteLines(window.location.origin, selectedCode)
    : [];
  const snippetText = snippetLines.join("\n");

  return (
    <>
      <SettingsCard title="Invite hosts" anchor="invite-hosts">
        <Box data-testid="invite-hosts-card">
          <Typography variant="body2" color="text.secondary" sx={{ mb: 1.5 }}>
            Share one enrollment command across machines. The code only files a
            pending join request; each host generates its own Ed25519 keypair
            and waits for the deployment owner to accept it (the request names
            the verified person who enrolled it) before receiving a 30-day
            key-bound credential.
          </Typography>
          {!ownerAccess ? (
            <Alert severity="info" data-testid="invite-hosts-owner-only">
              Only the deployment owner may view shared enrollment codes or
              accept joining hosts.
            </Alert>
          ) : (
            <>
              {codes.length > 0 ? (
                <Stack
                  direction="row"
                  spacing={0.75}
                  flexWrap="wrap"
                  useFlexGap
                  alignItems="center"
                  sx={{ mb: 1.5 }}
                >
                  {codes.map((c) => (
                    <Chip
                      key={c.name}
                      label={c.revoked_at ? `${c.name} (revoked)` : c.name}
                      color={
                        selectedCode?.name === c.name ? "primary" : "default"
                      }
                      variant={
                        selectedCode?.name === c.name ? "filled" : "outlined"
                      }
                      size="small"
                      onClick={() => setSelectedCodeName(c.name)}
                      data-testid={`enrollment-code-chip-${c.name}`}
                    />
                  ))}
                  <Button
                    size="small"
                    onClick={() => {
                      if (showNewCode) settle();
                      setShowNewCode((v) => !v);
                    }}
                  >
                    {showNewCode ? "Cancel new code" : "New code"}
                  </Button>
                </Stack>
              ) : (
                <Box sx={{ mb: 1.5 }}>
                  <Button
                    size="small"
                    variant="outlined"
                    onClick={() => {
                      if (showNewCode) settle();
                      setShowNewCode((v) => !v);
                    }}
                  >
                    {showNewCode ? "Cancel new code" : "New enrollment code"}
                  </Button>
                </Box>
              )}

              {selectedCode ? (
                <Paper
                  variant="outlined"
                  sx={{ p: 1.5, mb: showNewCode ? 2 : 0 }}
                >
                  {selectedCode.revoked_at ? (
                    <Alert severity="warning" sx={{ mb: 1 }}>
                      Enrollment code <strong>{selectedCode.name}</strong> is
                      revoked and can no longer file join requests. Already
                      accepted hosts are unaffected.
                    </Alert>
                  ) : snippetText ? (
                    <Stack
                      direction="row"
                      alignItems="flex-start"
                      spacing={0.5}
                    >
                      <Box
                        component="pre"
                        data-testid="invite-hosts-snippet"
                        sx={{
                          flex: 1,
                          minWidth: 0,
                          m: 0,
                          fontFamily: "monospace",
                          fontSize: "0.85em",
                          whiteSpace: "pre-wrap",
                          wordBreak: "break-all",
                        }}
                      >
                        {snippetText}
                      </Box>
                      <CopyButton
                        text={snippetText}
                        label="Copy invite command"
                      />
                    </Stack>
                  ) : null}
                  {selectedCode.install?.note ? (
                    <Typography
                      variant="body2"
                      color="text.secondary"
                      sx={{ mt: 0.75 }}
                    >
                      {selectedCode.install.note}
                    </Typography>
                  ) : null}
                  <Stack
                    direction="row"
                    spacing={0.75}
                    flexWrap="wrap"
                    useFlexGap
                    alignItems="center"
                    sx={{ mt: 1 }}
                  >
                    <Chip
                      size="small"
                      variant="outlined"
                      label={
                        selectedCode.max_uses > 0
                          ? `Uses: ${selectedCode.uses || 0}/${selectedCode.max_uses}`
                          : `Uses: ${selectedCode.uses || 0}`
                      }
                    />
                    {selectedCode.default_capacity > 0 ? (
                      <Chip
                        size="small"
                        variant="outlined"
                        label={`Default capacity: ${selectedCode.default_capacity}`}
                      />
                    ) : null}
                    {formatLabels(selectedCode.default_labels) ? (
                      <Chip
                        size="small"
                        variant="outlined"
                        label={`Labels: ${formatLabels(selectedCode.default_labels)}`}
                      />
                    ) : null}
                    {(selectedCode.allowed_principals || []).map((p) => (
                      <Chip
                        key={p}
                        size="small"
                        variant="outlined"
                        label={`Allow: ${p}`}
                      />
                    ))}
                    {selectedCode.expires_at ? (
                      <Chip
                        size="small"
                        variant="outlined"
                        label={`Expires: ${new Date(selectedCode.expires_at).toISOString().slice(0, 10)}`}
                      />
                    ) : null}
                    <Box sx={{ flex: 1 }} />
                    {!selectedCode.revoked_at ? (
                      <>
                        <Button
                          size="small"
                          disabled={busy}
                          onClick={() => rotateCode(selectedCode)}
                        >
                          Rotate code
                        </Button>
                        <Button
                          size="small"
                          color="error"
                          disabled={busy}
                          onClick={() => revokeCode(selectedCode)}
                        >
                          Revoke code
                        </Button>
                      </>
                    ) : null}
                  </Stack>
                </Paper>
              ) : null}

              {showNewCode ? (
                <Stack spacing={1.25} sx={{ mt: 1 }}>
                  <Stack
                    direction={{ xs: "column", sm: "row" }}
                    spacing={1}
                    useFlexGap
                  >
                    <TextField
                      size="small"
                      label="Code name"
                      value={codeName}
                      onChange={(e) => setCodeName(e.target.value)}
                      helperText="e.g. team-linux"
                    />
                    <TextField
                      size="small"
                      label="Expires in"
                      value={codeExpiresIn}
                      onChange={(e) => setCodeExpiresIn(e.target.value)}
                      helperText="Optional, e.g. 7d or 24h"
                    />
                    <TextField
                      size="small"
                      label="Max uses"
                      value={codeMaxUses}
                      onChange={(e) => setCodeMaxUses(e.target.value)}
                      helperText="Optional limit"
                    />
                    <TextField
                      size="small"
                      label="Default capacity"
                      value={codeCapacity}
                      onChange={(e) => setCodeCapacity(e.target.value)}
                      helperText="Optional max runs"
                    />
                  </Stack>
                  <Stack
                    direction={{ xs: "column", sm: "row" }}
                    spacing={1}
                    useFlexGap
                    alignItems={{ sm: "flex-start" }}
                  >
                    <TextField
                      size="small"
                      fullWidth
                      label="Default labels"
                      value={codeLabels}
                      onChange={(e) => setCodeLabels(e.target.value)}
                      helperText="Optional key=value,key2=value2"
                    />
                    <TextField
                      size="small"
                      fullWidth
                      label="Allowed principals"
                      value={codePrincipals}
                      onChange={(e) => setCodePrincipals(e.target.value)}
                      helperText="Optional user:a@x.com, group:g@x.com, github:login, org:acme"
                    />
                    <Button
                      variant="contained"
                      disabled={busy || !codeName.trim()}
                      onClick={createCode}
                      sx={{ whiteSpace: "nowrap" }}
                    >
                      Create code
                    </Button>
                  </Stack>
                </Stack>
              ) : null}
            </>
          )}
        </Box>
      </SettingsCard>

      {ownerAccess ? (
        <SettingsCard
          title={
            pending.length > 0
              ? `Pending host requests (${pending.length})`
              : "Pending host requests"
          }
          anchor="pending-hosts"
        >
          <Box data-testid="pending-hosts-card">
            {pending.length === 0 ? (
              <Typography variant="body2" color="text.secondary">
                No hosts are waiting for approval. Pending requests expire
                automatically after 24 hours.
              </Typography>
            ) : (
              <Stack spacing={1.5}>
                {pending.map((p) => {
                  const d = draftFor(p);
                  return (
                    <Paper
                      key={p.id}
                      variant="outlined"
                      sx={{ p: 1.5 }}
                      data-testid={`pending-host-${p.id}`}
                    >
                      <Stack
                        direction={{ xs: "column", sm: "row" }}
                        spacing={1.5}
                        justifyContent="space-between"
                        alignItems={{ sm: "center" }}
                      >
                        <Box sx={{ minWidth: 0 }}>
                          <Stack
                            direction="row"
                            spacing={1}
                            alignItems="center"
                            flexWrap="wrap"
                            useFlexGap
                          >
                            <Typography variant="subtitle2">
                              {p.hostname} ({p.os}/{p.arch})
                            </Typography>
                            {p.enrollment_code ? (
                              <Chip
                                size="small"
                                variant="outlined"
                                label={`code: ${p.enrollment_code}`}
                              />
                            ) : null}
                            {p.rekey_hostd ? (
                              <Chip
                                size="small"
                                variant="outlined"
                                label={`rekey: ${p.rekey_hostd}`}
                              />
                            ) : null}
                          </Stack>
                          <Typography
                            variant="body2"
                            color="text.secondary"
                            sx={{ mt: 0.5, wordBreak: "break-word" }}
                          >
                            Enrolled by <strong>{p.enrolled_by}</strong>
                            {p.local_user
                              ? ` (local user ${p.local_user})`
                              : ""}
                            {p.clis?.length
                              ? ` · CLIs: ${p.clis.join(", ")}`
                              : ""}
                            {p.version ? ` · ${p.version}` : ""}
                          </Typography>
                          <Typography
                            variant="caption"
                            color="text.secondary"
                            sx={{
                              display: "block",
                              fontFamily: "monospace",
                              wordBreak: "break-all",
                            }}
                          >
                            {p.key_fingerprint}
                          </Typography>
                        </Box>
                      </Stack>
                      <Stack
                        direction={{ xs: "column", sm: "row" }}
                        spacing={1}
                        useFlexGap
                        alignItems={{ sm: "center" }}
                        sx={{ mt: 1.5 }}
                      >
                        <TextField
                          size="small"
                          label="Host name"
                          value={d.name}
                          disabled={Boolean(p.rekey_hostd)}
                          onChange={(e) =>
                            updateDraft(p, { name: e.target.value })
                          }
                        />
                        <TextField
                          size="small"
                          label="Host labels"
                          value={d.labels}
                          onChange={(e) =>
                            updateDraft(p, { labels: e.target.value })
                          }
                        />
                        <TextField
                          size="small"
                          label="Host capacity"
                          value={d.capacity}
                          onChange={(e) =>
                            updateDraft(p, { capacity: e.target.value })
                          }
                          sx={{ width: { sm: 130 } }}
                        />
                        <Stack direction="row" spacing={1}>
                          <Button
                            variant="contained"
                            size="small"
                            disabled={busy || !d.name.trim()}
                            onClick={() => acceptPending(p)}
                          >
                            Accept
                          </Button>
                          <Button
                            color="error"
                            size="small"
                            disabled={busy}
                            onClick={() => rejectPending(p)}
                          >
                            Reject
                          </Button>
                        </Stack>
                      </Stack>
                    </Paper>
                  );
                })}
              </Stack>
            )}
          </Box>
        </SettingsCard>
      ) : null}

      <SettingsCard title="Hosts" anchor="hostds">
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          The hosts this deployment runs host work through. <code>local</code>{" "}
          is co-located on this machine without a handshake; every other host
          joins with <code>--join</code> above (30-day Ed25519 key-bound
          credential, renewed automatically while connected) or dials in with a
          legacy per-host token. Revoke cuts a host off; a revoked host can then
          be deleted, which frees its name.
        </Typography>
        {issued ? (
          <Alert
            severity="success"
            onClose={() => setIssued(null)}
            sx={{ mb: 2, wordBreak: "break-all" }}
            data-testid="hostd-issued-token"
          >
            Token for host <strong>{issued.hostd.name}</strong>, shown only this
            once: <code>{issued.token}</code>
            <CopyButton text={issued.token} label="Copy token" />
            <InstallInstructions
              name={issued.hostd.name}
              install={issued.install}
            />
          </Alert>
        ) : null}
        {shown ? (
          <Alert
            severity="info"
            onClose={() => setShown(null)}
            sx={{ mb: 2, wordBreak: "break-all" }}
            data-testid="hostd-install-again"
          >
            Installing host <strong>{shown.name}</strong>. Its token was shown
            when it was added or last rotated; rotate it for a new one, or
            migrate to key-bound auth with <code>grain host rekey</code>.
            <InstallInstructions
              name={shown.name}
              install={shown.install}
              intro={
                shown.install?.note
                  ? "Run this on its machine:"
                  : "Run this on its machine and paste the token when it asks:"
              }
            />
          </Alert>
        ) : null}
        {notice ? (
          <Alert
            severity={notice.severity}
            onClose={() => setNotice(null)}
            sx={{ mb: 2, wordBreak: "break-word" }}
            data-testid="hostd-delete-notice"
          >
            {notice.lines.map((l) => (
              <div key={l}>{l}</div>
            ))}
          </Alert>
        ) : null}
        {anyRevoked ? (
          <Stack direction="row" justifyContent="flex-end" sx={{ mb: 1 }}>
            <Button
              size="small"
              color="error"
              disabled={busy}
              onClick={removeAllRevoked}
              data-testid="hostds-delete-revoked"
            >
              Delete all revoked
            </Button>
          </Stack>
        ) : null}
        <Box sx={{ overflowX: "auto" }}>
          <Table size="small" data-testid="hostds-table">
            <TableHead>
              <TableRow>
                <TableCell>Name</TableCell>
                <TableCell>Owner</TableCell>
                <TableCell>Labels</TableCell>
                <TableCell>Status</TableCell>
                <TableCell>Auth</TableCell>
                <TableCell align="right">Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {(hostds || []).map((h) => {
                const isEditing = editing?.name === h.name;
                const isRenaming = renaming?.oldName === h.name;
                const remote = h.name !== "local" && !h.revoked_at && !h.shared;
                const keyBound = Boolean(h.key_fingerprint);
                const canDelete =
                  h.name !== "local" && !h.shared && Boolean(h.revoked_at);
                return (
                  <TableRow key={h.name} data-testid={`hostd-row-${h.name}`}>
                    <TableCell>
                      {isRenaming ? (
                        <Stack
                          direction="row"
                          spacing={0.5}
                          alignItems="center"
                        >
                          <TextField
                            size="small"
                            value={renaming.newName}
                            onChange={(e) =>
                              setRenaming({
                                ...renaming,
                                newName: e.target.value,
                              })
                            }
                            inputProps={{ "aria-label": "New host name" }}
                          />
                          <Button
                            size="small"
                            disabled={busy || !renaming.newName.trim()}
                            onClick={saveRename}
                          >
                            Save
                          </Button>
                          <Button
                            size="small"
                            onClick={() => {
                              settle();
                              setRenaming(null);
                            }}
                          >
                            Cancel
                          </Button>
                        </Stack>
                      ) : (
                        h.name
                      )}
                    </TableCell>
                    <TableCell>
                      {isEditing ? (
                        <TextField
                          size="small"
                          value={editing.owner}
                          onChange={(e) =>
                            setEditing({ ...editing, owner: e.target.value })
                          }
                          inputProps={{ "aria-label": "Owner" }}
                        />
                      ) : h.owner ? (
                        h.owner
                      ) : remote || h.shared ? (
                        <Typography
                          variant="body2"
                          color="text.secondary"
                          data-testid={`hostd-owner-missing-${h.name}`}
                        >
                          None: needed to dial in through IAP
                        </Typography>
                      ) : (
                        "—"
                      )}
                    </TableCell>
                    <TableCell>
                      {isEditing ? (
                        <Stack direction="row" spacing={0.5}>
                          <TextField
                            size="small"
                            value={editing.labels}
                            onChange={(e) =>
                              setEditing({ ...editing, labels: e.target.value })
                            }
                            inputProps={{ "aria-label": "Labels" }}
                          />
                          <TextField
                            size="small"
                            value={editing.capacity}
                            onChange={(e) =>
                              setEditing({
                                ...editing,
                                capacity: e.target.value,
                              })
                            }
                            placeholder="Capacity"
                            sx={{ width: 90 }}
                            inputProps={{ "aria-label": "Capacity" }}
                          />
                        </Stack>
                      ) : (
                        <Stack
                          direction="row"
                          spacing={0.5}
                          flexWrap="wrap"
                          useFlexGap
                        >
                          {h.capacity > 0 ? (
                            <Chip
                              size="small"
                              variant="outlined"
                              label={`cap=${h.capacity}`}
                            />
                          ) : null}
                          {Object.keys(h.labels || {})
                            .sort()
                            .map((k) => (
                              <Chip
                                key={k}
                                size="small"
                                label={`${k}=${h.labels[k]}`}
                              />
                            ))}
                        </Stack>
                      )}
                    </TableCell>
                    <TableCell>
                      <Box>{connectionText(h)}</Box>
                      {h.last_seen_at ? (
                        <Typography variant="caption" color="text.secondary">
                          Last check-in {formatDateTime(h.last_seen_at, zone)}
                        </Typography>
                      ) : null}
                    </TableCell>
                    <TableCell>
                      {h.shared ? (
                        <Typography
                          variant="body2"
                          color="text.secondary"
                          data-testid={`hostd-shared-${h.name}`}
                          sx={{ whiteSpace: "nowrap" }}
                        >
                          Shared token (deprecated)
                        </Typography>
                      ) : keyBound ? (
                        <Box>
                          <code title={h.key_fingerprint}>
                            {shortFingerprint(h.key_fingerprint)}
                          </code>
                          {h.enrollment_code ? (
                            <Typography
                              variant="caption"
                              color="text.secondary"
                              sx={{ display: "block" }}
                            >
                              via {h.enrollment_code}
                            </Typography>
                          ) : null}
                        </Box>
                      ) : h.token_prefix ? (
                        <code>{h.token_prefix}…</code>
                      ) : (
                        "—"
                      )}
                    </TableCell>
                    <TableCell align="right" sx={{ whiteSpace: "nowrap" }}>
                      {isEditing ? (
                        <>
                          <Button size="small" disabled={busy} onClick={save}>
                            Save
                          </Button>
                          <Button
                            size="small"
                            onClick={() => {
                              settle();
                              setEditing(null);
                            }}
                          >
                            Cancel
                          </Button>
                        </>
                      ) : (
                        <>
                          {!h.revoked_at && !h.shared ? (
                            <Button
                              size="small"
                              onClick={() =>
                                setEditing({
                                  name: h.name,
                                  owner: h.owner || "",
                                  labels: formatLabels(h.labels),
                                  capacity: h.capacity
                                    ? String(h.capacity)
                                    : "",
                                })
                              }
                            >
                              Edit
                            </Button>
                          ) : null}
                          {remote ? (
                            <>
                              <Button
                                size="small"
                                onClick={() =>
                                  setRenaming({
                                    oldName: h.name,
                                    newName: h.name,
                                  })
                                }
                              >
                                Rename
                              </Button>
                              {!keyBound ? (
                                <>
                                  <Button
                                    size="small"
                                    onClick={() => showInstall(h)}
                                  >
                                    Install
                                  </Button>
                                  <Button
                                    size="small"
                                    disabled={busy}
                                    onClick={() => rotate(h)}
                                  >
                                    Rotate
                                  </Button>
                                </>
                              ) : null}
                              <Button
                                size="small"
                                color="error"
                                disabled={busy}
                                onClick={() => revoke(h)}
                              >
                                Revoke
                              </Button>
                            </>
                          ) : null}
                          {canDelete ? (
                            <Button
                              size="small"
                              color="error"
                              disabled={busy}
                              onClick={() => remove(h)}
                            >
                              Delete
                            </Button>
                          ) : null}
                        </>
                      )}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </Box>
      </SettingsCard>

      <SettingsCard title="Add a host (legacy token)" anchor="add-hostd">
        <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
          Prefer <strong>Invite hosts</strong> above for Ed25519 key-bound
          enrollment. Use a per-host token only for non-interactive setups that
          cannot wait for the owner's acceptance.
        </Typography>
        <Stack
          direction={{ xs: "column", sm: "row" }}
          spacing={1}
          sx={{ mt: 1 }}
          alignItems={{ sm: "center" }}
        >
          <TextField
            size="small"
            label="Name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            helperText="Lowercase letters, digits and -"
          />
          <TextField
            size="small"
            label="Owner"
            value={owner}
            onChange={(e) => setOwner(e.target.value)}
            helperText="The email or service account it connects as"
          />
          <TextField
            size="small"
            label="Labels"
            value={labels}
            onChange={(e) => setLabels(e.target.value)}
            helperText="key=value,key2=value2"
          />
          <Button
            variant="contained"
            disabled={busy || !name.trim()}
            onClick={create}
            sx={{ alignSelf: { sm: "flex-start" }, mt: { sm: 0.5 } }}
          >
            Add host
          </Button>
        </Stack>
      </SettingsCard>
    </>
  );
}
