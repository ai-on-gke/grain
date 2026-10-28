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

import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { Alert, Box } from "@mui/material";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { formatTime } from "../time.js";
import { relativeAge } from "../state.js";

// DisconnectedBanner is what App.jsx's `disconnection` state becomes on
// screen: every request to the daemon has been failing in the way
// api.js calls unreachable -- a dead connection, a timeout, or a gateway
// in front of the daemon saying the backend isn't there, as opposed to
// the daemon itself answering a request with a problem (grain/task-26).
//
// That distinction is the point of a dedicated banner rather than the
// ordinary five-second ErrorBanner toast: the operator who restarted a
// daemon, suspended a host, or hit a Kubernetes rollout's own gap
// between endpoints needs to know the browser gave up trying to talk to
// anything at all, not that one request answered badly. Everything
// already on screen is left exactly as it was -- App.jsx's poll never
// clears tasks, config or any other state on a failed tick -- so this
// is the one thing that says that data is now however old `since` says,
// not current.
//
// The one standing banner left, above the top bar (grain/task-4008):
// unlike a dead reconcile loop or an agent's own pause -- rows in the
// alerts panel now (alerts.js) -- this is not a fact about the
// deployment's own state. It is that nothing asked could get an answer,
// which makes every alert's own claim about that state unverifiable for
// as long as it lasts.
//
// A disconnection that began after the daemon said it was restarting
// (App.jsx's restartingRef) carries `restarting`, and says so: an
// upgrade's restart is expected, and "cannot be reached" would read as
// an outage.
//
// role="status" rather than Alert's own "alert" (grain/task-1854): it is
// news, not an interruption, and a screen reader should hear it once
// when it appears rather than have it re-announced over whatever the
// reader is doing. It takes itself down on the first answer, so it has
// no close button to offer.
export default function DisconnectedBanner({ disconnection }) {
  const zone = useTimeZone();
  // The copy inside an open modal says when, not how long ago: it is a
  // live region, and an age that ticks over on the next render would be
  // a fresh announcement every minute of the outage.
  const mirrored = disconnection ? bannerText(disconnection, zone, false) : "";
  return (
    <>
      {disconnection && (
        <Alert
          role="status"
          data-testid="disconnected-banner"
          severity="warning"
          variant="filled"
          sx={{
            borderRadius: 0,
            justifyContent: "center",
          }}
        >
          {bannerText(disconnection, zone, true)}
        </Alert>
      )}
      <ModalStatusMirror text={mirrored} />
    </>
  );
}

function bannerText({ reason, since, restarting }, zone, withAge) {
  const at = formatTime(since, zone);
  const age = relativeAge(since);
  const sinceText =
    at && (age === "now" || !withAge)
      ? ` since ${at}`
      : at
        ? ` since ${at} (${age} ago)`
        : "";
  return restarting
    ? `${reason} Unavailable${sinceText} -- retrying automatically; this ` +
        "page will pick back up on its own once grain is back."
    : `${reason} Unreachable${sinceText} -- retrying automatically; this ` +
        "page will pick back up on its own once it answers again.";
}

// ModalStatusMirror is the banner's second voice, for while a modal is
// open (found in the 2026-10-15 intake walkthrough). MUI's Modal sets
// aria-hidden="true" on every sibling of its own mount under <body> --
// the app shell, and the banner in it, included -- so a screen reader
// working in a task pane, New task, Bulk edit or any other Dialog,
// Drawer or Popover never heard the deployment go away, and the banner
// was not in the accessibility tree at all until the modal closed. MUI
// offers no way to exempt one element from that sweep.
//
// So while a modal is open this puts a role="status" inside it -- in its
// role="dialog" element where it has one, since a reader honouring
// aria-modal ignores anything outside that -- carrying the banner's
// words. The region is there, empty, for as long as the modal is,
// whether or not anything is wrong yet: a live region that already
// exists when its text changes is what a screen reader reliably
// announces, where one inserted with its text already in it often is
// not.
//
// In a Dialog it is also what a sighted reader sees (found in the
// 2026-10-26 responsive navigation walkthrough): a task pane, New task
// or a session covers the shell banner outright on a phone, and
// everything but its rail-width sliver on a desktop, so the outage
// showed nowhere but the ErrorBanner toast of whichever request failed.
// There the mirror is a compact strip first in the paper, sticky at its
// top, so it pushes the pane's header -- and its Back -- down rather
// than covering it. A Drawer that covers the banner the same way says so
// with data-disconnected-strip (SidePanel, which New task is); the rest
// -- PhoneNav's menu, the alerts popover -- leave the banner showing
// beside them and keep the hidden copy.
//
// The topmost modal is the last one MUI mounted under <body> and has not
// itself hidden: opening a second modal over a first hides the first.
function ModalStatusMirror({ text }) {
  const [host, setHost] = useState(null);
  const [slot, setSlot] = useState(null);
  useEffect(() => {
    // Only <body>'s own children are watched -- a modal's mount arriving
    // or leaving, and MUI's aria-hidden (or a kept-mounted modal's
    // MuiModal-hidden class) on each -- never the page's subtree, whose
    // class names change on nearly every render.
    const observer = new MutationObserver(() => find());
    const find = () => {
      for (const child of document.body.children) {
        observer.observe(child, {
          attributes: true,
          attributeFilter: ["aria-hidden", "class"],
        });
      }
      const open = document.body.querySelectorAll(
        ':scope > .MuiModal-root:not(.MuiModal-hidden):not([aria-hidden="true"])',
      );
      const modal = open[open.length - 1];
      setHost(modal ? (modal.querySelector('[role="dialog"]') ?? modal) : null);
    };
    observer.observe(document.body, { childList: true });
    find();
    return () => observer.disconnect();
  }, []);
  const inDialog = Boolean(host?.matches(STRIP_HOSTS));
  // The strip has to come before the paper's own children to sit above
  // them, and a portal only appends, so it renders into a slot of its
  // own put first. React leaves a node it did not create alone.
  useEffect(() => {
    if (!host) {
      setSlot(null);
      return undefined;
    }
    const el = document.createElement("div");
    if (inDialog) {
      Object.assign(el.style, {
        position: "sticky",
        top: "0",
        zIndex: "1",
        flex: "none",
      });
      host.prepend(el);
    } else {
      host.append(el);
    }
    setSlot(el);
    return () => el.remove();
  }, [host, inDialog]);
  if (!slot) return null;
  // One element whichever way it looks, so the live region is the same
  // node before and after its text arrives.
  const visible = inDialog && text !== "";
  return createPortal(
    <Alert
      role="status"
      data-testid="disconnected-banner-mirror"
      severity="warning"
      variant="filled"
      icon={false}
      sx={visible ? strip : visuallyHidden}
    >
      {text}
    </Alert>,
    slot,
  );
}

const STRIP_HOSTS = ".MuiDialog-paper, [data-disconnected-strip]";

const strip = {
  borderRadius: 0,
  justifyContent: "center",
  py: 0,
  px: 2,
  typography: "body2",
  // Clear of Overlay's centred Dialog's close button, which floats over
  // the paper's top-right corner rather than sitting in a header.
  '.MuiDialog-paper:has(> [aria-label="Close dialog"]) > div > &': {
    px: 6,
  },
};

// The same visually hidden shape as AlertsButton's announcer, pixels
// spelled out for the same reason: in sx a bare 1 is 100%.
const visuallyHidden = {
  position: "absolute",
  width: "1px",
  height: "1px",
  overflow: "hidden",
  clip: "rect(0 0 0 0)",
  whiteSpace: "nowrap",
};
