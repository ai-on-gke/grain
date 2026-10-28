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

import {
  Alert,
  Box,
  Button,
  Checkbox,
  Chip,
  FormControlLabel,
  Paper,
  Stack,
  TextField,
  Typography,
} from "@mui/material";

export const DEFAULT_CONFIG_MAP_DIR = "/home/debian/.config-map";

export function isConfigMapServer(server) {
  if (
    server?.builtin?.name === "config-map" ||
    server?.status?.builtin === "config-map"
  ) {
    return true;
  }
  const props = server?.status?.configSchema?.properties;
  return Boolean(props?.config_maps && props?.secret_values);
}

// secretValueKey is the secret_values key of one secret map key, the
// "<map>/<key>" the server stores and reports back as configured.
export function secretValueKey(mapName, key) {
  return `${(mapName || "").trim()}/${(key || "").trim()}`;
}

// isSecretKeySet reports whether the stored secret_values holds a value
// for key of the secret map mapName; values themselves are never read.
export function isSecretKeySet(server, mapName, key) {
  if (!(mapName || "").trim() || !(key || "").trim()) return false;
  return (server?.configuredSecretKeys || []).includes(
    secretValueKey(mapName, key),
  );
}

export function defaultMapDir(name) {
  return `${DEFAULT_CONFIG_MAP_DIR}/${(name || "").trim() || "<name>"}`;
}

let nextRowId = 0;
const rowId = () => `row-${++nextRowId}`;

// configMapEditorValue turns stored config (maps keyed by file name) into
// the editor's rows, each file row carrying an id that its typed secret
// value is held under while its key is renamed.
export function configMapEditorValue(stored) {
  const rows = (data, secret) =>
    Object.keys(data || {})
      .sort()
      .map((key) => {
        const opts = data[key] || {};
        const row = {
          id: rowId(),
          key,
          mode: opts.mode ?? "",
          path: opts.path ?? "",
        };
        if (!secret) {
          row.content = opts.content ?? "";
          row.executable = Boolean(opts.executable);
        }
        return row;
      });
  const next = {};
  if (Array.isArray(stored?.config_maps)) {
    next.config_maps = stored.config_maps.map((cm) => ({
      name: cm?.name ?? "",
      description: cm?.description ?? "",
      dir: cm?.dir ?? "",
      files: rows(cm?.data, false),
    }));
  }
  if (Array.isArray(stored?.secrets)) {
    next.secrets = stored.secrets.map((sec) => ({
      name: sec?.name ?? "",
      description: sec?.description ?? "",
      dir: sec?.dir ?? "",
      files: rows(sec?.keys, true),
    }));
  }
  return next;
}

function mapHeader(m) {
  const entry = { name: (m.name || "").trim() };
  if ((m.description || "").trim()) entry.description = m.description.trim();
  if ((m.dir || "").trim()) entry.dir = m.dir.trim();
  return entry;
}

// normalizeConfigMapPayload turns the editor's rows back into the stored
// config and the configSecrets a save sends: only the secret keys whose
// value box was filled, as {"<map>/<key>": value}.
export function normalizeConfigMapPayload(value, perSecretValues) {
  const next = {};
  if (Array.isArray(value?.config_maps)) {
    next.config_maps = value.config_maps.map((cm) => {
      const entry = mapHeader(cm);
      entry.data = {};
      for (const f of cm.files || []) {
        const opts = { content: f.content ?? "" };
        if ((f.mode || "").trim()) opts.mode = f.mode.trim();
        if (f.executable) opts.executable = true;
        if ((f.path || "").trim()) opts.path = f.path.trim();
        entry.data[(f.key || "").trim()] = opts;
      }
      return entry;
    });
  }
  const secretMap = {};
  if (Array.isArray(value?.secrets)) {
    next.secrets = value.secrets.map((sec) => {
      const entry = mapHeader(sec);
      entry.keys = {};
      for (const f of sec.files || []) {
        const key = (f.key || "").trim();
        const opts = {};
        if ((f.mode || "").trim()) opts.mode = f.mode.trim();
        if ((f.path || "").trim()) opts.path = f.path.trim();
        entry.keys[key] = opts;
        const typed = perSecretValues?.[f.id] ?? "";
        if (entry.name && key && typed) {
          secretMap[secretValueKey(entry.name, key)] = typed;
        }
      }
      return entry;
    });
  }
  const configSecrets =
    Object.keys(secretMap).length > 0
      ? { secret_values: JSON.stringify(secretMap) }
      : {};
  return { config: next, configSecrets };
}

const mono = { style: { fontFamily: "monospace" } };

function FileRow({
  secret,
  file,
  mapName,
  server,
  readOnly,
  typedValue,
  onChange,
  onSecretValue,
  onRemove,
}) {
  const isSet = secret && isSecretKeySet(server, mapName, file.key);
  const defaultMode = secret ? "0600" : file.executable ? "0755" : "0644";
  return (
    <Box
      data-testid={secret ? "config-map-secret-key-row" : "config-map-file-row"}
      sx={{
        borderLeft: 2,
        borderColor: "divider",
        pl: 1.25,
        py: 0.5,
      }}
    >
      <Stack spacing={1}>
        <Stack
          direction={{ xs: "column", sm: "row" }}
          spacing={1}
          alignItems={{ sm: "center" }}
        >
          <TextField
            size="small"
            label="Key"
            required
            placeholder={secret ? "token" : "settings.toml"}
            disabled={readOnly}
            value={file.key ?? ""}
            onChange={(e) => onChange({ key: e.target.value })}
            sx={{ minWidth: { sm: 160 } }}
            inputProps={mono}
          />
          <TextField
            size="small"
            label="Mode"
            placeholder={defaultMode}
            disabled={readOnly}
            value={file.mode ?? ""}
            onChange={(e) => onChange({ mode: e.target.value })}
            sx={{ width: { xs: "100%", sm: 100 }, flexShrink: 0 }}
            inputProps={mono}
          />
          {!secret && (
            <FormControlLabel
              sx={{ mr: 0, flexShrink: 0 }}
              control={
                <Checkbox
                  size="small"
                  disabled={readOnly}
                  checked={Boolean(file.executable)}
                  onChange={(e) => onChange({ executable: e.target.checked })}
                />
              }
              label={<Typography variant="body2">Executable</Typography>}
            />
          )}
          <TextField
            size="small"
            fullWidth
            label="Path override"
            placeholder="Optional absolute path instead of <dir>/<key>"
            disabled={readOnly}
            value={file.path ?? ""}
            onChange={(e) => onChange({ path: e.target.value })}
            inputProps={mono}
          />
          {secret && (
            <Chip
              size="small"
              variant="outlined"
              color={isSet ? "success" : "default"}
              label={isSet ? "set" : "not set"}
              sx={{
                flexShrink: 0,
                alignSelf: { xs: "flex-start", sm: "center" },
              }}
              data-testid={`secret-state-${secretValueKey(mapName, file.key)}`}
            />
          )}
          <Button
            size="small"
            color="error"
            disabled={readOnly}
            onClick={onRemove}
            sx={{
              flexShrink: 0,
              alignSelf: { xs: "flex-start", sm: "center" },
            }}
          >
            Remove file
          </Button>
        </Stack>
        {secret ? (
          <TextField
            size="small"
            fullWidth
            type="password"
            label="Secret value"
            disabled={readOnly}
            value={typedValue ?? ""}
            placeholder={isSet ? "••••••••" : ""}
            helperText={
              isSet
                ? "Stored in the encrypted secrets store; enter a new value to replace it, or leave blank to keep it."
                : "Written to the encrypted secrets store; never saved in grain_config."
            }
            onChange={(e) => onSecretValue(e.target.value)}
            inputProps={{ autoComplete: "new-password", ...mono }}
          />
        ) : (
          <TextField
            size="small"
            fullWidth
            multiline
            minRows={3}
            label="File content"
            disabled={readOnly}
            value={file.content ?? ""}
            onChange={(e) => onChange({ content: e.target.value })}
            inputProps={mono}
          />
        )}
      </Stack>
    </Box>
  );
}

function MapCard({
  secret,
  map,
  server,
  readOnly,
  perSecretValues,
  onSecretValueChange,
  onChange,
  onRemove,
}) {
  const files = map.files || [];
  const updateFile = (idx, patch) =>
    onChange({
      files: files.map((f, i) => (i === idx ? { ...f, ...patch } : f)),
    });
  const addFile = () =>
    onChange({
      files: [
        ...files,
        secret
          ? { id: rowId(), key: "", mode: "", path: "" }
          : {
              id: rowId(),
              key: "",
              mode: "",
              path: "",
              content: "",
              executable: false,
            },
      ],
    });
  const removeFile = (idx) => {
    const removed = files[idx];
    onChange({ files: files.filter((_, i) => i !== idx) });
    if (secret && onSecretValueChange && removed) {
      const nextValues = { ...perSecretValues };
      delete nextValues[removed.id];
      onSecretValueChange(nextValues);
    }
  };
  return (
    <Paper
      variant="outlined"
      sx={{ p: 1.5 }}
      data-testid={secret ? "config-map-secret-row" : "config-map-entry-row"}
    >
      <Stack spacing={1}>
        <Stack
          direction={{ xs: "column", sm: "row" }}
          spacing={1}
          alignItems={{ sm: "center" }}
        >
          <Chip
            size="small"
            variant="outlined"
            label={secret ? "secret map" : "config map"}
            sx={{
              flexShrink: 0,
              alignSelf: { xs: "flex-start", sm: "center" },
            }}
          />
          <TextField
            size="small"
            label={secret ? "Secret map name" : "Name"}
            required
            disabled={readOnly}
            value={map.name ?? ""}
            onChange={(e) => onChange({ name: e.target.value })}
            sx={{ minWidth: { sm: 160 } }}
            inputProps={mono}
          />
          <TextField
            size="small"
            fullWidth
            label="Directory"
            placeholder={defaultMapDir(map.name)}
            disabled={readOnly}
            value={map.dir ?? ""}
            onChange={(e) => onChange({ dir: e.target.value })}
            inputProps={mono}
          />
          <Button
            size="small"
            color="error"
            disabled={readOnly}
            onClick={onRemove}
            sx={{
              flexShrink: 0,
              alignSelf: { xs: "flex-start", sm: "center" },
            }}
          >
            {secret ? "Remove secret map" : "Remove config map"}
          </Button>
        </Stack>
        <TextField
          size="small"
          fullWidth
          label="Description"
          placeholder={
            secret
              ? "Optional note shown in the agent prompt (never include a secret value)"
              : "Optional note shown in the agent prompt"
          }
          disabled={readOnly}
          value={map.description ?? ""}
          onChange={(e) => onChange({ description: e.target.value })}
        />
        <Stack spacing={1}>
          {files.map((f, idx) => (
            <FileRow
              key={f.id || idx}
              secret={secret}
              file={f}
              mapName={map.name}
              server={server}
              readOnly={readOnly}
              typedValue={perSecretValues?.[f.id]}
              onChange={(patch) => updateFile(idx, patch)}
              onSecretValue={(v) =>
                onSecretValueChange({ ...perSecretValues, [f.id]: v })
              }
              onRemove={() => removeFile(idx)}
            />
          ))}
        </Stack>
        <Box>
          <Button
            size="small"
            variant="text"
            disabled={readOnly}
            onClick={addFile}
          >
            Add file
          </Button>
        </Box>
      </Stack>
    </Paper>
  );
}

export function ConfigMapConfigEditor({
  server,
  value,
  onChange,
  perSecretValues,
  onSecretValueChange,
  errors = {},
  readOnly = false,
}) {
  const configMaps = Array.isArray(value?.config_maps) ? value.config_maps : [];
  const secrets = Array.isArray(value?.secrets) ? value.secrets : [];

  const update = (field, list) => (idx, patch) =>
    onChange({
      ...value,
      [field]: list.map((row, i) => (i === idx ? { ...row, ...patch } : row)),
    });
  const add = (field, list, secret) => () =>
    onChange({
      ...value,
      [field]: [
        ...list,
        {
          name: "",
          description: "",
          dir: "",
          files: [
            secret
              ? { id: rowId(), key: "", mode: "", path: "" }
              : {
                  id: rowId(),
                  key: "",
                  mode: "",
                  path: "",
                  content: "",
                  executable: false,
                },
          ],
        },
      ],
    });
  const remove = (field, list) => (idx) => {
    onChange({ ...value, [field]: list.filter((_, i) => i !== idx) });
    if (field === "secrets" && onSecretValueChange) {
      const nextValues = { ...perSecretValues };
      for (const f of list[idx]?.files || []) delete nextValues[f.id];
      onSecretValueChange(nextValues);
    }
  };

  const errorMessages = Object.entries(errors || {}).filter(([, msg]) => msg);

  const section = (field, list, secret) => (
    <Box
      data-testid={
        secret ? "config-map-secrets-section" : "config-map-entries-section"
      }
    >
      <Stack
        direction="row"
        justifyContent="space-between"
        alignItems="center"
        spacing={1}
        sx={{ mb: 0.5 }}
      >
        <Box>
          <Typography variant="body2" sx={{ fontWeight: 600 }}>
            {secret ? "Secret maps" : "Config maps"}
          </Typography>
          <Typography variant="caption" color="text.secondary">
            {secret
              ? "One name holding several secret files (mode 0600 by default). Values are write-only and stored in the encrypted secrets store."
              : "One name holding several plain configuration files, placed together under its directory and saved in grain_config."}
          </Typography>
        </Box>
        <Button
          size="small"
          variant="outlined"
          disabled={readOnly}
          onClick={add(field, list, secret)}
          sx={{ flexShrink: 0 }}
        >
          {secret ? "Add secret map" : "Add config map"}
        </Button>
      </Stack>
      <Stack spacing={1.25} sx={{ mt: 1 }}>
        {list.map((m, idx) => (
          <MapCard
            key={idx}
            secret={secret}
            map={m}
            server={server}
            readOnly={readOnly}
            perSecretValues={perSecretValues}
            onSecretValueChange={onSecretValueChange}
            onChange={(patch) => update(field, list)(idx, patch)}
            onRemove={() => remove(field, list)(idx)}
          />
        ))}
      </Stack>
    </Box>
  );

  return (
    <Stack spacing={2} data-testid="config-map-editor">
      {errorMessages.map(([field, msg]) => (
        <Alert key={field} severity="error" sx={{ overflowWrap: "anywhere" }}>
          <code>{field}</code>: {msg}
        </Alert>
      ))}
      {section("config_maps", configMaps, false)}
      {section("secrets", secrets, true)}
    </Stack>
  );
}

function MapSummary({ server, map, secret }) {
  const keys = Object.keys((secret ? map.keys : map.data) || {}).sort();
  const dir = map.dir || `${DEFAULT_CONFIG_MAP_DIR}/${map.name || ""}`;
  return (
    <Paper
      variant="outlined"
      sx={{ p: 1 }}
      data-testid={secret ? "config-map-secret-item" : "config-map-item"}
    >
      <Stack
        direction="row"
        spacing={1}
        alignItems="center"
        sx={{ flexWrap: "wrap", rowGap: 0.5 }}
      >
        <Typography
          variant="body2"
          sx={{ fontFamily: "monospace", fontWeight: 600 }}
        >
          {map.name}
        </Typography>
        <Chip
          size="small"
          variant="outlined"
          label={secret ? "secret map" : "config map"}
        />
        <Chip
          size="small"
          variant="outlined"
          label={dir}
          sx={{ fontFamily: "monospace", maxWidth: "100%" }}
        />
        {keys.map((k) => {
          const isSet = secret && isSecretKeySet(server, map.name, k);
          return (
            <Chip
              key={k}
              size="small"
              variant="outlined"
              color={secret ? (isSet ? "success" : "warning") : "default"}
              label={secret ? `${k}: ${isSet ? "set" : "not set"}` : k}
              sx={{ fontFamily: "monospace", maxWidth: "100%" }}
            />
          );
        })}
        {map.description && (
          <Typography variant="caption" color="text.secondary">
            {map.description}
          </Typography>
        )}
      </Stack>
    </Paper>
  );
}

export default function ConfigMapList({ server }) {
  const cfg = server?.config?.config || {};
  const configMaps = Array.isArray(cfg.config_maps) ? cfg.config_maps : [];
  const secrets = Array.isArray(cfg.secrets) ? cfg.secrets : [];
  if (configMaps.length === 0 && secrets.length === 0) return null;

  return (
    <Box sx={{ mt: 1.5 }} data-testid="config-map-list">
      <Typography variant="body2" sx={{ fontWeight: 600, mb: 0.75 }}>
        Configured maps ({configMaps.length} config map
        {configMaps.length === 1 ? "" : "s"}, {secrets.length} secret map
        {secrets.length === 1 ? "" : "s"})
      </Typography>
      <Stack spacing={0.75}>
        {configMaps.map((cm) => (
          <MapSummary key={`cm:${cm.name}`} server={server} map={cm} />
        ))}
        {secrets.map((sec) => (
          <MapSummary
            key={`sec:${sec.name}`}
            server={server}
            map={sec}
            secret
          />
        ))}
      </Stack>
    </Box>
  );
}
