import { Alert, Box, Button, Chip, Stack, Typography } from "@mui/material";
import { useCallback, useEffect, useState } from "react";
import api from "../../api.js";
import {
  advertisedModels,
  advertisesDiscovered,
  connectionPause,
  modelPause,
  modelPauses,
  NO_MODELS_LABEL,
} from "../../state.js";
import { useTimeZone } from "../../TimeZoneContext.jsx";
import { liftAgentPause, pausedUntil } from "../../alerts.js";
import { deploymentPath, useDeployment } from "../../useDeployment.js";
import { randomUUID } from "../../randomUUID.js";
import ModelBackendPage, { frameworkLabels } from "../ModelBackendPage.jsx";
import CLIRunLocations from "./CLIRunLocations.jsx";
import ModelDefaults from "./ModelDefaults.jsx";
import SettingsCard from "./SettingsCard.jsx";

// ModelsSection is Settings → Models (docs/design/settings-panel.md
// §3.2): the deployment's provider connections, then the default model
// and per-model settings (ModelDefaults). A connection is
// PUT/DELETE /api/deployment/agent-backends/{id}; the default model and
// model settings are PUT /api/deployment (useDeployment). Every stored
// connection is the deployment's: dispatch reads them all
// (model.DeploymentAgentBackends, docs/single-tenant-reset.md).
//
// It reads GET /api/settings itself rather than through the page's
// useSettingsSection: the connections it saves are kept here, and the
// editor (ModelBackendPage) takes the CLI defaults and efforts from the
// same read.
export default function ModelsSection({ config, onSaved, showError }) {
  const [editingBackend, setEditingBackend] = useState(null);
  const [settings, setSettings] = useState(null);
  const [agentBackends, setAgentBackends] = useState([]);
  const [testConnResults, setTestConnResults] = useState({});
  const [testingConnId, setTestingConnId] = useState(null);
  const zone = useTimeZone();
  const {
    deployment,
    error: deploymentError,
    saveDeployment,
  } = useDeployment({ onSaved, showError });

  const reloadSettings = useCallback(async () => {
    try {
      const loaded = await api("/api/settings");
      setSettings(loaded);
      setAgentBackends(loaded.agentBackends || []);
    } catch (err) {
      if (showError) showError(err);
    }
  }, [showError]);
  useEffect(() => {
    reloadSettings();
  }, [reloadSettings]);

  const backendPath = (id) =>
    `${deploymentPath}/agent-backends/${encodeURIComponent(id)}`;
  const refreshInBackground = () => {
    Promise.resolve()
      .then(() => onSaved?.())
      .catch((err) => {
        if (showError) showError(err);
      });
  };
  const saveBackend = async (backend) => {
    try {
      const saved = await api(backendPath(backend.id), {
        method: "PUT",
        body: JSON.stringify(backend),
      });
      const next = saved && saved.id ? saved : backend;
      setAgentBackends((current) =>
        current.some((b) => b.id === next.id)
          ? current.map((b) => (b.id === next.id ? next : b))
          : [...current, next],
      );
      // Refreshed in the background, as saveDeployment does: the PUT
      // has answered with the saved connection, which is already in
      // place above, and the editor holds its Save button until this
      // returns. Awaiting /api/config here kept a save that took a
      // millisecond on screen for as long as the whole config took to
      // read -- over a second on a deployment of a few thousand tasks
      // (grain/task-3903).
      refreshInBackground();
      return next;
    } catch (err) {
      if (showError) showError(err);
      return null;
    }
  };
  const deleteBackend = async (id) => {
    try {
      await api(backendPath(id), { method: "DELETE" });
      setAgentBackends((current) => current.filter((b) => b.id !== id));
      refreshInBackground();
    } catch (err) {
      if (showError) showError(err);
    }
  };
  // Removing a connection throws away the credential stored with it,
  // which nothing here can read back, so a stray press is asked about
  // first -- as SecretsPanel asks before deleting a key. An unfinished
  // new connection's own cleanup (onDiscard) is not a press, and has
  // already been asked about by the leave prompt, so it deletes directly.
  const removeBackend = async (id) => {
    const backend = agentBackends.find((b) => b.id === id);
    if (
      !window.confirm(
        `Remove the connection ${backend?.name || id}? A credential stored for it is deleted with it and cannot be read back, so adding it again means pasting that credential again.`,
      )
    )
      return;
    await deleteBackend(id);
  };

  if (!settings) {
    return (
      <Typography variant="body2" color="text.secondary">
        Loading settings...
      </Typography>
    );
  }

  if (editingBackend) {
    return (
      <SettingsCard label="Provider connections" anchor="models">
        <ModelBackendPage
          key={editingBackend.backend.id}
          backend={editingBackend.backend}
          backends={agentBackends}
          isNew={editingBackend.isNew}
          showError={showError}
          cliDefaults={settings?.agentCliDefaults || {}}
          onBack={() => setEditingBackend(null)}
          onDiscard={deleteBackend}
          onSave={async (backend) => {
            const saved = await saveBackend(backend);
            if (saved) {
              // Checked by id, not just truthiness: this call closes over
              // the backend it was saving, but it is not awaited by the
              // click that triggered it (ModelBackendPage's own
              // saveDraft awaits it, the test driving the click does
              // not), so it can still be in flight once that dialog is
              // gone. A slow save landing late used to pass the old
              // "current ? ... : null" check -- current was simply
              // whatever dialog was open by the time it resolved -- and
              // overwrite a second connection's own fresh, unsaved
              // editingBackend with the first one's saved copy, id and
              // all: ModelBackendPage is keyed on that id, so the second
              // dialog remounted as an edit of the first,
              // mid-interaction, with whatever the second connection's
              // own menu or button happened to be doing taken out from
              // under it (grain/task-50).
              setEditingBackend((current) =>
                current && current.backend.id === backend.id
                  ? { backend: saved, isNew: false }
                  : current,
              );
            }
            return Boolean(saved);
          }}
        />
      </SettingsCard>
    );
  }

  return (
    <>
      {deploymentError && (
        <Alert severity="error" sx={{ mb: 2 }}>
          Could not load the deployment&apos;s connections:{" "}
          {deploymentError.message || String(deploymentError)}
        </Alert>
      )}
      <SettingsCard label="Provider connections" anchor="models">
        {/* Connections first, then the models: the deployment needs a
              connection before it has any model to choose. */}
        <Stack
          direction={{ xs: "column", sm: "row" }}
          alignItems={{ xs: "stretch", sm: "center" }}
          justifyContent="space-between"
          spacing={1.5}
          sx={{ mb: 2 }}
        >
          <Box>
            <Typography variant="subtitle2">Provider Connections</Typography>
            <Typography variant="body2" color="text.secondary">
              A connection is a way to reach a provider: its credentials,
              concurrency and whether it is enabled. It advertises the models it
              serves; which model a task runs on is chosen below or by the task.
            </Typography>
          </Box>
          <Button
            type="button"
            variant="contained"
            onClick={() =>
              setEditingBackend({
                backend: {
                  // A random UUID rather than Date.now(): a
                  // wall clock that jumps -- a VM pause and resume,
                  // an NTP step -- can hand two clicks seconds apart
                  // the same millisecond, and ModelBackendPage is
                  // keyed on this id, so a repeat silently reopens
                  // the connection just saved under it instead of a
                  // fresh one (grain/task-50). randomUUID, not
                  // crypto.randomUUID: that one is undefined over
                  // plain http to a non-localhost host.
                  id: `backend-${randomUUID()}`,
                  name: "",
                  framework: "",
                  models: [],
                },
                isNew: true,
              })
            }
          >
            New connection
          </Button>
        </Stack>

        <Alert severity="info" sx={{ mb: 2 }}>
          The deployment&apos;s provider connections. Its tasks run only on
          these, and wait until it has one.
        </Alert>

        <Stack spacing={1.5} sx={{ mb: 3 }}>
          {agentBackends.map((backend) => {
            const isTesting = testingConnId === backend.id;
            const testRes = testConnResults[backend.id];
            // A usage-limit pause on this connection's credential
            // (config.agentPause), said here as well as on the banner:
            // "Active" alone reads as a connection dispatch is using.
            const paused = backend.disabled
              ? null
              : connectionPause(config?.agentPause, backend);
            // Pauses on single models of it: the connection still
            // serves its others, so it stays "Active" and the
            // models are marked one by one below.
            const pausedModels = backend.disabled
              ? []
              : modelPauses(config?.agentPause, backend);
            return (
              <Box
                key={backend.id}
                sx={{
                  border: 1,
                  borderColor: "divider",
                  borderRadius: 1,
                  p: 2,
                  display: "flex",
                  flexDirection: "column",
                  gap: 1.5,
                }}
              >
                <Box
                  sx={{
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "space-between",
                    flexWrap: "wrap",
                    gap: 1,
                  }}
                >
                  <Box>
                    <Stack direction="row" spacing={1} alignItems="center">
                      <Typography variant="subtitle1" sx={{ fontWeight: 600 }}>
                        {backend.name || backend.id}
                      </Typography>
                      <Chip
                        label={
                          frameworkLabels[backend.framework] ||
                          backend.framework ||
                          "Unknown framework"
                        }
                        size="small"
                        variant="outlined"
                      />
                      <Chip
                        label={
                          backend.disabled
                            ? "Disabled"
                            : paused
                              ? "Paused"
                              : "Active"
                        }
                        size="small"
                        color={
                          backend.disabled
                            ? "default"
                            : paused
                              ? "warning"
                              : "success"
                        }
                        variant="filled"
                      />
                    </Stack>
                    {paused && (
                      <Typography
                        variant="body2"
                        color="warning.main"
                        className="connection-paused"
                      >
                        {pausedUntil(paused, zone)}
                      </Typography>
                    )}
                    <Typography variant="body2" color="text.secondary">
                      Up to {backend.concurrency > 0 ? backend.concurrency : 4}{" "}
                      at a time
                    </Typography>
                  </Box>
                  <Stack direction="row" spacing={1}>
                    <Button
                      size="small"
                      variant="outlined"
                      disabled={isTesting}
                      onClick={async () => {
                        setTestingConnId(backend.id);
                        try {
                          const params = new URLSearchParams();
                          if (
                            backend.framework &&
                            backend.framework !== "antigravity"
                          ) {
                            params.set("framework", backend.framework);
                          }
                          if (backend.id) {
                            params.set("backend", backend.id);
                          }
                          const query = params.size ? `?${params}` : "";
                          const res = await api(`/api/agent-models${query}`);
                          if (res?.error) {
                            setTestConnResults((prev) => ({
                              ...prev,
                              [backend.id]: {
                                ok: false,
                                error: res.error,
                              },
                            }));
                          } else {
                            const listed = res?.models || [];
                            const discovered = listed
                              .map((m) => (typeof m === "string" ? m : m?.id))
                              .filter(Boolean);
                            setTestConnResults((prev) => ({
                              ...prev,
                              [backend.id]: {
                                ok: true,
                                message:
                                  listed.length > 0
                                    ? `Connection valid (${listed.length} models available)`
                                    : "Connection valid",
                                discovered,
                              },
                            }));
                          }
                        } catch (err) {
                          setTestConnResults((prev) => ({
                            ...prev,
                            [backend.id]: {
                              ok: false,
                              error: err.message,
                            },
                          }));
                        } finally {
                          setTestingConnId(null);
                        }
                      }}
                    >
                      {isTesting ? "Testing..." : "Test Connection"}
                    </Button>
                    <Button
                      size="small"
                      variant="outlined"
                      onClick={() =>
                        setEditingBackend({ backend, isNew: false })
                      }
                    >
                      Edit
                    </Button>
                    <Button
                      size="small"
                      variant="outlined"
                      onClick={() =>
                        saveBackend({
                          ...backend,
                          disabled: !backend.disabled,
                        })
                      }
                    >
                      {backend.disabled ? "Enable" : "Disable"}
                    </Button>
                    <Button
                      size="small"
                      color="error"
                      variant="outlined"
                      disabled={agentBackends.some(
                        (candidate) =>
                          candidate.credentialBackendId === backend.id,
                      )}
                      onClick={() => removeBackend(backend.id)}
                      title={
                        agentBackends.some(
                          (candidate) =>
                            candidate.credentialBackendId === backend.id,
                        )
                          ? "Another connection reuses this connection's credentials. Remove that connection first."
                          : undefined
                      }
                    >
                      {agentBackends.some(
                        (candidate) =>
                          candidate.credentialBackendId === backend.id,
                      )
                        ? "Used for authentication"
                        : "Remove"}
                    </Button>
                  </Stack>
                </Box>

                <Typography variant="caption" color="text.secondary">
                  Advertises:{" "}
                  {advertisedModels(backend)
                    .map((m) =>
                      modelPause(config?.agentPause, backend, m)
                        ? `${m} (paused)`
                        : m,
                    )
                    .join(", ") || NO_MODELS_LABEL}
                  {advertisesDiscovered(backend) &&
                    advertisedModels(backend).length > 0 &&
                    " (every model its provider lists)"}
                  {advertisesDiscovered(backend) &&
                    backend.discovered?.error &&
                    " · Provider unreachable, showing its last list"}
                  {backend.credentialBackendId &&
                    ` · Reuses credentials from connection ${backend.credentialBackendId}`}
                </Typography>

                {pausedModels.map((entry) => (
                  <Box
                    key={entry.model}
                    className="connection-model-paused"
                    sx={{
                      display: "flex",
                      alignItems: "center",
                      gap: 1,
                      flexWrap: "wrap",
                    }}
                  >
                    <Chip
                      label={`${entry.model}${entry.labels?.length ? ` (via ${entry.labels.join(", ")})` : ""}`}
                      size="small"
                      color="warning"
                      variant="outlined"
                    />
                    <Typography variant="body2" color="warning.main">
                      {pausedUntil(entry, zone)}
                    </Typography>
                    {entry.canLift && (
                      <Button
                        size="small"
                        variant="outlined"
                        onClick={async () => {
                          try {
                            await liftAgentPause(entry);
                            await onSaved?.();
                          } catch (err) {
                            showError?.(err.message);
                          }
                        }}
                      >
                        Resume
                      </Button>
                    )}
                  </Box>
                ))}

                {testRes && (
                  <Alert
                    severity={testRes.ok ? "success" : "error"}
                    onClose={() =>
                      setTestConnResults((prev) => {
                        const next = { ...prev };
                        delete next[backend.id];
                        return next;
                      })
                    }
                    sx={{ py: 0, px: 1.5 }}
                  >
                    <Box component="span" sx={{ display: "block" }}>
                      {testRes.message || testRes.error}
                    </Box>
                    {testRes.discovered?.length > 0 && (
                      <Box component="span" sx={{ display: "block" }}>
                        Discovered: {testRes.discovered.join(", ")}
                      </Box>
                    )}
                  </Alert>
                )}
              </Box>
            );
          })}
          {agentBackends.length === 0 && (
            <Alert severity="warning">
              This deployment has no provider connection, so its tasks wait.
              Create one to authenticate agent models.
            </Alert>
          )}
        </Stack>
      </SettingsCard>
      <SettingsCard label="Models" anchor="model-defaults">
        <ModelDefaults
          deployment={deployment}
          connections={agentBackends}
          pause={config?.agentPause}
          efforts={{
            gemini: settings.geminiEfforts || [],
            codex: settings.codexEfforts || [],
          }}
          onSave={saveDeployment}
        />
      </SettingsCard>
      <SettingsCard title="Where each agent CLI runs" anchor="cli-run">
        <CLIRunLocations showError={showError} onSaved={onSaved} />
      </SettingsCard>
    </>
  );
}
