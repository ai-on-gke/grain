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

import { Alert, Portal, Snackbar } from "@mui/material";

// Portalled to document.body rather than rendered where App mounts it.
// Most of the errors this reports come from inside a modal -- the New
// task panel, a task pane -- and an open MUI modal marks every other
// child of <body> aria-hidden, #root included. Rendered in place, the
// alert was drawn above the modal (the snackbar z-index below) but sat
// inside that aria-hidden #root, so a screen reader never heard it and
// the alert role was not in the accessibility tree at all. A portal
// mounted after the modal opened is a sibling the modal did not hide.
//
// The message scrolls inside a box at most 40% of the window tall. A
// refused template delete names every suite and schedule in the way, and
// with 25 of them (2026-10-07 standing-work walkthrough) the alert grew
// taller than a 375 px phone's screen: its first line, the one saying
// what was refused, sat above the top edge, and the alert covered every
// control on the page until it went away.
export default function ErrorBanner({ message }) {
  return (
    <Portal>
      <Snackbar
        open
        anchorOrigin={{ vertical: "bottom", horizontal: "center" }}
        sx={{ zIndex: (theme) => theme.zIndex.snackbar }}
      >
        <Alert
          severity="error"
          variant="filled"
          sx={{
            maxWidth: "min(90vw, 480px)",
            "& .MuiAlert-message": { maxHeight: "40vh", overflowY: "auto" },
          }}
        >
          {message}
        </Alert>
      </Snackbar>
    </Portal>
  );
}
