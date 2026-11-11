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
  Checkbox,
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
import SearchSelect, { SHORT_LIST } from "./SearchSelect.jsx";
import {
  deref,
  fieldKind,
  initialValue,
  isHiddenProperty,
  isFixedList,
  isMultiline,
  isSecretLooking,
  isStringMap,
  pickVariant,
  unsupportedReason,
  validateValue,
  variantTitle,
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
//
// Arguments whose schema is a oneOf/anyOf of objects (repo#write's
// {repo}, {repo, base} or {refs, force}) are a variant picker and then
// the picked option's fields. Any args form can be switched to the JSON
// editor with "Edit as JSON"; one whose schema the form cannot draw is
// only ever that.
export default function CapabilityArgsForm({
  schema,
  value,
  onChange,
  errors = {},
  idPrefix = "cap-arg",
  secrets = null,
}) {
  const why = unsupportedReason(schema);
  const [asJSON, setAsJSON] = useState(false);
  const top = deref(schema, schema);
  // A server's configuration keeps its secrets out of value, so it is
  // never offered as JSON text that could not show or set them.
  const canToggle = !why && !secrets;
  return (
    <Box sx={{ display: "flex", flexDirection: "column", gap: 1.5 }}>
      <Box sx={{ display: "flex", alignItems: "flex-start", gap: 1 }}>
        <Typography variant="caption" color="text.secondary" sx={{ flex: 1 }}>
          {secrets
            ? "Configuration is exported to grain/config as plain text, except write-only fields: what is typed into one goes only to the encrypted secrets store."
            : "Arguments are recorded on the task in plain text. Never put a secret in one; a server's credentials belong in Settings → Capability servers."}
        </Typography>
        {canToggle && (
          <Button
            size="small"
            sx={{ flexShrink: 0, py: 0 }}
            data-testid={`${idPrefix}-json-toggle`}
            onClick={() => setAsJSON(!asJSON)}
          >
            {asJSON ? "Edit as form" : "Edit as JSON"}
          </Button>
        )}
      </Box>
      {why || asJSON ? (
        <JSONArgs
          value={value}
          onChange={onChange}
          errors={errors}
          reason={why}
          idPrefix={idPrefix}
        />
      ) : fieldKind(top) === "variant" ? (
        <TopLevelVariant
          root={schema}
          schema={top}
          value={value || {}}
          onChange={onChange}
          errors={errors}
          idPrefix={idPrefix}
          secrets={secrets}
        />
      ) : (
        <ObjectFields
          root={schema}
          schema={top}
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

// TopLevelVariant is arguments that are one of several shapes: a picker
// labelled with each option's title, then the picked option's fields.
// Picking another option starts its own initial value. The picked
// option's own field errors are shown against its fields; the
// arguments-wide error ("matches none of the options -- ...") stays
// under the form.
function TopLevelVariant({
  root,
  schema,
  value,
  onChange,
  errors,
  idPrefix,
  secrets,
}) {
  const options = (schema.oneOf || schema.anyOf).map((s) => deref(root, s));
  const [picked, setPicked] = useState(() => pickVariant(root, options, value));
  const sub = options[picked];
  const { "": _whole, ...own } = validateValue(root, sub, value);
  const shown = { ...own, ...errors };
  const lbl = schema.title || "Kind of arguments";
  return (
    <Box sx={{ display: "flex", flexDirection: "column", gap: 1.5 }}>
      <Box sx={{ display: "flex", alignItems: "flex-start", gap: 0.5 }}>
        <FormControl size="small" fullWidth>
          <InputLabel id={`${idPrefix}--variant-label`}>{lbl}</InputLabel>
          <Select
            labelId={`${idPrefix}--variant-label`}
            label={lbl}
            value={String(picked)}
            inputProps={{ "data-arg-variant": "" }}
            onChange={(e) => {
              const i = Number(e.target.value);
              setPicked(i);
              onChange(initialValue(root, options[i], false, true) ?? {});
            }}
          >
            {options.map((o, i) => (
              <MenuItem key={i} value={String(i)}>
                {variantTitle(o, i)}
              </MenuItem>
            ))}
          </Select>
          {sub?.title && sub?.description && (
            <FormHelperText>{sub.description}</FormHelperText>
          )}
        </FormControl>
        <InfoPopover title={schema.description} label={lbl} sx={{ mt: 1 }} />
      </Box>
      {fieldKind(sub) === "object" ? (
        <ObjectFields
          key={picked}
          root={root}
          schema={sub}
          value={value}
          onChange={onChange}
          errors={shown}
          path=""
          idPrefix={idPrefix}
          secrets={secrets}
        />
      ) : (
        <Field
          key={picked}
          name={variantTitle(sub, picked)}
          root={root}
          schema={sub}
          required
          value={value}
          onChange={onChange}
          errors={shown}
          path=""
          idPrefix={idPrefix}
        />
      )}
    </Box>
  );
}

// JSONArgs is the JSON editor: the with: object as JSON text, parsed
// here only so far as to be an object -- the schema check is the
// server's. reason is why the form cannot draw the schema, or "" when
// the person chose to edit it as JSON.
function JSONArgs({ value, onChange, errors, reason, idPrefix }) {
  const [text, setText] = useState(() => JSON.stringify(value || {}, null, 2));
  const [parseError, setParseError] = useState("");
  const fieldErrors = Object.entries(errors).filter(([k]) => k !== "");
  return (
    <Box>
      {reason && (
        <Alert severity="info" sx={{ mb: 1, py: 0 }}>
          This method&apos;s arguments use a schema the form cannot draw (
          {reason}), so they are edited as JSON and checked by the server.
        </Alert>
      )}
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
      if (typeof schema.const === "boolean")
        return (
          <FixedBoolean
            id={id}
            lbl={lbl}
            infoTitle={infoTitle}
            schema={schema}
            value={value}
            onChange={onChange}
            error={error}
            path={path}
          />
        );
      const options = "enum" in schema ? schema.enum : [schema.const];
      const isTopLevel = !path.includes(".") && !path.includes("[");
      const effective =
        value === undefined && isTopLevel && "default" in schema
          ? schema.default
          : value;
      const index = options.findIndex(
        (o) => JSON.stringify(o) === JSON.stringify(effective),
      );
      const enumLabels = schema?.["x-grain-enum-labels"] || {};
      const optionText = (o) =>
        typeof o === "string" && typeof enumLabels[o] === "string"
          ? enumLabels[o]
          : typeof o === "string"
            ? o
            : JSON.stringify(o);
      const pick = (v) => onChange(v === "" ? undefined : options[Number(v)]);
      // A schema's enum can be long (every region, every role): past
      // SHORT_LIST it is searched rather than scrolled. A short one stays
      // a plain select, quicker to open.
      if (options.length > SHORT_LIST) {
        return (
          <Box sx={{ display: "flex", alignItems: "flex-start", gap: 0.5 }}>
            <SearchSelect
              id={id}
              label={label(name, schema, false)}
              required={required}
              error={Boolean(error)}
              value={index < 0 ? "" : String(index)}
              onChange={pick}
              helperText={help || undefined}
              inputProps={{ "data-arg": path }}
              options={[
                ...(required
                  ? []
                  : [{ value: "", label: "Not set", row: <em>Not set</em> }]),
                ...options.map((o, i) => ({
                  value: String(i),
                  label: optionText(o),
                })),
              ]}
            />
            <InfoPopover
              title={schema?.description}
              label={infoTitle}
              sx={{ mt: 1 }}
            />
          </Box>
        );
      }
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
              onChange={(e) => pick(e.target.value)}
              inputProps={{ "data-arg": path }}
            >
              {!required && (
                <MenuItem value="">
                  <em>Not set</em>
                </MenuItem>
              )}
              {options.map((o, i) => (
                <MenuItem key={i} value={String(i)}>
                  {optionText(o)}
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
                  onChange={(e) =>
                    // Leave an unset optional boolean out of with: rather than
                    // recording false; keep false when required or overriding a
                    // default: true that the server would otherwise put back.
                    onChange(
                      e.target.checked
                        ? true
                        : required || schema?.default === true
                          ? false
                          : undefined,
                    )
                  }
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
      if (required && isFixedList(root, schema))
        return (
          <FixedListField
            root={root}
            schema={schema}
            value={value}
            onChange={onChange}
            error={error}
            path={path}
            lbl={lbl}
            infoTitle={infoTitle}
          />
        );
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
            borderColor: error ? "error.main" : "divider",
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
          {help && (
            <FormHelperText error={Boolean(error)} sx={{ mt: 0.5 }}>
              {help}
            </FormHelperText>
          )}
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

// FixedBoolean is a boolean with one allowed value (repo#write's force:
// true): a box already ticked that cannot be unticked. Should the value
// be missing, ticking it sets the one value.
function FixedBoolean({
  id,
  lbl,
  infoTitle,
  schema,
  value,
  onChange,
  error,
  path,
}) {
  const set = value === schema.const;
  return (
    <FormControl error={Boolean(error)}>
      <Box sx={{ display: "flex", alignItems: "center", gap: 0.5 }}>
        <FormControlLabel
          sx={{ mr: 0 }}
          control={
            <Checkbox
              id={id}
              size="small"
              checked={set ? schema.const : !schema.const}
              disabled={set}
              onChange={() => onChange(schema.const)}
              inputProps={{ "data-arg": path, "data-arg-fixed": "true" }}
            />
          }
          label={lbl}
        />
        <InfoPopover title={schema?.description} label={infoTitle} />
      </Box>
      <FormHelperText sx={{ mt: -0.5 }}>
        {error || "Fixed: this option only accepts this value."}
      </FormHelperText>
    </FormControl>
  );
}

// FixedListField is a required array with only one possible set of
// items (isFixedList): each of them as a chip, nothing to add or remove.
function FixedListField({
  root,
  schema,
  value,
  onChange,
  error,
  path,
  lbl,
  infoTitle,
}) {
  const items = deref(root, schema.items).enum;
  const complete =
    Array.isArray(value) &&
    value.length === items.length &&
    items.every((i) =>
      value.some((v) => JSON.stringify(v) === JSON.stringify(i)),
    );
  return (
    <Box
      role="group"
      aria-label={lbl}
      data-arg-fixed-list={path}
      sx={{
        border: 1,
        borderColor: error ? "error.main" : "divider",
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
      <Box sx={{ display: "flex", flexWrap: "wrap", gap: 1 }}>
        {items.map((item) => (
          <Chip
            key={JSON.stringify(item)}
            size="small"
            variant={complete ? "filled" : "outlined"}
            label={typeof item === "string" ? item : JSON.stringify(item)}
            sx={{ fontFamily: "monospace" }}
          />
        ))}
        {!complete && (
          <Button size="small" onClick={() => onChange([...items])}>
            Use these
          </Button>
        )}
      </Box>
      <FormHelperText error={Boolean(error)}>
        {error || "Fixed: this option only accepts all of these together."}
      </FormHelperText>
    </Box>
  );
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
              update(next.length === 0 && !required ? undefined : next);
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
            initialValue(root, itemSchema, false, true) ??
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
  const [picked, setPicked] = useState(() => pickVariant(root, options, value));
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
              onChange(initialValue(root, options[i], false, required));
            }}
          >
            {options.map((o, i) => (
              <MenuItem key={i} value={String(i)}>
                {variantTitle(o, i)}
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
