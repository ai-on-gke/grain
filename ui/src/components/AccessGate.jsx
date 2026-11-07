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

import { Children, cloneElement, isValidElement } from "react";
import { Tooltip } from "@mui/material";
import { useAccess } from "../AccessContext.jsx";

// AccessGate is a write control drawn for what the signed-in person's
// role covers (access.js): its one child -- a Button, usually -- as it
// is when the role holds `scope` (on `repo`, for a role limited to some
// repositories), and otherwise disabled, with a tooltip naming the
// missing scope and the role, or left out altogether with `hide`. The
// server refuses the call either way; this is so the page does not offer
// what it will refuse.
//
// `config` overrides the AccessConfigProvider's, for a component already
// handed one. `wrapperStyle` styles the wrapper a refused control is
// drawn in -- a margin the button itself carried to place it in a row.
export default function AccessGate({
  scope,
  repo,
  config,
  hide = false,
  wrapperStyle,
  children,
}) {
  const { reason } = useAccess(config);
  const why = reason(scope, repo);
  if (!why) return children;
  if (hide) return null;
  const child = Children.only(children);
  if (!isValidElement(child)) return null;
  return (
    // A disabled button takes no pointer events, so the tooltip hangs on
    // a wrapper. inline-flex in a column keeps the button's own width in
    // a row and lets it stretch where its parent stretches its children
    // (the task pane's action column).
    <Tooltip title={why} describeChild>
      <span
        className="access-gate"
        data-access-denied={scope}
        tabIndex={0}
        style={{
          display: "inline-flex",
          flexDirection: "column",
          ...wrapperStyle,
        }}
      >
        {cloneElement(child, { disabled: true, title: undefined })}
      </span>
    </Tooltip>
  );
}
