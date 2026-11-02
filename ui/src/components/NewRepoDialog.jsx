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
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControlLabel,
  Link,
  MenuItem,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import api from "../api.js";
import { identifierField } from "../identifierField.js";

const STEP_LABELS = {
  create: "Create",
  "default-branch": "Default branch",
  access: "App access",
  connect: "Connect",
  "repo-config": "Repo settings",
};

const STATUS_SEVERITY = { done: "success", skipped: "info", failed: "error" };

// NewRepoDialog is Settings → Repos' "New repository": POST
// /api/repos/create, which makes the repository on the forge and
// connects it in one step. The host picker offers only the hosts a
// creator is registered for (GET /api/repo-hosts). The answer is shown
// as the steps it took, each with the manual fix -- a gh command, a
// settings link -- for one it could not do, because the usual failure
// is a permission only the operator can grant.
export default function NewRepoDialog({ open, onClose, onCreated }) {
  const [hosts, setHosts] = useState(["github.com"]);
  const [host, setHost] = useState("github.com");
  const [owner, setOwner] = useState("");
  const [name, setName] = useState("");
  const [visibility, setVisibility] = useState("private");
  const [description, setDescription] = useState("");
  const [init, setInit] = useState(true);
  const [template, setTemplate] = useState("");
  const [ciMode, setCIMode] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState(null);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!open) return undefined;
    let live = true;
    Promise.resolve()
      .then(() => api("/api/repo-hosts"))
      .then((res) => {
        const list = Array.isArray(res?.hosts) ? res.hosts : [];
        if (live && list.length > 0) {
          setHosts(list);
          setHost((h) => (list.includes(h) ? h : list[0]));
        }
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [open]);

  const reset = () => {
    setResult(null);
    setError("");
  };

  const close = () => {
    reset();
    onClose();
  };

  const submit = async (evt) => {
    evt.preventDefault();
    reset();
    setBusy(true);
    try {
      const res = await api("/api/repos/create", {
        method: "POST",
        body: JSON.stringify({
          host,
          owner: owner.trim(),
          name: name.trim(),
          visibility,
          description: description.trim(),
          init,
          ...(template.trim() ? { template: template.trim() } : {}),
          ...(ciMode ? { ci_mode: ciMode } : {}),
        }),
      });
      setResult(res);
      if (res?.connected && onCreated) await onCreated(res);
    } catch (err) {
      setError(err?.message || String(err));
    } finally {
      setBusy(false);
    }
  };

  const canSubmit = owner.trim() !== "" && name.trim() !== "" && !busy;

  return (
    <Dialog open={open} onClose={close} fullWidth maxWidth="sm">
      <Box component="form" onSubmit={submit} aria-label="New repository">
        <DialogTitle>New repository</DialogTitle>
        <DialogContent>
          <Stack spacing={2} sx={{ pt: 1 }}>
            <TextField
              select
              label="Host"
              size="small"
              value={host}
              onChange={(evt) => setHost(evt.target.value)}
              inputProps={{ "aria-label": "Host" }}
            >
              {hosts.map((h) => (
                <MenuItem key={h} value={h}>
                  {h}
                </MenuItem>
              ))}
            </TextField>
            <Stack direction="row" spacing={1} alignItems="center">
              <TextField
                label="Owner"
                size="small"
                value={owner}
                onChange={(evt) => setOwner(evt.target.value)}
                inputProps={{ ...identifierField, "aria-label": "Owner" }}
                sx={{ flex: 1 }}
              />
              <Typography aria-hidden>/</Typography>
              <TextField
                label="Name"
                size="small"
                value={name}
                onChange={(evt) => setName(evt.target.value)}
                inputProps={{ ...identifierField, "aria-label": "Name" }}
                sx={{ flex: 1 }}
              />
            </Stack>
            <TextField
              label="Description"
              size="small"
              value={description}
              onChange={(evt) => setDescription(evt.target.value)}
              inputProps={{ "aria-label": "Description" }}
            />
            <Stack direction="row" spacing={2}>
              <TextField
                select
                label="Visibility"
                size="small"
                value={visibility}
                onChange={(evt) => setVisibility(evt.target.value)}
                inputProps={{ "aria-label": "Visibility" }}
                sx={{ flex: 1 }}
              >
                <MenuItem value="private">Private</MenuItem>
                <MenuItem value="public">Public</MenuItem>
              </TextField>
              <TextField
                select
                label="CI mode"
                size="small"
                value={ciMode}
                onChange={(evt) => setCIMode(evt.target.value)}
                inputProps={{ "aria-label": "CI mode" }}
                sx={{ flex: 1 }}
              >
                <MenuItem value="">Deployment default</MenuItem>
                <MenuItem value="grain">grain</MenuItem>
                <MenuItem value="github">github</MenuItem>
                <MenuItem value="none">none</MenuItem>
              </TextField>
            </Stack>
            <TextField
              label="Template (optional)"
              placeholder="owner/repo"
              size="small"
              value={template}
              onChange={(evt) => setTemplate(evt.target.value)}
              inputProps={{ ...identifierField, "aria-label": "Template" }}
            />
            <FormControlLabel
              control={
                <Checkbox
                  checked={init}
                  disabled={template.trim() !== ""}
                  onChange={(evt) => setInit(evt.target.checked)}
                />
              }
              label="Add a README, so the default branch exists"
            />
            {error && (
              <Alert severity="error" data-testid="new-repo-error">
                {error}
              </Alert>
            )}
            {result && <NewRepoResult result={result} />}
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={close}>
            {result?.connected ? "Done" : "Cancel"}
          </Button>
          <Button type="submit" variant="contained" disabled={!canSubmit}>
            {busy ? "Creating…" : "Create and connect"}
          </Button>
        </DialogActions>
      </Box>
    </Dialog>
  );
}

// NewRepoResult is the step-by-step answer: one alert a step, coloured
// by how it went, with its fix, command and link under it.
function NewRepoResult({ result }) {
  const steps = Array.isArray(result.steps) ? result.steps : [];
  return (
    <Stack spacing={1} data-testid="new-repo-result" role="status">
      <Typography variant="subtitle2">
        {result.connected
          ? `${result.repo} is connected${result.complete ? "." : ", with a step left for you below."}`
          : `${result.repo} was not created.`}
      </Typography>
      {steps.map((s, i) => (
        <Alert
          key={`${s.name}-${i}`}
          severity={STATUS_SEVERITY[s.status] || "info"}
          data-testid={`new-repo-step-${s.name}`}
          data-status={s.status}
        >
          <strong>{STEP_LABELS[s.name] || s.name}: </strong>
          {s.detail}
          {s.fix && (
            <Typography variant="body2" sx={{ mt: 0.5 }}>
              {s.fix}
            </Typography>
          )}
          {s.command && (
            <Box
              component="pre"
              data-testid="new-repo-command"
              sx={{
                mt: 0.5,
                mb: 0,
                p: 1,
                fontSize: "0.8rem",
                whiteSpace: "pre-wrap",
                overflowWrap: "anywhere",
                bgcolor: "action.hover",
                borderRadius: 1,
              }}
            >
              {s.command}
            </Box>
          )}
          {s.link && (
            <Typography
              variant="body2"
              sx={{ mt: 0.5, overflowWrap: "anywhere" }}
            >
              <Link href={s.link} target="_blank" rel="noreferrer">
                {s.link}
              </Link>
            </Typography>
          )}
        </Alert>
      ))}
    </Stack>
  );
}
