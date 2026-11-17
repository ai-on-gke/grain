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
import { Alert, Box, Button, Chip, Stack, Typography } from "@mui/material";
import api from "../api.js";

// DeclarationAcceptance is one repository declaration -- a schedule,
// template, suite or CI job in a repository's own .grain directory --
// that asks for something only a person may grant (GET
// /api/declarations, model.DeclarationAcceptance). Accepting records the
// person, the scopes they held and the declaration's commit, and its work
// lands approved for as long as it asks for no more; a declaration that
// widened past what was accepted shows the new items and waits again.
// Revoking clears the acceptance, so its next firing is held again until
// a person accepts once more.
export function DeclarationAcceptance({ d, onAccept, onRevoke }) {
  const [busy, setBusy] = useState(false);
  const run = async (fn) => {
    if (busy) return;
    setBusy(true);
    try {
      await fn();
    } finally {
      setBusy(false);
    }
  };
  const unaccepted = new Set(d.unaccepted || []);
  return (
    <Alert
      severity={d.awaiting ? "warning" : "success"}
      icon={false}
      sx={{ mb: 1, "& .MuiAlert-message": { width: "100%", minWidth: 0 } }}
      className="declaration-acceptance"
      data-declaration={d.id}
    >
      <Stack
        direction="row"
        spacing={1}
        sx={{ alignItems: "center", flexWrap: "wrap", rowGap: 0.5 }}
      >
        <strong>{d.kind}</strong>
        <Box
          component="code"
          sx={{ wordBreak: "break-all", minWidth: 0 }}
          className="declaration-id"
        >
          {d.id}
        </Box>
        <Chip
          size="small"
          label={d.awaiting ? "Awaiting acceptance" : "Accepted"}
          color={d.awaiting ? "warning" : "success"}
          variant="outlined"
          data-testid="declaration-status"
        />
      </Stack>
      <Typography variant="body2" sx={{ mt: 1 }}>
        Asks for:
      </Typography>
      <Box component="ul" sx={{ my: 0.5, pl: 3 }}>
        {d.requested.map((item) => (
          <li key={item}>
            <Box component="code" sx={{ wordBreak: "break-all" }}>
              {item}
            </Box>
            {unaccepted.has(item) && d.acceptedAt && (
              <em> (new since it was accepted)</em>
            )}
          </li>
        ))}
      </Box>
      <Typography variant="body2" color="text.secondary">
        {d.repo && <>From {d.repo}. </>}
        Accepting needs {d.needsScopes.join(" and ")}.
        {d.acceptedAt && (
          <>
            {" "}
            Accepted by {d.acceptedBy || "the operator"}
            {d.acceptedCommit && <> at {d.acceptedCommit.slice(0, 12)}</>}.
          </>
        )}
      </Typography>
      <Stack direction="row" spacing={1} sx={{ mt: 1 }}>
        {d.awaiting && (
          <Button
            size="small"
            variant="contained"
            color="warning"
            disabled={busy}
            onClick={() => run(() => onAccept(d))}
          >
            Accept
          </Button>
        )}
        {d.acceptedAt && (
          <Button
            size="small"
            variant="outlined"
            color="error"
            disabled={busy}
            onClick={() => run(() => onRevoke(d))}
          >
            Revoke
          </Button>
        )}
      </Stack>
    </Alert>
  );
}

// DeclarationAcceptancesPanel lists every repository declaration that has
// asked for a grant only a person may give, those awaiting acceptance
// first, with Accept on each one that is and Revoke on each one a person
// has accepted.
export default function DeclarationAcceptancesPanel({ showError }) {
  const [rows, setRows] = useState(null);
  const load = useCallback(async () => {
    try {
      const got = await api("/api/declarations");
      setRows(Array.isArray(got) ? got : []);
    } catch (e) {
      setRows([]);
      showError?.(e.message);
    }
  }, [showError]);
  useEffect(() => {
    load();
  }, [load]);
  const post = async (path, body) => {
    try {
      await api(path, { method: "POST", body: JSON.stringify(body) });
    } catch (e) {
      showError?.(e.message);
    }
    await load();
  };
  if (rows === null) return null;
  return (
    <div className="declaration-acceptances">
      <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
        A repository&apos;s own <code>.grain</code> declarations are written by
        whoever can merge there, so a grant only a person may give -- a GCP key,
        a host script, a GitHub credential, a dangerous API scope, admission
        above standard, open network access -- is held until a person holding
        its scope accepts it. Accepted work lands approved until the declaration
        asks for more, or until a person holding the same scopes revokes the
        acceptance.
      </Typography>
      {rows.length === 0 ? (
        <Typography variant="body2" data-testid="declarations-empty">
          No declaration has asked for anything a person must accept.
        </Typography>
      ) : (
        rows.map((d) => (
          <DeclarationAcceptance
            key={d.id}
            d={d}
            onAccept={() =>
              post("/api/declarations/accept", { id: d.id, seen: d.requested })
            }
            onRevoke={() => post("/api/declarations/revoke", { id: d.id })}
          />
        ))
      )}
    </div>
  );
}
