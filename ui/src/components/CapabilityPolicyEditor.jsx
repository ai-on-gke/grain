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
  FormControlLabel,
  FormGroup,
  Link,
  MenuItem,
  Paper,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import api from "../api.js";
import { invalidateCapabilityMethods } from "./capabilityMethods.js";
import { UsageList } from "./CapabilityServersPanel.jsx";
import { summariseCatalogueEntries } from "./catalogueSummary.js";

// CapabilityPolicyEditor is grain_config.capability_policy
// (model.CapabilityPolicy, docs/capabilities-spec.md §4.2.2): rules that
// say which runs may hold a <capability_id>#<method>, or a pattern of
// one. A reference no allow rule names may be held by any trusted run;
// one that some allow rule names only by a run one of them admits; a
// deny rule refuses it to every run it covers, ahead of any allow rule.
// An untrusted run (a pull request from a fork or an author without
// write access) holds none unless an allow rule opts it in.
//
// Saving first previews the change (POST /api/capability-policy/preview)
// and lists every existing grant it would newly refuse.

const RUN_KINDS = [
  ["task", "Agent tasks"],
  ["process", "Processes"],
  ["ci", "CI jobs"],
];

const listText = (list) => (list || []).join(", ");
const textList = (text) =>
  text
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);

function toRule(r) {
  const rule = { capability: r.capability.trim() };
  if (r.repos.trim()) rule.repos = textList(r.repos);
  if (r.runKinds.length) rule.runKinds = r.runKinds;
  if (r.events.trim()) rule.events = textList(r.events);
  if (r.deny) rule.deny = true;
  else if (r.allowUntrusted) rule.allowUntrusted = true;
  return rule;
}

function fromRule(rule) {
  return {
    capability: rule.capability || "",
    repos: listText(rule.repos),
    runKinds: rule.runKinds || [],
    events: listText(rule.events),
    allowUntrusted: Boolean(rule.allowUntrusted),
    deny: Boolean(rule.deny),
  };
}

const emptyRule = () => fromRule({});

function RuleRow({ rule, index, onChange, onRemove, readOnly }) {
  const set = (patch) => onChange({ ...rule, ...patch });
  const toggleKind = (kind) =>
    set({
      runKinds: rule.runKinds.includes(kind)
        ? rule.runKinds.filter((k) => k !== kind)
        : [...rule.runKinds, kind],
    });
  return (
    <Paper
      variant="outlined"
      sx={{ p: 1.5, display: "grid", gap: 1, minWidth: 0 }}
      data-policy-rule={index}
    >
      <Box
        sx={{
          display: "grid",
          gap: 1,
          gridTemplateColumns: { xs: "1fr", sm: "8rem 1fr" },
        }}
      >
        <TextField
          select
          size="small"
          label="Effect"
          value={rule.deny ? "deny" : "allow"}
          onChange={(e) => set({ deny: e.target.value === "deny" })}
          disabled={readOnly}
          id={`policy-rule-${index}-effect`}
        >
          <MenuItem value="allow">Allow only</MenuItem>
          <MenuItem value="deny">Deny</MenuItem>
        </TextField>
        <TextField
          size="small"
          required
          label="Capability"
          placeholder="https://github.com/acme/caps/cloud#*"
          helperText="<capability_id>#<method>; * matches any run of characters"
          value={rule.capability}
          onChange={(e) => set({ capability: e.target.value })}
          disabled={readOnly}
          inputProps={{ spellCheck: false }}
        />
      </Box>
      <FormGroup row aria-label="Run kinds">
        {RUN_KINDS.map(([kind, lbl]) => (
          <FormControlLabel
            key={kind}
            control={
              <Checkbox
                size="small"
                checked={rule.runKinds.includes(kind)}
                onChange={() => toggleKind(kind)}
                disabled={readOnly}
              />
            }
            label={lbl}
          />
        ))}
        {!rule.deny && (
          <FormControlLabel
            control={
              <Checkbox
                size="small"
                checked={rule.allowUntrusted}
                onChange={(e) => set({ allowUntrusted: e.target.checked })}
                disabled={readOnly}
              />
            }
            label="Untrusted CI (forks)"
          />
        )}
      </FormGroup>
      <Typography variant="caption" color="text.secondary">
        {rule.runKinds.length
          ? ""
          : "No run kind ticked: the rule covers every kind. "}
        Scheduled CI runs are the event <code>schedule</code>.
      </Typography>
      <Box
        sx={{
          display: "grid",
          gap: 1,
          gridTemplateColumns: { xs: "1fr", sm: "1fr 1fr auto" },
          alignItems: "start",
        }}
      >
        <TextField
          size="small"
          label="Events (CI)"
          placeholder="push, pull_request, schedule"
          helperText="empty covers every event"
          value={rule.events}
          onChange={(e) => set({ events: e.target.value })}
          disabled={readOnly}
        />
        <TextField
          size="small"
          label="Repositories"
          placeholder="acme/*"
          helperText="empty covers every repository"
          value={rule.repos}
          onChange={(e) => set({ repos: e.target.value })}
          disabled={readOnly}
        />
        <Button
          color="error"
          size="small"
          onClick={onRemove}
          disabled={readOnly}
          sx={{ mt: 0.5 }}
        >
          Remove rule
        </Button>
      </Box>
    </Paper>
  );
}

export default function CapabilityPolicyEditor({ showError, readOnly }) {
  const [rules, setRules] = useState(null);
  const [catalogue, setCatalogue] = useState(null);
  const [dirty, setDirty] = useState(false);
  // preview is the denials a save is waiting on a confirmation for.
  const [preview, setPreview] = useState(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const data = await api("/api/capability-policy");
      setRules((data?.policy?.rules || []).map(fromRule));
      setCatalogue(data?.policy?.catalogue || null);
      setDirty(false);
      setPreview(null);
    } catch (err) {
      showError(err);
    }
  }, [showError]);

  useEffect(() => {
    load();
  }, [load]);

  if (!rules) return null;
  const catalogueEntries = summariseCatalogueEntries(catalogue);

  const update = (next) => {
    setRules(next);
    setDirty(true);
    setPreview(null);
  };
  const policy = () => ({ rules: rules.map(toRule) });

  const save = async () => {
    setBusy(true);
    try {
      const data = await api("/api/capability-policy", {
        method: "PUT",
        body: JSON.stringify({ policy: policy() }),
      });
      invalidateCapabilityMethods();
      setRules((data?.policy?.rules || []).map(fromRule));
      setCatalogue(data?.policy?.catalogue || null);
      setDirty(false);
      setPreview(null);
    } catch (err) {
      showError(err);
    } finally {
      setBusy(false);
    }
  };

  const check = async () => {
    setBusy(true);
    try {
      const data = await api("/api/capability-policy/preview", {
        method: "POST",
        body: JSON.stringify({ policy: policy() }),
      });
      const denied = data?.denied || [];
      setBusy(false);
      if (denied.length === 0) await save();
      else setPreview(denied);
    } catch (err) {
      setBusy(false);
      showError(err);
    }
  };

  return (
    <Box data-testid="capability-policy">
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        Which runs may hold each capability server method. A method no allow
        rule names may be held by any trusted run; once an allow rule names it,
        only the runs some allow rule admits may. A deny rule refuses it to
        every run it covers. Runs triggered by untrusted input hold none unless
        an allow rule opts them in.
      </Typography>
      <Stack spacing={1.5}>
        {rules.length === 0 && (
          <Typography variant="body2">
            {catalogueEntries.length > 0
              ? "No method rules; catalogue restrictions from settings/capabilities.yml apply below."
              : "No rules: every trusted run may."}
          </Typography>
        )}
        {rules.map((rule, i) => (
          <RuleRow
            key={i}
            index={i}
            rule={rule}
            readOnly={readOnly}
            onChange={(r) => update(rules.map((x, j) => (j === i ? r : x)))}
            onRemove={() => update(rules.filter((_, j) => j !== i))}
          />
        ))}
      </Stack>
      {catalogueEntries.length > 0 && (
        <Alert
          severity={
            catalogueEntries.some((e) => e.retired) ? "warning" : "info"
          }
          sx={{ mt: 2 }}
          data-testid="policy-catalogue-entries"
        >
          <Typography variant="body2" sx={{ mb: 0.5 }}>
            Catalogue restrictions in <code>settings/capabilities.yml</code>
            {catalogueEntries.some((e) => e.scopeSet) && (
              <>
                {" "}
                (see{" "}
                <Link href="/settings/capabilities#api-scope-sets">
                  API scope sets
                </Link>
                )
              </>
            )}
            :
          </Typography>
          <Box component="ul" sx={{ m: 0, pl: 2.5 }}>
            {catalogueEntries.map((item) => (
              <Typography
                key={item.key}
                component="li"
                variant="caption"
                sx={{ overflowWrap: "anywhere" }}
              >
                <code>{item.target}</code> — {item.details.join(" · ")} (
                {item.source})
              </Typography>
            ))}
          </Box>
        </Alert>
      )}
      {preview && (
        <Alert severity="warning" sx={{ mt: 2 }} data-testid="policy-preview">
          <Typography variant="body2" sx={{ mb: 1 }}>
            This change would refuse {preview.length} existing grant
            {preview.length === 1 ? "" : "s"}:
          </Typography>
          <UsageList holders={preview} />
          {preview.map((d) => (
            <Typography
              key={`${d.kind}:${d.id}:${d.grant}`}
              variant="caption"
              sx={{ display: "block", overflowWrap: "anywhere" }}
            >
              {d.reason}
            </Typography>
          ))}
        </Alert>
      )}
      <Stack
        direction="row"
        spacing={1}
        sx={{ mt: 2, flexWrap: "wrap", rowGap: 1 }}
      >
        <Button
          variant="outlined"
          onClick={() => update([...rules, emptyRule()])}
          disabled={readOnly}
        >
          Add rule
        </Button>
        {preview ? (
          <Button
            variant="contained"
            color="warning"
            onClick={save}
            disabled={readOnly || busy}
          >
            Save anyway
          </Button>
        ) : (
          <Button
            variant="contained"
            onClick={check}
            disabled={readOnly || busy || !dirty}
          >
            Save policy
          </Button>
        )}
        {dirty && (
          <Button onClick={load} disabled={busy}>
            Discard changes
          </Button>
        )}
      </Stack>
    </Box>
  );
}
