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

import { Button } from "@mui/material";
import GrainMark from "./GrainMark.jsx";

const MARK_SIZE = 120;

// ConfigUnavailable is what the shell shows in place of LoadingScreen
// once the first load has settled and /api/config is still not answered
// (grain/task-1435). Every pane reads config, so there is still nothing
// to draw -- but a splash that never ends says "wait" to somebody for
// whom waiting will not help. The likeliest cause in practice is
// something in front of grain answering for it: a captive portal, a
// corporate proxy, or an auth front end's sign-in page, all of which
// answer 200 with HTML (api.js's NOT_GRAIN). A reload at the top level
// is what lets any of those finish whatever they wanted to do.
//
// App's poll keeps retrying the config underneath this, so the shell
// replaces it by itself the moment an answer arrives.
export default function ConfigUnavailable({ error }) {
  const notGrain = !!error?.notGrain;
  return (
    <div className="access-denied-screen config-unavailable">
      <GrainMark size={MARK_SIZE} title="grain" />
      <div className="access-denied-content">
        <h2>grain could not load</h2>
        {notGrain ? (
          <p>
            grain&apos;s API answered with something that isn&apos;t grain --
            possibly a proxy or a sign-in page in front of it. Reload to sign in
            again or to let it through.
          </p>
        ) : (
          <p>
            grain&apos;s configuration could not be loaded. It will keep trying;
            reload to try again now.
          </p>
        )}
        {error?.message && (
          <p className="access-denied-user">
            <code>{error.message}</code>
          </p>
        )}
        <Button variant="contained" onClick={() => window.location.reload()}>
          Reload
        </Button>
      </div>
    </div>
  );
}
