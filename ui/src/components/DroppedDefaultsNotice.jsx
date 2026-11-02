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

import { Alert, AlertTitle, Box, Portal, Snackbar } from "@mui/material";

// droppedDefaults is the deployment and repository default grants the
// capability policy kept off a task POST /api/tasks just filed, each
// {grant, reason} (ui.Task.DroppedDefaults) -- [] for a filing that
// dropped none, or an answer that is not a task at all.
export function droppedDefaults(created) {
  const list = created?.droppedDefaults;
  return Array.isArray(list) ? list.filter((d) => d && d.grant) : [];
}

// DroppedDefaultsNotice is the toast the New task form's filing leaves
// behind when grain filed the task without a default grant the
// capability policy denies it. The task's thread says the same, but a
// person who has just pressed Create is looking at the board, not the
// thread, and expected the default to be there. A warning, not an
// error: the task was filed.
//
// Portalled for ErrorBanner's reason, and rendered by App rather than by
// the form, which closes as the filing succeeds. It stays up longer than
// an error toast, since it names grants and reasons to read, and closes
// on its own button too.
export default function DroppedDefaultsNotice({ created, onClose }) {
  const dropped = droppedDefaults(created);
  if (dropped.length === 0) return null;
  const which =
    dropped.length === 1
      ? "a default grant"
      : `${dropped.length} default grants`;
  return (
    <Portal>
      <Snackbar
        open
        autoHideDuration={15000}
        onClose={(_, reason) => {
          if (reason !== "clickaway") onClose();
        }}
        anchorOrigin={{ vertical: "bottom", horizontal: "center" }}
        sx={{ zIndex: (theme) => theme.zIndex.snackbar }}
      >
        <Alert
          severity="warning"
          variant="filled"
          onClose={onClose}
          data-testid="dropped-defaults-notice"
          sx={{
            maxWidth: "min(90vw, 480px)",
            "& .MuiAlert-message": {
              maxHeight: "40vh",
              overflowY: "auto",
              overflowWrap: "anywhere",
            },
          }}
        >
          <AlertTitle>
            Task #{created.id} was filed without {which}
          </AlertTitle>
          <Box component="ul" sx={{ m: 0, pl: 2 }}>
            {dropped.map((d) => (
              <li key={d.grant}>
                <code>{d.grant}</code>: {d.reason}
              </li>
            ))}
          </Box>
        </Alert>
      </Snackbar>
    </Portal>
  );
}
