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

import { Box, Chip, IconButton, Tooltip, Typography } from "@mui/material";
import CloseIcon from "@mui/icons-material/Close";
import WarningAmberIcon from "@mui/icons-material/WarningAmber";
import InfoPopover from "./InfoPopover.jsx";

export const sectionSx = {
  border: 1,
  borderColor: "divider",
  borderRadius: 1,
  minWidth: 0,
};

// GrantLine is a grant with nothing to set, on one line: its label (the
// whole grant in its tooltip), a caption, a warning badge, and remove.
export default function GrantLine({
  grant,
  kind,
  label,
  ariaLabel,
  tip,
  caption,
  info,
  badge,
  onRemove,
}) {
  return (
    <Box
      role="group"
      data-grant={grant}
      data-grant-kind={kind}
      aria-label={ariaLabel}
      className="grant-line"
      sx={{
        ...sectionSx,
        display: "flex",
        alignItems: "center",
        gap: 1,
        pl: 1,
        pr: onRemove ? 0.25 : 1,
        py: 0.25,
        minHeight: 32,
      }}
    >
      <Tooltip
        title={
          <Box
            component="span"
            sx={{
              display: "block",
              whiteSpace: "pre-line",
              wordBreak: "break-word",
            }}
            data-grant-tooltip
          >
            {tip}
          </Box>
        }
        describeChild
      >
        <Typography
          variant="body2"
          component="span"
          tabIndex={0}
          className="grant-label"
          noWrap
          sx={{ flexShrink: 1, minWidth: 0, fontWeight: 500 }}
        >
          {label}
        </Typography>
      </Tooltip>
      <InfoPopover title={info} label={label} />
      {caption && (
        <Typography
          variant="caption"
          color="text.secondary"
          noWrap
          sx={{ flex: "1 1 0", minWidth: 0 }}
        >
          {caption}
        </Typography>
      )}
      {!caption && <Box sx={{ flex: "1 1 0" }} />}
      {badge && (
        <Chip
          size="small"
          variant="outlined"
          color={badge.color}
          icon={<WarningAmberIcon />}
          label={badge.label}
          className="grant-badge"
          sx={{ height: 20, fontSize: "0.7rem", flexShrink: 0 }}
        />
      )}
      {onRemove && <RemoveButton label={ariaLabel} onClick={onRemove} />}
    </Box>
  );
}

export function RemoveButton({ label, onClick }) {
  return (
    <Tooltip title="Remove">
      <IconButton
        size="small"
        aria-label={`Remove ${label}`}
        className="grant-remove"
        onClick={onClick}
        sx={{ flexShrink: 0 }}
      >
        <CloseIcon fontSize="small" />
      </IconButton>
    </Tooltip>
  );
}
