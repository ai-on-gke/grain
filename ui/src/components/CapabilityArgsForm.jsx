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

import { useEffect, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  FormControl,
  FormControlLabel,
  FormHelperText,
  IconButton,
  InputLabel,
  MenuItem,
  Select,
  Switch,
  TextField,
  Typography,
} from "@mui/material";
import CloseIcon from "@mui/icons-material/Close";
import InfoPopover from "./InfoPopover.jsx";
import {
  deref,
  fieldKind,
  initialValue,
  isHiddenProperty,
  isMultiline,
  isSecretLooking,
  isStringMap,
  unsupportedReason,
} from "../capabilityArgs.js";

// CapabilityArgsForm renders a capability method's with: arguments from
// its args schema (capabilityArgs.js has which keywords, and why a schema
// outside them is a JSON editor instead). value is the with: object;
// errors are messages keyed by argument path, the live check's and the
// server's together.
//
// secrets switches the form to a server's configuration (§4.5): there a
// field marked writeOnly or format: password is a secret, drawn as a
// write-only input showing whether the store holds it, and its value is
// kept apart from value -- secrets.values, sent as configSecrets -- so
// it is never part of what is saved as plain text. secrets is
// { fields: {name: "set"|"missing"|"unset"}, values, onChange(name, v),
// managed: {name: {overridden}}, onRotate(name) }: a managed field is one
// grain generates and keeps itself (a data-plane token), drawn as
// "managed by grain" with a Rotate action and its override under
// Advanced.
export default function CapabilityArgsForm({
  schema,
  value,
  onChange,
  errors = {},
  idPrefix = "cap-arg",
  secrets = null,
}) {
  const why = unsupportedReason(schema);
  return (
    <Box sx={{ display: "flex", flexDirection: "column", gap: 1.5 }}>
      <Typography variant="caption" color="text.secondary">
        {secrets
          ? "Configuration is exported to grain/config as plain text, except write-only fields: what is typed into one goes only to the encrypted secrets store."
          : "Arguments are recorded on the task in plain text. Never put a secret in one; a server's credentials belong in Settings → Capability servers."}
      </Typography>
      {why ? (
        <JSONArgs
          value={value}
          onChange={onChange}
          errors={errors}
          reason={why}
          idPrefix={idPrefix}
        />
      ) : (
        <ObjectFields
          root={schema}
          schema={deref(schema, schema)}
          value={value || {}}
          onChange={onChange}
          errors={errors}
          path=""
          idPrefix={idPrefix}
          secrets={secrets}
        />
      )}
      {errors[""] && (
        <Alert severity="error" sx={{ py: 0 }}>
          {errors[""]}
        </Alert>
      )}
    </Box>
  );
}

// JSONArgs is the fallback editor: the with: object as JSON text, parsed
// here only so far as to be an object -- the schema check is the
// server's.
function JSONArgs({ value, onChange, errors, reason, idPrefix }) {
  const [text, setText] = useState(() => JSON.stringify(value || {}, null, 2));
  const [parseError, setParseError] = useState("");
  const fieldErrors = Object.entries(errors).filter(([k]) => k !== "");
  return (
    <Box>
      <Alert severity="info" sx={{ mb: 1, py: 0 }}>
        This method&apos;s arguments use a schema the form cannot draw ({reason}
        ), so they are edited as JSON and checked by the server.
      </Alert>
      <TextField
        id={`${idPrefix}-json`}
        label="Arguments (JSON)"
        multiline
        minRows={4}
        fullWidth
        value={text}
        error={Boolean(parseError) || fieldErrors.length > 0}
        helperText={
          parseError ||
          fieldErrors.map(([k, msg]) => `${k}: ${msg}`).join("; ") ||
          " "
        }
        inputProps={{ spellCheck: false, style: { fontFamily: "monospace" } }}
        onChange={(e) => {
          setText(e.target.value);
          try {
            const parsed = JSON.parse(e.target.value || "{}");
            if (
              !parsed ||
              typeof parsed !== "object" ||
              Array.isArray(parsed)
            ) {
              setParseError("Arguments must be a JSON object.");
              return;
            }
            setParseError("");
            onChange(parsed);
          } catch (err) {
            setParseError(`Not JSON: ${err.message}`);
          }
        }}
      />
    </Box>
  );
}

function label(name, schema, required) {
  const title = schema?.title || name;
  return required ? `${title} *` : title;
}

function ObjectFields({
  root,
  schema,
  value,
  onChange,
  errors,
  path,
  idPrefix,
  secrets = null,
}) {
  const props = schema?.properties || {};
  const visibleEntries = Object.entries(props).filter(
    ([name, sub]) => !isHiddenProperty(name, deref(root, sub)),
  );
  const required = new Set(schema?.required || []);
  // In a server's configuration only a field the schema marks is a
  // secret (§4.5.2), and it is drawn write-only rather than hidden.
  const secretField = (name) =>
    Boolean(secrets) && path === "" && isSecretField(deref(root, props[name]));
  const hidden = secrets
    ? []
    : visibleEntries
        .map(([name]) => name)
        .filter((name) => isSecretLooking(name, deref(root, props[name])));
  const set = (name, v) => {
    const next = { ...value };
    if (v === undefined) delete next[name];
    else next[name] = v;
    onChange(next);
  };
  return (
    <Box sx={{ display: "flex", flexDirection: "column", gap: 1.5 }}>
      {visibleEntries.map(([name, sub]) => {
        if (hidden.includes(name)) return null;
        const childPath = path ? `${path}.${name}` : name;
        const managed = secretField(name) && secrets.managed?.[name];
        if (managed && !managed.overridden) {
          return (
            <ManagedSecretField
              key={name}
              name={name}
              schema={deref(root, sub)}
              state={secrets.fields?.[name] || "unset"}
              value={secrets.values?.[name] || ""}
              onChange={(v) => secrets.onChange(name, v)}
              onRotate={secrets.onRotate && (() => secrets.onRotate(name))}
              error={errors[childPath]}
              idPrefix={idPrefix}
            />
          );
        }
        if (secretField(name)) {
          return (
            <SecretField
              key={name}
              name={name}
              schema={deref(root, sub)}
              required={required.has(name)}
              state={secrets.fields?.[name] || "unset"}
              value={secrets.values?.[name] || ""}
              onChange={(v) => secrets.onChange(name, v)}
              error={errors[childPath]}
              idPrefix={idPrefix}
            />
          );
        }
        return (
          <Field
            key={name}
            name={name}
            root={root}
            schema={deref(root, sub)}
            required={required.has(name)}
            value={value[name]}
            onChange={(v) => set(name, v)}
            errors={errors}
            path={childPath}
            idPrefix={idPrefix}
          />
        );
      })}
      {hidden.length > 0 && (
        <Alert
          severity={hidden.some((n) => required.has(n)) ? "warning" : "info"}
          sx={{ py: 0 }}
        >
          {hidden.join(", ")} {hidden.length === 1 ? "looks" : "look"} like{" "}
          {hidden.length === 1 ? "a secret" : "secrets"} and{" "}
          {hidden.length === 1 ? "is" : "are"} not offered here: with: arguments
          must never carry secrets.
          {hidden.some((n) => required.has(n)) &&
            " This method requires one, so it cannot be granted from the UI."}
        </Alert>
      )}
    </Box>
  );
}

// isSecretField is a configuration field the schema marks secret.
function isSecretField(schema) {
  return Boolean(
    schema && (schema.writeOnly === true || schema.format === "password"),
  );
}

const secretStateLabel = {
  set: "set",
  missing: "missing",
  unset: "not set",
};

// SecretField is a write-only configuration field: what the store holds
// is never read back, only whether it holds it; typing replaces it.
function SecretField({
  name,
  schema,
  required,
  state,
  value,
  onChange,
  error,
  idPrefix,
}) {
  const help =
    error ||
    (state === "set"
      ? "Stored in the secrets store. Type a new value to replace it."
      : "Stored only in the secrets store.");
  return (
    <Box
      sx={{ display: "flex", gap: 1, alignItems: "flex-start" }}
      data-secret-field={name}
    >
      <TextField
        id={`${idPrefix}-${name}`}
        label={label(name, schema, required)}
        type="password"
        size="small"
        fullWidth
        autoComplete="new-password"
        value={value}
        error={Boolean(error)}
        helperText={help}
        placeholder={state === "set" ? "••••••••" : ""}
        onChange={(e) => onChange(e.target.value)}
      />
      <InfoPopover
        title={schema?.description}
        label={schema?.title || name}
        sx={{ mt: 1 }}
      />
      <Chip
        size="small"
        variant="outlined"
        sx={{ mt: 1 }}
        color={
          state === "set"
            ? "success"
            : state === "missing"
              ? "warning"
              : "default"
        }
        label={secretStateLabel[state] || state}
        data-testid={`secret-state-${name}`}
      />
    </Box>
  );
}

// ManagedSecretField is a secret grain generates and keeps itself: there
// is nothing to type, only a Rotate action, and an operator's own value
// -- which then wins -- under Advanced.
function ManagedSecretField({
  name,
  schema,
  state,
  value,
  onChange,
  onRotate,
  error,
  idPrefix,
}) {
  const [advanced, setAdvanced] = useState(Boolean(value || error));
  return (
    <Box
      sx={{ display: "flex", flexDirection: "column", gap: 1 }}
      data-managed-field={name}
    >
      <Box
        sx={{
          display: "flex",
          gap: 1,
          alignItems: "center",
          flexWrap: "wrap",
        }}
      >
        <Typography variant="body2">{schema?.title || name}</Typography>
        <InfoPopover
          title={schema?.description}
          label={schema?.title || name}
        />
        <Chip
          size="small"
          variant="outlined"
          color="success"
          label="managed by grain"
          data-testid={`managed-state-${name}`}
        />
        {onRotate && (
          <Button size="small" onClick={onRotate}>
            Rotate
          </Button>
        )}
        <Button size="small" onClick={() => setAdvanced(!advanced)}>
          {advanced ? "Hide advanced" : "Advanced"}
        </Button>
      </Box>
      <Typography variant="caption" color="text.secondary">
        Generated by grain and kept in the encrypted secrets store; nobody needs
        to set it.
      </Typography>
      {advanced && (
        <SecretField
          name={name}
          schema={{
            ...schema,
            title: `Override ${schema?.title || name}`,
            description: undefined,
          }}
          state={state}
          value={value}
          onChange={onChange}
          error={error}
          idPrefix={idPrefix}
        />
      )}
    </Box>
  );
}

function Field({
  name,
  root,
  schema,
  required,
  value,
  onChange,
  errors,
  path,
  idPrefix,
}) {
  const kind = fieldKind(schema);
  const error = errors[path];
  const help = error || "";
  const id = `${idPrefix}-${path.replace(/[^A-Za-z0-9_-]/g, "-")}`;
  const lbl = label(name, schema, required);
  const infoTitle = schema?.title || name;
  switch (kind) {
    case "choice": {
      const options = "enum" in schema ? schema.enum : [schema.const];
      const effective =
        value === undefined && "default" in schema ? schema.default : value;
      const index = options.findIndex(
        (o) => JSON.stringify(o) === JSON.stringify(effective),
      );
      return (
        <Box sx={{ display: "flex", alignItems: "flex-start", gap: 0.5 }}>
          <FormControl
            size="small"
            fullWidth
            error={Boolean(error)}
            required={required}
          >
            <InputLabel id={`${id}-label`}>
              {label(name, schema, false)}
            </InputLabel>
            <Select
              labelId={`${id}-label`}
              id={id}
              label={label(name, schema, false)}
              value={index < 0 ? "" : String(index)}
              onChange={(e) =>
                onChange(
                  e.target.value === ""
                    ? undefined
                    : options[Number(e.target.value)],
                )
              }
              inputProps={{ "data-arg": path }}
            >
              {!required && (
                <MenuItem value="">
                  <em>Not set</em>
                </MenuItem>
              )}
              {options.map((o, i) => (
                <MenuItem key={i} value={String(i)}>
                  {typeof o === "string" ? o : JSON.stringify(o)}
                </MenuItem>
              ))}
            </Select>
            {help && <FormHelperText>{help}</FormHelperText>}
          </FormControl>
          <InfoPopover
            title={schema?.description}
            label={infoTitle}
            sx={{ mt: 1 }}
          />
        </Box>
      );
    }
    case "boolean":
      return (
        <FormControl error={Boolean(error)}>
          <Box sx={{ display: "flex", alignItems: "center", gap: 0.5 }}>
            <FormControlLabel
              sx={{ mr: 0 }}
              control={
                <Switch
                  id={id}
                  checked={value === true}
                  onChange={(e) => onChange(e.target.checked)}
                  inputProps={{ "data-arg": path, role: "switch" }}
                />
              }
              label={lbl}
            />
            <InfoPopover title={schema?.description} label={infoTitle} />
          </Box>
          {help && <FormHelperText sx={{ mt: -0.5 }}>{help}</FormHelperText>}
        </FormControl>
      );
    case "string":
      return (
        <Box sx={{ display: "flex", alignItems: "flex-start", gap: 0.5 }}>
          <TextField
            id={id}
            size="small"
            fullWidth
            multiline={isMultiline(schema)}
            minRows={
              schema.format === "code" ? 6 : isMultiline(schema) ? 3 : undefined
            }
            maxRows={isMultiline(schema) ? 24 : undefined}
            sx={
              schema.format === "code"
                ? { "& textarea": { fontFamily: "monospace", fontSize: 13 } }
                : undefined
            }
            label={lbl}
            value={value ?? ""}
            error={Boolean(error)}
            helperText={
              help ||
              (schema.pattern ? `Pattern: ${schema.pattern}` : undefined)
            }
            placeholder={schema.pattern ? schema.pattern : undefined}
            inputProps={{ "data-arg": path, maxLength: undefined }}
            onChange={(e) =>
              onChange(e.target.value === "" ? undefined : e.target.value)
            }
          />
          <InfoPopover
            title={schema?.description}
            label={infoTitle}
            sx={{ mt: 1 }}
          />
        </Box>
      );
    case "number":
      return (
        <NumberField
          id={id}
          lbl={lbl}
          infoTitle={infoTitle}
          schema={schema}
          value={value}
          onChange={onChange}
          error={error}
          help={help}
          path={path}
        />
      );
    case "array":
      return (
        <ArrayField
          name={name}
          root={root}
          schema={schema}
          required={required}
          value={value}
          onChange={onChange}
          errors={errors}
          path={path}
          idPrefix={idPrefix}
          lbl={lbl}
          infoTitle={infoTitle}
          help={help}
        />
      );
    case "object":
      if (isStringMap(root, schema))
        return (
          <StringMapField
            root={root}
            schema={schema}
            required={required}
            value={value}
            onChange={onChange}
            errors={errors}
            path={path}
            id={id}
            lbl={lbl}
            infoTitle={infoTitle}
            help={help}
          />
        );
      return (
        <Box
          role="group"
          aria-label={lbl}
          sx={{
            border: 1,
            borderColor: "divider",
            borderRadius: 1,
            px: 1.5,
            pt: 1,
            pb: 1.5,
          }}
        >
          <Box sx={{ display: "flex", alignItems: "center", gap: 0.5, mb: 1 }}>
            <Typography variant="body2" sx={{ fontWeight: 500 }}>
              {lbl}
            </Typography>
            <InfoPopover title={schema.description} label={infoTitle} />
          </Box>
          <ObjectFields
            root={root}
            schema={schema}
            value={value || {}}
            onChange={(v) =>
              onChange(Object.keys(v).length === 0 && !required ? undefined : v)
            }
            errors={errors}
            path={path}
            idPrefix={idPrefix}
          />
        </Box>
      );
    case "variant":
      return (
        <VariantField
          name={name}
          root={root}
          schema={schema}
          required={required}
          value={value}
          onChange={onChange}
          errors={errors}
          path={path}
          idPrefix={idPrefix}
          lbl={lbl}
          infoTitle={infoTitle}
          help={help}
        />
      );
    default:
      return null;
  }
}

// NumberField keeps its own text so "1." or "-" can be typed on the way
// to a number.
function NumberField({
  id,
  lbl,
  infoTitle,
  schema,
  value,
  onChange,
  error,
  help,
  path,
}) {
  const [text, setText] = useState(value === undefined ? "" : String(value));
  useEffect(() => {
    if (value === undefined ? text !== "" : Number(text) !== value)
      setText(value === undefined ? "" : String(value));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value]);
  const bounds = [
    schema.minimum !== undefined && `≥ ${schema.minimum}`,
    schema.exclusiveMinimum !== undefined && `> ${schema.exclusiveMinimum}`,
    schema.maximum !== undefined && `≤ ${schema.maximum}`,
    schema.exclusiveMaximum !== undefined && `< ${schema.exclusiveMaximum}`,
  ]
    .filter(Boolean)
    .join(", ");
  return (
    <Box sx={{ display: "flex", alignItems: "flex-start", gap: 0.5 }}>
      <TextField
        id={id}
        size="small"
        fullWidth
        type="number"
        label={lbl}
        value={text}
        error={Boolean(error)}
        helperText={
          [help, !error && bounds].filter(Boolean).join(" · ") || undefined
        }
        inputProps={{
          "data-arg": path,
          step: schema.type === "integer" ? 1 : "any",
          min: schema.minimum,
          max: schema.maximum,
        }}
        onChange={(e) => {
          setText(e.target.value);
          if (e.target.value === "") onChange(undefined);
          else if (!Number.isNaN(Number(e.target.value)))
            onChange(Number(e.target.value));
        }}
      />
      <InfoPopover
        title={schema?.description}
        label={infoTitle}
        sx={{ mt: 1 }}
      />
    </Box>
  );
}

function ArrayField({
  name,
  root,
  schema,
  value,
  onChange,
  errors,
  path,
  idPrefix,
  lbl,
  infoTitle,
  help,
}) {
  const items = Array.isArray(value) ? value : [];
  const itemSchema = deref(root, schema.items);
  const max = schema.maxItems;
  const constraints = [
    schema.minItems !== undefined && `at least ${schema.minItems}`,
    max !== undefined && `at most ${max}`,
    schema.uniqueItems && "no repeats",
  ]
    .filter(Boolean)
    .join(", ");
  const update = (next) => onChange(next);
  const helper =
    errors[path] || [help, constraints].filter(Boolean).join(" · ");
  return (
    <Box
      role="group"
      aria-label={lbl}
      data-arg-list={path}
      sx={{
        border: 1,
        borderColor: errors[path] ? "error.main" : "divider",
        borderRadius: 1,
        px: 1.5,
        pt: 1,
        pb: 1,
      }}
    >
      <Box sx={{ display: "flex", alignItems: "center", gap: 0.5, mb: 1 }}>
        <Typography variant="body2" sx={{ fontWeight: 500 }}>
          {lbl}
        </Typography>
        <InfoPopover title={schema?.description} label={infoTitle} />
      </Box>
      {items.map((item, i) => (
        <Box
          key={i}
          sx={{ display: "flex", gap: 1, alignItems: "flex-start", mb: 1 }}
        >
          <Box sx={{ flex: 1, minWidth: 0 }}>
            <Field
              name={
                typeof item?.name === "string" && item.name
                  ? `${name} ${i + 1}: ${item.name}`
                  : `${name} ${i + 1}`
              }
              root={root}
              schema={itemSchema}
              required
              value={item}
              onChange={(v) => {
                const next = items.slice();
                next[i] = v;
                update(next);
              }}
              errors={errors}
              path={`${path}[${i}]`}
              idPrefix={idPrefix}
            />
          </Box>
          <IconButton
            aria-label={`Remove ${name} ${i + 1}`}
            size="small"
            sx={{ mt: 0.5 }}
            onClick={() => {
              const next = items.filter((_, j) => j !== i);
              update(next.length === 0 && !schema.minItems ? undefined : next);
            }}
          >
            <CloseIcon fontSize="small" />
          </IconButton>
        </Box>
      ))}
      <Button
        size="small"
        disabled={max !== undefined && items.length >= max}
        onClick={() =>
          update([
            ...items,
            initialValue(root, itemSchema) ??
              (itemSchema?.type === "string" ? "" : undefined),
          ])
        }
      >
        Add {name}
      </Button>
      {helper && (
        <FormHelperText error={Boolean(errors[path])}>{helper}</FormHelperText>
      )}
    </Box>
  );
}

// StringMapField edits an object of free-form names to strings as one
// name/value row each. A row's name is kept as typed while it is edited,
// so a blank or repeated name stays on screen to be fixed rather than
// being dropped from the value.
function StringMapField({
  root,
  schema,
  required,
  value,
  onChange,
  errors,
  path,
  id,
  lbl,
  infoTitle,
  help,
}) {
  const toRows = (v) => Object.entries(v || {}).map(([k, x]) => [k, x]);
  const [rows, setRows] = useState(() => toRows(value));
  useEffect(() => {
    const current = Object.fromEntries(rows.filter(([k]) => k !== ""));
    if (JSON.stringify(current) !== JSON.stringify(value || {}))
      setRows(toRows(value));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value]);
  const valueSchema = deref(root, schema.additionalProperties);
  const update = (next) => {
    setRows(next);
    const obj = Object.fromEntries(next.filter(([k]) => k !== ""));
    onChange(Object.keys(obj).length === 0 && !required ? undefined : obj);
  };
  const helper = errors[path] || help;
  return (
    <Box
      role="group"
      aria-label={lbl}
      data-arg-map={path}
      sx={{
        border: 1,
        borderColor: errors[path] ? "error.main" : "divider",
        borderRadius: 1,
        px: 1.5,
        pt: 1,
        pb: 1,
      }}
    >
      <Box sx={{ display: "flex", alignItems: "center", gap: 0.5, mb: 1 }}>
        <Typography variant="body2" sx={{ fontWeight: 500 }}>
          {lbl}
        </Typography>
        <InfoPopover
          title={schema?.description || valueSchema?.description}
          label={infoTitle}
        />
      </Box>
      {rows.map(([k, v], i) => {
        const rowError = k ? errors[path ? `${path}.${k}` : k] : "";
        const dup = k !== "" && rows.findIndex(([n]) => n === k) !== i;
        return (
          <Box
            key={i}
            sx={{
              display: "flex",
              gap: 1,
              alignItems: "flex-start",
              mb: 1,
              flexWrap: { xs: "wrap", sm: "nowrap" },
            }}
          >
            <TextField
              id={`${id}-name-${i}`}
              size="small"
              label="Name"
              value={k}
              error={dup}
              helperText={dup ? "repeats an earlier name" : " "}
              sx={{ flex: { xs: "1 1 100%", sm: "0 0 35%" } }}
              inputProps={{ "data-arg-map-name": `${path}[${i}]` }}
              onChange={(e) => {
                const next = rows.slice();
                next[i] = [e.target.value, v];
                update(next);
              }}
            />
            <TextField
              id={`${id}-value-${i}`}
              size="small"
              label="Value"
              value={v ?? ""}
              error={Boolean(rowError)}
              helperText={rowError || " "}
              placeholder={valueSchema?.examples?.[0]}
              sx={{ flex: 1, minWidth: 0 }}
              inputProps={{ "data-arg-map-value": `${path}[${i}]` }}
              onChange={(e) => {
                const next = rows.slice();
                next[i] = [k, e.target.value];
                update(next);
              }}
            />
            <IconButton
              aria-label={`Remove ${k || `row ${i + 1}`}`}
              size="small"
              sx={{ mt: 0.5 }}
              onClick={() => update(rows.filter((_, j) => j !== i))}
            >
              <CloseIcon fontSize="small" />
            </IconButton>
          </Box>
        );
      })}
      <Button size="small" onClick={() => update([...rows, ["", ""]])}>
        Add entry
      </Button>
      {helper && (
        <FormHelperText error={Boolean(errors[path])}>{helper}</FormHelperText>
      )}
    </Box>
  );
}

function VariantField({
  name,
  root,
  schema,
  required,
  value,
  onChange,
  errors,
  path,
  idPrefix,
  lbl,
  infoTitle,
  help,
}) {
  const options = (schema.oneOf || schema.anyOf).map((s) => deref(root, s));
  const [picked, setPicked] = useState(() => {
    const i = options.findIndex(
      (o) => value !== undefined && fieldKind(o) && matchesShape(o, value),
    );
    return i < 0 ? 0 : i;
  });
  const sub = options[picked];
  return (
    <Box sx={{ display: "flex", flexDirection: "column", gap: 1 }}>
      <Box sx={{ display: "flex", alignItems: "flex-start", gap: 0.5 }}>
        <FormControl size="small" fullWidth>
          <InputLabel id={`${idPrefix}-${path}-variant-label`}>
            {lbl}
          </InputLabel>
          <Select
            labelId={`${idPrefix}-${path}-variant-label`}
            label={lbl}
            value={String(picked)}
            inputProps={{ "data-arg-variant": path }}
            onChange={(e) => {
              const i = Number(e.target.value);
              setPicked(i);
              onChange(initialValue(root, options[i]));
            }}
          >
            {options.map((o, i) => (
              <MenuItem key={i} value={String(i)}>
                {o?.title ||
                  o?.description ||
                  `${o?.type || "option"} ${i + 1}`}
              </MenuItem>
            ))}
          </Select>
          {help && <FormHelperText>{help}</FormHelperText>}
        </FormControl>
        <InfoPopover
          title={schema?.description}
          label={infoTitle}
          sx={{ mt: 1 }}
        />
      </Box>
      <Field
        name={sub?.title || name}
        root={root}
        schema={sub}
        required={required}
        value={value}
        onChange={onChange}
        errors={errors}
        path={path}
        idPrefix={idPrefix}
      />
    </Box>
  );
}

function matchesShape(schema, value) {
  if ("const" in schema)
    return JSON.stringify(schema.const) === JSON.stringify(value);
  if (schema.enum)
    return schema.enum.some((e) => JSON.stringify(e) === JSON.stringify(value));
  switch (schema.type) {
    case "array":
      return Array.isArray(value);
    case "object":
      return value && typeof value === "object" && !Array.isArray(value);
    case "integer":
    case "number":
      return typeof value === "number";
    default:
      return typeof value === schema.type;
  }
}
