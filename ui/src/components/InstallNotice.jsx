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

import { Alert, AlertTitle, Box, Link } from "@mui/material";

// InstallNotice is GET /api/config's installNotice on screen: something
// about how this deployment is installed that only its operator can fix
// -- today a host-mode install no installer has migrated, still running
// on the bare machine after an upgrade through the UI or by hand
// (pkg/daemon's installnotice.go). It names what fixes it, the CLIs that
// cannot run until then, and where the migration is written up.
//
// InstallNoticeBanner is the standing strip above the top bar; it has no
// close button, since nothing changes until the install is migrated and
// the daemon restarted. InstallNoticeAlert is the same notice in full, for
// System → Health and Settings → Upgrade, where an in-place upgrade is
// refused until it is migrated.

function DocsLink({ notice }) {
  if (!notice.docsUrl) return null;
  return (
    <Link
      href={notice.docsUrl}
      target="_blank"
      rel="noreferrer"
      color="inherit"
      underline="always"
      data-testid="install-notice-docs"
    >
      Migration docs
    </Link>
  );
}

function capitalise(s) {
  return s ? s[0].toUpperCase() + s.slice(1) : s;
}

export function InstallNoticeBanner({ notice }) {
  if (!notice) return null;
  const clis = notice.affectedClis || [];
  return (
    <Alert
      role="status"
      data-testid="install-notice-banner"
      severity="error"
      variant="filled"
      sx={{
        borderRadius: 0,
        justifyContent: "center",
        "& .MuiAlert-message": { minWidth: 0, overflowWrap: "anywhere" },
      }}
    >
      {capitalise(notice.message)}.{" "}
      {clis.length > 0 && (
        <>
          Cannot run until then:{" "}
          <span data-testid="install-notice-banner-clis">
            {clis.map((c) => c.split(":")[0]).join(", ")}
          </span>
          .{" "}
        </>
      )}
      <DocsLink notice={notice} />
    </Alert>
  );
}

export function InstallNoticeAlert({ notice, testId = "install-notice" }) {
  if (!notice) return null;
  const clis = notice.affectedClis || [];
  return (
    <Alert
      severity="error"
      sx={{
        mb: 1,
        "& .MuiAlert-message": { minWidth: 0, overflowWrap: "anywhere" },
      }}
      data-testid={testId}
    >
      <AlertTitle>Unmigrated host-mode install</AlertTitle>
      {capitalise(notice.message)}.
      {notice.command && (
        <Box sx={{ mt: 0.5 }}>
          Run{" "}
          <Box component="code" data-testid="install-notice-command">
            {notice.command}
          </Box>
          .
        </Box>
      )}
      {clis.length > 0 && (
        <Box sx={{ mt: 0.5 }}>
          CLIs that lost their run location:
          <Box
            component="ul"
            sx={{ my: 0.25, pl: 2.5 }}
            data-testid="install-notice-clis"
          >
            {clis.map((c) => (
              <li key={c}>{c}</li>
            ))}
          </Box>
        </Box>
      )}
      <Box sx={{ mt: 0.5 }}>
        <DocsLink notice={notice} />
      </Box>
    </Alert>
  );
}
