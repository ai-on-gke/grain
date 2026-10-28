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

import CloseIcon from "@mui/icons-material/Close";
import { useId } from "react";
import { Box, Drawer, IconButton, Typography } from "@mui/material";

// SidePanel is the full-height panel that slides in from the right, for
// a form too long to sit in Overlay's centred Dialog (grain/task-85).
// The Dialog grows with its content and, with scroll="body", takes the
// *page* with it -- so a long form's own submit button ends up below the
// bottom of the window and has to be scrolled to before it can be
// clicked. Here the panel is always exactly the height of the window:
// the header and the footer are pinned to it, and only the column
// between them scrolls, so whatever finishes the form is on screen from
// the moment it opens however much has been typed into it.
//
// onSubmit, when given, makes that middle column plus the footer one
// <form> element -- the footer's button is a type="submit" inside the
// same form as the fields it submits, rather than a sibling of it that
// would need a form= attribute to reach them.
export default function SidePanel({
  title,
  onClose,
  onSubmit,
  footer,
  children,
}) {
  const titleId = useId();
  const body = (
    <Box sx={{ flex: 1, minHeight: 0, overflowY: "auto", px: 3, py: 2 }}>
      {children}
    </Box>
  );
  const foot = footer ? (
    <Box sx={{ px: 3, py: 2, borderTop: 1, borderColor: "divider" }}>
      {footer}
    </Box>
  ) : null;

  return (
    <Drawer
      anchor="right"
      open
      onClose={onClose}
      // A Drawer is a modal -- the page behind it is hidden from
      // assistive tech while it is open -- but, unlike a Dialog, gives
      // its paper no role or name, so a screen reader landed in the form
      // with nothing saying what it was in. Named by its own title, as
      // every Dialog here is.
      PaperProps={{
        role: "dialog",
        "aria-modal": true,
        "aria-labelledby": titleId,
        // It covers the disconnected banner, so DisconnectedBanner shows
        // its strip in here instead.
        "data-disconnected-strip": true,
        sx: { width: { xs: "100%", sm: 560 }, maxWidth: "100%" },
      }}
    >
      {/* The rest of the paper, beneath that strip when it is up. */}
      <Box
        sx={{ display: "flex", flexDirection: "column", flex: 1, minHeight: 0 }}
      >
        <Box
          sx={{
            display: "flex",
            alignItems: "center",
            gap: 1,
            px: 3,
            py: 1.5,
            borderBottom: 1,
            borderColor: "divider",
          }}
        >
          <Typography
            id={titleId}
            variant="h6"
            component="h2"
            sx={{ flex: 1, m: 0, fontSize: "1.05rem", fontWeight: 600 }}
          >
            {title}
          </Typography>
          <IconButton
            aria-label="Close dialog"
            onClick={onClose}
            size="small"
            sx={{ color: "text.secondary" }}
          >
            <CloseIcon fontSize="small" />
          </IconButton>
        </Box>
        {onSubmit ? (
          <Box
            component="form"
            onSubmit={onSubmit}
            sx={{
              display: "flex",
              flexDirection: "column",
              flex: 1,
              minHeight: 0,
            }}
          >
            {body}
            {foot}
          </Box>
        ) : (
          <>
            {body}
            {foot}
          </>
        )}
      </Box>
    </Drawer>
  );
}
