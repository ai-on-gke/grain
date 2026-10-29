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

import { useEffect, useRef, useState } from "react";
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  Divider,
  MenuItem,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import api from "../api.js";
import ConnectionModelsField from "./ConnectionModelsField.jsx";
import CodexAuthFields from "./CodexAuthFields.jsx";
import Overlay from "./Overlay.jsx";

export const frameworkLabels = {
  antigravity: "Antigravity",
  claude: "Claude",
  codex: "Codex",
  custom: "Custom command",
};

export function modelBackendLabel(backend) {
  return (
    backend.name ||
    frameworkLabels[backend.framework] ||
    backend.id ||
    "Connection"
  );
}

// foldLegacyModel moves a connection's old single `model` to the front of
// the models it advertises, which is what the daemon's own migration
// (model.Store.MigrateModelSelection) does with it, so the page never
// writes the field again. `effort` is left as it was stored: it is not
// shown or edited here any more (the deployment sets effort per model), and
// dropping it would lose a value written after the migration ran.
export function foldLegacyModel(backend) {
  if (!backend || !backend.model) return backend;
  const { model, ...rest } = backend;
  const models = [model, ...(backend.models || []).filter((m) => m !== model)];
  return { ...rest, models };
}

function authenticationValue(backend) {
  if (backend.credentialBackendId)
    return `reuse:${backend.credentialBackendId}`;
  if (
    backend.framework === "antigravity" ||
    backend.framework === "codex" ||
    backend.framework === "claude"
  )
    return backend.authMode || "";
  return "own";
}

// cliBinaryFor is the CLI a connection of framework runs, the one its
// CLI path overrides -- "" for a custom command, which names its own.
export function cliBinaryFor(framework) {
  return (
    { claude: "claude", codex: "codex", antigravity: "agy" }[framework] || ""
  );
}

export default function ModelBackendPage({
  backend,
  backends,
  isNew,
  onSave,
  onBack,
  onDiscard,
  showError,
  backLabel = "Models",
  cliDefaults = {},
}) {
  const [draft, setDraft] = useState(() => foldLegacyModel(backend));
  const [persisted, setPersisted] = useState(() =>
    isNew ? null : foldLegacyModel(backend),
  );
  const [creating] = useState(isNew);
  // A connection Continue has stored but Save connection has not yet
  // finished. Continue has to store it -- its credential and model
  // catalog are kept against its id -- but until Save it is stored
  // disabled, so no run is dispatched to a connection with no key, and
  // leaving the page from here deletes it (onDiscard) rather than
  // leaving a nameless, keyless connection behind (grain/task-4697).
  const [unfinished, setUnfinished] = useState(false);
  const unfinishedRef = useRef(false);
  const onDiscardRef = useRef(onDiscard);
  onDiscardRef.current = onDiscard;
  const [opened] = useState(() => foldLegacyModel(backend));
  const [saving, setSaving] = useState(false);
  const [credential, setCredential] = useState("");
  const [catalogRevision, setCatalogRevision] = useState(0);
  const [testingCredential, setTestingCredential] = useState(false);
  const [testStatus, setTestStatus] = useState(null);
  // The draft as of the last change rather than as of this render, so a
  // save fired in the same event as a change stores that change.
  const latest = useRef(draft);
  const takeDraft = (next) => {
    latest.current = next;
    setDraft(next);
    return next;
  };
  // commit saves the draft on the spot, for a gesture that is a choice
  // rather than typing.
  const change = (values, commit) => {
    const next = takeDraft({ ...latest.current, ...values });
    if (commit) saveDraft(next);
  };
  const sources = backends.filter(
    (candidate) =>
      candidate.id !== draft.id &&
      candidate.framework === draft.framework &&
      !candidate.credentialBackendId,
  );
  const authValue = authenticationValue(draft);
  const typeDirty = persisted && draft.framework !== persisted.framework;
  const dirty =
    unfinished ||
    !persisted ||
    JSON.stringify(draft) !== JSON.stringify(persisted);
  // What leaving the page would throw away, asked by Overlay on Escape,
  // "← Models & connections" and browser Back: this form saves only on Save, so
  // an edit to a stored connection, or anything put into a new one (a
  // credential typed but not yet set included), is lost without a word
  // otherwise. Read from `latest`, not this render's draft, for a change
  // made in the same event as the leaving.
  const isDirty = () =>
    unfinished ||
    credential !== "" ||
    JSON.stringify(latest.current) !== JSON.stringify(persisted || opened);

  const saveDraft = async (next = latest.current) => {
    setSaving(true);
    try {
      if (!(await onSave(next))) return false;
      takeDraft(next);
      setPersisted(next);
      return true;
    } finally {
      setSaving(false);
    }
  };

  const continueDraft = async () => {
    if (await saveDraft({ ...latest.current, disabled: true })) {
      unfinishedRef.current = true;
      setUnfinished(true);
    }
  };

  // Save connection: what finishes a connection Continue started, so it
  // is the save that enables it.
  const submitDraft = async () => {
    if (!unfinished) return saveDraft();
    const { disabled: _disabled, ...enabled } = latest.current;
    if (await saveDraft(enabled)) {
      unfinishedRef.current = false;
      setUnfinished(false);
    }
  };

  const leave = (...args) => {
    if (unfinishedRef.current) {
      unfinishedRef.current = false;
      onDiscard?.(latest.current.id);
    }
    onBack?.(...args);
  };

  useEffect(
    () => () => {
      if (unfinishedRef.current) {
        unfinishedRef.current = false;
        onDiscardRef.current?.(latest.current.id);
      }
    },
    [],
  );

  // A reload or a closed tab unmounts nothing, so the cleanup above never
  // ran for it and the draft stayed behind as a nameless, disabled row
  // named by its id. pagehide is the last moment the page has; keepalive
  // lets the DELETE outlive it. The leave prompt has already been
  // answered by then (guardLeaving's beforeunload).
  useEffect(() => {
    const onPageHide = () => {
      if (!unfinishedRef.current) return;
      unfinishedRef.current = false;
      onDiscardRef.current?.(latest.current.id, { keepalive: true });
    };
    window.addEventListener("pagehide", onPageHide);
    return () => window.removeEventListener("pagehide", onPageHide);
  }, []);

  const chooseAuthentication = async (value) => {
    const next = value.startsWith("reuse:")
      ? {
          ...latest.current,
          credentialBackendId: value.slice("reuse:".length),
          authMode: undefined,
        }
      : {
          ...latest.current,
          credentialBackendId: undefined,
          authMode:
            draft.framework === "antigravity" ||
            draft.framework === "codex" ||
            draft.framework === "claude"
              ? value || undefined
              : undefined,
        };
    if (await saveDraft(next)) setCatalogRevision((revision) => revision + 1);
  };

  const credentialLabel =
    draft.framework === "antigravity"
      ? "Gemini API key"
      : draft.framework === "claude"
        ? authValue === "vertex"
          ? "Service account key (JSON, optional)"
          : "Claude Code OAuth token"
        : draft.framework === "codex"
          ? "OpenAI API key"
          : "Credential";
  const requiresChoice =
    draft.framework === "antigravity" ||
    draft.framework === "codex" ||
    draft.framework === "claude";
  const authenticationChosen = !requiresChoice || Boolean(authValue);
  // A connection being created is asked for authentication before its
  // models: until there is a credential there is nothing to discover, and
  // the empty list below would otherwise be blamed on it. A connection
  // that already exists is not asked twice -- every backend stored before
  // authMode existed has none, and gating on it left those pages with no
  // name or models field at all. The prompt above stays either way.
  const editsModel = (authenticationChosen || !creating) && !typeDirty;
  const usesOwnCredential =
    authValue === "own" || authValue === "api_key" || authValue === "vertex";

  // The CLI a claude, codex or antigravity connection runs can be its
  // own build (model.AgentBackend.CLIPath); empty falls back to the
  // deployment's flag, then the daemon's PATH, which the hint names.
  const cliBinary = cliBinaryFor(draft.framework);
  const cliPathHint = cliBinary
    ? `Absolute path, or a bare name on the daemon's PATH, to the ${cliBinary} ` +
      `this connection runs. Empty uses the deployment default: ` +
      (cliDefaults[cliBinary] || `${cliBinary} on the daemon's PATH`) +
      `. In container mode it must exist inside the daemon's container.`
    : "";

  const typeField = (
    <>
      <TextField
        select
        label="Agent type"
        value={draft.framework}
        onChange={(event) =>
          change({
            framework: event.target.value,
            models: [],
            command: "",
            cliPath: undefined,
            modelTurnTimeout: undefined,
            modelRetryCutoff: undefined,
            authMode: undefined,
            credentialBackendId: undefined,
          })
        }
        fullWidth
        SelectProps={{ displayEmpty: true }}
        InputLabelProps={{ shrink: true }}
      >
        <MenuItem value="" disabled>
          Choose an agent type
        </MenuItem>
        {Object.entries(frameworkLabels).map(([value, label]) => (
          <MenuItem key={value} value={value}>
            {label}
          </MenuItem>
        ))}
      </TextField>
      {draft.framework === "custom" && (
        <TextField
          label="Command"
          helperText="Relative to the config repository"
          value={draft.command || ""}
          onChange={(event) => change({ command: event.target.value })}
          required
          fullWidth
        />
      )}
    </>
  );

  return (
    <Overlay
      pane
      // This pane only ever opens from TeamSettingsOverlay's own
      // Models & connections tab -- a pane already on screen -- so, per Overlay's
      // own note on `animate`, it never fades in over it (grain/task-2179's
      // own reason for the same prop on a task pane).
      animate={false}
      label={persisted ? modelBackendLabel(draft) : "New connection"}
      backLabel={backLabel}
      onClose={leave}
      isDirty={isDirty}
      header={
        <Box>
          <Typography variant="h5">
            {persisted ? modelBackendLabel(draft) : "New connection"}
          </Typography>
          <Typography variant="body2" color="text.secondary">
            {persisted
              ? "Edit provider connection"
              : "Choose an agent type to begin"}
          </Typography>
        </Box>
      }
    >
      <Box sx={{ maxWidth: 900, mx: "auto", pb: 4 }}>
        {!persisted ? (
          <Box>
            <Typography variant="h6" sx={{ mb: 0.5 }}>
              Agent type
            </Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
              Choose the agent first. You will configure its authentication and
              the models it serves next.
            </Typography>
            <Stack spacing={2}>
              {typeField}
              <Button
                type="button"
                variant="contained"
                disabled={saving || !draft.framework}
                onClick={continueDraft}
                sx={{ alignSelf: "flex-start" }}
              >
                Continue
              </Button>
            </Stack>
          </Box>
        ) : (
          <>
            <Typography variant="h6" sx={{ mb: 0.5 }}>
              Agent type
            </Typography>
            <Stack spacing={2}>
              {typeField}
              {typeDirty && (
                <Button
                  type="button"
                  variant="contained"
                  disabled={saving}
                  onClick={() => saveDraft()}
                  sx={{ alignSelf: "flex-start" }}
                >
                  Save agent type
                </Button>
              )}
            </Stack>

            <Divider sx={{ my: 4 }} />
            <Typography variant="h6" sx={{ mb: 0.5 }}>
              Authentication
            </Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
              Authentication comes first so Grain can discover the models this
              account serves.
            </Typography>
            {typeDirty ? (
              <Alert severity="info">
                Save the agent type before configuring authentication.
              </Alert>
            ) : (
              <Stack spacing={2}>
                <TextField
                  select
                  label="Authentication method"
                  value={authValue}
                  disabled={saving}
                  onChange={(event) => chooseAuthentication(event.target.value)}
                  fullWidth
                  SelectProps={{ displayEmpty: true }}
                  InputLabelProps={{ shrink: true }}
                >
                  {requiresChoice && (
                    <MenuItem value="" disabled>
                      Choose an authentication method
                    </MenuItem>
                  )}
                  {draft.framework === "antigravity" && [
                    <MenuItem key="ambient" value="ambient">
                      Ambient Antigravity login
                    </MenuItem>,
                    <MenuItem key="api_key" value="api_key">
                      Gemini API key
                    </MenuItem>,
                  ]}
                  {draft.framework === "codex" && [
                    <MenuItem key="api_key" value="api_key">
                      OpenAI API key
                    </MenuItem>,
                    <MenuItem key="subscription" value="subscription">
                      ChatGPT subscription (auth.json)
                    </MenuItem>,
                  ]}
                  {draft.framework === "claude" && [
                    <MenuItem key="api_key" value="api_key">
                      Claude Code OAuth token
                    </MenuItem>,
                    <MenuItem key="vertex" value="vertex">
                      Google Cloud Vertex AI
                    </MenuItem>,
                  ]}
                  {draft.framework === "custom" && (
                    <MenuItem value="own">
                      Use a credential for this connection
                    </MenuItem>
                  )}
                  {sources.map((source) => (
                    <MenuItem key={source.id} value={`reuse:${source.id}`}>
                      Reuse authentication from {modelBackendLabel(source)}
                    </MenuItem>
                  ))}
                </TextField>
                {!authenticationChosen && (
                  <Alert severity="info">
                    Choose one authentication method. Only the selected method
                    will be used.
                  </Alert>
                )}
                {authValue === "ambient" && (
                  <Alert severity="info">
                    Grain will use the Antigravity login already available to
                    the daemon. A stored Gemini API key is ignored.
                  </Alert>
                )}
                {authValue === "vertex" && (
                  <Alert severity="info">
                    Grain will run Claude Code against Google Cloud Vertex AI.
                    Enter a service account key (JSON) to authenticate with it,
                    or leave empty to use ambient Google Cloud credentials.
                  </Alert>
                )}
                {authValue.startsWith("reuse:") && (
                  <Alert severity="info">
                    This connection follows the selected connection&apos;s
                    authentication, including future credential rotations.
                  </Alert>
                )}
                {authValue === "subscription" && (
                  <CodexAuthFields
                    backend={draft}
                    showError={showError}
                    onCredentialChange={() =>
                      setCatalogRevision((revision) => revision + 1)
                    }
                  />
                )}
                {usesOwnCredential && (
                  <>
                    <TextField
                      type="password"
                      label={credentialLabel}
                      value={credential}
                      // Not "off": browsers ignore that on a password box
                      // and fill in the password saved for grain's own
                      // sign-in, which then reads as a credential typed but
                      // not set -- so leaving a just-saved connection asked
                      // about unsaved changes (grain/task-3624).
                      autoComplete="new-password"
                      onChange={(event) => setCredential(event.target.value)}
                      fullWidth
                    />
                    <Stack direction="row" spacing={1}>
                      <Button
                        type="button"
                        variant="outlined"
                        disabled={!credential.trim()}
                        onClick={async () => {
                          try {
                            await api(
                              `/api/agent-backends/${encodeURIComponent(draft.id)}/credential`,
                              {
                                method: "PUT",
                                body: JSON.stringify({ value: credential }),
                              },
                            );
                            setCredential("");
                            setCatalogRevision((revision) => revision + 1);
                          } catch (error) {
                            showError(error);
                          }
                        }}
                      >
                        Set credential
                      </Button>
                      <Button
                        type="button"
                        onClick={async () => {
                          try {
                            await api(
                              `/api/agent-backends/${encodeURIComponent(draft.id)}/credential`,
                              { method: "DELETE" },
                            );
                            setCatalogRevision((revision) => revision + 1);
                          } catch (error) {
                            showError(error);
                          }
                        }}
                      >
                        Clear credential
                      </Button>
                      <Button
                        type="button"
                        variant="outlined"
                        disabled={testingCredential}
                        onClick={async () => {
                          setTestingCredential(true);
                          setTestStatus(null);
                          try {
                            const params = new URLSearchParams();
                            if (
                              draft.framework &&
                              draft.framework !== "antigravity"
                            ) {
                              params.set("framework", draft.framework);
                            }
                            if (draft.id) {
                              params.set("backend", draft.id);
                            }
                            const query = params.size ? `?${params}` : "";
                            const res = await api(`/api/agent-models${query}`);
                            if (res?.error) {
                              setTestStatus({ ok: false, error: res.error });
                            } else {
                              const count = res?.models?.length || 0;
                              setTestStatus({
                                ok: true,
                                message:
                                  count > 0
                                    ? `Validated (${count} models available)`
                                    : "Validated successfully",
                              });
                            }
                          } catch (err) {
                            setTestStatus({ ok: false, error: err.message });
                          } finally {
                            setTestingCredential(false);
                          }
                        }}
                      >
                        {testingCredential ? "Testing..." : "Test"}
                      </Button>
                    </Stack>
                    {testStatus && (
                      <Alert
                        severity={testStatus.ok ? "success" : "error"}
                        onClose={() => setTestStatus(null)}
                        sx={{ mt: 1 }}
                      >
                        {testStatus.message || testStatus.error}
                      </Alert>
                    )}
                  </>
                )}
              </Stack>
            )}

            {editsModel && (
              <>
                <Divider sx={{ my: 4 }} />
                <Box
                  component="form"
                  onSubmit={(event) => {
                    event.preventDefault();
                    submitDraft();
                  }}
                >
                  <Typography variant="h6" sx={{ mb: 0.5 }}>
                    Connection
                  </Typography>
                  <Typography
                    variant="body2"
                    color="text.secondary"
                    sx={{ mb: 2 }}
                  >
                    Name this account and list the models it serves. Which model
                    a run uses is chosen in Settings (Models) or by the task,
                    not by the connection.
                  </Typography>
                  <Stack spacing={2}>
                    <TextField
                      label="Name"
                      value={draft.name || ""}
                      onChange={(event) => change({ name: event.target.value })}
                      helperText="A readable name for this account or installation"
                      fullWidth
                    />
                    <TextField
                      type="number"
                      label="Concurrency"
                      placeholder="4"
                      value={draft.concurrency ?? ""}
                      onChange={(event) => {
                        const val =
                          event.target.value === ""
                            ? undefined
                            : parseInt(event.target.value, 10);
                        change({ concurrency: isNaN(val) ? undefined : val });
                      }}
                      inputProps={{ min: 1 }}
                      helperText="Maximum number of concurrent agents on this connection (4 by default)"
                      fullWidth
                    />
                    {cliBinary && (
                      <TextField
                        label="CLI path (optional)"
                        value={draft.cliPath || ""}
                        onChange={(event) =>
                          change({ cliPath: event.target.value || undefined })
                        }
                        helperText={cliPathHint}
                        fullWidth
                      />
                    )}
                    {/* The model-turn deadline is antigravity's alone
                        (pkg/agent/antigravity/turndeadline.go): agy retries
                        provider errors silently, and grain reads its log to
                        tell a retried turn from a slow one. Empty is the
                        default; the server holds the 30s-60m bounds. */}
                    {draft.framework === "antigravity" && (
                      <>
                        <Typography variant="subtitle2" sx={{ pt: 1 }}>
                          Advanced
                        </Typography>
                        <TextField
                          label="Model turn timeout (optional)"
                          value={draft.modelTurnTimeout || ""}
                          onChange={(event) =>
                            change({
                              modelTurnTimeout: event.target.value || undefined,
                            })
                          }
                          placeholder="15m"
                          helperText={
                            "How long one model turn may run while agy logs no " +
                            "provider errors -- a slow model rather than a " +
                            "refused one. A duration such as 20m, from 30s to " +
                            "60m. Empty uses the default, 15m."
                          }
                          fullWidth
                        />
                        <TextField
                          label="Model retry cutoff (optional)"
                          value={draft.modelRetryCutoff || ""}
                          onChange={(event) =>
                            change({
                              modelRetryCutoff: event.target.value || undefined,
                            })
                          }
                          placeholder="100s"
                          helperText={
                            "How long a turn may run once agy's log shows it " +
                            "retrying provider errors (429, 503, " +
                            "RESOURCE_EXHAUSTED), and the whole limit when that " +
                            "log cannot be read. From 30s to 60m, no longer " +
                            "than the turn timeout. Empty uses the default, 100s."
                          }
                          fullWidth
                        />
                      </>
                    )}
                    <ConnectionModelsField
                      backend={draft}
                      onChange={change}
                      refreshKey={catalogRevision}
                    />
                    {/* A save that landed is otherwise invisible: the pane
                        stays open and nothing on it moves. Gating the
                        button on the draft still differing from what is
                        stored -- as the agent type above gates on
                        typeDirty -- lets it go quiet on a save, which is
                        the page's answer to "is what I typed stored?". */}
                    <Stack
                      direction="row"
                      spacing={2}
                      alignItems="center"
                      sx={{ alignSelf: "flex-start" }}
                    >
                      <Button
                        type="submit"
                        variant="contained"
                        disabled={saving || !dirty}
                        aria-busy={saving}
                        startIcon={
                          saving ? (
                            <CircularProgress size={16} color="inherit" />
                          ) : undefined
                        }
                      >
                        Save connection
                      </Button>
                      {saving && (
                        <Typography
                          variant="body2"
                          color="text.secondary"
                          role="status"
                        >
                          Saving…
                        </Typography>
                      )}
                      {!dirty && !saving && (
                        <Typography variant="body2" color="text.secondary">
                          No unsaved changes
                        </Typography>
                      )}
                    </Stack>
                  </Stack>
                </Box>
              </>
            )}
          </>
        )}
      </Box>
    </Overlay>
  );
}
