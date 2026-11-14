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

import { HttpErrorResponse } from "@angular/common/http";
import { describe, expect, it } from "vitest";
import {
  ConnectionService,
  NETWORK_UNREACHABLE_REASON,
  RESTARTING_REASON,
  ReachTimeoutError,
  isUnreachable,
  unreachableReason,
} from "./connection.service";

const dropped = () => new HttpErrorResponse({ status: 0, url: "/api/config" });

describe("isUnreachable", () => {
  it("counts dead connections, timeouts and bare gateway errors", () => {
    expect(isUnreachable(dropped())).toBe(true);
    expect(isUnreachable(new ReachTimeoutError(10000, 0))).toBe(true);
    expect(
      isUnreachable(new HttpErrorResponse({ status: 502, error: "<html>" })),
    ).toBe(true);
  });

  it("does not count an answer grain itself sent", () => {
    expect(
      isUnreachable(
        new HttpErrorResponse({ status: 503, error: { error: "draining" } }),
      ),
    ).toBe(false);
    expect(isUnreachable(new HttpErrorResponse({ status: 500 }))).toBe(false);
    expect(isUnreachable(new HttpErrorResponse({ status: 403 }))).toBe(false);
    expect(isUnreachable(new TypeError("x is undefined"))).toBe(false);
  });

  it("words each case the way the classic banner does", () => {
    expect(unreachableReason(dropped())).toBe(NETWORK_UNREACHABLE_REASON);
    expect(unreachableReason(new ReachTimeoutError(10000, 0))).toContain(
      "accepted the connection but sent no answer within 10 seconds",
    );
    expect(
      unreachableReason(new HttpErrorResponse({ status: 504, error: "" })),
    ).toContain("504 Gateway Timeout");
  });
});

describe("ConnectionService", () => {
  it("raises after two dropped reads from one source, and clears on the first answer", () => {
    const c = new ConnectionService();
    c.note("shell", dropped());
    expect(c.disconnection()).toBeNull();
    c.note("shell", dropped());
    expect(c.disconnection()?.reason).toBe(NETWORK_UNREACHABLE_REASON);
    c.note("shell");
    expect(c.disconnection()).toBeNull();
  });

  it("raises at once on a read that sat unanswered, dated from when it was sent", () => {
    const c = new ConnectionService();
    c.note("feed", new ReachTimeoutError(10000, 1234));
    expect(c.disconnection()?.since).toBe(1234);
    expect(c.disconnection()?.reason).toContain("no answer within 10 seconds");
  });

  it("stays up while any source is still failing", () => {
    const c = new ConnectionService();
    c.note("shell", new ReachTimeoutError(10000, 1));
    c.note("feed", new ReachTimeoutError(10000, 2));
    c.note("shell");
    expect(c.disconnection()).not.toBeNull();
    c.note("feed");
    expect(c.disconnection()).toBeNull();
  });

  it("says grain is restarting when the daemon said so before going away", () => {
    const c = new ConnectionService();
    c.restarting = true;
    c.note("shell", dropped());
    expect(c.disconnection()).toMatchObject({
      reason: RESTARTING_REASON,
      restarting: true,
    });
  });

  it("does not raise for an error grain answered with", () => {
    const c = new ConnectionService();
    const e = new HttpErrorResponse({ status: 500, error: { error: "boom" } });
    c.note("shell", e);
    c.note("shell", e);
    expect(c.disconnection()).toBeNull();
  });
});
