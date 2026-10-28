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

import Alert from "@mui/material/Alert";
import Box from "@mui/material/Box";

const ALLOWLIST = "/etc/grain/hostd.yaml";

// hostdEntryProblem is why a run: host server's command cannot start
// through grain hostd, from hostEntries (GET
// /api/capability-servers/host-entries): hostd is not reachable, or its
// allowlist has no entry of that name. null when nothing is wrong, or
// before hostEntries has loaded.
export function hostdEntryProblem(hostEntries, command) {
  if (!hostEntries || !command) return null;
  if (!hostEntries.available) {
    return { kind: "unreachable", reason: hostEntries.reason || "" };
  }
  if ((hostEntries.entries || []).includes(command)) return null;
  const own = (hostEntries.missing || []).find((m) => m.name === command);
  return { kind: "missing", name: command, own };
}

// HostdMissingNote says plainly what is wrong with a run: host server's
// hostd entry, and what grain does about it: grain's own entries
// (hostd.EnsureBuiltins) are added on the host by grain itself at hostd's
// next start or upgrade, with the command to add them now and the text
// to paste meanwhile; any other entry is the operator's to add.
export function HostdMissingNote({ problem, sx }) {
  if (!problem) return null;
  if (problem.kind === "unreachable") {
    return (
      <Alert
        severity="warning"
        sx={{ overflowWrap: "anywhere", ...sx }}
        data-testid="hostd-unreachable"
      >
        grain hostd is not reachable, so no run: host server can start
        {problem.reason ? `: ${problem.reason}` : "."}
      </Alert>
    );
  }
  const own = problem.own;
  return (
    <Alert
      severity="warning"
      sx={{ overflowWrap: "anywhere", minWidth: 0, ...sx }}
      data-testid="hostd-entry-missing"
    >
      grain hostd&apos;s allowlist on the host has no capabilities entry{" "}
      <code>{problem.name}</code>.{" "}
      {own ? (
        <>
          It is grain&apos;s own entry: grain adds it automatically the next
          time hostd starts or is upgraded, and this server attaches by itself
          once it is there. To add it now, run <code>{own.command}</code> on the
          host (nothing restarts; on an install whose setup.sh predates that
          service, re-run setup.sh once), or paste this under{" "}
          <code>capabilities:</code> in <code>{ALLOWLIST}</code>:
          <Box
            component="pre"
            data-testid="hostd-entry-text"
            sx={{
              m: 0,
              mt: 1,
              p: 1,
              fontSize: "0.75rem",
              overflowX: "auto",
              maxWidth: "100%",
              bgcolor: "action.hover",
              borderRadius: 1,
            }}
          >
            {own.entry}
          </Box>
        </>
      ) : (
        <>
          Add one under <code>capabilities:</code> in <code>{ALLOWLIST}</code>{" "}
          on the host (grain never edits entries it does not own), or run the
          server in the container.
        </>
      )}
    </Alert>
  );
}

// HostdHealthNote is System -> Health's line on grain hostd, from
// hostExecutor (daemon.HostExecutorStatus): unreachable, or missing some
// of grain's own entries. Nothing for a deployment without hostd.
export function HostdHealthNote({ hostExecutor }) {
  const hd = hostExecutor?.hostd;
  if (!hd) return null;
  if (!hd.reachable) {
    return (
      <HostdMissingNote
        problem={{ kind: "unreachable", reason: hd.error }}
        sx={{ mb: 2 }}
      />
    );
  }
  const missing = hd.missing || [];
  if (missing.length === 0) return null;
  return (
    <Box data-testid="hostd-health-missing" sx={{ mb: 2 }}>
      {missing.map((m) => (
        <HostdMissingNote
          key={m.name}
          problem={{ kind: "missing", name: m.name, own: m }}
          sx={{ mb: 1 }}
        />
      ))}
    </Box>
  );
}
