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
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControlLabel,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import api from "../api.js";
import { UsageList } from "./CapabilityServersPanel.jsx";
import {
  formatScopeSetCatalogue,
  hasRunRestrictions,
} from "./catalogueSummary.js";

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
        {set.catalogue?.disabled && (
          <Chip size="small" color="error" label="disabled" />
        )}
        {!set.catalogue?.disabled &&
          hasRunRestrictions(set.catalogue?.restrictions) && (
            <Chip
              size="small"
              color="warning"
              variant="outlined"
              label="restricted"
            />
          )}
        {(set.catalogue?.legacyInProcess || []).map((legacy) => (
          <Chip
            key={legacy}
            size="small"
            color="warning"
            variant="outlined"
            label={`in_process.${legacy}`}
          />
        ))}
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
      {set.catalogue && (
        <Typography
          variant="caption"
          color="text.secondary"
          data-testid={`api-scope-set-catalogue-${set.name}`}
          sx={{ display: "block", mt: 0.5, overflowWrap: "anywhere" }}
        >
          {formatScopeSetCatalogue(set.catalogue)}
        </Typography>
      )}
    </Box>
  );
}

export default function ApiScopeSetsPanel({ showError, readOnly }) {
  const [data, setData] = useState(null);
  const [form, setForm] = useState(null);
  const [busy, setBusy] = useState(false);
  // confirm is the removal waiting on its usage warning: the set, and
  // who holds it (GET /api/api-scope-sets/usage), null when that could
  // not be read.
  const [confirm, setConfirm] = useState(null);

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

  // askRemove counts the set's grants before anything is removed: every
  // one of them grants nothing afterwards.
  const askRemove = async (set) => {
    setBusy(true);
    let holders = null;
    try {
      const usage = await api("/api/api-scope-sets/usage");
      const own = (Array.isArray(usage) ? usage : []).find(
        (u) => u.name === set.name,
      );
      holders = own?.holders || [];
    } catch {
      // The count is the warning's detail; removing works without it.
    } finally {
      setBusy(false);
    }
    setConfirm({ set, holders });
  };

  const remove = async (set) => {
    setConfirm(null);
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
          onDelete={askRemove}
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
      <Dialog
        open={Boolean(confirm)}
        onClose={() => setConfirm(null)}
        maxWidth="sm"
        fullWidth
      >
        {confirm && (
          <RemoveConfirmation
            confirm={confirm}
            onCancel={() => setConfirm(null)}
            onRemove={() => remove(confirm.set)}
          />
        )}
      </Dialog>
    </Box>
  );
}

function RemoveConfirmation({ confirm, onCancel, onRemove }) {
  const { set, holders } = confirm;
  const grant = `api:${set.name}`;
  let summary;
  if (holders === null) {
    summary = `Grain could not count what grants ${grant}. Every grant of it will grant nothing once it is removed.`;
  } else if (holders.length === 0) {
    summary = `Nothing grants ${grant}.`;
  } else {
    const n = holders.length;
    summary = `${n} ${n === 1 ? "grant holds" : "grants hold"} ${grant}, and will grant nothing once it is removed:`;
  }
  return (
    <>
      <DialogTitle sx={{ overflowWrap: "anywhere" }}>
        Remove scope set {set.name}?
      </DialogTitle>
      <DialogContent data-testid="api-scope-set-remove-dialog">
        <Typography variant="body2" sx={{ mb: 1, overflowWrap: "anywhere" }}>
          {summary}
        </Typography>
        {holders?.length > 0 && <UsageList holders={holders} />}
      </DialogContent>
      <DialogActions>
        <Button onClick={onCancel}>Cancel</Button>
        <Button color="error" variant="contained" onClick={onRemove}>
          Remove
        </Button>
      </DialogActions>
    </>
  );
}
