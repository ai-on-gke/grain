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
import IconButton from "@mui/material/IconButton";
import Tooltip from "@mui/material/Tooltip";
import InfoOutlinedIcon from "@mui/icons-material/InfoOutlined";

// InfoPopover renders an (i) icon button whose tooltip/popover holds a
// field, method, or server description. It opens on hover, keyboard focus,
// Enter/Space, or touch/click, and uses component="span" so it can sit
// inside links or interactive rows without nesting native <button> elements.
export default function InfoPopover({ title, label, testId, sx }) {
  const [open, setOpen] = useState(false);
  if (!title) return null;
  const cleanLabel =
    typeof label === "string" ? label.replace(/\s*\*$/, "").trim() : "";
  const ariaLabel = cleanLabel ? `About ${cleanLabel}` : "More information";
  return (
    <Tooltip
      open={open}
      onOpen={() => setOpen(true)}
      onClose={() => setOpen(false)}
      title={
        <span style={{ whiteSpace: "pre-line", display: "block" }}>
          {title}
        </span>
      }
      describeChild
      enterDelay={0}
      enterTouchDelay={0}
      leaveTouchDelay={4000}
      slotProps={{
        transition: { timeout: 0 },
        tooltip: {
          sx: { maxWidth: 420, fontSize: "0.75rem", lineHeight: 1.4 },
        },
      }}
    >
      <IconButton
        component="span"
        role="button"
        tabIndex={0}
        size="small"
        aria-label={ariaLabel}
        data-testid={testId || "info-popover"}
        data-info-for={cleanLabel || undefined}
        onFocus={() => setOpen(true)}
        onBlur={() => setOpen(false)}
        onClick={(e) => {
          e.stopPropagation();
          e.preventDefault();
          setOpen((prev) => !prev);
        }}
        onKeyDown={(e) => {
          // Enter and Space reach onClick above through ButtonBase, which
          // clicks a non-native button on Enter's keydown and Space's
          // keyup; toggling here as well would undo that toggle. Only
          // keep the key from the row or link the button sits in.
          if (e.key === "Enter" || e.key === " ") {
            e.stopPropagation();
          } else if (e.key === "Escape") {
            setOpen(false);
          }
        }}
        sx={{
          p: 0.25,
          color: "text.secondary",
          verticalAlign: "middle",
          ...sx,
        }}
      >
        <InfoOutlinedIcon sx={{ fontSize: 16 }} />
      </IconButton>
    </Tooltip>
  );
}
