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

const SERVICE_ACCOUNT_DOMAIN = /\.gserviceaccount\.com$/;
const EMAIL = /^[^\s@/,;'"<>]+@[^\s@/,;'"<>]+$/;

export interface PrincipalEntryCheck {
  canonical: string;
  error: string;
}

// validatePrincipalEntry checks an allowlist entry the way the server
// will (pkg/access), so Grant access can say what is wrong beside the box
// rather than after a round trip: `user:<email>`, `group:<email>` or
// `serviceAccount:<address>` (a *.gserviceaccount.com address), with the
// address lowercased. It is classic's AccessSection validatePrincipalEntry.
export function validatePrincipalEntry(raw: string): PrincipalEntryCheck {
  const trimmed = (raw ?? "").trim();
  if (!trimmed) {
    return {
      canonical: "",
      error: "Enter user:<email>, group:<email> or serviceAccount:<email>.",
    };
  }
  const colon = trimmed.indexOf(":");
  if (colon <= 0) {
    return {
      canonical: "",
      error:
        "Entry must start with user:, group: or serviceAccount: (for example, user:alice@example.com).",
    };
  }
  const prefix = trimmed.slice(0, colon).trim();
  const value = trimmed
    .slice(colon + 1)
    .trim()
    .toLowerCase();
  if (prefix === "domain") {
    return {
      canonical: "",
      error:
        "domain: entries are not supported — use user:<email>, group:<email> or serviceAccount:<email>.",
    };
  }
  if (prefix !== "user" && prefix !== "group" && prefix !== "serviceAccount") {
    return {
      canonical: "",
      error: `Unknown prefix "${prefix}:" — expected user:, group: or serviceAccount:.`,
    };
  }
  if (!value || !EMAIL.test(value)) {
    return {
      canonical: "",
      error: `${prefix}: entry must be a valid email address.`,
    };
  }
  const isSA = SERVICE_ACCOUNT_DOMAIN.test(value);
  if (prefix === "serviceAccount" && !isSA) {
    return {
      canonical: "",
      error:
        "serviceAccount: entries must name a service account address (*.gserviceaccount.com).",
    };
  }
  if (prefix !== "serviceAccount" && isSA) {
    return {
      canonical: "",
      error: `Service account addresses must be added as serviceAccount:${value}.`,
    };
  }
  return { canonical: `${prefix}:${value}`, error: "" };
}
