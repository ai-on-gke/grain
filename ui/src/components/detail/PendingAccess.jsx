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

import { useState } from "react";
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
import api from "../../api.js";
import AccessGate from "../AccessGate.jsx";
import GrantChip from "../GrantChip.jsx";

// PendingAccess is what a run's request_access call turns into for the
// person reading the task (pkg/ui's task_access_request.go): each thing
// the run asked for -- a repository to read or write, a capability grant
// rendered the way the grants editor renders it, open network access --
// with the dangerous and person-only marks, the run's reason, what the
// task already holds, and any earlier answer to the same request.
//
// Grant applies the ticked items through the task's own editors and
// re-queues the task; Deny opens a required "Why?" box, and the reason
// is what the next run is told. Both routes answer a person only -- an
// agent cannot approve its own request, whatever scopes it holds.
//
// t.pendingAccess is nil for almost every task, and this renders nothing
// for those. act is DetailOverlay's (fn, id) -> Promise; the inbox passes
// its own.
export default function PendingAccess({ t, act }) {
  const pending = t.pendingAccess;
  const items = pending?.items || [];
  const [unticked, setUnticked] = useState(() => new Set());
  const [denying, setDenying] = useState(false);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  if (!pending) return null;

  const chosen = items
    .map((_, i) => i)
    .filter((i) => !unticked.has(i) && !items[i].error);
  const toggle = (i) =>
    setUnticked((prev) => {
      const next = new Set(prev);
      if (next.has(i)) next.delete(i);
      else next.add(i);
      return next;
    });

  const decide = async (verb, body) => {
    setBusy(true);
    try {
      await act(
        () =>
          api(`/api/tasks/${t.id}/access-request/${verb}`, {
            method: "POST",
            body: JSON.stringify(body),
          }),
        t.id,
      );
    } finally {
      setBusy(false);
    }
  };
  const holds = pending.holds || {};
  const earlier = pending.earlier || [];
  const all = chosen.length === items.length;

  return (
    <Alert
      severity="info"
      icon={false}
      sx={{ mb: 2, "& .MuiAlert-message": { width: "100%", minWidth: 0 } }}
      role="region"
      aria-label="Access request waiting for your decision"
      className="pending-access"
    >
      <Typography variant="subtitle2" component="div">
        This run asked for more access
      </Typography>
      <Typography variant="body2" sx={{ mt: 0.5, fontWeight: 600 }}>
        {pending.summary}
      </Typography>
      {pending.reason && (
        <Typography
          variant="body2"
          sx={{ mt: 0.5, whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}
          data-testid="access-reason"
        >
          {pending.reason}
        </Typography>
      )}

      {earlier.map((d) => {
        const declined = d.declined || [];
        const who = d.by?.id || d.by?.ID;
        return (
          <Alert
            key={d.at}
            severity={d.denied || declined.length > 0 ? "warning" : "success"}
            sx={{ mt: 1, overflowWrap: "anywhere" }}
            data-testid="access-earlier"
          >
            {d.denied
              ? `Denied before${who ? ` by ${who}` : ""}: ${d.reason}`
              : `Answered before${who ? ` by ${who}` : ""}: ${(d.granted || []).length} granted, ${declined.length} declined${declined.length ? ` (${declined.map(itemText).join(", ")})` : ""}.`}
          </Alert>
        );
      })}

      <Box
        component="ul"
        sx={{ listStyle: "none", m: 0, mt: 1, p: 0 }}
        aria-label="Requested access"
      >
        {items.map((it, i) => (
          <Box
            component="li"
            key={i}
            className="pending-access-item"
            sx={{
              display: "flex",
              alignItems: "center",
              flexWrap: "wrap",
              gap: 0.5,
              minWidth: 0,
            }}
          >
            <FormControlLabel
              sx={{ mr: 0, minWidth: 0, maxWidth: "100%" }}
              control={
                <Checkbox
                  size="small"
                  checked={!unticked.has(i) && !it.error}
                  disabled={busy || Boolean(it.error)}
                  onChange={() => toggle(i)}
                  inputProps={{ "aria-label": `Grant ${itemText(it)}` }}
                />
              }
              label={<AccessItemChip it={it} />}
            />
            {it.dangerous && (
              <Chip
                size="small"
                color="error"
                variant="outlined"
                label="dangerous"
              />
            )}
            {it.requiresPerson && !it.dangerous && (
              <Chip
                size="small"
                color="warning"
                variant="outlined"
                label="person only"
              />
            )}
            {it.held && <Chip size="small" label="already held" />}
            {it.error && (
              <Typography
                variant="caption"
                color="error"
                sx={{ flexBasis: "100%", pl: 4, overflowWrap: "anywhere" }}
              >
                {it.error}
              </Typography>
            )}
          </Box>
        ))}
      </Box>

      <Typography
        variant="caption"
        component="div"
        sx={{ mt: 1, overflowWrap: "anywhere" }}
        data-testid="access-holds"
      >
        Already holds: {holdsText(holds)}
      </Typography>
      {holds.grants?.length > 0 && (
        <Stack
          direction="row"
          spacing={0.5}
          useFlexGap
          flexWrap="wrap"
          sx={{ mt: 0.5 }}
          aria-label="Grants already held"
        >
          {holds.grants.map((g) => (
            <GrantChip key={g} grant={g} variant="outlined" />
          ))}
        </Stack>
      )}

      {denying ? (
        <Stack spacing={1} sx={{ mt: 1 }}>
          <TextField
            size="small"
            fullWidth
            multiline
            minRows={2}
            required
            autoFocus
            label="Why?"
            helperText="The next run is told this, so it can change its approach."
            value={reason}
            onChange={(evt) => setReason(evt.target.value)}
          />
          <Stack direction="row" spacing={1} useFlexGap flexWrap="wrap">
            <Button
              type="button"
              variant="contained"
              color="error"
              disabled={busy || !reason.trim()}
              onClick={() => decide("deny", { reason })}
            >
              Deny request
            </Button>
            <Button
              type="button"
              disabled={busy}
              onClick={() => setDenying(false)}
            >
              Cancel
            </Button>
          </Stack>
        </Stack>
      ) : (
        <Stack
          direction="row"
          spacing={1}
          useFlexGap
          flexWrap="wrap"
          sx={{ mt: 1 }}
        >
          {/* Both answers are POST /api/tasks/{id}/access-request/...,
              which a role needs capabilities:admin for (pkg/ui
              humanRouteScopes): a viewer or contributor sees the
              request, and is not offered an answer the server refuses. */}
          <AccessGate scope="capabilities:admin" repo={t.repo}>
            <Button
              type="button"
              variant="contained"
              color="success"
              disabled={busy || chosen.length === 0}
              onClick={() => decide("grant", all ? {} : { items: chosen })}
            >
              {all ? "Grant all" : `Grant ${chosen.length} of ${items.length}`}
            </Button>
          </AccessGate>
          <AccessGate scope="capabilities:admin" repo={t.repo}>
            <Button
              type="button"
              variant="outlined"
              color="error"
              disabled={busy}
              onClick={() => setDenying(true)}
            >
              Deny
            </Button>
          </AccessGate>
        </Stack>
      )}
    </Alert>
  );
}

// AccessItemChip is one requested item as a chip: a grant the way the
// grants editor shows it (GrantChip), anything else in the same shape.
function AccessItemChip({ it }) {
  if (it.kind === "grant") {
    return (
      <GrantChip
        grant={it.grant || it.capability}
        data-testid="access-item-chip"
      />
    );
  }
  return (
    <Chip
      size="small"
      label={itemText(it)}
      data-testid="access-item-chip"
      sx={{ maxWidth: "100%" }}
    />
  );
}

// itemText is a non-grant item as its chip reads: "acme/lib › read",
// "acme/lib › write · base: dev", "network › open".
export function itemText(it) {
  switch (it.kind) {
    case "read":
      return `${it.repo} › read`;
    case "write":
      return it.base
        ? `${it.repo} › write · base: ${it.base}`
        : `${it.repo} › write`;
    case "network":
      return `network › ${it.access}`;
    default:
      if (it.grant) return it.grant;
      if (it.with && Object.keys(it.with).length > 0) {
        return `${it.capability}=${JSON.stringify(it.with)}`;
      }
      return it.capability;
  }
}

function holdsText(holds) {
  const parts = [];
  if (holds.reads?.length) parts.push(`reads ${holds.reads.join(", ")}`);
  if (holds.writes?.length)
    parts.push(
      `writes ${holds.writes
        .map((w) => (w.base ? `${w.repo} (${w.base})` : w.repo))
        .join(", ")}`,
    );
  if (holds.grants?.length)
    parts.push(
      `${holds.grants.length} grant${holds.grants.length === 1 ? "" : "s"}`,
    );
  if (holds.networkAccess) parts.push(`network ${holds.networkAccess}`);
  return parts.length ? parts.join(" · ") : "nothing beyond its own repository";
}
