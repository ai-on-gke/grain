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

import { Alert, Box, Link, Typography } from "@mui/material";
import { configBrowserPath } from "../settingsSections.js";

// openInApp follows an in-app link without reloading the page: the
// address changes and App's popstate handler reads it, as it does for
// Back and Forward. A modified click (new tab, new window) is left to
// the browser.
export function openInApp(evt, href) {
  if (
    evt.defaultPrevented ||
    evt.button !== 0 ||
    evt.metaKey ||
    evt.ctrlKey ||
    evt.shiftKey ||
    evt.altKey
  )
    return;
  evt.preventDefault();
  window.history.pushState(window.history.state, "", href);
  window.dispatchEvent(new PopStateEvent("popstate"));
}

// ConfigRepoLink is "View in config repo → <path>", opening path in the
// Config Repo Browser (docs/design/config-repo-settings.md §2.3).
export default function ConfigRepoLink({
  path,
  label = "View in config repo",
}) {
  const href = configBrowserPath(path);
  return (
    <Link
      href={href}
      onClick={(evt) => openInApp(evt, href)}
      data-testid="config-repo-link"
      data-config-path={path}
    >
      {label} → <code>{path}</code>
    </Link>
  );
}

// ConfigManagedNotice is the line a config-repo-only setting leads
// with: the values below are what grain/config holds, read-only here,
// and where to change them.
export function ConfigManagedNotice({
  paths,
  what = "These settings",
  id,
  sx,
}) {
  return (
    <Alert
      id={id}
      severity="info"
      sx={{ mb: 2, textTransform: "none", ...sx }}
      data-testid="config-managed-notice"
    >
      <Typography variant="body2">
        {what} live in the config repo and are read-only here. Change them with
        a config pull request, or ask the Director to.
      </Typography>
      <Box
        component="ul"
        sx={{ m: 0, mt: 0.5, pl: 2.5, "& li": { typography: "body2" } }}
      >
        {paths.map((p) => (
          <li key={p}>
            <ConfigRepoLink path={p} />
          </li>
        ))}
      </Box>
    </Alert>
  );
}
