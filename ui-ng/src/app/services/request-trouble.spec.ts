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

import {
  HttpClient,
  provideHttpClient,
  withInterceptors,
} from "@angular/common/http";
import {
  HttpTestingController,
  provideHttpClientTesting,
} from "@angular/common/http/testing";
import { TestBed } from "@angular/core/testing";
import { firstValueFrom } from "rxjs";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  POLL_BACKOFF_MAX_MS,
  ReadTrouble,
  SUSTAINED_MS,
  ServerTrouble,
  VERIFIER_SUSTAINED_MS,
  isVerifierUnavailable,
  normalizeSnackMessage,
  retryAfterMs,
  serverTroubleInterceptor,
  verifierSustainedReason,
} from "./request-trouble";
import { SnackbarService } from "./snackbar.service";

const headers = (h: Record<string, string>) => ({
  get: (name: string) => h[name] ?? null,
});

describe("ReadTrouble", () => {
  it("is not sustained by one or two failures, and is by the third", () => {
    const t = new ReadTrouble();
    expect(t.fail({}, 0)).toBe(false);
    expect(t.fail({}, 1000)).toBe(false);
    expect(t.fail({}, 2000)).toBe(true);
  });

  it("is sustained by failures lasting SUSTAINED_MS however few", () => {
    const t = new ReadTrouble();
    expect(t.fail({}, 0)).toBe(false);
    expect(t.fail({}, SUSTAINED_MS)).toBe(true);
  });

  it("holds verifier_unavailable 503s to VERIFIER_SUSTAINED_MS even past three reads", () => {
    const v = {
      status: 503,
      error: { code: "verifier_unavailable", error: "retrying (ref a1)" },
    };
    const t = new ReadTrouble();
    expect(t.fail(v, 0)).toBe(false);
    expect(t.fail(v, 2_000)).toBe(false);
    expect(t.fail(v, 5_000)).toBe(false);
    expect(t.fail(v, VERIFIER_SUSTAINED_MS - 1)).toBe(false);
    expect(t.fail(v, VERIFIER_SUSTAINED_MS)).toBe(true);
  });

  it("starts over after a success", () => {
    const t = new ReadTrouble();
    t.fail({}, 0);
    t.fail({}, 1);
    t.ok();
    expect(t.failing).toBe(false);
    expect(t.fail({}, 2)).toBe(false);
  });

  it("backs off exponentially, capped, and honours Retry-After", () => {
    const t = new ReadTrouble();
    expect(t.nextDelay(3000)).toBe(3000);
    t.fail({});
    expect(t.nextDelay(3000)).toBe(3000);
    t.fail({});
    expect(t.nextDelay(3000)).toBe(6000);
    t.fail({});
    expect(t.nextDelay(3000)).toBe(12000);
    for (let i = 0; i < 10; i++) t.fail({});
    expect(t.nextDelay(3000)).toBe(POLL_BACKOFF_MAX_MS);
    t.ok();
    t.fail({ headers: headers({ "Retry-After": "20" }) });
    expect(t.nextDelay(3000)).toBe(20000);
  });
});

describe("isVerifierUnavailable and snackbar deduplication", () => {
  afterEach(() => {
    vi.useRealTimers();
    TestBed.resetTestingModule();
  });

  it("recognises structured object and JSON-string verifier_unavailable 503s", () => {
    expect(
      isVerifierUnavailable({
        status: 503,
        error: { code: "verifier_unavailable" },
      }),
    ).toBe(true);
    expect(
      isVerifierUnavailable({
        status: 503,
        error: '{"code":"verifier_unavailable","ref":"x1"}',
      }),
    ).toBe(true);
    expect(
      isVerifierUnavailable({
        status: 503,
        error: { error: "upstream request timed out" },
      }),
    ).toBe(false);
    expect(
      isVerifierUnavailable({
        status: 403,
        error: { code: "verifier_unavailable" },
      }),
    ).toBe(false);
  });

  it("collapses two verifier/gateway snackbars that differ only in their (ref …) id into one", () => {
    TestBed.configureTestingModule({});
    const snacks = TestBed.inject(SnackbarService);
    const msgA =
      "sign-in verification could not check your identity right now; retrying shortly (ref 1111aaaa)";
    const msgB =
      "sign-in verification could not check your identity right now; retrying shortly (ref 2222bbbb)";
    expect(normalizeSnackMessage(msgA)).toBe(normalizeSnackMessage(msgB));
    const first = snacks.show(msgA);
    const second = snacks.show(msgB);
    expect(second.id).toBe(first.id);
    expect(snacks.current()?.id).toBe(first.id);
    snacks.dismiss();
  });
});

describe("ServerTrouble and serverTroubleInterceptor", () => {
  afterEach(() => {
    vi.useRealTimers();
    TestBed.resetTestingModule();
  });

  it("cools down across /api/ 503s, clears on 200 or 304, and raises verifierSustained at 30s", async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-11-16T00:00:00Z"));
    TestBed.configureTestingModule({
      providers: [
        provideHttpClient(withInterceptors([serverTroubleInterceptor])),
        provideHttpClientTesting(),
      ],
    });
    const http = TestBed.inject(HttpClient);
    const ctrl = TestBed.inject(HttpTestingController);
    const trouble = TestBed.inject(ServerTrouble);

    const p1 = firstValueFrom(http.get("/api/config")).catch((e) => e);
    ctrl.expectOne("/api/config").flush(
      {
        code: "verifier_unavailable",
        error:
          "sign-in verification could not check your identity right now; retrying shortly (ref r1)",
      },
      {
        status: 503,
        statusText: "Service Unavailable",
        headers: { "Retry-After": "8" },
      },
    );
    await p1;

    expect(trouble.coolingDown()).toBe(true);
    expect(trouble.remainingMs()).toBe(8_000);
    expect(trouble.verifierSustained()).toBe(false);

    // At 29.9s the global verifier banner is still silent; at 30s it flips on.
    await vi.advanceTimersByTimeAsync(VERIFIER_SUSTAINED_MS - 100);
    expect(trouble.verifierSustained()).toBe(false);
    await vi.advanceTimersByTimeAsync(100);
    expect(trouble.verifierSustained()).toBe(true);
    expect(
      verifierSustainedReason(trouble.verifierSince()!, Date.now()),
    ).toContain("30s");

    // A 304 Not Modified on any /api/ read proves the server and verifier are back.
    const p304 = firstValueFrom(http.get("/api/tasks")).catch((e) => e);
    ctrl
      .expectOne("/api/tasks")
      .flush(null, { status: 304, statusText: "Not Modified" });
    await p304;
    expect(trouble.coolingDown()).toBe(false);
    expect(trouble.verifierSustained()).toBe(false);
    expect(trouble.verifierSince()).toBeNull();
    ctrl.verify();
  });
});

describe("retryAfterMs", () => {
  it("reads seconds and dates, and nothing else", () => {
    expect(retryAfterMs({ headers: headers({ "Retry-After": "5" }) })).toBe(
      5000,
    );
    const now = Date.parse("2026-11-15T00:00:00Z");
    expect(
      retryAfterMs(
        {
          headers: headers({ "Retry-After": "Sun, 15 Nov 2026 00:00:10 GMT" }),
        },
        now,
      ),
    ).toBe(10000);
    expect(retryAfterMs({})).toBeNull();
  });
});
