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
// The wrapper then carries that margin instead of the button: a refused
// button keeps none of its own, or it would count twice (SuiteOverlay's
// "+ New template" sat 6px low and pushed the footer down 14px).
//
// The wrapper is the Stack's child now, not the control: a Stack zeroes
// its own children's margins, so an `sx` margin on the control that did
// nothing ungated suddenly applies inside the wrapper and pushes every
// sibling below it down. Give a gated control no margin of its own. The
// wrapper's child grows to fill it (style.css's .access-gate), so a row
// that stretches its items -- a footer whose neighbour wrapped onto two
// lines -- stretches a refused button as it would an allowed one.
export default function AccessGate({
  scope,
  repo,
  config,
  hide = false,
  wrapperStyle,
  children,
}) {
  const { reason } = useAccess(config);
  const scopes = Array.isArray(scope) ? scope.filter(Boolean) : [scope];
  let deniedScope = "";
  let why = "";
  for (const sc of scopes) {
    const r = reason(sc, repo);
    if (r) {
      deniedScope = sc;
      why = r;
      break;
    }
  }
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
        data-access-denied={deniedScope}
        tabIndex={0}
        style={{
          display: "inline-flex",
          flexDirection: "column",
          ...wrapperStyle,
        }}
      >
        {cloneElement(child, {
          disabled: true,
          title: undefined,
          ...(wrapperStyle && { style: { ...child.props.style, margin: 0 } }),
        })}
      </span>
    </Tooltip>
  );
}
