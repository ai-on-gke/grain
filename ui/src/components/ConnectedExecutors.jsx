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
import { Alert, Box, Chip, Stack, Typography } from "@mui/material";
import api from "../api.js";
import { frameworkLabels } from "./ModelBackendPage.jsx";
import { formatUptime } from "./SandboxHealthPage.jsx";

// ConnectedExecutors is the remote executor nodes connected to the
// deployment now (GET /api/executors), on System → Sandboxes. The tokens
// they connect with are on Settings → Executors.
export default function ConnectedExecutors() {
  const [executors, setExecutors] = useState([]);
  const [loadError, setLoadError] = useState("");
  useEffect(() => {
    let active = true;
    Promise.resolve()
      .then(() => api("/api/executors"))
      .then((res) => {
        if (!active) return;
        setLoadError("");
        if (Array.isArray(res)) setExecutors(res);
      })
      .catch((err) => {
        if (active) setLoadError(err?.message || String(err));
      });
    return () => {
      active = false;
    };
  }, []);

  return (
    <>
      <Typography variant="subtitle2" sx={{ mb: 1 }}>
        Connected executors
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 1.5 }}>
        Remote executor nodes connected now, providing agent execution
        environments and binaries. The tokens they connect with are on Settings
        &rarr; Executors.
      </Typography>
      <Stack spacing={1.25} sx={{ mb: 3 }}>
        {loadError && (
          <Alert severity="error">
            Connected executors could not be read ({loadError}).
          </Alert>
        )}
        {executors.map((executor) => {
          const uptimeMs = executor.since
            ? Date.now() - new Date(executor.since).getTime()
            : null;
          const binaryKeys = Object.keys(executor.binaries || {});
          return (
            <Box
              key={`${executor.host}-${executor.addr}`}
              sx={{
                border: 1,
                borderColor: "divider",
                borderRadius: 1,
                px: 2,
                py: 1.5,
              }}
            >
              <Box
                sx={{
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "space-between",
                  flexWrap: "wrap",
                  gap: 1,
                }}
              >
                <Box
                  sx={{
                    display: "flex",
                    alignItems: "center",
                    gap: 1,
                    flexWrap: "wrap",
                  }}
                >
                  <Typography variant="subtitle1" sx={{ fontWeight: 600 }}>
                    {executor.host}
                  </Typography>
                  <Chip
                    label="Connected"
                    size="small"
                    color="success"
                    variant="outlined"
                  />
                </Box>
                <Typography variant="caption" color="text.secondary">
                  {executor.addr}
                </Typography>
              </Box>

              <Box
                sx={{
                  mt: 1,
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "space-between",
                  flexWrap: "wrap",
                  gap: 1,
                }}
              >
                <Box>
                  <Typography
                    variant="caption"
                    color="text.secondary"
                    sx={{ display: "block", mb: 0.5 }}
                  >
                    Supported model frameworks:
                  </Typography>
                  <Stack direction="row" spacing={0.75} flexWrap="wrap">
                    {binaryKeys.map((bin) => (
                      <Chip
                        key={bin}
                        label={
                          bin === "agy"
                            ? "Antigravity (agy)"
                            : frameworkLabels[bin] || bin
                        }
                        size="small"
                        color="primary"
                        variant="filled"
                      />
                    ))}
                    {binaryKeys.length === 0 && (
                      <Typography variant="caption" color="text.secondary">
                        None reported
                      </Typography>
                    )}
                  </Stack>
                </Box>

                <Stack
                  direction="row"
                  spacing={2}
                  sx={{ mt: { xs: 1, sm: 0 } }}
                >
                  <Typography variant="body2" color="text.secondary">
                    <strong>Uptime:</strong> {formatUptime(uptimeMs)}
                  </Typography>
                  <Typography variant="body2" color="text.secondary">
                    <strong>Active jobs:</strong>{" "}
                    {executor.running ?? executor.activeJobs ?? 0}
                    {executor.concurrency ? ` / ${executor.concurrency}` : ""}
                  </Typography>
                </Stack>
              </Box>
            </Box>
          );
        })}
        {!loadError && executors.length === 0 && (
          <Alert severity="info">No remote executors connected.</Alert>
        )}
      </Stack>
    </>
  );
}
