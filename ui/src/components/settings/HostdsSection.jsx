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
import CopyButton from "../CopyButton.jsx";
import SettingsCard from "./SettingsCard.jsx";

// formatLabels is a hostd's labels as the label field takes them:
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

// installCommand is the one line that installs hostd name on another
// machine from this daemon's /api/hostd/install.sh (pkg/ui/hostd_install.sh).
// The token is not in it: the script asks for it on the terminal, so it
// stays out of the URL and the shell's history. The daemon's release and
// image are (install, the issued token's HostdInstall): the script tells
// a caller without access neither, and the machine running it has none.
export function installCommand(origin, name, install) {
  let cmd = `sudo sh -c "$(curl -fsSL '${origin}/api/hostd/install.sh')" grain-hostd-install --name ${name}`;
  cmd += install?.release ? ` --release ${install.release}` : " --any-version";
  if (install?.image) cmd += ` --image ${install.image}`;
  return cmd;
}

// installLines is what installs hostd name on its machine, one shell line
// each: the lines the daemon rendered (install.lines, pkg/ui's
// RegisterHostdInstall, which a build may replace), or -- from an older
// daemon that sends none -- the root installer's line, assembled here.
// Neither ever carries the token.
export function installLines(origin, name, install) {
  if (Array.isArray(install?.lines) && install.lines.length > 0) {
    return install.lines;
  }
  return [installCommand(origin, name, install)];
}

// InstallInstructions shows how to install hostd name on its machine:
// the lines as one copyable block, and the daemon's note beside them.
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

// connectionText says whether a hostd is connected, as the status
// column shows it.
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

// HostdsSection is Settings → Hostds: the hostd registry (model.Hostd).
// The co-located hostd is listed as local; every other hostd dials in to
// /api/hostd/connect with a token of its own, which names it. A token is
// shown once, when the hostd is created or its token rotated.
export default function HostdsSection({ showError }) {
  const [hostds, setHostds] = useState(null);
  const [name, setName] = useState("");
  const [owner, setOwner] = useState("");
  const [labels, setLabels] = useState("");
  const [issued, setIssued] = useState(null);
  const [shown, setShown] = useState(null);
  const [editing, setEditing] = useState(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      setHostds(await api("/api/hostds"));
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
      await load();
    } catch (err) {
      showError?.(err);
    } finally {
      setBusy(false);
    }
  };

  const create = () =>
    act(async () => {
      const out = await api("/api/hostds", {
        method: "POST",
        body: JSON.stringify({
          name: name.trim(),
          owner: owner.trim(),
          labels: parseLabels(labels),
        }),
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
  // showInstall shows a registered hostd's install instructions again,
  // without rotating its token (GET /api/hostds/{name}/install).
  const showInstall = async (h) => {
    try {
      const install = await api(
        `/api/hostds/${encodeURIComponent(h.name)}/install`,
      );
      setShown({ name: h.name, install });
    } catch (err) {
      showError?.(err);
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
  const save = () =>
    act(async () => {
      await api(`/api/hostds/${encodeURIComponent(editing.name)}`, {
        method: "PATCH",
        body: JSON.stringify({
          owner: editing.owner.trim(),
          labels: parseLabels(editing.labels),
        }),
      });
      setEditing(null);
    });

  return (
    <>
      <SettingsCard title="Hosts" anchor="hostds">
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          The hosts this daemon runs host work through. local is the one on this
          machine; any other dials in with{" "}
          <code>
            grain host -server &lt;this daemon&apos;s URL&gt; -token-file
            &lt;file&gt;
          </code>{" "}
          and is known by the token it presents. A host that dials in through
          IAP is also admitted only as its owner: set the owner to the email or
          service account it connects as. Adding one shows the command that
          installs it on its machine; Install shows it again.
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
            when it was added or last rotated; rotate it for a new one.
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
        <Box sx={{ overflowX: "auto" }}>
          <Table size="small" data-testid="hostds-table">
            <TableHead>
              <TableRow>
                <TableCell>Name</TableCell>
                <TableCell>Owner</TableCell>
                <TableCell>Labels</TableCell>
                <TableCell>Status</TableCell>
                <TableCell>Token</TableCell>
                <TableCell align="right">Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {(hostds || []).map((h) => {
                const isEditing = editing?.name === h.name;
                // The deprecated shared token's hostd is not in the
                // registry: it is shown, and changed only by the daemon's
                // -hostd-token-* flags.
                const remote = h.name !== "local" && !h.revoked_at && !h.shared;
                return (
                  <TableRow key={h.name} data-testid={`hostd-row-${h.name}`}>
                    <TableCell>{h.name}</TableCell>
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
                        <TextField
                          size="small"
                          value={editing.labels}
                          onChange={(e) =>
                            setEditing({ ...editing, labels: e.target.value })
                          }
                          inputProps={{ "aria-label": "Labels" }}
                        />
                      ) : (
                        <Stack direction="row" spacing={0.5} flexWrap="wrap">
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
                    <TableCell>{connectionText(h)}</TableCell>
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
                          <Button size="small" onClick={() => setEditing(null)}>
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
      <SettingsCard title="Add a host" anchor="add-hostd">
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
