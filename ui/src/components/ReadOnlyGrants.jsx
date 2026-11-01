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

import { Box, Typography } from "@mui/material";
import GrantChip from "./GrantChip.jsx";
import ConfigRepoLink, { useStateRepoFiles } from "./ConfigRepoLink.jsx";

// CAPABILITIES_FILE is the config repo file default and CI grants live
// in once a deployment has adopted the capability catalogue
// (design/design/config-repo-settings.design.md §3).
export const CAPABILITIES_FILE = "settings/capabilities.yml";

// ReadOnlyGrants is a list of default grants as chips, with an optional
// link to settings/capabilities.yml when that file exists: what
// Settings → Tasks and a repository's page show where they used to offer
// a GrantsPicker (design/design/config-repo-settings.design.md §7.4). Settings →
// Tasks suppresses the per-card link (`showLink={false}`) because the
// section's ConfigManagedNotice already links its config-repo files.
export default function ReadOnlyGrants({
  grants = [],
  config,
  empty = "None",
  testId = "read-only-grants",
  showLink = true,
}) {
  const present = useStateRepoFiles(showLink);
  const canLinkCatalogue =
    showLink && (!present || present.has(CAPABILITIES_FILE));
  return (
    <Box data-testid={testId}>
      {grants.length === 0 ? (
        <Typography variant="body2" color="text.secondary">
          {empty}
        </Typography>
      ) : (
        <Box
          component="ul"
          aria-label="Grants"
          sx={{
            display: "flex",
            flexWrap: "wrap",
            gap: 0.5,
            listStyle: "none",
            m: 0,
            p: 0,
          }}
        >
          {grants.map((g) => (
            <li key={g}>
              <GrantChip grant={g} config={config} />
            </li>
          ))}
        </Box>
      )}
      {canLinkCatalogue && (
        <Typography variant="body2" sx={{ mt: 0.5 }}>
          <ConfigRepoLink
            path={CAPABILITIES_FILE}
            label="Edit in config repo"
          />
        </Typography>
      )}
    </Box>
  );
}
