import { useCallback, useEffect, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Checkbox,
  Chip,
  FormControlLabel,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import api from "../api.js";

// ApiScopeSetsPanel is Settings → Capabilities → API scope sets
// (docs/agent-api.md, "Scope sets"): every named set of agent API scopes
// in effect -- grain's own built-ins (grain/...) and the ones this
// deployment declares in its config repository
// (tables/api_scope_set.json) -- with the scopes each resolves to and
// whether only a person may grant it. A set is a definition, not a
// grant: it is granted as api:<name>, through the ordinary capability
// pickers.

const listText = (list) => (list || []).join(", ");
const textList = (text) =>
  text
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);

const EMPTY = {
  name: "",
  description: "",
  scopes: "",
  includes: "",
  repos: "",
  deny: "",
  requiresPerson: false,
  deprecated: false,
  version: 1,
};

function scopeLabel(set, scope) {
  const limit = set.repoLimits?.[scope];
  return limit ? `${scope} @ ${limit.join(", ")}` : scope;
}

export function ScopeSetRow({ set, readOnly, onEdit, onDelete }) {
  return (
    <Box
      data-testid={`api-scope-set-${set.name}`}
      sx={{ py: 1.5, borderBottom: 1, borderColor: "divider" }}
    >
      <Stack
        direction="row"
        spacing={1}
        useFlexGap
        sx={{ alignItems: "center", flexWrap: "wrap" }}
      >
        <Typography variant="subtitle2" component="span">
          {set.name}
        </Typography>
        {set.builtin ? (
          <Chip size="small" label="built-in" />
        ) : (
          <Chip
            size="small"
            variant="outlined"
            label={`config repo · ${set.path}`}
          />
        )}
        {set.person && (
          <Chip size="small" color="warning" label="requires a person" />
        )}
        {set.deprecated && <Chip size="small" label="deprecated" />}
        {!set.builtin && !readOnly && (
          <Box sx={{ ml: "auto", display: "flex", gap: 1 }}>
            <Button size="small" onClick={() => onEdit(set)}>
              Edit
            </Button>
            <Button size="small" color="error" onClick={() => onDelete(set)}>
              Remove
            </Button>
          </Box>
        )}
      </Stack>
      {set.description && (
        <Typography variant="body2" color="text.secondary">
          {set.description}
        </Typography>
      )}
      <Box sx={{ mt: 0.5, display: "flex", flexWrap: "wrap", gap: 0.5 }}>
        {(set.resolved || []).map((scope) => (
          <Chip
            key={scope}
            size="small"
            variant="outlined"
            label={scopeLabel(set, scope)}
          />
        ))}
      </Box>
      {!set.builtin && (set.includes?.length > 0 || set.deny?.length > 0) && (
        <Typography variant="caption" color="text.secondary">
          {set.includes?.length > 0 && `includes ${listText(set.includes)}`}
          {set.includes?.length > 0 && set.deny?.length > 0 && " · "}
          {set.deny?.length > 0 && `denies ${listText(set.deny)}`}
        </Typography>
      )}
    </Box>
  );
}

export default function ApiScopeSetsPanel({ showError, readOnly }) {
  const [data, setData] = useState(null);
  const [form, setForm] = useState(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      setData(await api("/api/api-scope-sets"));
    } catch (err) {
      showError?.(err);
    }
  }, [showError]);

  useEffect(() => {
    load();
  }, [load]);

  const edit = (set) =>
    setForm(
      set
        ? {
            name: set.name,
            description: set.description || "",
            scopes: listText(set.scopes),
            includes: listText(set.includes),
            repos: listText(set.repos),
            deny: listText(set.deny),
            requiresPerson: Boolean(set.requiresPerson),
            deprecated: Boolean(set.deprecated),
            version: set.version || 0,
            existing: true,
          }
        : { ...EMPTY },
    );

  const save = async () => {
    setBusy(true);
    try {
      const got = await api("/api/api-scope-sets", {
        method: "PUT",
        body: JSON.stringify({
          set: {
            name: form.name.trim(),
            description: form.description,
            scopes: textList(form.scopes),
            includes: textList(form.includes),
            repos: textList(form.repos),
            deny: textList(form.deny),
            requiresPerson: form.requiresPerson,
            deprecated: form.deprecated,
            version: Number(form.version) || 0,
          },
        }),
      });
      setData(got);
      setForm(null);
    } catch (err) {
      showError?.(err);
    } finally {
      setBusy(false);
    }
  };

  const remove = async (set) => {
    setBusy(true);
    try {
      setData(
        await api("/api/api-scope-sets/delete", {
          method: "POST",
          body: JSON.stringify({ name: set.name }),
        }),
      );
    } catch (err) {
      showError?.(err);
    } finally {
      setBusy(false);
    }
  };

  const field = (key, label, helperText) => (
    <TextField
      size="small"
      label={label}
      value={form[key]}
      helperText={helperText}
      onChange={(e) => setForm({ ...form, [key]: e.target.value })}
      fullWidth
    />
  );

  return (
    <Box>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
        Named sets of agent API scopes, granted as <code>api:&lt;name&gt;</code>{" "}
        through any capability picker. Built-ins ship with grain; this
        deployment&apos;s own live in the config repository at{" "}
        <code>{data?.path || "tables/api_scope_set.json"}</code>, where an agent
        can propose one by pull request. Declaring a set grants it to nobody.
      </Typography>
      {data?.error && (
        <Alert severity="error" sx={{ mb: 1 }}>
          The config repository&apos;s scope sets do not resolve, so the
          previous ones stay in effect: {data.error}
        </Alert>
      )}
      {(data?.sets || []).map((set) => (
        <ScopeSetRow
          key={set.name}
          set={set}
          readOnly={readOnly || busy}
          onEdit={edit}
          onDelete={remove}
        />
      ))}
      {!readOnly && !form && (
        <Button sx={{ mt: 1 }} onClick={() => edit(null)}>
          New scope set
        </Button>
      )}
      {form && (
        <Stack spacing={1.5} sx={{ mt: 2 }} data-testid="api-scope-set-form">
          {form.existing ? (
            <Typography variant="subtitle2">{form.name}</Typography>
          ) : (
            field("name", "Name", "e.g. ops/triage; grain/ is reserved")
          )}
          {field("description", "Description")}
          {field(
            "scopes",
            "Scopes",
            "comma-separated; tasks:* is every tasks scope",
          )}
          {field("includes", "Includes", "other sets, e.g. grain/ci-read")}
          {field(
            "repos",
            "Repositories",
            "owner/name, comma-separated; empty is every one",
          )}
          {field("deny", "Deny", "scopes removed after composition")}
          <Stack direction="row" spacing={2} sx={{ alignItems: "center" }}>
            <FormControlLabel
              control={
                <Checkbox
                  checked={form.requiresPerson}
                  onChange={(e) =>
                    setForm({ ...form, requiresPerson: e.target.checked })
                  }
                />
              }
              label="Requires a person (forced on by a dangerous scope)"
            />
            <FormControlLabel
              control={
                <Checkbox
                  checked={form.deprecated}
                  onChange={(e) =>
                    setForm({ ...form, deprecated: e.target.checked })
                  }
                />
              }
              label="Deprecated"
            />
          </Stack>
          <Stack direction="row" spacing={1}>
            <Button variant="contained" disabled={busy} onClick={save}>
              Save
            </Button>
            <Button onClick={() => setForm(null)}>Cancel</Button>
          </Stack>
        </Stack>
      )}
    </Box>
  );
}
