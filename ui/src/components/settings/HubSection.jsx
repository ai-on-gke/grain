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
  Checkbox,
  Chip,
  FormControlLabel,
  IconButton,
  MenuItem,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import api from "../../api.js";
import AppIcon from "../AppIcon.jsx";
import SecretField from "../SecretField.jsx";
import { ConfigManagedNotice } from "../ConfigRepoLink.jsx";
import { identifierField } from "../../identifierField.js";
import SettingsCard, { ReadOnlyFieldset } from "./SettingsCard.jsx";
import { formatDateTime } from "../../time.js";
import { useTimeZone } from "../../TimeZoneContext.jsx";

function connectionBadge(state, enabled) {
  if (!enabled || state === "disabled") {
    return { label: "Not configured", color: "default", variant: "outlined" };
  }
  switch (state) {
    case "connected":
      return { label: "Connected", color: "success", variant: "filled" };
    case "degraded":
      return { label: "Degraded", color: "warning", variant: "filled" };
    case "error":
      return { label: "Heartbeat error", color: "error", variant: "filled" };
    case "connecting":
    default:
      return { label: "Connecting", color: "info", variant: "outlined" };
  }
}

function originLabel(origin) {
  switch (origin) {
    case "override":
      return "override";
    case "added":
      return "added in settings";
    case "repo_config":
    default:
      return "from repo config";
  }
}

// HubSection is Settings -> Hub (design/design/hub.design.md,
// "Connected entries"): live connection and heartbeat state, advertised scopes,
// registration credential secret, and owner-managed catalog metadata.
export default function HubSection({ s }) {
  const zone = useTimeZone();
  const {
    settings,
    saveChoice,
    onEnterKey,
    reloadSettings,
    showError,
    onSaved,
    readOnly,
  } = s;

  const reg = settings.hub || settings.registry || {};
  const tokenSet = Boolean(settings.hubTokenSet ?? settings.registryTokenSet);
  const authMode = reg.authMode || "token";
  const rules = Array.isArray(reg.scopes) ? reg.scopes : [];
  const locked = Boolean(readOnly) || Boolean(s.configLocked);

  const [snapshot, setSnapshot] = useState(null);
  const [triggering, setTriggering] = useState(false);
  const [newRepo, setNewRepo] = useState("");
  const [newPrefix, setNewPrefix] = useState("");
  const [newMode, setNewMode] = useState("primary");

  const loadSnapshot = useCallback(async () => {
    try {
      const data = await api("/api/registry");
      if (data && typeof data === "object") {
        setSnapshot(data);
      }
    } catch {
      // Non-fatal: status card renders from stored settings until /api/registry responds.
    }
  }, []);

  useEffect(() => {
    loadSnapshot();
  }, [loadSnapshot, reg.url, reg.entry, reg.authMode, tokenSet]);

  const updateRegistry = (patch) => {
    const next = {
      ...reg,
      ...patch,
    };
    saveChoice({ hub: next });
  };

  const onRegistryTextBlur = (field) => (evt) => {
    const nextVal = evt.target.value.trim();
    if ((reg[field] || "") === nextVal) return;
    updateRegistry({ [field]: nextVal });
  };

  const handleHeartbeatNow = async () => {
    setTriggering(true);
    try {
      const nextSnap = await api("/api/registry/heartbeat", { method: "POST" });
      if (nextSnap && typeof nextSnap === "object") {
        setSnapshot(nextSnap);
      }
    } catch (err) {
      showError?.(err);
      await loadSnapshot();
    } finally {
      setTriggering(false);
    }
  };

  const handleSecretChanged = async () => {
    await reloadSettings?.();
    await onSaved?.();
    await loadSnapshot();
  };

  const handleAddRule = () => {
    const repo = newRepo.trim();
    if (!repo) return;
    const prefix = newPrefix.trim().replace(/^\/+|\/+$/g, "");
    const rule =
      newMode === "exclude"
        ? { repo, pathPrefix: prefix, exclude: true }
        : { repo, pathPrefix: prefix, role: newMode };
    updateRegistry({ scopes: [...rules, rule] });
    setNewRepo("");
    setNewPrefix("");
  };

  const handleRemoveRule = (idx) => {
    updateRegistry({ scopes: rules.filter((_, i) => i !== idx) });
  };

  const enabled = Boolean(snapshot?.enabled || (reg.url && reg.entry));
  const badge = connectionBadge(snapshot?.state || "disabled", enabled);
  const advertised = Array.isArray(snapshot?.advertisedScopes)
    ? snapshot.advertisedScopes
    : [];
  const conflicts = Array.isArray(snapshot?.scopeConflicts)
    ? snapshot.scopeConflicts
    : [];

  return (
    <>
      <SettingsCard
        title="Hub connection"
        anchor="registry-status"
        allowInExternalMode
      >
        <Stack
          direction={{ xs: "column", sm: "row" }}
          spacing={1.5}
          alignItems={{ xs: "flex-start", sm: "center" }}
          justifyContent="space-between"
        >
          <Stack
            direction="row"
            spacing={1}
            alignItems="center"
            flexWrap="wrap"
            useFlexGap
            sx={{ minWidth: 0 }}
          >
            <Chip
              size="small"
              label={badge.label}
              color={badge.color}
              variant={badge.variant}
              data-testid="registry-state-chip"
            />
            {enabled && (
              <Typography
                variant="body2"
                data-testid="registry-endpoint-label"
                sx={{ fontFamily: "monospace", overflowWrap: "anywhere" }}
              >
                {reg.url} · {reg.entry}
              </Typography>
            )}
          </Stack>
          <Button
            type="button"
            size="small"
            variant="outlined"
            disabled={!enabled || triggering}
            onClick={handleHeartbeatNow}
            data-testid="registry-heartbeat-now"
          >
            {triggering ? "Sending…" : "Heartbeat now"}
          </Button>
        </Stack>

        <Typography variant="body2" color="text.secondary" sx={{ mt: 1.5 }}>
          {!enabled
            ? "Configure a hub URL and entry name below (or run 'grain hub register') so this daemon self-registers as a connected entry and heartbeats its version, health, task counts, and repository scopes."
            : snapshot?.lastHeartbeatAt
              ? `Last heartbeat ${formatDateTime(snapshot.lastHeartbeatAt, zone)}${snapshot.heartbeatIntervalSeconds ? ` (every ${snapshot.heartbeatIntervalSeconds}s)` : ""} · health ${snapshot.health || "ok"} · ${snapshot.activeTasks ?? 0} active, ${snapshot.awaitingReplyTasks ?? 0} awaiting reply`
              : "Waiting for first heartbeat…"}
        </Typography>

        {snapshot?.lastError && (
          <Alert
            severity="error"
            sx={{ mt: 1.5 }}
            data-testid="registry-last-error"
          >
            {snapshot.lastError}
          </Alert>
        )}

        {conflicts.length > 0 && (
          <Alert
            severity="warning"
            sx={{ mt: 1.5 }}
            data-testid="registry-scope-conflicts"
          >
            Scope conflict reported by the hub:{" "}
            {conflicts
              .map(
                (c) =>
                  `${c.repo}${c.path_prefix ? ` (${c.path_prefix})` : ""} already claimed as primary by ${c.conflicting_entry || "another entry"}`,
              )
              .join("; ")}
          </Alert>
        )}

        <Typography variant="subtitle2" sx={{ mt: 2.5, mb: 1 }}>
          Advertised scopes
        </Typography>
        {advertised.length === 0 ? (
          <Typography variant="body2" color="text.secondary">
            No repositories advertised yet. Configured repositories are reported
            automatically from Settings → Repositories.
          </Typography>
        ) : (
          <Stack spacing={0.75} data-testid="registry-advertised-scopes">
            {advertised.map((sc) => (
              <Box
                key={`${sc.repo}:${sc.pathPrefix || ""}`}
                sx={{
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "space-between",
                  flexWrap: "wrap",
                  gap: 1,
                  px: 1.25,
                  py: 0.75,
                  border: 1,
                  borderColor: "divider",
                  borderRadius: 1,
                }}
              >
                <Box
                  sx={{
                    display: "flex",
                    alignItems: "center",
                    gap: 1,
                    flexWrap: "wrap",
                  }}
                >
                  <Typography
                    variant="body2"
                    sx={{ fontFamily: "monospace", fontWeight: 500 }}
                  >
                    {sc.repo}
                  </Typography>
                  <Chip
                    size="small"
                    variant="outlined"
                    label={
                      sc.pathPrefix ? `${sc.pathPrefix}/` : "/ (whole repo)"
                    }
                    sx={{ fontFamily: "monospace" }}
                  />
                </Box>
                <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
                  <Chip
                    size="small"
                    color={sc.role === "primary" ? "primary" : "default"}
                    variant={sc.role === "primary" ? "filled" : "outlined"}
                    label={sc.role}
                  />
                  <Typography variant="caption" color="text.secondary">
                    {originLabel(sc.origin)}
                  </Typography>
                </Box>
              </Box>
            ))}
          </Stack>
        )}
      </SettingsCard>

      <SettingsCard
        title="Catalog & authentication"
        anchor="registry-config"
        allowInExternalMode
      >
        <ReadOnlyFieldset disabled={locked}>
          {!readOnly && s.configLocked && (
            <ConfigManagedNotice
              what="Hub connection and catalog settings"
              paths={["tables/grain_config.json"]}
            />
          )}

          <TextField
            key={`registryUrl-${reg.url || ""}`}
            name="registryUrl"
            data-setting-id="registryUrl"
            label="Hub URL"
            helperText="Base https:// URL of the Grain Hub (leave empty together with Entry name to disable self-registration)."
            defaultValue={reg.url || ""}
            onBlur={onRegistryTextBlur("url")}
            onKeyDown={onEnterKey}
            autoComplete="off"
            inputProps={identifierField}
            fullWidth
            margin="normal"
          />

          <TextField
            key={`registryEntry-${reg.entry || ""}`}
            name="registryEntry"
            data-setting-id="registryEntry"
            label="Entry name"
            helperText="Unique 3..32 lowercase DNS label for this grain in the hub directory (e.g. acme-widgets)."
            defaultValue={reg.entry || ""}
            onBlur={onRegistryTextBlur("entry")}
            onKeyDown={onEnterKey}
            autoComplete="off"
            inputProps={identifierField}
            fullWidth
            margin="normal"
          />

          <Stack
            direction={{ xs: "column", sm: "row" }}
            spacing={2}
            sx={{ mt: 1 }}
          >
            <TextField
              select
              name="registryAuthMode"
              data-setting-id="registryAuthMode"
              label="Credential source"
              value={authMode}
              onChange={(evt) => updateRegistry({ authMode: evt.target.value })}
              helperText="How heartbeats authenticate to the hub."
              fullWidth
            >
              <MenuItem value="token">
                Registration credential (grc_… secret)
              </MenuItem>
              <MenuItem value="oidc">OIDC ID token</MenuItem>
            </TextField>

            <TextField
              select
              name="registryHostKind"
              data-setting-id="registryHostKind"
              label="Host kind"
              value={reg.hostKind || ""}
              onChange={(evt) => updateRegistry({ hostKind: evt.target.value })}
              helperText="Defaults automatically from the active sandbox backend when unset."
              fullWidth
            >
              <MenuItem value="">
                {/* The effective kind is the chosen one while a kind is
                  chosen, so it says nothing about what Auto-detect would
                  pick then. */}
                {!reg.hostKind && snapshot?.effectiveHostKind
                  ? `Auto-detect (${snapshot.effectiveHostKind})`
                  : "Auto-detect"}
              </MenuItem>
              <MenuItem value="workstation">Workstation</MenuItem>
              <MenuItem value="kubernetes">Kubernetes</MenuItem>
              <MenuItem value="other">Other</MenuItem>
            </TextField>
          </Stack>

          <TextField
            key={`registryOidcAudience-${reg.oidcAudience || ""}`}
            name="registryOidcAudience"
            data-setting-id="registryOidcAudience"
            label="OIDC audience"
            disabled={locked || authMode !== "oidc"}
            helperText="Target audience when Credential source is OIDC ID token (defaults to Hub URL when blank)."
            defaultValue={reg.oidcAudience || ""}
            onBlur={onRegistryTextBlur("oidcAudience")}
            onKeyDown={onEnterKey}
            autoComplete="off"
            inputProps={identifierField}
            fullWidth
            margin="normal"
          />

          <TextField
            key={`registryDisplayName-${reg.displayName || ""}`}
            name="registryDisplayName"
            data-setting-id="registryDisplayName"
            label="Display name"
            helperText="Human-readable title shown on this grain's card in the hub directory."
            defaultValue={reg.displayName || ""}
            onBlur={onRegistryTextBlur("displayName")}
            onKeyDown={onEnterKey}
            autoComplete="off"
            fullWidth
            margin="normal"
          />

          <TextField
            key={`registryPurpose-${reg.purpose || ""}`}
            name="registryPurpose"
            data-setting-id="registryPurpose"
            label="Purpose"
            helperText="Short description of what repositories or workflows this grain handles."
            defaultValue={reg.purpose || ""}
            onBlur={onRegistryTextBlur("purpose")}
            onKeyDown={onEnterKey}
            autoComplete="off"
            fullWidth
            margin="normal"
          />

          <FormControlLabel
            control={
              <Checkbox
                name="registryListed"
                data-setting-id="registryListed"
                // Unset is unlisted: the daemon registers an entry with
                // listed only when this is true, and a saved false is
                // omitted from the settings (2026-11-11 walkthrough: the
                // box showed ticked for an unlisted grain, and unticking
                // it came back ticked).
                checked={reg.listed === true}
                onChange={(evt) =>
                  updateRegistry({ listed: evt.target.checked })
                }
              />
            }
            label={
              <>
                Listed in hub directory{" "}
                <span className="hint">
                  when off, this grain stays addressable by name and route
                  queries for its owners but is omitted from default directory
                  listings
                </span>
              </>
            }
            sx={{ display: "flex", mt: 1 }}
          />
        </ReadOnlyFieldset>

        <Typography variant="subtitle2" sx={{ mt: 3 }}>
          Registration credential
        </Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 0.5 }}>
          Minted by <code>grain hub register</code> (or rotated in the hub) and
          stored encrypted in this daemon&apos;s secret store; never exported to{" "}
          <code>grain/config</code>.
        </Typography>
        <SecretField
          secret={{
            name: "Registration token (grc_…)",
            secret: "registry-registration-token",
            key: "value",
            set: tokenSet,
          }}
          showError={showError || (() => {})}
          onChanged={handleSecretChanged}
        />
      </SettingsCard>

      <SettingsCard
        title="Scope overrides"
        anchor="registry-scopes"
        allowInExternalMode
      >
        <ReadOnlyFieldset disabled={locked}>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 1.5 }}>
            Scopes come automatically from Settings → Repositories and each
            repo&apos;s work ownership (<code>on</code> → <code>primary</code>,{" "}
            <code>off</code> → <code>secondary</code>). Add a rule here only to
            narrow a repo to a path prefix, exclude a repo from hub routing, or
            advertise an extra scope.
          </Typography>

          {rules.length > 0 && (
            <Stack
              spacing={0.75}
              sx={{ mb: 2 }}
              data-testid="registry-scope-rules"
            >
              {rules.map((r, idx) => (
                <Box
                  key={`${r.repo}:${r.pathPrefix || ""}:${idx}`}
                  sx={{
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "space-between",
                    gap: 1,
                    px: 1.25,
                    py: 0.5,
                    border: 1,
                    borderColor: "divider",
                    borderRadius: 1,
                  }}
                >
                  <Box
                    sx={{
                      display: "flex",
                      alignItems: "center",
                      gap: 1,
                      flexWrap: "wrap",
                    }}
                  >
                    <Typography
                      variant="body2"
                      sx={{ fontFamily: "monospace" }}
                    >
                      {r.repo}
                    </Typography>
                    {r.pathPrefix && (
                      <Chip
                        size="small"
                        variant="outlined"
                        label={`${r.pathPrefix}/`}
                        sx={{ fontFamily: "monospace" }}
                      />
                    )}
                    <Chip
                      size="small"
                      color={
                        r.exclude
                          ? "default"
                          : r.role === "secondary"
                            ? "default"
                            : "primary"
                      }
                      variant={r.exclude ? "outlined" : "filled"}
                      label={r.exclude ? "excluded" : r.role || "primary"}
                    />
                  </Box>
                  <IconButton
                    size="small"
                    aria-label={`Remove scope rule for ${r.repo}`}
                    onClick={() => handleRemoveRule(idx)}
                  >
                    <AppIcon name="close" size={16} />
                  </IconButton>
                </Box>
              ))}
            </Stack>
          )}

          <Stack
            direction={{ xs: "column", sm: "row" }}
            spacing={1}
            alignItems={{ xs: "stretch", sm: "flex-start" }}
          >
            <TextField
              size="small"
              label="Repository (owner/name)"
              placeholder="acme/monorepo"
              value={newRepo}
              onChange={(evt) => setNewRepo(evt.target.value)}
              inputProps={identifierField}
              sx={{ flex: 2 }}
            />
            <TextField
              size="small"
              label="Path prefix (optional)"
              placeholder="services/billing"
              value={newPrefix}
              onChange={(evt) => setNewPrefix(evt.target.value)}
              inputProps={identifierField}
              sx={{ flex: 2 }}
            />
            <TextField
              select
              size="small"
              label="Rule"
              value={newMode}
              onChange={(evt) => setNewMode(evt.target.value)}
              sx={{ minWidth: 140 }}
            >
              <MenuItem value="primary">Primary</MenuItem>
              <MenuItem value="secondary">Secondary</MenuItem>
              <MenuItem value="exclude">Exclude repo</MenuItem>
            </TextField>
            <Button
              type="button"
              variant="outlined"
              size="small"
              disabled={!newRepo.trim()}
              onClick={handleAddRule}
              sx={{ height: 40, whiteSpace: "nowrap" }}
            >
              Add rule
            </Button>
          </Stack>
        </ReadOnlyFieldset>
      </SettingsCard>
    </>
  );
}
