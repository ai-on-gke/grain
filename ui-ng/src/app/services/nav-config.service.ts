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

import { Injectable, computed, signal } from "@angular/core";
import { DESTINATIONS, type Destination, SETTINGS } from "../shell/nav";
import { readStorage, writeStorage } from "./theme.service";

export const SIDEBAR_HIDDEN_KEY = "grain.sidebar.hidden";
export const SIDEBAR_ORDER_KEY = "grain.sidebar.order";
const LEGACY_NAV_ORDER_KEY = "grain.nav.order";

const DEFAULT_IDS = DESTINATIONS.map((d) => d.id);
const BY_ID = new Map<string, Destination>(DESTINATIONS.map((d) => [d.id, d]));

export function parseNavOrder(raw: string | null): string[] {
  let parsed: unknown = null;
  if (raw) {
    try {
      parsed = JSON.parse(raw);
    } catch {
      parsed = null;
    }
  }
  const rest: string[] = [];
  if (Array.isArray(parsed)) {
    for (const item of parsed) {
      const id = String(item);
      if (id !== "inbox" && BY_ID.has(id) && !rest.includes(id)) {
        rest.push(id);
      }
    }
  }
  for (const id of DEFAULT_IDS) {
    if (id !== "inbox" && !rest.includes(id)) {
      rest.push(id);
    }
  }
  // Inbox is always pinned first on the rail (spec D-27).
  return ["inbox", ...rest];
}

export function parseNavHidden(raw: string | null): string[] {
  if (!raw) return [];
  try {
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return [];
    const seen = new Set<string>();
    const out: string[] = [];
    for (const item of parsed) {
      const id = String(item);
      if (id !== "inbox" && BY_ID.has(id) && !seen.has(id)) {
        seen.add(id);
        out.push(id);
      }
    }
    return out;
  } catch {
    return [];
  }
}

// NavConfigService manages per-browser rail customization (spec D-27):
// hiding/showing and reordering non-pinned rail destinations while keeping
// Inbox pinned first and Settings pinned at the bottom.
@Injectable({ providedIn: "root" })
export class NavConfigService {
  private readonly _order = signal<readonly string[]>(
    parseNavOrder(
      readStorage(SIDEBAR_ORDER_KEY) ?? readStorage(LEGACY_NAV_ORDER_KEY),
    ),
  );
  private readonly _hidden = signal<ReadonlySet<string>>(
    new Set(parseNavHidden(readStorage(SIDEBAR_HIDDEN_KEY))),
  );

  readonly order = this._order.asReadonly();
  readonly hidden = this._hidden.asReadonly();
  readonly dialogOpen = signal(false);
  private opener: HTMLElement | null = null;

  openDialog(opener?: HTMLElement | null): void {
    this.opener = opener ?? null;
    this.dialogOpen.set(true);
  }

  closeDialog(): void {
    this.dialogOpen.set(false);
    const target = this.opener;
    this.opener = null;
    if (target && typeof target.focus === "function") {
      setTimeout(() => target.focus());
    }
  }

  readonly orderedDestinations = computed<Destination[]>(() =>
    this._order()
      .map((id) => BY_ID.get(id))
      .filter((d): d is Destination => !!d),
  );

  readonly visibleDestinations = computed<Destination[]>(() => {
    const h = this._hidden();
    return this.orderedDestinations().filter((d) => !h.has(d.id));
  });

  readonly hiddenDestinations = computed<Destination[]>(() => {
    const h = this._hidden();
    return this.orderedDestinations().filter((d) => h.has(d.id));
  });

  readonly phoneDestinations = computed<Destination[]>(() =>
    DESTINATIONS.filter((d) => d.phone),
  );

  readonly phoneMoreDestinations = computed<Destination[]>(() => {
    const onPhone = new Set(this.phoneDestinations().map((d) => d.id));
    return [
      ...this.orderedDestinations().filter((d) => !onPhone.has(d.id)),
      SETTINGS,
    ];
  });

  save(orderIds: readonly string[], hiddenIds: readonly string[]): void {
    const normalizedOrder = parseNavOrder(JSON.stringify(orderIds));
    const normalizedHidden = parseNavHidden(JSON.stringify(hiddenIds));
    this._order.set(normalizedOrder);
    this._hidden.set(new Set(normalizedHidden));
    writeStorage(SIDEBAR_ORDER_KEY, JSON.stringify(normalizedOrder));
    writeStorage(SIDEBAR_HIDDEN_KEY, JSON.stringify(normalizedHidden));
  }

  reset(): void {
    this._order.set([...DEFAULT_IDS]);
    this._hidden.set(new Set());
    writeStorage(SIDEBAR_ORDER_KEY, null);
    writeStorage(LEGACY_NAV_ORDER_KEY, null);
    writeStorage(SIDEBAR_HIDDEN_KEY, JSON.stringify([]));
  }
}
