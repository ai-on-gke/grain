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
import { Box, Link, Typography } from "@mui/material";
import UpgradePanel from "../UpgradePanel.jsx";
import { InstallNoticeAlert } from "../InstallNotice.jsx";

// UpgradeSection is Settings → Upgrade (/settings/upgrade): choosing a
// channel, branch or image to run, its release notes, upgrading now or
// once tasks finish, cancelling a scheduled upgrade, watching the
// restart, and rolling back. It was a System tab (/system/upgrade) and
// moved here so the deployment's own version sits beside the rest of
// its configuration; the old path redirects (settingsSections.js).
//
// It is an action rather than a saved setting, so it takes no `s`: it
// never marks a card dirty, never joins the save bar, and is never
// read-only under an external config repo. Who may upgrade is still
// the daemon's to say -- POST /api/upgrade needs deployment:upgrade --
// so drawing it in Settings shows it to nobody it was hidden from.
export default function UpgradeSection({ config, showError, onOpenUpdates }) {
  // UpgradePanel remounts after a replace (onReplaced), so it rereads
  // the status of the build that replaced it.
  const [upgradeKey, setUpgradeKey] = useState(0);
  return (
    <Box id="upgrade" data-testid="upgrade-section">
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        What grain does after an upgrade lands is set in{" "}
        <Link
          component="button"
          type="button"
          onClick={() => onOpenUpdates?.()}
          data-testid="upgrade-check-settings-link"
        >
          Settings → General → Updates
        </Link>
        .
      </Typography>
      {/* Before any upgrade is picked: this build expects container
        mode, and the daemon refuses an in-place upgrade of a
        host-mode install until the installer has migrated it. */}
      <InstallNoticeAlert
        notice={config?.installNotice}
        testId="upgrade-install-notice"
      />
      <UpgradePanel
        key={upgradeKey}
        config={config}
        onReplaced={() => setUpgradeKey((k) => k + 1)}
        showError={showError}
        version={config?.version}
      />
    </Box>
  );
}
