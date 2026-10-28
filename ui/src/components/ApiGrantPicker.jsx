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
  Accordion,
  AccordionDetails,
  AccordionSummary,
  Box,
  Checkbox,
  Chip,
  FormControlLabel,
  MenuItem,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import ExpandMoreIcon from "@mui/icons-material/ExpandMore";
import WarningAmberIcon from "@mui/icons-material/WarningAmber";
import {
  DANGEROUS_API_HINT,
  apiGrantId,
  apiShortName,
  groupApiRows,
  splitApiGrant,
} from "../apiGrants.js";

// ApiGrantPicker is the "Grain API" group of a capability picker: the
// agent API's presets and scopes (docs/agent-api.md, ui.APICapabilities)
// drawn as one collapsible group -- presets first, each with what it
// expands to, then the scopes by area, the dangerous ones flagged --
// rather than as fifty rows of the generic multi-select beside it.
//
// `selected` is the api grant names the task, template, schedule or
// session holds (apiGrants.js isApiGrant); onChange(next) is called with
// the whole new list, and where it lands is the caller's. A grant can be
// limited to one repository: the repository picked here is the one a
// tick writes (api:<name>@owner/repo), and every grant already held,
// limited or not, is a chip above the lists that can be removed.
export default function ApiGrantPicker({
  config,
  selected = [],
  onChange,
  idPrefix,
  disabled = false,
}) {
  const offered = config?.capabilities || [];
  const { presets, areas } = groupApiRows(offered);
  const repos = config?.targetRepos || [];
  const [repo, setRepo] = useState("");
  if (presets.length === 0 && areas.length === 0 && selected.length === 0) {
    return null;
  }
  const rowOf = (id) => offered.find((c) => c.id === splitApiGrant(id)[0]);
  const toggle = (rowId, on) => {
    const id = apiGrantId(rowId, repo);
    onChange(
      on
        ? [...selected.filter((s) => s !== id), id]
        : selected.filter((s) => s !== id),
    );
  };
  const regionId = `${idPrefix}-api-grants`;

  const line = (row) => {
    const id = apiGrantId(row.id, repo);
    const name = apiShortName(row.id);
    return (
      <Box
        key={row.id}
        className="api-grant-row"
        data-api-grant={row.id}
        data-api-kind={row.preset ? "preset" : "scope"}
        sx={{ mb: 0.5 }}
      >
        <FormControlLabel
          sx={{ display: "flex", alignItems: "flex-start", mr: 0 }}
          control={
            <Checkbox
              size="small"
              sx={{ pt: 0.25 }}
              checked={selected.includes(id)}
              disabled={disabled}
              onChange={(e) => toggle(row.id, e.target.checked)}
              inputProps={{ "aria-label": name }}
            />
          }
          title={row.description}
          label={
            <Box component="span" sx={{ display: "block", pt: 0.25 }}>
              <Box
                component="span"
                sx={{
                  display: "flex",
                  flexWrap: "wrap",
                  alignItems: "center",
                  gap: 0.5,
                }}
              >
                <Box component="code" sx={{ fontSize: "0.85rem" }}>
                  {name}
                </Box>
                {row.dangerous && <DangerousChip />}
              </Box>
              <Typography
                variant="caption"
                color="text.secondary"
                component="span"
                sx={{ display: "block" }}
              >
                {row.summary || row.description}
              </Typography>
              {row.preset && (row.scopes || []).length > 0 && (
                <Box
                  component="span"
                  className="api-preset-scopes"
                  aria-label={`${name} grants`}
                  sx={{ display: "flex", flexWrap: "wrap", gap: 0.5, mt: 0.25 }}
                >
                  {row.scopes.map((s) => (
                    <Chip
                      key={s}
                      size="small"
                      variant="outlined"
                      label={s}
                      sx={{ height: 20, fontSize: "0.7rem" }}
                    />
                  ))}
                </Box>
              )}
            </Box>
          }
        />
      </Box>
    );
  };

  const heading = (text) => (
    <Typography
      variant="overline"
      component="h4"
      color="text.secondary"
      sx={{ display: "block", lineHeight: 1.5, mt: 1 }}
    >
      {text}
    </Typography>
  );

  return (
    <Accordion
      disableGutters
      variant="outlined"
      sx={{ mt: 1 }}
      data-testid={regionId}
      // Mounted only while open: fifty labelled boxes ("templates:read",
      // "API scopes: templates") would otherwise sit hidden in every
      // form, answering any lookup by label meant for the form's own
      // fields ("Template").
      slotProps={{ transition: { unmountOnExit: true } }}
    >
      <AccordionSummary
        expandIcon={<ExpandMoreIcon />}
        aria-controls={`${regionId}-content`}
        id={`${regionId}-header`}
      >
        <Typography sx={{ mr: 1 }}>Grain API</Typography>
        <Typography variant="body2" color="text.secondary">
          {selected.length === 0
            ? "none granted"
            : `${selected.length} granted`}
        </Typography>
      </AccordionSummary>
      <AccordionDetails id={`${regionId}-content`} sx={{ pt: 0 }}>
        <Typography variant="caption" color="text.secondary" component="p">
          Lets the agent call grain&apos;s own API as tools, with the
          permissions ticked here (docs/agent-api.md).
        </Typography>
        {selected.length > 0 && (
          <Box
            className="api-grants-held"
            sx={{ display: "flex", flexWrap: "wrap", gap: 0.5, mt: 1 }}
          >
            {selected.map((id) => {
              const [, limit] = splitApiGrant(id);
              const row = rowOf(id);
              const label = `${apiShortName(id)}${limit ? ` on ${limit}` : ""}`;
              return (
                <Tooltip
                  key={id}
                  title={
                    row
                      ? row.dangerous
                        ? `${DANGEROUS_API_HINT} ${row.description}`
                        : row.description
                      : "No longer offered -- remove it"
                  }
                >
                  <Chip
                    size="small"
                    label={label}
                    color={
                      row?.dangerous ? "warning" : row ? "default" : "error"
                    }
                    icon={row?.dangerous ? <WarningAmberIcon /> : undefined}
                    onDelete={
                      disabled
                        ? undefined
                        : () => onChange(selected.filter((s) => s !== id))
                    }
                  />
                </Tooltip>
              );
            })}
          </Box>
        )}
        {repos.length > 0 && (
          <TextField
            select
            size="small"
            margin="dense"
            fullWidth
            id={`${regionId}-repo`}
            label="Limit what you tick to"
            value={repo}
            onChange={(e) => setRepo(e.target.value)}
            helperText={
              repo
                ? `A tick grants it on ${repo} only (api:<scope>@${repo}).`
                : "A tick grants it on every repository."
            }
            sx={{ mt: 1.5 }}
          >
            <MenuItem value="">All repositories</MenuItem>
            {repos.map((r) => (
              <MenuItem key={r} value={r}>
                {r}
              </MenuItem>
            ))}
          </TextField>
        )}
        {presets.length > 0 && (
          <Box role="group" aria-label="API presets" className="api-presets">
            {heading("Presets")}
            {presets.map(line)}
          </Box>
        )}
        {areas.map(({ area, rows }) => (
          <Box
            key={area}
            role="group"
            aria-label={`API scopes: ${area}`}
            className="api-scope-area"
          >
            {heading(area)}
            {rows.map(line)}
          </Box>
        ))}
      </AccordionDetails>
    </Accordion>
  );
}

// DangerousChip flags a scope only a person may grant.
function DangerousChip() {
  return (
    <Tooltip title={DANGEROUS_API_HINT}>
      <Chip
        size="small"
        color="warning"
        variant="outlined"
        icon={<WarningAmberIcon />}
        label="dangerous"
        className="api-dangerous"
        sx={{ height: 20, fontSize: "0.7rem" }}
      />
    </Tooltip>
  );
}
