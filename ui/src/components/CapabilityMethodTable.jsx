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

import {
  Box,
  Chip,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Typography,
} from "@mui/material";
import { argumentRows } from "../capabilityArgs.js";
import InfoPopover from "./InfoPopover.jsx";

// CapabilityMethodTable is one attached server's methods as Settings →
// Capability servers lists them (servers.Status.methodDetails): each
// method's description, whether it is stateful (advertises
// release_<method>), and its with: arguments -- name, type, whether
// required, default, and the values or pattern it allows.
export default function CapabilityMethodTable({ details }) {
  return (
    <Box sx={{ mt: 1, display: "flex", flexDirection: "column", gap: 1.5 }}>
      {details.map((m) => {
        const rows = argumentRows(m.args);
        return (
          <Box key={m.name} data-method-detail={m.name}>
            <Box
              sx={{
                display: "flex",
                alignItems: "center",
                gap: 0.5,
                flexWrap: "wrap",
              }}
            >
              <Typography
                variant="body2"
                sx={{ fontFamily: "monospace", fontWeight: 600 }}
              >
                {m.name}
              </Typography>
              <InfoPopover title={m.description} label={m.name} />
              <Chip
                size="small"
                variant="outlined"
                label={m.releasable ? "stateful" : "stateless"}
                title={
                  m.releasable
                    ? `Advertises release_${m.name}: what it mints is released when the run ends.`
                    : "Advertises no release_ tool: nothing is held between mint and teardown."
                }
              />
            </Box>
            {rows.length === 0 ? (
              <Typography variant="caption" color="text.secondary">
                Takes no arguments.
              </Typography>
            ) : (
              <Box sx={{ overflowX: "auto" }}>
                <Table
                  size="small"
                  aria-label={`${m.name} arguments`}
                  sx={{
                    minWidth: 560,
                    "& td, & th": { px: 1, py: 0.5 },
                    "& th": { whiteSpace: "nowrap" },
                  }}
                >
                  <TableHead>
                    <TableRow>
                      <TableCell>Argument</TableCell>
                      <TableCell>Type</TableCell>
                      <TableCell>Required</TableCell>
                      <TableCell>Default</TableCell>
                      <TableCell>Allowed</TableCell>
                    </TableRow>
                  </TableHead>
                  <TableBody>
                    {rows.map((r) => (
                      <TableRow key={r.name} title={r.description || undefined}>
                        <TableCell
                          sx={{ fontFamily: "monospace", whiteSpace: "nowrap" }}
                        >
                          {r.name}
                        </TableCell>
                        <TableCell sx={{ fontFamily: "monospace" }}>
                          {r.type}
                        </TableCell>
                        <TableCell>{r.required ? "yes" : "no"}</TableCell>
                        <TableCell
                          sx={{
                            fontFamily: "monospace",
                            overflowWrap: "anywhere",
                          }}
                        >
                          {r.default}
                        </TableCell>
                        <TableCell
                          sx={{
                            fontFamily: "monospace",
                            overflowWrap: "anywhere",
                          }}
                        >
                          {r.allowed}
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
