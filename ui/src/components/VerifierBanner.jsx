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
import { Alert } from "@mui/material";
import {
  onVerifierTrouble,
  VERIFIER_SUSTAINED_MS,
  verifierSustainedReason,
  verifierTroubleSince,
} from "../api.js";

// VERIFIER_BANNER_TICK_MS is how often the banner's "for 45s" is moved
// on while it is up.
const VERIFIER_BANNER_TICK_MS = 5000;

// useVerifierTrouble is api.js's verifierTroubleSince, as state: when the
// current run of verifier_unavailable refusals began, or null.
export function useVerifierTrouble() {
  const [since, setSince] = useState(verifierTroubleSince);
  useEffect(() => onVerifierTrouble(setSince), []);
  return since;
}

// VerifierBanner says that the sign-in verifier in front of grain's
// allowlist has gone on refusing every request it could not check --
// its identity backend out of reach -- for VERIFIER_SUSTAINED_MS. Before
// that it says nothing: one such refusal is a blip a poll's next tick
// gets past, and the toast and "cannot be reached" banner it used to
// raise read as the daemon having gone away when it was answering all
// along. It is not DisconnectedBanner for the same reason, and its words
// name sign-in verification, not the deployment's reachability. It goes
// the moment any request gets through (api.js clears `since`).
export default function VerifierBanner({ since }) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!since) return;
    setNow(Date.now());
    const wait = Math.max(
      0,
      since.getTime() + VERIFIER_SUSTAINED_MS - Date.now(),
    );
    let interval;
    const first = setTimeout(() => {
      setNow(Date.now());
      interval = setInterval(() => setNow(Date.now()), VERIFIER_BANNER_TICK_MS);
    }, wait);
    return () => {
      clearTimeout(first);
      clearInterval(interval);
    };
  }, [since]);
  if (!since || now - since.getTime() < VERIFIER_SUSTAINED_MS) return null;
  return (
    <Alert
      role="status"
      data-testid="verifier-banner"
      severity="info"
      variant="filled"
      sx={{ borderRadius: 0, justifyContent: "center" }}
    >
      {verifierSustainedReason(since, now)}
    </Alert>
  );
}
