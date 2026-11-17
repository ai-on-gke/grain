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

import { afterEach, describe, expect, it } from "vitest";
import {
  NavConfigService,
  SIDEBAR_HIDDEN_KEY,
  SIDEBAR_ORDER_KEY,
} from "./nav-config.service";

describe("NavConfigService", () => {
  afterEach(() => localStorage.clear());

  it("follows Customize navigation saved in another tab", () => {
    const svc = new NavConfigService();
    expect(svc.hidden().has("ci")).toBe(false);

    // Another tab saves: localStorage changes under us and the browser
    // fires a storage event in this tab.
    localStorage.setItem(SIDEBAR_HIDDEN_KEY, JSON.stringify(["ci"]));
    window.dispatchEvent(
      new StorageEvent("storage", { key: SIDEBAR_HIDDEN_KEY }),
    );
    expect(svc.hidden().has("ci")).toBe(true);
    expect(svc.visibleDestinations().some((d) => d.id === "ci")).toBe(false);

    localStorage.setItem(
      SIDEBAR_ORDER_KEY,
      JSON.stringify(["inbox", "sessions", "tasks"]),
    );
    svc.onStorage({ key: SIDEBAR_ORDER_KEY });
    expect(svc.order().slice(0, 3)).toEqual(["inbox", "sessions", "tasks"]);

    // localStorage.clear() in another tab resets both.
    localStorage.clear();
    svc.onStorage({ key: null });
    expect(svc.hidden().size).toBe(0);
    expect(svc.order()[1]).not.toBe("sessions");
  });

  it("ignores other keys", () => {
    const svc = new NavConfigService();
    localStorage.setItem(SIDEBAR_HIDDEN_KEY, JSON.stringify(["ci"]));
    svc.onStorage({ key: "grain.themeMode" });
    expect(svc.hidden().has("ci")).toBe(false);
  });
});
