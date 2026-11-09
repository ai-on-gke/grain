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
  Link,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import api from "../api.js";
import AppIcon from "./AppIcon.jsx";
import { identifierField } from "../identifierField.js";
import { serverPath } from "./CapabilityServersPanel.jsx";
import { ReadOnlyFieldset } from "./settings/SettingsCard.jsx";

// bwsalmon/agents#357: this pane can set and delete secrets on a server
// this UI shares a host with, and report which ones exist, but it never
// asks for -- or gets back -- a value. GET /api/secrets' own "enabled"
// flag says whether that colocation is configured at all; when it isn't,
// the list and form are hidden behind a note instead of showing controls
// that could only ever 404.
//
// grain/task-110: this was the whole Secrets tab, a flat list of every
// secret in the store with a three-box form under it. Credentials with a
// dedicated field sit with whatever uses them -- each backend's credential
// under Settings -> Models and the hub token under Settings -> Hub.
//
// What is left is this: the remainder, on the Deployment credentials card at
// the foot of Settings -> Capabilities. `claimed` names the secrets a control
// elsewhere in Settings already owns, and they are filtered out here rather
// than shown twice. The free-form form stays, so credentials with no dedicated
// field -- github-app, gcp-key-minter, release signing/push credentials, or
// anything a custom capability reads -- remain visible, settable and deletable
// from the UI rather than only from `grain secrets` on the host.
//
// grain/task-2263: GET /api/secrets says which capability servers use
// each secret (capabilityServers) -- their $secret: references, and the
// secrets grain made for them. Those are a server's own, set on its page
// under Settings -> Capabilities, so they are listed apart with a link
// there and no Delete, which would break the server. One grain made for
// a server that no configured server uses any more (orphaned: left by a
// rename) stays in Other secrets, labelled, so it can still be deleted.
// refreshKey reads the store again whenever it changes: the capability
// servers card above bumps it when a server's own secrets may have changed.
// readOnly draws the list's deletes and the set form disabled, with
// readOnlyReason as their tooltip: for a role without secrets:write,
// which every PUT and DELETE below needs (pkg/ui/route_scopes.go). The
// Deployment credentials card follows Capabilities' capabilities:admin,
// so that alone did not stop a role holding it without secrets:write
// from being offered buttons the server refused with 403 (2026-11-08
// deployment credentials walkthrough).
export default function SecretsPanel({
  showError,
  claimed,
  onSecretsChanged,
  refreshKey,
  readOnly = false,
  readOnlyReason,
}) {
  const [resp, setResp] = useState(null);

  const refresh = useCallback(async () => {
    try {
      const data = await api("/api/secrets");
      if (data) setResp(data);
    } catch (err) {
      showError(err);
    }
  }, [showError]);

  useEffect(() => {
    refresh();
  }, [refresh, refreshKey]);

  // Both deletes are confirmed first: nothing here can read a value
  // back, so a stray press -- or Backspace on a focused key chip, which
  // is the chip's own delete -- would otherwise lose it for good.
  const deleteKey = async (secret, key) => {
    if (
      !window.confirm(
        `Delete ${secret}/${key}? The stored value cannot be read back, so putting it back means pasting it again.`,
      )
    )
      return;
    try {
      await api(
        `/api/secrets/${encodeURIComponent(secret)}/${encodeURIComponent(key)}`,
        { method: "DELETE" },
      );
      await refresh();
      await onSecretsChanged?.();
    } catch (err) {
      showError(err);
    }
  };

  const deleteSecret = async (secret) => {
    if (
      !window.confirm(
        `Delete the secret ${secret} and every key in it? No stored value can be read back, so putting them back means pasting each one again.`,
      )
    )
      return;
    try {
      await api(`/api/secrets/${encodeURIComponent(secret)}`, {
        method: "DELETE",
      });
      await refresh();
      await onSecretsChanged?.();
    } catch (err) {
      showError(err);
    }
  };

  const submit = async (evt) => {
    evt.preventDefault();
    const form = evt.target;
    const secret = form.elements.secret.value.trim();
    const key = form.elements.key.value.trim();
    const value = form.elements.value.value;
    // `required` lets a name of only spaces through, and the server
    // refuses one once trimmed; say so here rather than send it.
    if (!secret || !key) {
      showError("Secret and key names cannot be blank");
      return;
    }
    try {
      await api(
        `/api/secrets/${encodeURIComponent(secret)}/${encodeURIComponent(key)}`,
        {
          method: "PUT",
          body: JSON.stringify({ value }),
        },
      );
      form.reset();
      await refresh();
      await onSecretsChanged?.();
    } catch (err) {
      showError(err);
    }
  };

  if (!resp) return null;

  // A secret claimed elsewhere is dropped from the listing, not from the
  // store: setting one from the form below still works, it just shows up
  // where it belongs afterwards rather than here.
  const owned = new Set(claimed || []);
  const listed = (resp.secrets || []).filter(
    (s) => !owned.has(s.name) && !s.name.startsWith("repo-release-"),
  );
  const serverSecrets = listed.filter((s) => s.capabilityServers?.length);
  const others = listed.filter((s) => !s.capabilityServers?.length);

  return (
    <Box id="other-secrets" sx={{ mt: 3, scrollMarginTop: 16 }}>
      <Typography variant="subtitle2">Other secrets</Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
        Everything in this deployment&apos;s secrets store that nothing else
        claims -- backend credentials are set on each connection under Settings
        &rarr; Models, the hub token under Settings &rarr; Hub, and each
        capability server&apos;s own on its page under{" "}
        <Link href="/settings/capabilities#capability-servers">
          Capability servers
        </Link>{" "}
        above. Names and keys only, never values: a secret can be set, replaced
        and deleted from here, and never read back.
      </Typography>
      {!resp.enabled && (
        <Alert severity="info" sx={{ mb: 2 }}>
          Not available: this UI was not started with a local secrets directory
          to write to (see -server-data-dir), which only works when the UI runs
          on the same host as the server (bwsalmon/agents#357). You can only set
          and delete secrets here, never read one back.
        </Alert>
      )}
      {resp.enabled && (
        <ReadOnlyFieldset
          disabled={Boolean(readOnly)}
          title={readOnlyReason || undefined}
        >
          {serverSecrets.length > 0 && (
            <>
              <Typography variant="body2" sx={{ fontWeight: 500 }}>
                Used by capability servers
              </Typography>
              <ul
                className="secrets-list"
                aria-label="Secrets used by capability servers"
              >
                {serverSecrets.map((s) => (
                  <li className="secret-row" key={s.name}>
                    <span className="secret-name">{s.name}</span>
                    <Box
                      sx={{
                        display: "flex",
                        gap: 0.6,
                        flexWrap: "wrap",
                        flex: 1,
                      }}
                    >
                      {s.keys.map((key) => (
                        <Chip key={key} size="small" label={key} />
                      ))}
                    </Box>
                    <Typography
                      variant="body2"
                      color="text.secondary"
                      sx={{ overflowWrap: "anywhere" }}
                    >
                      Used by{" "}
                      {s.capabilityServers.map((id, i) => (
                        <span key={id}>
                          {i > 0 && ", "}
                          <Link href={serverPath(id)}>{id}</Link>
                        </span>
                      ))}
                    </Typography>
                  </li>
                ))}
              </ul>
            </>
          )}
          <ul className="secrets-list">
            {others.map((s) => (
              <li className="secret-row" key={s.name}>
                <span className="secret-name">{s.name}</span>
                {s.orphaned && (
                  <Chip
                    size="small"
                    color="warning"
                    variant="outlined"
                    label="orphaned"
                    title="Grain made this for a capability server that no configured server uses any more"
                  />
                )}
                <Box
                  sx={{ display: "flex", gap: 0.6, flexWrap: "wrap", flex: 1 }}
                >
                  {s.keys.map((key) => (
                    <Chip
                      key={key}
                      size="small"
                      label={key}
                      onDelete={
                        readOnly ? undefined : () => deleteKey(s.name, key)
                      }
                      deleteIcon={
                        <span
                          title={`delete ${s.name}/${key}`}
                          style={{
                            display: "inline-flex",
                            alignItems: "center",
                          }}
                        >
                          <AppIcon name="close" size={14} />
                        </span>
                      }
                    />
                  ))}
                </Box>
                <Button
                  size="small"
                  variant="outlined"
                  startIcon={<AppIcon name="delete" size={15} />}
                  onClick={() => deleteSecret(s.name)}
                >
                  Delete secret
                </Button>
              </li>
            ))}
          </ul>
          {others.length === 0 && (
            <p className="empty">No other secrets set.</p>
          )}
          <form onSubmit={submit}>
            <TextField
              name="secret"
              label="Secret"
              placeholder="github"
              autoComplete="off"
              inputProps={identifierField}
              required
              InputLabelProps={{ required: false }}
              fullWidth
              margin="normal"
            />
            <TextField
              name="key"
              label="Key"
              placeholder="token"
              autoComplete="off"
              inputProps={identifierField}
              required
              InputLabelProps={{ required: false }}
              fullWidth
              margin="normal"
            />
            <TextField
              name="value"
              label="Value"
              helperText="write-only -- never shown or read back"
              type="password"
              autoComplete="off"
              required
              InputLabelProps={{ required: false }}
              fullWidth
              margin="normal"
            />
            <Stack direction="row" justifyContent="flex-end" sx={{ mt: 2 }}>
              <Button type="submit" variant="contained">
                Set secret
              </Button>
            </Stack>
          </form>
        </ReadOnlyFieldset>
      )}
    </Box>
  );
}
