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
  Box,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Typography,
} from "@mui/material";
import api from "../../api.js";

// HealthCountersPanel is System → Health's reading of the counters a
// layer built on grain registered with ui.RegisterHealthCounters
// (pkg/ui/health_counters.go): one table per section, counted in the
// daemon's memory since it started. GET /api/sandboxes?section=counters
// is that and nothing else, so re-reading it costs no sandbox sweep.
//
// Nothing is shown when nothing is registered, which in core is always.
export default function HealthCountersPanel({ sx }) {
  const [sections, setSections] = useState(null);
  // Re-read while Health is open, as the GitHub budget above it is; a
  // failed read keeps the last figures rather than blanking the panel.
  useEffect(() => {
    let live = true;
    let timer = null;
    const tick = async () => {
      try {
        const r = await api("/api/sandboxes?section=counters");
        if (live) setSections(r?.counters || []);
      } catch {
        // Keep what was last read.
      }
      if (live) timer = setTimeout(tick, COUNTERS_REFRESH_MS);
    };
    tick();
    return () => {
      live = false;
      if (timer) clearTimeout(timer);
    };
  }, []);
  return <HealthCountersView sections={sections} sx={sx} />;
}

export const COUNTERS_REFRESH_MS = 10000;

export function HealthCountersView({ sections, sx }) {
  if (!sections?.length) return null;
  return (
    <Box sx={sx} data-testid="health-counters">
      {sections.map((section, idx) => {
        const keys = Object.keys(section.counters || {}).sort();
        return (
          <Box
            key={`${section.name}-${idx}`}
            sx={{ mb: 1.5 }}
            data-testid="health-counters-section"
          >
            <Typography variant="subtitle2" component="h3">
              {section.name}
            </Typography>
            <Typography variant="caption" color="text.secondary">
              Since the daemon started.
            </Typography>
            {keys.length === 0 ? (
              <Typography variant="body2" color="text.secondary">
                Nothing counted yet.
              </Typography>
            ) : (
              <Box sx={{ overflowX: "auto" }}>
                <Table size="small">
                  <TableHead>
                    <TableRow>
                      <TableCell>Counter</TableCell>
                      <TableCell align="right">Count</TableCell>
                    </TableRow>
                  </TableHead>
                  <TableBody>
                    {keys.map((k) => (
                      <TableRow key={k}>
                        <TableCell sx={{ wordBreak: "break-word" }}>
                          {k}
                        </TableCell>
                        <TableCell
                          align="right"
                          sx={{ fontVariantNumeric: "tabular-nums" }}
                        >
                          {Number(section.counters[k]).toLocaleString()}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </Box>
            )}
          </Box>
        );
      })}
    </Box>
  );
}
