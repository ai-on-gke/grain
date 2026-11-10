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
  FormControlLabel,
  TextField,
  Typography,
} from "@mui/material";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { formatDateTime } from "../time.js";

// ExternalRepoWritersWarning says, where the remote is set, what naming
// one hands out: grain applies whatever lands on that branch, with no
// Submit of anybody's in between (docs/external-config-repo.md).
export function ExternalRepoWritersWarning() {
  return (
    <Alert severity="warning" sx={{ mt: 2 }}>
      Anybody who can push to an external config repository&apos;s branch is an
      administrator of this deployment. grain applies what lands there &mdash;
      host scripts, capability servers, scope sets, secret bindings &mdash; on
      its next sync, and nobody&apos;s Submit in grain is involved. Protect the
      branch on the forge, or turn on <strong>Require verified commits</strong>{" "}
      below once the repository is named.
    </Alert>
  );
}

// StateRepoVerification is require_verified mode for an external config
// repository: what verifies a commit, and the commit being held.
export default function StateRepoVerification({ status, busy, act }) {
  const zone = useTimeZone();
  const v = status.verification || {};
  const [on, setOn] = useState(!!v.requireVerified);
  const [approvals, setApprovals] = useState(String(v.requiredApprovals || 1));
  const [mergers, setMergers] = useState((v.verifiedMergers || []).join(", "));
  const [signers, setSigners] = useState("");
  useEffect(() => {
    setOn(!!v.requireVerified);
    setApprovals(String(v.requiredApprovals || 1));
    setMergers((v.verifiedMergers || []).join(", "));
  }, [
    v.requireVerified,
    v.requiredApprovals,
    (v.verifiedMergers || []).join(","),
  ]);

  const base = { remote: status.remote, branch: status.branch || "" };
  const held = v.requireVerified ? v.held : null;
  return (
    <Box sx={{ mt: 2 }}>
      <Typography variant="subtitle2">Require verified commits</Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
        Apply a commit only if grain can vouch for it: the merge of a pull
        request with the required approvals on its final commit, a pull request
        merged by grain&apos;s own forge account, or a commit signed by an
        allowed SSH key. Anything else is held, with everything after it, until
        it verifies or you accept it here.
        {!v.forgeReviews &&
          " grain cannot read this remote's pull requests, so only signatures and acceptances verify."}
      </Typography>
      {held && (
        <Alert
          severity="warning"
          sx={{ mt: 1 }}
          action={
            <Button
              type="button"
              size="small"
              color="inherit"
              disabled={busy}
              onClick={() =>
                act("/api/state-repo/remote", {
                  ...base,
                  acceptCommit: held.sha,
                })
              }
            >
              Accept
            </Button>
          }
        >
          Holding <code>{held.sha.slice(0, 12)}</code>
          {held.subject ? ` (${held.subject})` : ""} since{" "}
          {formatDateTime(held.since, zone)}: {held.reason}. Nothing from it on
          is applied. Accept it only once you have read what it changes.
        </Alert>
      )}
      <FormControlLabel
        control={
          <Checkbox
            size="small"
            checked={on}
            onChange={(e) => setOn(e.target.checked)}
          />
        }
        label="Require verified commits"
      />
      <TextField
        label="Required approvals"
        value={approvals}
        onChange={(e) => setApprovals(e.target.value)}
        type="number"
        inputProps={{ min: 1, max: 20 }}
        margin="normal"
        size="small"
        fullWidth
      />
      <TextField
        label="grain's forge accounts"
        value={mergers}
        onChange={(e) => setMergers(e.target.value)}
        helperText="The accounts grain merges pull requests as, comma-separated (such as my-grain-app[bot]). A pull request one of them merged went through grain's merge queue."
        autoComplete="off"
        margin="normal"
        size="small"
        fullWidth
      />
      <TextField
        label="Allowed signing keys"
        value={signers}
        onChange={(e) => setSigners(e.target.value)}
        helperText={`ssh allowed_signers lines (principal, then key). ${v.allowedSigners || 0} configured; leave empty to keep them.`}
        autoComplete="off"
        margin="normal"
        size="small"
        multiline
        minRows={2}
        fullWidth
      />
      <Button
        type="button"
        size="small"
        variant="outlined"
        disabled={busy}
        onClick={() =>
          act("/api/state-repo/remote", {
            ...base,
            requireVerified: on,
            requiredApprovals: Math.max(1, parseInt(approvals, 10) || 1),
            verifiedMergers: mergers,
            ...(signers.trim() ? { allowedSigners: signers } : {}),
          })
        }
      >
        Save verification
      </Button>
    </Box>
  );
}
