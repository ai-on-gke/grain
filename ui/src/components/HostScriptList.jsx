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
  Chip,
  IconButton,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import CloseIcon from "@mui/icons-material/Close";
import api from "../api.js";

// HostScriptList is a host-script server's saved scripts
// (docs/host-scripts.md) on its Settings card: each one's name,
// description, usage and limits, and a Test button that runs it once with
// an argv typed here -- one field per argument, so an argument holding a
// space or $(…) is passed as exactly that one argument, never through a
// shell. Test calls POST /api/capability-servers/test-script, which runs
// the script the server has configured now (the saved configuration, not
// an unsaved form) in a throwaway grant audited with caller
// settings-test.
export const HOST_SCRIPT_TEST_TOOL = "test_script";

// isHostScriptServer is a server whose attach status advertises the
// test_script control-plane tool.
export function isHostScriptServer(server) {
  return Boolean(server?.status?.tools?.includes(HOST_SCRIPT_TEST_TOOL));
}

export default function HostScriptList({ server, readOnly }) {
  const scripts = Array.isArray(server.config?.config?.scripts)
    ? server.config.config.scripts
    : [];
  if (scripts.length === 0) {
    return (
      <Typography
        variant="body2"
        color="text.secondary"
        sx={{ mt: 1 }}
        data-testid="host-script-list"
      >
        No scripts yet: Configure adds them.
      </Typography>
    );
  }
  return (
    <Stack spacing={1} sx={{ mt: 1 }} data-testid="host-script-list">
      <Typography variant="body2" sx={{ fontWeight: 600 }}>
        Scripts
      </Typography>
      {scripts.map((s, i) => (
        <HostScriptRow
          key={`${s.name}-${i}`}
          serverID={server.id}
          script={s}
          readOnly={readOnly}
        />
      ))}
    </Stack>
  );
}

function limits(s) {
  return [
    s.timeout !== undefined && `timeout ${s.timeout}s`,
    s.max_output !== undefined && `output ≤ ${s.max_output} B`,
    s.max_calls !== undefined && `${s.max_calls} calls`,
    s.max_args !== undefined && `≤ ${s.max_args} args`,
    s.max_arg_len !== undefined && `arg ≤ ${s.max_arg_len} B`,
    s.cwd && `cwd ${s.cwd}`,
  ].filter(Boolean);
}

function HostScriptRow({ serverID, script, readOnly }) {
  const [open, setOpen] = useState(false);
  const [argv, setArgv] = useState([]);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState(null);
  const run = async () => {
    setBusy(true);
    setResult(null);
    try {
      setResult(
        await api("/api/capability-servers/test-script", {
          method: "POST",
          body: JSON.stringify({ id: serverID, script: script.name, argv }),
        }),
      );
    } catch (err) {
      setResult({ error: err.message });
    } finally {
      setBusy(false);
    }
  };
  return (
    <Box
      data-host-script={script.name}
      sx={{
        border: 1,
        borderColor: "divider",
        borderRadius: 1,
        px: 1.5,
        py: 1,
        minWidth: 0,
      }}
    >
      <Stack
        direction={{ xs: "column", sm: "row" }}
        spacing={1}
        alignItems={{ xs: "flex-start", sm: "center" }}
        justifyContent="space-between"
      >
        <Box sx={{ minWidth: 0, overflowWrap: "anywhere" }}>
          <Typography
            variant="body2"
            component="h5"
            sx={{ fontFamily: "monospace", fontWeight: 600 }}
          >
            {script.name}
          </Typography>
          <Typography variant="body2" color="text.secondary">
            {script.description}
          </Typography>
        </Box>
        <Button
          size="small"
          variant={open ? "contained" : "outlined"}
          disabled={readOnly}
          aria-expanded={open}
          onClick={() => setOpen(!open)}
        >
          Test
        </Button>
      </Stack>
      {script.usage && (
        <Box
          component="pre"
          data-testid="host-script-usage"
          sx={{
            mt: 1,
            mb: 0,
            p: 1,
            bgcolor: "action.hover",
            borderRadius: 1,
            fontSize: 12,
            whiteSpace: "pre-wrap",
            overflowWrap: "anywhere",
          }}
        >
          {script.usage}
        </Box>
      )}
      <Stack
        direction="row"
        sx={{ mt: 1, flexWrap: "wrap", gap: 0.5 }}
        data-testid="host-script-limits"
      >
        {limits(script).map((l) => (
          <Chip key={l} size="small" variant="outlined" label={l} />
        ))}
        {Object.keys(script.env || {}).length > 0 && (
          <Chip
            size="small"
            variant="outlined"
            label={`env ${Object.keys(script.env).join(", ")}`}
          />
        )}
      </Stack>
      {open && (
        <Box sx={{ mt: 1.5 }} data-testid="host-script-test">
          <Typography variant="caption" color="text.secondary" component="p">
            Runs the saved script once on the server&apos;s side, with these
            arguments exactly — no shell reads them. The call is logged with its
            arguments (caller settings-test), so never pass a secret. Stopped
            after at most a minute.
          </Typography>
          {argv.map((a, i) => (
            <Box
              key={i}
              sx={{ display: "flex", gap: 1, alignItems: "flex-start", mt: 1 }}
            >
              <TextField
                size="small"
                fullWidth
                label={`Argument ${i + 1}`}
                value={a}
                inputProps={{
                  "data-host-script-arg": i,
                  style: { fontFamily: "monospace" },
                }}
                onChange={(e) => {
                  const next = argv.slice();
                  next[i] = e.target.value;
                  setArgv(next);
                }}
              />
              <IconButton
                size="small"
                sx={{ mt: 0.5 }}
                aria-label={`Remove argument ${i + 1}`}
                onClick={() => setArgv(argv.filter((_, j) => j !== i))}
              >
                <CloseIcon fontSize="small" />
              </IconButton>
            </Box>
          ))}
          <Stack
            direction="row"
            spacing={1}
            sx={{ mt: 1, flexWrap: "wrap", rowGap: 1 }}
          >
            <Button size="small" onClick={() => setArgv([...argv, ""])}>
              Add argument
            </Button>
            <Button
              size="small"
              variant="contained"
              disabled={readOnly || busy}
              onClick={run}
            >
              {busy ? "Running…" : `Run ${script.name}`}
            </Button>
          </Stack>
          {result && <TestResult result={result} />}
        </Box>
      )}
    </Box>
  );
}

function Stream({ label, text, testid }) {
  return (
    <Box sx={{ mt: 1 }}>
      <Typography variant="caption" color="text.secondary">
        {label}
      </Typography>
      <Box
        component="pre"
        data-testid={testid}
        sx={{
          m: 0,
          p: 1,
          maxHeight: 240,
          overflow: "auto",
          bgcolor: "action.hover",
          borderRadius: 1,
          fontSize: 12,
          whiteSpace: "pre-wrap",
          overflowWrap: "anywhere",
        }}
      >
        {text || <em>(empty)</em>}
      </Box>
    </Box>
  );
}

function TestResult({ result }) {
  if (result.error) {
    return (
      <Alert
        severity="error"
        sx={{ mt: 1, overflowWrap: "anywhere" }}
        data-testid="host-script-result"
      >
        {result.error}
      </Alert>
    );
  }
  if (result.refused) {
    return (
      <Alert
        severity="warning"
        sx={{ mt: 1, overflowWrap: "anywhere" }}
        data-testid="host-script-result"
      >
        Not run: {result.refused}
      </Alert>
    );
  }
  return (
    <Box sx={{ mt: 1 }} data-testid="host-script-result">
      <Stack direction="row" sx={{ flexWrap: "wrap", gap: 0.5 }}>
        <Chip
          size="small"
          color={result.exit_code === 0 ? "success" : "error"}
          label={`exit ${result.exit_code}`}
          data-testid="host-script-exit"
        />
        <Chip
          size="small"
          variant="outlined"
          label={`${result.duration_ms} ms`}
          data-testid="host-script-duration"
        />
        {result.timed_out && (
          <Chip size="small" color="warning" label="timed out" />
        )}
        {result.truncated && (
          <Chip size="small" color="warning" label="output truncated" />
        )}
      </Stack>
      <Stream label="stdout" text={result.stdout} testid="host-script-stdout" />
      <Stream label="stderr" text={result.stderr} testid="host-script-stderr" />
    </Box>
  );
}
