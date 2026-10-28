import { useMemo } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  MenuItem,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import { identifierField } from "../../identifierField.js";
import {
  connectionLabel,
  defaultModels,
  frameworkLabel,
  connectionPause,
  NO_MODELS_LABEL,
  PROVIDER_DEFAULT_LABEL,
  providerDefaultModel,
  connectionModels,
} from "../../state.js";
import { useTimeZone } from "../../TimeZoneContext.jsx";
import { pausedUntil } from "../../alerts.js";
import ModelSelect from "../ModelSelect.jsx";

// effortChoices is the reasoning-effort vocabulary a model's framework
// takes, or null for one that takes free text (a custom command), or []
// for one that takes no effort at all (Claude: the CLI has no flag).
function effortChoices(framework, efforts) {
  if (framework === "claude") return [];
  if (framework === "custom") return null;
  if (framework === "codex") return efforts.codex || [];
  return efforts.gemini || [];
}

// ModelDefaults is the deployment's model configuration
// (docs/connections-and-models.md), on Settings → Models: the default
// model its tasks run on, and its own settings for each model, which
// today are the reasoning effort. The choices are the models its
// connections advertise; each model lists every connection that serves
// it, enabled or not.
//
// A model and its settings are the deployment's, whichever connection
// ends up serving a run, so this pane writes only to the deployment's
// record (PUT /api/deployment: defaultModel, modelSettings) and never to
// a connection. `deployment` is that record (useDeployment), null while
// it loads.
export default function ModelDefaults({
  deployment,
  connections,
  // pause is config.agentPause: a connection whose framework it covers
  // is named as paused in "Served by", since it serves nothing until
  // the pause ends.
  pause,
  efforts = {},
  onSave,
}) {
  const zone = useTimeZone();
  const advertised = useMemo(
    () => connectionModels(connections),
    [connections],
  );
  const selectedDefaults = useMemo(
    () => defaultModels(deployment),
    [deployment],
  );
  const defaultModel = selectedDefaults[0] || "";
  const settings = deployment?.modelSettings || [];
  const loading = !deployment;

  const saveDefaultModels = (nextList) => {
    const cleaned = defaultModels(nextList);
    const nextValue = cleaned.join(",");
    const currentValue = selectedDefaults.join(",");
    if (nextValue === currentValue) return;
    onSave({ defaultModel: nextValue });
  };

  // Models the deployment names that no connection advertises any more -- a
  // default or a setting left behind when a connection was removed --
  // are listed too, so the gap shows instead of hiding behind it.
  const rows = useMemo(() => {
    const out = [...advertised];
    const known = new Set(out.map((m) => m.id));
    const orphan = (id) => {
      if (id && !known.has(id)) {
        known.add(id);
        out.push({ id, framework: "", connections: [], enabled: false });
      }
    };
    for (const id of selectedDefaults) orphan(id);
    for (const s of settings) orphan(s.model);
    return out;
  }, [advertised, selectedDefaults, settings]);

  const effortFor = (id) =>
    settings.find((s) => s.model.toLowerCase() === id.toLowerCase())?.effort ||
    "";

  const saveEffort = (id, effort) => {
    const value = (effort || "").trim();
    if (value === effortFor(id)) return;
    const rest = settings.filter(
      (s) => s.model.toLowerCase() !== id.toLowerCase(),
    );
    onSave({
      modelSettings: value ? [...rest, { model: id, effort: value }] : rest,
    });
  };

  const defaultRows = selectedDefaults
    .map((id) => rows.find((m) => m.id.toLowerCase() === id.toLowerCase()))
    .filter(Boolean);
  const anyDefaultEnabled = defaultRows.some((r) => r.enabled);
  const remainingRows = rows.filter(
    (m) =>
      !selectedDefaults.some((id) => id.toLowerCase() === m.id.toLowerCase()),
  );
  // What each enabled connection runs while the deployment has no default: its
  // provider's own (providerDefaultModel), which nobody chose -- so the
  // pane asks for a choice rather than making one. grain never saves a
  // default model on its own.
  const enabled = (connections || []).filter((c) => !c.disabled);
  const unset = !loading && selectedDefaults.length === 0;
  const empty = enabled.filter((c) => !providerDefaultModel(c));

  return (
    <>
      <Box sx={{ mb: 2 }}>
        <Typography variant="subtitle2">Models</Typography>
        <Typography variant="body2" color="text.secondary">
          Choose the model this deployment runs on, and how each model is run.
          The choices are the models its connections advertise. A task runs on
          its own model or on this default, on any enabled connection that
          serves it.
        </Typography>
      </Box>

      {unset && advertised.length > 0 && (
        <Alert severity="info" sx={{ mb: 2 }} data-testid="pick-default-model">
          <strong>Pick a default model.</strong> This deployment has not chosen
          one, so its tasks run on each connection&apos;s{" "}
          {PROVIDER_DEFAULT_LABEL}
          {enabled.some((c) => providerDefaultModel(c)) &&
            ` (${enabled
              .filter((c) => providerDefaultModel(c))
              .map((c) => `${c.name || c.id}: ${providerDefaultModel(c)}`)
              .join(", ")})`}
          . Choose one of the {advertised.length} model
          {advertised.length === 1 ? "" : "s"} its connections advertise below.
        </Alert>
      )}
      {unset && empty.length > 0 && (
        <Alert severity="warning" sx={{ mb: 2 }} data-testid="no-models">
          {empty.map((c) => c.name || c.id).join(", ")}: {NO_MODELS_LABEL}.
          Tasks that would run on {empty.length === 1 ? "it" : "them"} wait
          until a model is listed for the connection or picked here.
        </Alert>
      )}

      <ModelSelect
        name="defaultModel"
        settingId="defaultModel"
        label="Default model"
        value={defaultModel}
        disabled={loading}
        onChange={(next) => {
          if (!next) {
            saveDefaultModels([]);
            return;
          }
          const rest = selectedDefaults
            .slice(1)
            .filter((id) => id.toLowerCase() !== next.toLowerCase());
          saveDefaultModels([next, ...rest]);
        }}
        options={[
          {
            value: "",
            label: `None — ${PROVIDER_DEFAULT_LABEL}`,
          },
          ...rows.map((m) => ({
            value: m.id,
            label:
              m.connections.length === 0
                ? `${m.id} (no connection advertises it)`
                : !m.enabled
                  ? `${m.id} (disabled connections only)`
                  : m.id,
          })),
        ]}
        helperText="The model a task runs on when it does not choose one. Type to search."
        size="medium"
        margin="normal"
      />
      {selectedDefaults.slice(1).map((modelId, i) => {
        const idx = i + 1;
        const label =
          selectedDefaults.length === 2
            ? "Fallback model"
            : `Fallback model ${idx}`;
        return (
          <Stack
            key={`${idx}-${modelId}`}
            direction="row"
            spacing={1}
            alignItems="flex-start"
            sx={{ mt: 1 }}
          >
            <Box sx={{ flex: 1, minWidth: 0 }}>
              <ModelSelect
                name={`defaultModel-${idx + 1}`}
                label={label}
                value={modelId}
                disabled={loading}
                onChange={(next) => {
                  const copy = [...selectedDefaults];
                  if (!next) {
                    copy.splice(idx, 1);
                  } else {
                    copy[idx] = next;
                  }
                  saveDefaultModels(copy);
                }}
                options={[
                  {
                    value: "",
                    label: "Remove fallback model",
                  },
                  ...rows.map((m) => ({
                    value: m.id,
                    label:
                      m.connections.length === 0
                        ? `${m.id} (no connection advertises it)`
                        : !m.enabled
                          ? `${m.id} (disabled connections only)`
                          : m.id,
                  })),
                ]}
                helperText={`Tried #${idx + 1} when earlier default models cannot be dispatched.`}
                size="medium"
                margin="dense"
              />
            </Box>
            <Stack direction="row" spacing={0.5} sx={{ pt: 1 }}>
              <Button
                size="small"
                variant="outlined"
                disabled={loading}
                aria-label={`Move ${modelId} up`}
                onClick={() => {
                  const copy = [...selectedDefaults];
                  [copy[idx - 1], copy[idx]] = [copy[idx], copy[idx - 1]];
                  saveDefaultModels(copy);
                }}
              >
                Up
              </Button>
              {idx < selectedDefaults.length - 1 && (
                <Button
                  size="small"
                  variant="outlined"
                  disabled={loading}
                  aria-label={`Move ${modelId} down`}
                  onClick={() => {
                    const copy = [...selectedDefaults];
                    [copy[idx], copy[idx + 1]] = [copy[idx + 1], copy[idx]];
                    saveDefaultModels(copy);
                  }}
                >
                  Down
                </Button>
              )}
              <Button
                size="small"
                color="error"
                variant="outlined"
                disabled={loading}
                aria-label={`Remove ${modelId}`}
                onClick={() => {
                  const copy = [...selectedDefaults];
                  copy.splice(idx, 1);
                  saveDefaultModels(copy);
                }}
              >
                Remove
              </Button>
            </Stack>
          </Stack>
        );
      })}
      {selectedDefaults.length > 0 && remainingRows.length > 0 && (
        <ModelSelect
          name="addDefaultModel"
          label="Add fallback model"
          value=""
          disabled={loading}
          onChange={(next) => {
            if (next) saveDefaultModels([...selectedDefaults, next]);
          }}
          options={[
            {
              value: "",
              label: "None",
            },
            ...remainingRows.map((m) => ({
              value: m.id,
              label:
                m.connections.length === 0
                  ? `${m.id} (no connection advertises it)`
                  : !m.enabled
                    ? `${m.id} (disabled connections only)`
                    : m.id,
            })),
          ]}
          helperText="Optional. Tried in order after the models above when they are paused or busy."
          size="medium"
          margin="dense"
        />
      )}
      {selectedDefaults.length > 0 &&
        defaultRows.length > 0 &&
        !anyDefaultEnabled && (
          <Alert severity="warning" sx={{ mb: 2 }}>
            {selectedDefaults.length === 1
              ? `No enabled connection serves model ${defaultModel}. Tasks on the default model wait in the queue until one does.`
              : `No enabled connection serves any default model (${selectedDefaults.join(", ")}). Tasks on the default model wait in the queue until one does.`}
          </Alert>
        )}

      <Typography variant="subtitle2" sx={{ mt: 3, mb: 1 }}>
        Models and the connections that serve them
      </Typography>
      <Stack spacing={1.25} sx={{ mb: 3 }}>
        {rows.map((m) => {
          const choices = effortChoices(m.framework, efforts);
          const effort = effortFor(m.id);
          const defaultIndex = selectedDefaults.findIndex(
            (id) => id.toLowerCase() === m.id.toLowerCase(),
          );
          return (
            <Box
              key={m.id}
              data-testid={`model-${m.id}`}
              sx={{
                border: 1,
                borderColor: "divider",
                borderRadius: 1,
                px: 2,
                py: 1.5,
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
                      {m.id}
                    </Typography>
                    {defaultIndex === 0 && (
                      <Chip size="small" color="primary" label="Default" />
                    )}
                    {defaultIndex > 0 && (
                      <Chip
                        size="small"
                        color="primary"
                        variant="outlined"
                        label={`Default #${defaultIndex + 1}`}
                      />
                    )}
                  </Stack>
                  <Typography variant="caption" color="text.secondary">
                    {m.framework
                      ? `Framework: ${frameworkLabel(m.framework)}`
                      : "No connection advertises this model"}
                  </Typography>
                </Box>
                <Chip
                  label={`${m.connections.length} connection${m.connections.length === 1 ? "" : "s"}`}
                  size="small"
                  color="primary"
                  variant="outlined"
                />
              </Box>

              {m.connections.length > 0 && (
                <Box sx={{ mt: 1.5 }}>
                  <Typography
                    variant="caption"
                    color="text.secondary"
                    sx={{ display: "block", mb: 0.5 }}
                  >
                    Served by:
                  </Typography>
                  <Stack
                    direction="row"
                    spacing={0.75}
                    useFlexGap
                    flexWrap="wrap"
                  >
                    {m.connections.map((conn) => {
                      const paused = conn.disabled
                        ? null
                        : connectionPause(pause, conn, m.id);
                      return (
                        <Chip
                          key={conn.id}
                          label={`${conn.name || conn.id} (${conn.disabled ? "Disabled" : paused ? "Paused" : "Enabled"})`}
                          title={
                            paused
                              ? `${connectionLabel(conn)}: ${pausedUntil(paused, zone)}`
                              : connectionLabel(conn)
                          }
                          size="small"
                          color={paused ? "warning" : "default"}
                          variant={conn.disabled ? "outlined" : "filled"}
                        />
                      );
                    })}
                  </Stack>
                </Box>
              )}

              <Box sx={{ mt: 1.5, maxWidth: 320 }}>
                {choices === null ? (
                  <TextField
                    key={`${m.id}-${effort}`}
                    size="small"
                    label="Reasoning effort"
                    defaultValue={effort}
                    disabled={loading}
                    onBlur={(event) => saveEffort(m.id, event.target.value)}
                    onKeyDown={(event) => {
                      if (event.key === "Enter") {
                        event.preventDefault();
                        event.target.blur();
                      }
                    }}
                    helperText="Passed to the custom command as it is typed."
                    inputProps={identifierField}
                    fullWidth
                  />
                ) : choices.length === 0 && !effort ? (
                  <Typography variant="caption" color="text.secondary">
                    {m.framework === "claude"
                      ? "Claude takes no reasoning effort."
                      : "No effort choices are known for this model."}
                  </Typography>
                ) : (
                  <TextField
                    select
                    size="small"
                    label="Reasoning effort"
                    value={effort}
                    disabled={loading}
                    onChange={(event) => saveEffort(m.id, event.target.value)}
                    SelectProps={{ displayEmpty: true }}
                    InputLabelProps={{ shrink: true }}
                    fullWidth
                  >
                    <MenuItem value="">Model default</MenuItem>
                    {effort && !choices.includes(effort) && (
                      <MenuItem value={effort}>{effort} (not offered)</MenuItem>
                    )}
                    {choices.map((e) => (
                      <MenuItem key={e} value={e}>
                        {e}
                      </MenuItem>
                    ))}
                  </TextField>
                )}
              </Box>
            </Box>
          );
        })}
        {rows.length === 0 && (
          <Alert severity="info">
            No connection advertises a model yet. Add a connection above, and
            list the models it serves.
          </Alert>
        )}
      </Stack>
    </>
  );
}
