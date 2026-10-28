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
import { Alert, Box, Button, Stack } from "@mui/material";
import api from "../../api.js";

// callLine is the call as the script will see it: the script, then each
// argument quoted, so a `$(...)` or a `;` reads as the plain text the
// script is actually given (no shell sees it).
export function callLine(a) {
  const args = (a.args || []).map((arg) => JSON.stringify(arg)).join(" ");
  const stdin = a.stdinBytes > 0 ? ` (and ${a.stdinBytes} bytes on stdin)` : "";
  return `${a.script}${args ? " " + args : ""}${stdin}`;
}

// stdinLine says what the script gets on stdin: how much, the SHA-256 of
// all of it, and, when the preview is cut, how much of it is shown.
export function stdinLine(a) {
  let line = `On stdin: ${a.stdinBytes} bytes, sha256 ${a.stdinSha256 || "unknown"}`;
  if (a.stdinTruncated) {
    const shown = new TextEncoder().encode(a.stdinPreview || "").length;
    line += ` (truncated: first ${shown} of ${a.stdinBytes} bytes shown)`;
  }
  return line;
}

// answerLocalScript approves or denies one waiting call (POST
// /api/tasks/{id}/local-script-approvals/{approval}/answer).
export function answerLocalScript(a, approve) {
  return api(
    `/api/tasks/${a.taskId}/local-script-approvals/${encodeURIComponent(a.id)}/answer`,
    { method: "POST", body: JSON.stringify({ approve }) },
  );
}

// LocalScriptApproval is one call a run's local script (approve: true in
// its declaration, docs/local-scripts.md) is waiting on a person for,
// with Approve and Deny. The script runs on the node host, so the call
// is shown exactly: which script, every argument, and the start of its
// stdin -- a script that reads its instructions from stdin is approved
// by somebody who can see them.
//
// onAnswer(approve) sends the answer and refreshes whatever the call is
// listed in; it reports a failure itself.
export function LocalScriptApproval({ a, onAnswer }) {
  const [busy, setBusy] = useState(false);
  const answer = async (approve) => {
    if (busy) return;
    setBusy(true);
    try {
      await onAnswer(approve);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Alert
      severity="warning"
      icon={false}
      sx={{ mb: 1 }}
      className="local-script-approval"
      data-approval={a.id}
    >
      <strong>A local script is waiting for approval.</strong> The run asks to
      run <code>{a.tool}</code> on the node host:
      <pre className="local-script-call">{callLine(a)}</pre>
      {a.stdinBytes > 0 && (
        <>
          <div className="local-script-stdin-line">{stdinLine(a)}</div>
          <Box
            component="pre"
            className="local-script-stdin"
            sx={{
              maxHeight: 240,
              overflow: "auto",
              whiteSpace: "pre-wrap",
              wordBreak: "break-all",
              p: 1,
              my: 1,
              bgcolor: "action.hover",
              borderRadius: 1,
            }}
          >
            {a.stdinPreview}
            {a.stdinTruncated && "\n[… truncated]"}
          </Box>
        </>
      )}
      <Stack direction="row" spacing={1} sx={{ mt: 1 }}>
        <Button
          size="small"
          variant="contained"
          color="warning"
          disabled={busy}
          onClick={() => answer(true)}
        >
          Approve
        </Button>
        <Button
          size="small"
          variant="outlined"
          disabled={busy}
          onClick={() => answer(false)}
        >
          Deny
        </Button>
      </Stack>
    </Alert>
  );
}

// LocalScriptApprovals is every call t's run is waiting on, for the task
// pane and the chat. t.localScriptApprovals is absent for almost every
// task, and this renders nothing for those.
export default function LocalScriptApprovals({ t, act }) {
  const pending = (t.localScriptApprovals || []).filter(
    (a) => a.status === "pending",
  );
  if (pending.length === 0) return null;
  return (
    <div className="local-script-approvals">
      {pending.map((a) => (
        <LocalScriptApproval
          key={a.id}
          a={a}
          onAnswer={(approve) => act(() => answerLocalScript(a, approve), t.id)}
        />
      ))}
    </div>
  );
}
