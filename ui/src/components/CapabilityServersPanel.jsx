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
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  MenuItem,
  ButtonBase,
  Link,
  Paper,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import api from "../api.js";
import CapabilityArgsForm from "./CapabilityArgsForm.jsx";
import { invalidateCapabilityMethods } from "./capabilityMethods.js";
import CapabilityMethodTable from "./CapabilityMethodTable.jsx";
import InfoPopover from "./InfoPopover.jsx";
import ConfigMapList, {
  ConfigMapConfigEditor,
  configMapEditorValue,
  isConfigMapServer,
  normalizeConfigMapPayload,
} from "./ConfigMapList.jsx";
import HostScriptList, { isHostScriptServer } from "./HostScriptList.jsx";
import GChatSpaceList, { isGChatServer } from "./GChatSpaceList.jsx";
import { argumentRows } from "../capabilityArgs.js";
import { identifierField } from "../identifierField.js";
import { HostdMissingNote, hostdEntryProblem } from "./HostdMissingNote.jsx";
import { guardLeaving, leavingLosesEdits } from "../unsavedEdits.js";
import { DISCARD_PROMPT } from "./Overlay.jsx";

// The operator's guide to this page (and the Capability policy card
// below it).
export const CAPABILITY_SERVERS_GUIDE =
  "https://github.com/ai-on-gke/grain/blob/main/docs/capability-servers-ui.md";

// CapabilityServersPanel is the deployment's mcpServers
// (docs/capabilities-spec.md §3.1): the capability servers grain attaches,
// keyed by capability_id, with each one's attach status. Everything here
// is plain configuration that is exported to grain/config; a credential
// is bound to a secret in the encrypted store, so what the entry holds is
// a $secret:<name> reference and the value itself is written to the store
// and never shown again.
//
// The servers are a full-width list, one row each; a row opens that
// server's page, /settings/capabilities/<capability_id> when the panel
// is given onSelect (Settings is), so a server is a link.
//
// A server's Connection -- how grain runs or reaches it -- is edited in
// place (the same PUT that adds one, with renameFrom when its
// capability_id changes); its Server settings are the form its
// configure tool's schema draws (§4.5); and it is tried with Test,
// which runs initialize, tools/list and configure on the unsaved values
// and saves nothing. Removing or renaming a server first lists what still
// grants it (GET /api/capability-servers/usage).
//
// A capability_id is a URL, so every call names it in its body
// (pkg/ui/capability_servers.go).

const stateColor = {
  attached: "success",
  failed: "error",
  disabled: "default",
  pending: "warning",
  "needs-config": "warning",
};

const stateLabel = {
  "needs-config": "needs configuring",
  unknown: "not attached",
};

// What each state chip means, for its tooltip and the page's legend.
export const stateHelp = {
  attached:
    "grain is connected to this server, and tasks can be granted its methods.",
  pending:
    "Saved; grain connects to it on its next pass, usually within seconds.",
  failed:
    "grain could not start or reach the server. The error is on its page; its methods cannot be minted until it attaches.",
  "needs-config":
    "The server is running but is waiting for its Server settings to be filled in before it can hand anything out.",
  disabled:
    "Turned off: grain does not connect to it and refuses grants of its methods. Its entry and settings are kept.",
  unknown:
    "No status reported yet: the daemon behind this page has not tried to connect to it.",
};

function serverState(server) {
  return (
    server.status?.state || (server.config?.disabled ? "disabled" : "unknown")
  );
}

// runsAt is where a server runs, as a short label and what it means.
export function runsAt(config) {
  if (!config?.command)
    return {
      key: "http",
      label: "remote (HTTP)",
      help: "grain reaches this server at its URL; it runs wherever that is, not under grain.",
    };
  if (config.run === "host")
    return {
      key: "host",
      label: "on the host",
      help: "grain starts the command on the bare machine through grain hostd, outside any container.",
    };
  return {
    key: "container",
    label: "beside grain",
    help: "grain starts the command itself, in the same container (or machine) as the grain daemon.",
  };
}

const kindHelp = {
  builtin:
    "Ships with grain and is listed on every deployment. Its capability ID is fixed; disable it if you do not want it.",
  custom: "Added on this deployment, by hand or through grain/config.",
};

const grantedHelp =
  "How many open tasks, templates, schedules and Settings-declared workflow jobs grant one of this server's methods.";

// HelpChip is a chip whose meaning is its tooltip.
function HelpChip({ help, ...props }) {
  return (
    <Tooltip title={help} enterTouchDelay={0}>
      <Chip size="small" {...props} />
    </Tooltip>
  );
}

// Section is one titled part of a server's page: a header, the one
// sentence under it saying what it is for, and its body.
function Section({ title, caption, testid, children }) {
  return (
    <Box
      component="section"
      aria-label={title}
      data-testid={testid}
      sx={{ mt: 2, pt: 1.5, borderTop: 1, borderColor: "divider" }}
    >
      <Typography variant="subtitle2" component="h5">
        {title}
      </Typography>
      <Typography
        variant="body2"
        color="text.secondary"
        data-testid="capability-section-caption"
        sx={{ mb: 1 }}
      >
        {caption}
      </Typography>
      {children}
    </Box>
  );
}

// ActionRow is one action of a server's page: its button and what it
// does, said beside it rather than left to be guessed.
function ActionRow({ button, help }) {
  return (
    <Box
      sx={{
        display: "grid",
        gridTemplateColumns: { xs: "1fr", sm: "11rem 1fr" },
        gap: { xs: 0.25, sm: 1.5 },
        alignItems: "baseline",
        py: 0.5,
      }}
    >
      <Box>{button}</Box>
      <Typography
        variant="body2"
        color="text.secondary"
        data-testid="capability-action-help"
      >
        {help}
      </Typography>
    </Box>
  );
}

// Glossary is the page's legend: every label a row or a server's page
// shows, in plain words. Behind a disclosure so it is there for the
// first visit without being in the way of every later one.
function Glossary() {
  const rows = [
    ["built-in", kindHelp.builtin],
    ["custom", kindHelp.custom],
    ...Object.entries(stateHelp).map(([k, v]) => [stateLabel[k] || k, v]),
    ["beside grain", runsAt({ command: "x" }).help],
    ["on the host", runsAt({ command: "x", run: "host" }).help],
    ["remote (HTTP)", runsAt({}).help],
    ["granted by N", grantedHelp],
    [
      "capability ID",
      "The server's name in grants, a URL such as https://github.com/my-org/capabilities/cloud. A task is granted one method of it as <capability ID>#<method>.",
    ],
    [
      "$secret:<name>",
      "A reference to a value in grain's encrypted secrets store. grain/config keeps only the reference; grain fills in the value when it starts the server.",
    ],
  ];
  return (
    <Box
      component="details"
      data-testid="capability-glossary"
      sx={{ mb: 1.5, "& summary": { cursor: "pointer" } }}
    >
      <Typography component="summary" variant="body2">
        What do the labels on this page mean?
      </Typography>
      <Box
        component="dl"
        sx={{
          m: 0,
          mt: 1,
          display: "grid",
          gridTemplateColumns: { xs: "1fr", sm: "10rem 1fr" },
          columnGap: 1.5,
          rowGap: 0.5,
          "& dt": { fontWeight: 600 },
          "& dd": { m: 0, mb: { xs: 0.75, sm: 0 } },
        }}
      >
        {rows.map(([term, def]) => (
          <Box key={term} sx={{ display: "contents" }}>
            <Typography component="dt" variant="body2">
              {term}
            </Typography>
            <Typography component="dd" variant="body2" color="text.secondary">
              {def}
            </Typography>
          </Box>
        ))}
      </Box>
    </Box>
  );
}

// unreportedReadLimit is how many times the list is read again, after
// loading or saving, while a server has no status at all.
const unreportedReadLimit = 5;

// rowStatus is the one-word health a row's dot shows: ok, degraded
// (anything short of attached) or disabled.
function rowStatus(server) {
  const state = serverState(server);
  if (server.config?.disabled || state === "disabled") return "disabled";
  if (state === "attached") return "ok";
  return "degraded";
}

function linesToPairs(text) {
  const out = {};
  for (const line of text.split("\n")) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const i = trimmed.indexOf("=");
    if (i <= 0) throw new Error(`"${trimmed}" is not NAME=VALUE`);
    out[trimmed.slice(0, i)] = trimmed.slice(i + 1);
  }
  return out;
}

function pairsToLines(pairs) {
  return Object.entries(pairs || {})
    .map(([k, v]) => `${k}=${v}`)
    .join("\n");
}

const SECRET_PREFIX = "$secret:";

function isSecretSchema(schema) {
  return Boolean(
    schema && (schema.writeOnly === true || schema.format === "password"),
  );
}

// secretFieldNames are the top-level config fields the schema marks
// secret (§4.5.2).
function secretFieldNames(schema) {
  return Object.entries(schema?.properties || {})
    .filter(([, sub]) => isSecretSchema(sub))
    .map(([name]) => name);
}

// formFromServer is the edit form's state for a stored entry, or an
// empty one for a new server.
function formFromServer(server) {
  const config = server?.config || {};
  return {
    id: server?.id || "",
    transport: server && !config.command ? "http" : "stdio",
    command: config.command || "",
    args: (config.args || []).join("\n"),
    env: pairsToLines(config.env),
    run: config.run || "container",
    serverUrl: config.serverUrl || "",
    headers: pairsToLines(config.headers),
  };
}

// configFromForm is the protocol.ServerConfig a form describes, keeping
// what the form does not edit (disabled, the configure config) from base.
function configFromForm(form, base) {
  const config = {};
  if (base?.disabled) config.disabled = true;
  if (base?.config && Object.keys(base.config).length)
    config.config = base.config;
  if (form.transport === "stdio") {
    config.command = form.command.trim();
    const args = form.args
      .split("\n")
      .map((a) => a.trim())
      .filter(Boolean);
    if (args.length) config.args = args;
    const env = linesToPairs(form.env);
    if (Object.keys(env).length) config.env = env;
    if (form.run === "host") config.run = "host";
  } else {
    config.serverUrl = form.serverUrl.trim();
    const headers = linesToPairs(form.headers);
    if (Object.keys(headers).length) config.headers = headers;
  }
  return config;
}

// hostChoice says whether run: host can be offered for command, and why
// not when it cannot (#5031: no grain hostd beside the daemon, as on
// Kubernetes, or no allowlist entry for this command).
function hostChoice(hostEntries, command) {
  if (!hostEntries) return { ok: false, why: "checking for grain hostd…" };
  if (!hostEntries.available)
    return {
      ok: false,
      why: hostEntries.reason || "grain hostd is not available here",
    };
  const entries = hostEntries.entries || [];
  if (!command.trim())
    return { ok: false, why: "name the command to see if hostd offers it" };
  if (!entries.includes(command.trim()))
    return {
      ok: false,
      why: `grain hostd's allowlist has no capabilities entry for "${command.trim()}"${
        entries.length ? ` (it offers ${entries.join(", ")})` : ""
      }`,
    };
  return { ok: true, why: "" };
}

const holderKind = {
  task: "Task",
  template: "Template",
  schedule: "Schedule",
  workflow: "Workflow job",
};

// UsageList is where a server's references are granted.
export function UsageList({ holders }) {
  if (!holders?.length)
    return (
      <Typography variant="body2" color="text.secondary">
        Nothing grants this server&apos;s capabilities.
      </Typography>
    );
  return (
    <Box
      component="ul"
      sx={{ m: 0, pl: 2.5, overflowWrap: "anywhere" }}
      data-testid="capability-usage"
    >
      {holders.map((h) => (
        <Typography
          component="li"
          variant="body2"
          key={`${h.kind}:${h.id}:${h.grant}`}
          data-holder={`${h.kind}:${h.id}`}
        >
          {holderKind[h.kind] || h.kind} {h.kind === "task" ? `#${h.id} ` : ""}
          {h.name ? `“${h.name}”` : h.id}
          {h.repo ? ` on ${h.repo}` : ""}
          <Typography
            component="span"
            variant="caption"
            color="text.secondary"
            sx={{ display: "block", fontFamily: "monospace" }}
          >
            {h.grant}
          </Typography>
        </Typography>
      ))}
    </Box>
  );
}

// ProbeResult is what Test found: each method with its description,
// arguments and whether it is stateful, the config schema, and the
// error verbatim -- which names the keyword grain would not enforce.
function ProbeResult({ result }) {
  if (!result) return null;
  const configRows = argumentRows(result.configSchema);
  const methodReports = (result.methods || [])
    .map((m) =>
      (m.description || "").split("\n\n").slice(1).join("\n\n").trim(),
    )
    .filter(Boolean);
  return (
    <Box sx={{ mt: 1 }} data-testid="capability-test-result">
      <Alert
        severity={result.ok ? "success" : "error"}
        sx={{ overflowWrap: "anywhere", whiteSpace: "pre-wrap" }}
      >
        {result.ok
          ? `The server answered with ${result.methods?.length || 0} method${result.methods?.length === 1 ? "" : "s"}.`
          : result.error || "The server could not be reached."}
      </Alert>
      {(result.warnings || []).map((w) => (
        <Alert
          key={w}
          severity="warning"
          sx={{ mt: 1, overflowWrap: "anywhere" }}
          data-testid="capability-test-warning"
        >
          {w}
        </Alert>
      ))}
      {methodReports.map((rep, i) => (
        <Typography
          key={i}
          variant="body2"
          color="text.secondary"
          sx={{ mt: 1, overflowWrap: "anywhere", whiteSpace: "pre-wrap" }}
          data-testid="capability-test-report"
        >
          {rep}
        </Typography>
      ))}
      {result.methods?.length > 0 && (
        <CapabilityMethodTable details={result.methods} />
      )}
      {result.configSchema && (
        <Box sx={{ mt: 1 }} data-testid="capability-test-config-schema">
          <Typography variant="body2" sx={{ fontWeight: 600 }}>
            Server settings it asks for
          </Typography>
          {configRows.length > 0 ? (
            <Box
              component="ul"
              sx={{ m: 0, pl: 2.5, overflowWrap: "anywhere" }}
            >
              {configRows.map((row) => (
                <Typography component="li" variant="body2" key={row.name}>
                  <code>{row.name}</code>
                  {row.type ? ` (${row.type})` : ""}
                  {row.required ? ", required" : ""}
                  {isSecretSchema(result.configSchema.properties?.[row.name])
                    ? ", secret"
                    : ""}
                  {row.description && (
                    <>
                      {" "}
                      <InfoPopover title={row.description} label={row.name} />
                    </>
                  )}
                </Typography>
              ))}
            </Box>
          ) : (
            <Typography variant="body2" color="text.secondary">
              The server takes no configuration fields.
            </Typography>
          )}
        </Box>
      )}
    </Box>
  );
}

// ServerForm adds a server, or edits one in place: transport,
// command/args/env or URL/headers, and for stdio where it runs.
function ServerForm({
  server,
  hostEntries,
  readOnly,
  onSave,
  onCancel,
  showError,
}) {
  const [form, setForm] = useState(() => formFromServer(server));
  const [testing, setTesting] = useState(false);
  const [result, setResult] = useState(null);
  const set = (key) => (evt) => setForm({ ...form, [key]: evt.target.value });
  const host = hostChoice(hostEntries, form.command);
  const prefix = server ? `edit-${server.id}` : "add";
  // An edit not yet saved is asked about on the way out, as every other
  // Settings form's is (unsavedEdits.js).
  const dirty = useRef(false);
  dirty.current =
    JSON.stringify(form) !== JSON.stringify(formFromServer(server));
  useEffect(() => guardLeaving(() => dirty.current), []);

  const build = () => {
    try {
      return configFromForm(form, server?.config);
    } catch (err) {
      showError(err.message);
      return null;
    }
  };

  const submit = async (evt) => {
    evt.preventDefault();
    const config = build();
    if (!config) return;
    if (await onSave(form.id.trim(), config)) {
      if (!server) setForm(formFromServer(null));
      setResult(null);
    }
  };

  const test = async () => {
    const config = build();
    if (!config) return;
    setTesting(true);
    try {
      setResult(
        await api("/api/capability-servers/test", {
          method: "POST",
          body: JSON.stringify({ id: form.id.trim(), config }),
        }),
      );
    } catch (err) {
      setResult({ ok: false, error: err.message });
    } finally {
      invalidateCapabilityMethods();
      setTesting(false);
    }
  };

  return (
    <Box
      component="form"
      onSubmit={submit}
      sx={{ display: "grid", gap: 1, mt: 1 }}
      data-testid={server ? "capability-server-edit" : "capability-server-add"}
    >
      <TextField
        name="capabilityId"
        size="small"
        required
        label="Capability ID"
        placeholder="https://github.com/my-org/capabilities/custom"
        value={form.id}
        onChange={set("id")}
        helperText={
          server?.builtin
            ? "A built-in server's ID is grain's own and the same on every deployment."
            : server && form.id.trim() !== server.id
              ? "Renaming: grants naming the old ID will no longer resolve."
              : "The name tasks grant this server's methods by, as <ID>#<method>: a URL you control, such as https://github.com/my-org/capabilities/cloud. Pick it once; renaming it later breaks the grants that name it."
        }
        data-testid="capability-field-capabilityId"
        inputProps={identifierField}
        disabled={readOnly || Boolean(server?.builtin)}
      />
      <TextField
        select
        size="small"
        label="Transport"
        value={form.transport}
        onChange={set("transport")}
        disabled={readOnly}
        id={`${prefix}-transport`}
        helperText={
          form.transport === "stdio"
            ? "stdio: grain starts a program and talks to it over its standard input and output."
            : "HTTP: grain connects to a server that is already running at a URL."
        }
        data-testid="capability-field-transport"
      >
        <MenuItem value="stdio">stdio (command)</MenuItem>
        <MenuItem value="http">HTTP (serverUrl)</MenuItem>
      </TextField>
      {form.transport === "stdio" ? (
        <>
          <TextField
            name="command"
            size="small"
            required
            label="Command"
            helperText="The program grain starts: a name on PATH (grain), an absolute path (/usr/local/bin/my-cap), or a path inside grain/config (plugins/my-cap/server)."
            data-testid="capability-field-command"
            value={form.command}
            onChange={set("command")}
            inputProps={identifierField}
            disabled={readOnly}
          />
          <TextField
            name="args"
            size="small"
            multiline
            minRows={1}
            label="Arguments, one per line"
            helperText="Given to the command, one per line. grain's own servers take two: capability, then the server's name (gcp)."
            data-testid="capability-field-args"
            value={form.args}
            onChange={set("args")}
            disabled={readOnly}
          />
          <TextField
            name="env"
            size="small"
            multiline
            minRows={1}
            label="Env, NAME=VALUE per line"
            helperText="Environment variables the command starts with, such as REGION=us-central1. Write a credential as $secret:<name> (API_TOKEN=$secret:my-token), or add it with Bind a secret after saving, so its value never reaches grain/config."
            data-testid="capability-field-env"
            value={form.env}
            onChange={set("env")}
            disabled={readOnly}
          />
          <TextField
            select
            size="small"
            label="Run location"
            value={form.run}
            onChange={set("run")}
            disabled={readOnly}
            id={`${prefix}-run`}
            helperText={
              host.ok
                ? "Where grain starts the command. container: beside the grain daemon, in its container. host: on the bare machine through grain hostd, for a server that needs the host's own tools or files."
                : `Where grain starts the command. container: beside the grain daemon, in its container. host is not available: ${host.why}`
            }
            data-testid="capability-server-run"
          >
            <MenuItem value="container">container (beside the daemon)</MenuItem>
            <MenuItem value="host" disabled={!host.ok}>
              host (through grain hostd)
            </MenuItem>
          </TextField>
        </>
      ) : (
        <>
          <TextField
            name="serverUrl"
            size="small"
            required
            label="Server URL"
            placeholder="https://cap.example.com/mcp"
            helperText="Where the server answers MCP requests, such as https://cap.example.com/mcp."
            data-testid="capability-field-serverUrl"
            value={form.serverUrl}
            onChange={set("serverUrl")}
            inputProps={identifierField}
            disabled={readOnly}
          />
          <TextField
            name="headers"
            size="small"
            multiline
            minRows={1}
            label="Headers, NAME=VALUE per line"
            helperText="Sent with every request to the server, such as Authorization=Bearer $secret:cap-token. Write a credential as $secret:<name>, or add it with Bind a secret after saving, so its value never reaches grain/config."
            data-testid="capability-field-headers"
            value={form.headers}
            onChange={set("headers")}
            disabled={readOnly}
          />
        </>
      )}
      <Stack direction="row" spacing={1} sx={{ flexWrap: "wrap", rowGap: 1 }}>
        <Button type="submit" variant="contained" disabled={readOnly}>
          {server ? "Save connection" : "Add server"}
        </Button>
        <Button
          variant="outlined"
          onClick={test}
          disabled={readOnly || testing || !form.id.trim()}
        >
          {testing ? "Testing…" : "Test"}
        </Button>
        {onCancel && <Button onClick={onCancel}>Cancel</Button>}
      </Stack>
      <Typography variant="body2" color="text.secondary">
        Test starts the server from the values above, unsaved, lists what it
        offers and saves nothing.
        {server
          ? " Saving applies to attempts that start afterwards."
          : " Adding saves it to grain/config and grain connects to it within seconds."}
      </Typography>
      <ProbeResult result={result} />
    </Box>
  );
}

// ServerConfigForm is a server's configuration (§4.5), drawn from the
// schema its configure tool declared. Secret fields are write-only: the
// stored entry keeps their $secret:<name> references, and what is typed
// is sent as configSecrets for the server to store.
function ServerConfigForm({ server, readOnly, onSaved, showError }) {
  const schema = server.status?.configSchema;
  const isConfigMap = isConfigMapServer(server);
  const secretNames = secretFieldNames(schema);
  const stored = server.config.config || {};
  const plain = Object.fromEntries(
    Object.entries(stored).filter(([k]) => !secretNames.includes(k)),
  );
  const [value, setValue] = useState(() =>
    isConfigMap ? configMapEditorValue(plain) : plain,
  );
  const [secretValues, setSecretValues] = useState({});
  const [perSecretValues, setPerSecretValues] = useState({});
  const [errors, setErrors] = useState({});
  const [result, setResult] = useState(null);
  const [busy, setBusy] = useState(false);
  const initial = useRef(JSON.stringify(value));
  const dirty = useRef(false);
  dirty.current =
    JSON.stringify(value) !== initial.current ||
    Object.values(secretValues).some(Boolean) ||
    Object.keys(perSecretValues).length > 0;
  useEffect(() => guardLeaving(() => dirty.current), []);

  const present = Object.fromEntries(
    (server.secrets || []).map((s) => [s.name, s.present]),
  );
  const fields = {};
  const refs = {};
  for (const name of secretNames) {
    const ref = stored[name];
    if (typeof ref === "string" && ref.startsWith(SECRET_PREFIX)) {
      refs[name] = ref;
      fields[name] = present[ref.slice(SECRET_PREFIX.length)]
        ? "set"
        : "missing";
    } else fields[name] = "unset";
  }
  const managed = Object.fromEntries(
    (server.managed || []).map((m) => [m.field, m]),
  );
  const typed = Object.fromEntries(
    Object.entries(secretValues).filter(([, v]) => v),
  );
  const rotate = async () => {
    setBusy(true);
    try {
      onSaved(
        await api("/api/capability-servers/rotate-managed", {
          method: "POST",
          body: JSON.stringify({ id: server.id }),
        }),
      );
    } catch (err) {
      showError(err);
    } finally {
      setBusy(false);
    }
  };
  const body = () => {
    if (isConfigMap) {
      const normalized = normalizeConfigMapPayload(value, perSecretValues);
      return {
        id: server.id,
        config: {
          ...server.config,
          config: { ...normalized.config, ...refs },
        },
        configSecrets: normalized.configSecrets,
      };
    }
    return {
      id: server.id,
      config: { ...server.config, config: { ...value, ...refs } },
      configSecrets: typed,
    };
  };

  const save = async () => {
    setBusy(true);
    try {
      const data = await api("/api/capability-servers", {
        method: "PUT",
        body: JSON.stringify(body()),
      });
      invalidateCapabilityMethods();
      setErrors({});
      setSecretValues({});
      setPerSecretValues({});
      onSaved(data);
    } catch (err) {
      // A refusal keyed by field is shown on that field, not again as
      // a toast over the form.
      if (err.fields && Object.keys(err.fields).length) setErrors(err.fields);
      else {
        setErrors({});
        showError(err);
      }
    } finally {
      setBusy(false);
    }
  };

  const test = async () => {
    setBusy(true);
    try {
      setResult(
        await api("/api/capability-servers/test", {
          method: "POST",
          body: JSON.stringify(body()),
        }),
      );
    } catch (err) {
      setResult({ ok: false, error: err.message });
    } finally {
      invalidateCapabilityMethods();
      setBusy(false);
    }
  };

  return (
    <Box sx={{ mt: 1.5 }} data-testid="capability-server-config">
      <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
        The fields below are the ones this server asks for. A secret field is
        write-only: its chip says whether the store holds a value (set), the
        entry names one the store lacks (missing) or none was given (not set);
        type to replace it, or leave it empty to keep it.
      </Typography>
      {isConfigMap ? (
        <ConfigMapConfigEditor
          server={server}
          value={value}
          onChange={setValue}
          perSecretValues={perSecretValues}
          onSecretValueChange={setPerSecretValues}
          errors={errors}
          readOnly={readOnly}
        />
      ) : (
        <CapabilityArgsForm
          schema={schema}
          value={value}
          onChange={setValue}
          errors={errors}
          idPrefix={`config-${server.id}`}
          secrets={{
            fields,
            values: secretValues,
            onChange: (name, v) =>
              setSecretValues({ ...secretValues, [name]: v }),
            managed,
            onRotate: readOnly || busy ? null : rotate,
          }}
        />
      )}
      <Stack
        direction="row"
        spacing={1}
        sx={{ mt: 1, flexWrap: "wrap", rowGap: 1 }}
      >
        <Button
          variant="contained"
          size="small"
          onClick={save}
          disabled={readOnly || busy}
        >
          Save server settings
        </Button>
        <Button
          variant="outlined"
          size="small"
          onClick={test}
          disabled={readOnly || busy}
        >
          Test server settings
        </Button>
      </Stack>
      <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
        Test tries the values above, typed secrets included, without storing
        them. Saving checks them against the server&apos;s own rules first;
        attempts that start afterwards use them.
      </Typography>
      <ProbeResult result={result} />
    </Box>
  );
}

// serverName is what a server is called on this page: a built-in's own
// name, or a custom server's capability_id without its scheme.
function serverName(server) {
  return server.builtin
    ? server.builtin.name
    : server.id.replace(/^https?:\/\//, "");
}

// serverSummary is a row's one line on what the server gives a task.
function serverSummary(server) {
  if (server.builtin?.summary) return server.builtin.summary;
  if (server.status?.summary) return server.status.summary;
  if (server.config?.summary) return server.config.summary;
  const methods = server.status?.methods || [];
  if (methods.length)
    return `Gives tasks: ${methods.map((m) => `#${m}`).join(", ")}`;
  return server.config.command
    ? [server.config.command, ...(server.config.args || [])].join(" ")
    : server.config.serverUrl || "";
}

const SUMMARY_MAX_CHARS = 120;

// summarizeServer returns a one-sentence/one-clause short summary when text
// exceeds SUMMARY_MAX_CHARS or spans multiple sentences/lines, and keeps
// the full text for an InfoPopover.
export function summarizeServer(text, max = SUMMARY_MAX_CHARS) {
  const full = (text || "").trim();
  if (!full) return { short: "", full: "", truncated: false };
  const firstLine = full.split(/\r?\n/)[0].trim();
  const sentenceMatch = firstLine.match(/^(.+?[.!?])(?=\s+\S)/);
  if (sentenceMatch && sentenceMatch[1].length <= max) {
    const short = sentenceMatch[1].trim();
    return { short, full, truncated: short !== full };
  }
  if (firstLine.length <= max) {
    return { short: firstLine, full, truncated: firstLine !== full };
  }
  const head = firstLine.slice(0, max);
  for (const sep of [" — ", " -- ", "; ", ": ", ", "]) {
    const idx = head.lastIndexOf(sep);
    if (idx >= 24) {
      const short = head.slice(0, idx).trim();
      return { short, full, truncated: true };
    }
  }
  const spaceIdx = head.lastIndexOf(" ");
  const short = `${(spaceIdx >= 24 ? head.slice(0, spaceIdx) : head).trim()}…`;
  return { short, full, truncated: true };
}

function ServerSummaryLine({ text, label }) {
  const { short, full, truncated } = summarizeServer(text);
  if (!short) return null;
  return (
    <Typography
      variant="body2"
      color="text.secondary"
      component="div"
      data-testid="capability-server-summary"
      sx={{
        display: "flex",
        alignItems: "center",
        gap: 0.5,
        flexWrap: "wrap",
      }}
    >
      <span>{short}</span>
      {truncated && <InfoPopover title={full} label={label} />}
    </Typography>
  );
}

export function serverPath(id) {
  return `/settings/capabilities/${encodeURIComponent(id)}`;
}

function StatusDot({ server }) {
  const state = serverState(server);
  const status = rowStatus(server);
  const color =
    status === "ok"
      ? "success.main"
      : status === "disabled"
        ? "text.disabled"
        : state === "failed"
          ? "error.main"
          : "warning.main";
  return (
    <Box
      component="span"
      title={stateLabel[state] || state}
      data-testid="capability-server-row-status"
      data-status={status}
      data-state={state}
      sx={{
        width: 10,
        height: 10,
        flex: "none",
        borderRadius: "50%",
        bgcolor: color,
        mt: 0.75,
      }}
    />
  );
}

// ServerChips are the facts about one server a row and its page both
// show, each with its meaning as a tooltip.
function ServerChips({ server, holders }) {
  const state = serverState(server);
  const builtin = Boolean(server.builtin);
  const where = runsAt(server.config);
  return (
    <Stack
      direction="row"
      spacing={0.75}
      alignItems="center"
      sx={{ flexWrap: "wrap", rowGap: 0.75 }}
    >
      <HelpChip
        help={builtin ? kindHelp.builtin : kindHelp.custom}
        variant="outlined"
        color={builtin ? "primary" : "default"}
        label={builtin ? "built-in" : "custom"}
        data-testid="capability-server-kind"
      />
      <HelpChip
        help={where.help}
        variant="outlined"
        label={where.label}
        data-testid="capability-server-runs"
        data-runs={where.key}
      />
      <HelpChip
        help={grantedHelp}
        variant="outlined"
        label={`granted by ${holders.length}`}
        data-testid="capability-server-usage-count"
      />
      <HelpChip
        help={stateHelp[state] || ""}
        label={stateLabel[state] || state}
        color={stateColor[state] || "default"}
        data-testid="capability-server-state"
      />
    </Stack>
  );
}

// ServerRow is one server in the list: the whole width of the page,
// its full name, what it gives a task and the chips saying what state
// it is in. It is a link to the server's own page.
function ServerRow({ server, holders, onOpen }) {
  const builtin = Boolean(server.builtin);
  return (
    <ButtonBase
      component="a"
      href={serverPath(server.id)}
      onClick={(evt) => {
        if (evt.metaKey || evt.ctrlKey || evt.shiftKey || evt.button !== 0)
          return;
        evt.preventDefault();
        onOpen(server.id);
      }}
      data-capability-server-row={server.id}
      data-builtin-row={builtin ? server.builtin.name : undefined}
      sx={{
        display: "grid",
        gridTemplateColumns: {
          xs: "auto minmax(0, 1fr)",
          md: "auto minmax(0, 1fr) auto",
        },
        columnGap: 1.25,
        rowGap: 0.75,
        alignItems: "start",
        textAlign: "left",
        width: "100%",
        p: 1.5,
        border: 1,
        borderColor: "divider",
        borderRadius: 1,
        color: "inherit",
        "&:hover, &.Mui-focusVisible": { bgcolor: "action.hover" },
      }}
    >
      <StatusDot server={server} />
      <Box sx={{ minWidth: 0, overflowWrap: "anywhere" }}>
        <Typography variant="body1" sx={{ fontWeight: 600 }}>
          {serverName(server)}
        </Typography>
        <ServerSummaryLine
          text={serverSummary(server)}
          label={serverName(server)}
        />
        {builtin && (
          <Typography
            variant="caption"
            color="text.secondary"
            sx={{ fontFamily: "monospace" }}
          >
            {server.id}
          </Typography>
        )}
      </Box>
      <Box sx={{ gridColumn: { xs: "2", md: "auto" } }}>
        <ServerChips server={server} holders={holders} />
      </Box>
    </ButtonBase>
  );
}

export default function CapabilityServersPanel({
  showError,
  readOnly,
  selected: selectedProp,
  onSelect,
}) {
  const [servers, setServers] = useState(null);
  // The open server, by capability_id, or "" for the list. Held by the
  // caller when it routes it (Settings puts it in the path), and here
  // otherwise.
  const [ownSelected, setOwnSelected] = useState("");
  const routed = typeof onSelect === "function";
  const selected = routed ? selectedProp || "" : ownSelected;
  const [adding, setAdding] = useState(false);
  const [usage, setUsage] = useState({});
  const [hostEntries, setHostEntries] = useState(null);
  // The one form open on the server's page -- bind-secret, connection
  // or server settings -- so the page is not a stack of forms.
  const [binding, setBinding] = useState(null);
  const [editing, setEditing] = useState(null);
  const [configuring, setConfiguring] = useState(null);
  const [testing, setTesting] = useState({});
  // confirm is the removal or rename waiting on its usage warning.
  const [confirm, setConfirm] = useState(null);

  const go = (id) => {
    if (leavingLosesEdits() && !window.confirm(DISCARD_PROMPT)) return;
    setEditing(null);
    setConfiguring(null);
    setBinding(null);
    setAdding(false);
    if (routed) onSelect(id);
    else setOwnSelected(id);
    window.scrollTo?.({ top: 0 });
  };

  const refreshUsage = useCallback(async () => {
    try {
      const data = await api("/api/capability-servers/usage");
      const byID = {};
      for (const u of Array.isArray(data) ? data : [])
        byID[u.id] = u.holders || [];
      setUsage(byID);
    } catch {
      // Usage is a warning's detail; the list works without it.
      setUsage({});
    }
  }, []);

  const refresh = useCallback(async () => {
    try {
      const data = await api("/api/capability-servers");
      // Read again while a saved server is still attaching, too: its
      // methods appear in the listing only once it has.
      invalidateCapabilityMethods();
      setServers(Array.isArray(data) ? data : []);
    } catch (err) {
      showError(err);
    }
  }, [showError]);

  // What hostd offers, read again every 30 seconds: an entry grain adds
  // on the host, or a hostd back from a restart, clears a row's note
  // without a reload.
  const refreshHostEntries = useCallback(() => {
    api("/api/capability-servers/host-entries")
      .then((data) => setHostEntries(data || { available: false }))
      .catch((err) =>
        setHostEntries({ available: false, reason: err.message, entries: [] }),
      );
  }, []);

  useEffect(() => {
    refresh();
    refreshUsage();
    refreshHostEntries();
    const t = setInterval(refreshHostEntries, 30000);
    return () => clearInterval(t);
  }, [refresh, refreshUsage, refreshHostEntries]);

  // A saved server attaches on the daemon's next tick: while one is
  // still pending, read the list again until it says how that went. One
  // with no status at all is one no registry here reports (grain demo's
  // built-ins, say), which more reads will not change: it is read again
  // only a few times after loading or saving, not forever.
  const live = (servers || []).filter((s) => !s.config.disabled);
  const pending = live.some((s) => s.status?.state === "pending");
  const unreported = live.some((s) => !s.status);
  const unreportedReads = useRef(0);
  const settling = unreported && unreportedReads.current < unreportedReadLimit;
  useEffect(() => {
    if (!pending && !settling) return undefined;
    const t = setTimeout(() => {
      if (!pending) unreportedReads.current += 1;
      refresh();
    }, 2000);
    return () => clearTimeout(t);
  }, [pending, settling, servers, refresh]);

  const call = async (path, body, method = "POST") => {
    try {
      const data = await api(path, { method, body: JSON.stringify(body) });
      unreportedReads.current = 0;
      invalidateCapabilityMethods();
      setServers(Array.isArray(data) ? data : []);
      refreshUsage();
      return true;
    } catch (err) {
      showError(err);
      return false;
    }
  };

  const add = async (id, config) => {
    const ok = await call("/api/capability-servers", { id, config }, "PUT");
    if (ok) {
      setAdding(false);
      if (routed) onSelect(id);
      else setOwnSelected(id);
    }
    return ok;
  };

  const saveEdit = async (server, id, config) => {
    if (id !== server.id) {
      setConfirm({ kind: "rename", server, id, config });
      return false;
    }
    const ok = await call("/api/capability-servers", { id, config }, "PUT");
    if (ok) setEditing(null);
    return ok;
  };

  const confirmed = async () => {
    const c = confirm;
    setConfirm(null);
    if (c.kind === "remove") {
      if (await call("/api/capability-servers/remove", { id: c.server.id }))
        go("");
    } else if (
      await call(
        "/api/capability-servers",
        { id: c.id, config: c.config, renameFrom: c.server.id },
        "PUT",
      )
    ) {
      setEditing(null);
      if (routed) onSelect(c.id);
      else setOwnSelected(c.id);
    }
  };

  const test = async (server) => {
    setTesting({ ...testing, [server.id]: { busy: true } });
    let result;
    try {
      result = await api("/api/capability-servers/test", {
        method: "POST",
        body: JSON.stringify({ id: server.id }),
      });
    } catch (err) {
      result = { ok: false, error: err.message };
    }
    invalidateCapabilityMethods();
    setTesting((prev) => ({ ...prev, [server.id]: { result } }));
  };

  const bind = async (evt, server) => {
    evt.preventDefault();
    const form = evt.target;
    const ok = await call("/api/capability-servers/bind-secret", {
      id: server.id,
      kind: server.config.command ? "env" : "header",
      name: form.elements.name.value.trim(),
      secret: form.elements.secret.value.trim(),
      prefix: form.elements.prefix.value,
      value: form.elements.value.value,
    });
    if (ok) {
      form.reset();
      setBinding(null);
    }
  };

  if (!servers) return null;

  const activeServer = selected
    ? servers.find((s) => s.id === selected) || null
    : null;

  const backButton = (
    <Button
      size="small"
      onClick={() => go("")}
      data-testid="capability-servers-back"
      sx={{ mb: 1, ml: -0.5 }}
    >
      ← All capability servers
    </Button>
  );

  const renderServerDetail = (server) => {
    const state = serverState(server);
    const builtin = Boolean(server.builtin);
    const isStdio = Boolean(server.config.command);
    const entries = isStdio
      ? server.config.env || {}
      : server.config.headers || {};
    const holders = usage[server.id] || [];
    const tested = testing[server.id];
    const where = runsAt(server.config);
    const hasSettings =
      Boolean(server.status?.configSchema) ||
      Boolean(builtin && server.status?.error) ||
      isHostScriptServer(server) ||
      isGChatServer(server) ||
      isConfigMapServer(server);
    return (
      <Paper
        key={server.id}
        variant="outlined"
        data-capability-server={server.id}
        data-builtin={builtin ? server.builtin.name : undefined}
        sx={{ p: { xs: 1.5, sm: 2 }, minWidth: 0 }}
      >
        <Box sx={{ minWidth: 0, overflowWrap: "anywhere" }}>
          <Typography variant="h6" component="h4">
            {builtin ? `${server.builtin.name} server` : server.id}
          </Typography>
          {(server.builtin?.summary ||
            server.status?.summary ||
            server.config?.summary) && (
            <ServerSummaryLine
              text={
                server.builtin?.summary ||
                server.status?.summary ||
                server.config?.summary
              }
              label={builtin ? `${server.builtin.name} server` : server.id}
            />
          )}
          <Typography
            variant="body2"
            color="text.secondary"
            sx={{ mb: 1 }}
            title="The capability ID: the name grants use for this server's methods, as <capability ID>#<method>."
          >
            Capability ID: <code>{server.id}</code>
          </Typography>
          <ServerChips server={server} holders={holders} />
          <Typography
            variant="body2"
            color="text.secondary"
            data-testid="capability-server-state-help"
            sx={{ mt: 0.75 }}
          >
            {stateHelp[state]}
          </Typography>
        </Box>
        {server.config.notice && (
          <Alert
            severity="info"
            sx={{ mt: 1, overflowWrap: "anywhere" }}
            data-testid="capability-server-notice"
            action={
              <Button
                size="small"
                color="inherit"
                disabled={readOnly}
                onClick={() =>
                  call("/api/capability-servers/dismiss-notice", {
                    id: server.id,
                  })
                }
              >
                Dismiss
              </Button>
            }
          >
            {server.config.notice}
          </Alert>
        )}
        {server.status?.reason && (
          <Alert
            severity="info"
            sx={{ mt: 1, overflowWrap: "anywhere" }}
            data-testid="capability-server-needs-config"
          >
            Needs configuring: {server.status.reason}
          </Alert>
        )}
        {server.config.run === "host" && (
          <HostdMissingNote
            problem={hostdEntryProblem(hostEntries, server.config.command)}
            sx={{ mt: 1 }}
          />
        )}
        {server.status?.error && (
          <Alert
            severity="error"
            sx={{ mt: 1, overflowWrap: "anywhere", whiteSpace: "pre-wrap" }}
          >
            {!server.status?.configSchema && builtin
              ? `Configure becomes available once the server starts — fix: ${server.status.error}`
              : server.status.error}
          </Alert>
        )}

        <Section
          title="What it gives a task"
          caption="Each method is one capability a task, template, schedule or CI job can be granted, named <capability ID>#<method>. Methods appear once grain has connected to the server."
          testid="capability-server-methods"
        >
          {server.status?.methodDetails?.length > 0 ? (
            <CapabilityMethodTable details={server.status.methodDetails} />
          ) : server.status?.methods?.length > 0 ? (
            <Typography variant="body2">
              Methods: {server.status.methods.join(", ")}
            </Typography>
          ) : (
            <Typography variant="body2" color="text.secondary">
              No methods reported yet.
            </Typography>
          )}
        </Section>

        <Section
          title="Connection"
          caption="How grain starts or reaches this server: its command or URL, where it runs, and the environment or headers it is given. Saved to grain/config; attempts that start after a change use it."
          testid="capability-server-connection"
        >
          <Box
            component="dl"
            sx={{
              m: 0,
              display: "grid",
              gridTemplateColumns: { xs: "1fr", sm: "9rem 1fr" },
              columnGap: 1.5,
              rowGap: 0.25,
              "& dt": { color: "text.secondary" },
              "& dd": {
                m: 0,
                mb: { xs: 0.5, sm: 0 },
                overflowWrap: "anywhere",
              },
            }}
          >
            <Typography component="dt" variant="body2">
              {isStdio ? "Command" : "Server URL"}
            </Typography>
            <Typography
              component="dd"
              variant="body2"
              sx={{ fontFamily: "monospace" }}
            >
              {isStdio
                ? [server.config.command, ...(server.config.args || [])].join(
                    " ",
                  )
                : server.config.serverUrl}
            </Typography>
            <Typography component="dt" variant="body2">
              Runs
            </Typography>
            <Typography component="dd" variant="body2">
              {where.label} — {where.help}
            </Typography>
            {Object.entries(entries).map(([k, v]) => (
              <Box key={k} sx={{ display: "contents" }}>
                <Typography component="dt" variant="body2">
                  {isStdio ? "Env" : "Header"} {k}
                </Typography>
                <Typography
                  component="dd"
                  variant="body2"
                  sx={{ fontFamily: "monospace" }}
                >
                  {v}
                </Typography>
              </Box>
            ))}
          </Box>
          {server.secrets.length > 0 && (
            <Stack
              direction="row"
              spacing={1}
              sx={{ mt: 1, flexWrap: "wrap", gap: 1 }}
            >
              {server.secrets.map((sec) => (
                <HelpChip
                  key={sec.name}
                  variant="outlined"
                  color={sec.present ? "success" : "warning"}
                  label={`${sec.name}: ${sec.present ? "set" : "missing"}`}
                  help={
                    sec.present
                      ? `The secrets store holds a value for $secret:${sec.name}.`
                      : `The entry refers to $secret:${sec.name}, but the secrets store has no value for it. Bind a secret with that name to fill it in.`
                  }
                />
              ))}
            </Stack>
          )}
          <Stack
            direction="row"
            spacing={0.5}
            sx={{ mt: 1, flexWrap: "wrap", rowGap: 0.5, ml: -0.5 }}
          >
            <Button
              size="small"
              variant="outlined"
              disabled={readOnly}
              aria-expanded={editing === server.id}
              onClick={() =>
                setEditing(editing === server.id ? null : server.id)
              }
            >
              Edit connection
            </Button>
            <Button
              size="small"
              disabled={readOnly}
              aria-expanded={binding === server.id}
              onClick={() =>
                setBinding(binding === server.id ? null : server.id)
              }
            >
              Bind a secret
            </Button>
          </Stack>
          <Typography
            variant="body2"
            color="text.secondary"
            data-testid="capability-action-help"
            sx={{ mt: 0.5 }}
          >
            Bind a secret stores a credential in the encrypted secrets store and
            adds {isStdio ? "an environment variable" : "a header"} to this
            entry that refers to it as <code>$secret:&lt;name&gt;</code>, so the
            value itself never reaches grain/config.
          </Typography>
          {editing === server.id && (
            <ServerForm
              server={server}
              hostEntries={hostEntries}
              readOnly={readOnly}
              showError={showError}
              onSave={(id, config) => saveEdit(server, id, config)}
              onCancel={() => setEditing(null)}
            />
          )}
          {binding === server.id && (
            <Box
              component="form"
              onSubmit={(evt) => bind(evt, server)}
              sx={{
                mt: 1,
                display: "grid",
                gap: 1,
                gridTemplateColumns: {
                  xs: "1fr",
                  sm: "repeat(2, 1fr)",
                },
                alignItems: "start",
              }}
            >
              <TextField
                name="name"
                size="small"
                required
                label={isStdio ? "Env name" : "Header name"}
                helperText={
                  isStdio
                    ? "The variable the server reads, such as API_TOKEN."
                    : "The header sent to the server, such as Authorization."
                }
                inputProps={identifierField}
                disabled={readOnly}
              />
              <TextField
                name="secret"
                size="small"
                required
                label="Secret name"
                helperText="What the value is stored under, such as cap-token. The entry keeps $secret:cap-token."
                inputProps={identifierField}
                disabled={readOnly}
              />
              <TextField
                name="prefix"
                size="small"
                label="Prefix (optional)"
                placeholder={isStdio ? "" : "Bearer "}
                helperText="Text put before the secret, such as “Bearer ” for an Authorization header."
                disabled={readOnly}
              />
              <TextField
                name="value"
                size="small"
                type="password"
                label="Value (optional)"
                helperText="The credential itself. Leave it empty to point at a secret the store already holds."
                autoComplete="new-password"
                disabled={readOnly}
              />
              <Box>
                <Button type="submit" variant="outlined" disabled={readOnly}>
                  Bind secret
                </Button>
              </Box>
            </Box>
          )}
        </Section>

        {hasSettings && (
          <Section
            title="Server settings"
            caption="Options this server defines for itself, such as the accounts, scripts or chat spaces it offers. grain sends them to the server each time it connects; attempts that start after a change use them."
            testid="capability-server-settings"
          >
            {server.status?.configSchema ? (
              <Button
                size="small"
                variant="outlined"
                disabled={readOnly}
                aria-expanded={configuring === server.id}
                onClick={() =>
                  setConfiguring(configuring === server.id ? null : server.id)
                }
              >
                Edit server settings
              </Button>
            ) : (
              builtin &&
              server.status?.error && (
                <Button
                  size="small"
                  variant="outlined"
                  disabled
                  data-testid="capability-server-configure-unavailable"
                >
                  Edit server settings
                </Button>
              )
            )}
            {configuring === server.id && server.status?.configSchema && (
              <ServerConfigForm
                server={server}
                readOnly={readOnly}
                showError={showError}
                onSaved={(data) => {
                  setServers(Array.isArray(data) ? data : []);
                  setConfiguring(null);
                }}
              />
            )}
            {isHostScriptServer(server) && (
              <HostScriptList server={server} readOnly={readOnly} />
            )}
            {isGChatServer(server) && (
              <GChatSpaceList server={server} readOnly={readOnly} />
            )}
            {isConfigMapServer(server) && <ConfigMapList server={server} />}
          </Section>
        )}

        <Section
          title="Granted by"
          caption="The open tasks, templates, schedules and Settings-declared workflow jobs whose grants name one of this server's methods."
          testid="capability-server-usage"
        >
          <UsageList holders={holders} />
        </Section>

        <Section
          title="Check and manage"
          caption="Try the server as it is saved, or take it out of use."
          testid="capability-server-manage"
        >
          <ActionRow
            button={
              <Button
                size="small"
                variant="outlined"
                disabled={readOnly || tested?.busy}
                onClick={() => test(server)}
              >
                {tested?.busy ? "Testing…" : "Test"}
              </Button>
            }
            help="Starts the saved entry in a session of its own, lists what it offers and runs its server settings. Saves nothing and changes nothing for any task."
          />
          {tested?.result && <ProbeResult result={tested.result} />}
          <ActionRow
            button={
              <Button
                size="small"
                variant="outlined"
                disabled={readOnly}
                onClick={() =>
                  call(
                    `/api/capability-servers/${server.config.disabled ? "enable" : "disable"}`,
                    { id: server.id },
                  )
                }
              >
                {server.config.disabled ? "Enable" : "Disable"}
              </Button>
            }
            help={
              server.config.disabled
                ? "Turns the server back on: grain connects to it again and admits grants of its methods."
                : "Turns the server off but keeps its entry and settings. Until it is enabled again, anything that grants its methods is refused when it next starts."
            }
          />
          {!builtin && (
            <ActionRow
              button={
                <Button
                  size="small"
                  variant="outlined"
                  color="error"
                  disabled={readOnly}
                  onClick={() => setConfirm({ kind: "remove", server })}
                >
                  Remove
                </Button>
              }
              help="Deletes the entry from grain/config. Anything still granting its methods is refused until those grants change or a server is added back under the same ID; you see that list before anything is removed."
            />
          )}
        </Section>
      </Paper>
    );
  };

  const renderList = () => (
    <>
      <Stack
        direction="row"
        alignItems="center"
        justifyContent="space-between"
        spacing={1}
        sx={{ mb: 1, flexWrap: "wrap", rowGap: 1 }}
      >
        <Typography variant="body2" color="text.secondary">
          {servers.length === 0
            ? "No capability servers configured."
            : `${servers.length} server${servers.length === 1 ? "" : "s"}. Open one to see what it gives a task and to change it.`}
        </Typography>
        <Button
          variant="contained"
          size="small"
          disabled={readOnly}
          onClick={() => {
            setAdding(true);
          }}
          data-testid="capability-server-add-button"
        >
          + Add server
        </Button>
      </Stack>
      <Stack spacing={1} role="list" aria-label="Capability servers">
        {servers.map((server) => (
          <Box role="listitem" key={server.id}>
            <ServerRow
              server={server}
              holders={usage[server.id] || []}
              onOpen={go}
            />
          </Box>
        ))}
      </Stack>
    </>
  );

  return (
    <Box>
      <Box sx={{ mb: 1.5 }} data-testid="capability-servers-intro">
        <Typography variant="body2" sx={{ mb: 1 }}>
          A <strong>capability</strong> is something a task is given for the
          length of one attempt: a short-lived cloud key, a checkout of another
          repository, an extra set of tools for its agent. A{" "}
          <strong>capability server</strong> is the program that hands
          capabilities out: grain asks it for one as an attempt starts, and
          tells it when the attempt is over so it can take it back.
        </Typography>
        <Typography variant="body2" sx={{ mb: 1 }}>
          A task holds a capability when it, its template, its schedule or a CI
          job grants it as <code>&lt;capability ID&gt;#&lt;method&gt;</code>,
          and the Capability policy below allows it. Everything here is saved to
          grain/config as plain text; credentials go only to the encrypted
          secrets store.{" "}
          <Link href={CAPABILITY_SERVERS_GUIDE} target="_blank" rel="noopener">
            Guide to this page
          </Link>
        </Typography>
        <Alert severity="info" data-testid="capability-servers-vs-legacy">
          This is grain&apos;s current capability system. The older capabilities
          built into grain itself, granted by a bare name such as{" "}
          <code>gcp-key</code>, and their credentials are on{" "}
          <Link href="/settings/credentials">Legacy capabilities</Link>.
        </Alert>
      </Box>
      <Glossary />
      {adding ? (
        <Box>
          <Button
            size="small"
            onClick={() => go(selected)}
            data-testid="capability-servers-back"
            sx={{ mb: 1, ml: -0.5 }}
          >
            ← All capability servers
          </Button>
          <Typography variant="subtitle2" component="h4">
            Add a capability server
          </Typography>
          <Typography variant="body2" color="text.secondary">
            Tell grain how to start or reach the server. Test it before you add
            it: Test starts it from these values and saves nothing.
          </Typography>
          <ServerForm
            hostEntries={hostEntries}
            readOnly={readOnly}
            showError={showError}
            onSave={add}
            onCancel={() => go(selected)}
          />
        </Box>
      ) : selected ? (
        <>
          {backButton}
          {activeServer ? (
            renderServerDetail(activeServer)
          ) : (
            <Alert severity="warning" data-testid="capability-server-unknown">
              No capability server here is called{" "}
              <code style={{ overflowWrap: "anywhere" }}>{selected}</code>.
            </Alert>
          )}
        </>
      ) : (
        renderList()
      )}

      <Dialog
        open={Boolean(confirm)}
        onClose={() => setConfirm(null)}
        maxWidth="sm"
        fullWidth
      >
        {confirm && (
          <>
            <DialogTitle>
              {confirm.kind === "remove"
                ? "Remove this capability server?"
                : "Rename this capability server?"}
            </DialogTitle>
            <DialogContent>
              <Typography
                variant="body2"
                sx={{ mb: 1, overflowWrap: "anywhere" }}
              >
                {confirm.kind === "remove"
                  ? `${confirm.server.id} will be detached.`
                  : `${confirm.server.id} becomes ${confirm.id}.`}{" "}
                {(usage[confirm.server.id] || []).length > 0
                  ? "These still grant its capabilities, and will be refused them until they are changed:"
                  : ""}
              </Typography>
              <UsageList holders={usage[confirm.server.id]} />
            </DialogContent>
            <DialogActions>
              <Button onClick={() => setConfirm(null)}>Cancel</Button>
              <Button
                color={confirm.kind === "remove" ? "error" : "primary"}
                variant="contained"
                onClick={confirmed}
              >
                {confirm.kind === "remove" ? "Remove" : "Rename"}
              </Button>
            </DialogActions>
          </>
        )}
      </Dialog>
    </Box>
  );
}
