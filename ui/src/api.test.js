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

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import api, {
  forgetETag,
  forgetETags,
  forgetVerifierTrouble,
  isUnreachable,
  isVerifierUnavailable,
  unreachableReason,
  VERIFIER_SUSTAINED_MS,
  verifierSustainedReason,
  verifierTroubleSince,
  verifierTroubleSustained,
} from "./api.js";
import { UNCHANGED } from "./poll.js";
import { browserTimeZone } from "./time.js";

// What every write carries on a browser left on "Browser default".
const zoneHeader = () => ({ "X-Grain-Time-Zone": browserTimeZone() });

function jsonResponse(status, body, headers = {}) {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText: "",
    headers: new Map([
      ["Content-Type", "application/json"],
      ...Object.entries(headers),
    ]),
    json: () => Promise.resolve(body),
  };
}

// notModified is what pkg/ui answers a poller holding the current ETag:
// no body at all, and the validator repeated so a client that had lost
// track of it still knows what it is holding.
function notModified(etag) {
  return {
    ok: false,
    status: 304,
    statusText: "Not Modified",
    headers: new Map([["ETag", etag]]),
    json: () => Promise.reject(new Error("a 304 has no body to parse")),
  };
}

describe("api", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    localStorage.clear();
    vi.unstubAllGlobals();
    forgetETags();
  });

  it("returns the parsed body on a JSON 2xx response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(jsonResponse(200, { id: 7 })),
    );

    await expect(api("/api/tasks/7")).resolves.toEqual({ id: 7 });
  });

  it("sends JSON content-type by default and forwards opts through", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, null));
    vi.stubGlobal("fetch", fetchMock);

    await api("/api/tasks", { method: "POST", body: "{}" });

    expect(fetchMock).toHaveBeenCalledWith("/api/tasks", {
      method: "POST",
      body: "{}",
      headers: { "Content-Type": "application/json", ...zoneHeader() },
    });
  });

  it("sends no X-Grain-User header, whatever an old browser still stores", async () => {
    localStorage.setItem("grain.username", "alice");
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, null));
    vi.stubGlobal("fetch", fetchMock);

    await api("/api/tasks", { method: "POST", body: "{}" });

    expect(fetchMock).toHaveBeenCalledWith("/api/tasks", {
      method: "POST",
      body: "{}",
      headers: {
        "Content-Type": "application/json",
        ...zoneHeader(),
      },
    });
  });

  it("merges a caller's own headers into the defaults rather than replacing them", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, null));
    vi.stubGlobal("fetch", fetchMock);

    await api("/api/deployment/broadcast", {
      method: "POST",
      headers: { "X-Extra": "1" },
      body: "{}",
    });

    expect(fetchMock.mock.calls[0][1].headers).toEqual({
      "Content-Type": "application/json",
      ...zoneHeader(),
      "X-Extra": "1",
    });
  });

  it("tells the server the browser's own zone on a write when Preferences say Browser default", async () => {
    vi.spyOn(Intl, "DateTimeFormat").mockImplementation(() => ({
      resolvedOptions: () => ({ timeZone: "America/Los_Angeles" }),
    }));
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, null));
    vi.stubGlobal("fetch", fetchMock);
    try {
      await api("/api/tasks", { method: "POST", body: "{}" });
    } finally {
      vi.restoreAllMocks();
    }

    expect(fetchMock.mock.calls[0][1].headers["X-Grain-Time-Zone"]).toBe(
      "America/Los_Angeles",
    );
  });

  it("tells the server the zone picked in Preferences over the browser's", async () => {
    localStorage.setItem("grain.timeZone", "Europe/London");
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, null));
    vi.stubGlobal("fetch", fetchMock);

    await api("/api/tasks/7/comments", { method: "POST", body: "{}" });

    expect(fetchMock.mock.calls[0][1].headers["X-Grain-Time-Zone"]).toBe(
      "Europe/London",
    );
  });

  it("sends no zone on a read", async () => {
    localStorage.setItem("grain.timeZone", "Europe/London");
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, null));
    vi.stubGlobal("fetch", fetchMock);

    await api("/api/tasks");

    expect(
      fetchMock.mock.calls[0][1].headers["X-Grain-Time-Zone"],
    ).toBeUndefined();
  });

  it("returns null for a non-JSON response body", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        status: 204,
        statusText: "No Content",
        headers: new Map(),
        json: () => Promise.reject(new Error("should not be called")),
      }),
    );

    await expect(
      api("/api/tasks/7/approve", { method: "POST" }),
    ).resolves.toBeNull();
  });

  it("throws the server's error message on a non-2xx JSON response", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(jsonResponse(400, { error: "title is required" })),
    );

    await expect(api("/api/tasks", { method: "POST" })).rejects.toThrow(
      "title is required",
    );
  });

  it("carries a refused config change's code and validation report", async () => {
    const body = {
      error: "the config change does not pass validation",
      code: "config_change_stale",
      validation: { liveness: ["max_parallel is 0, which lets no run start"] },
    };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(422, body)));

    const err = await api("/api/state-repo/pull-requests", {
      method: "POST",
    }).catch((e) => e);
    expect(err.status).toBe(422);
    expect(err.code).toBe("config_change_stale");
    expect(err.validation).toEqual(body.validation);
  });

  it("falls back to the status line when a non-2xx response carries no JSON error", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: false,
        status: 500,
        statusText: "Internal Server Error",
        headers: new Map(),
        json: () => Promise.reject(new Error("should not be called")),
      }),
    );

    const err = await api("/api/tasks").catch((e) => e);
    expect(err).toMatchObject({ message: "500 Internal Server Error" });
    // The daemon answered this one, even if the answer was a problem --
    // not the "cannot be reached at all" case the poll handles
    // differently.
    expect(isUnreachable(err)).toBe(false);
  });

  // The two shapes a request that never reached the daemon can take:
  // fetch itself rejecting (a dead connection, a timeout, a DNS
  // failure -- the Fetch spec gives no more than a vague TypeError for
  // any of them) and a gateway in front of the daemon answering on its
  // behalf because there is nothing back there to ask. Both are marked
  // so App.jsx's poll can show a "can't reach this deployment" state
  // instead of quietly warning to the console, which is right for every
  // other rejection but not these two.
  it("marks a network-level failure as unreachable, with a message fit to show", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockRejectedValue(new TypeError("Failed to fetch")),
    );

    const err = await api("/api/tasks").catch((e) => e);
    expect(isUnreachable(err)).toBe(true);
    // Still the TypeError fetch itself produced -- SystemOverlay.jsx's
    // reboot handler keys on exactly that type to tell a reboot's own
    // cut connection apart from a real failure.
    expect(err).toBeInstanceOf(TypeError);
    expect(unreachableReason(err)).not.toMatch(/Failed to fetch/);
  });

  // grain/task-3619: a daemon that accepts the connection and never
  // answers leaves fetch waiting forever, so the caller that asked for
  // a bound gets an unreachable error at it instead.
  it("gives up on a request asked with a timeout, as unreachable", async () => {
    vi.useFakeTimers();
    try {
      const fetchMock = vi.fn(
        (_path, init) =>
          new Promise((_resolve, reject) => {
            init.signal.addEventListener("abort", () =>
              reject(new DOMException("aborted", "AbortError")),
            );
          }),
      );
      vi.stubGlobal("fetch", fetchMock);

      const asked = api("/api/config", { timeout: 10_000 }).catch((e) => e);
      await vi.advanceTimersByTimeAsync(9_999);
      expect(fetchMock.mock.calls[0][1].signal.aborted).toBe(false);
      await vi.advanceTimersByTimeAsync(1);
      const err = await asked;
      expect(isUnreachable(err)).toBe(true);
      expect(err.timedOut).toBe(true);
      expect(err.sentAt).toBeInstanceOf(Date);
      expect(unreachableReason(err)).toMatch(/cannot be reached/);
      expect(unreachableReason(err)).toMatch(/10 seconds/);
    } finally {
      vi.useRealTimers();
    }
  });

  it("leaves a request with a timeout that answers in time alone", async () => {
    vi.useFakeTimers();
    try {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue({
          ok: true,
          status: 200,
          headers: new Map([["Content-Type", "application/json"]]),
          json: () => Promise.resolve({ ok: 1 }),
        }),
      );
      await expect(api("/api/config", { timeout: 10_000 })).resolves.toEqual({
        ok: 1,
      });
      // Nothing left to fire: the timer went with the answer.
      expect(vi.getTimerCount()).toBe(0);
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not count an abort of the caller's own as a timeout", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockRejectedValue(new DOMException("aborted", "AbortError")),
    );
    const err = await api("/api/config", { timeout: 10_000 }).catch((e) => e);
    expect(err.name).toBe("AbortError");
    expect(isUnreachable(err)).toBe(false);
  });

  it.each([502, 503, 504])(
    "marks a %d from a gateway in front of the daemon as unreachable",
    async (status) => {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue({
          ok: false,
          status,
          statusText: "",
          headers: new Map(),
          json: () => Promise.reject(new Error("should not be called")),
        }),
      );

      const err = await api("/api/tasks").catch((e) => e);
      expect(isUnreachable(err)).toBe(true);
      expect(unreachableReason(err)).toMatch(String(status));
    },
  );

  // A 502 with an error body of its own -- grain relaying an upstream
  // that did not answer, as a capability check does -- is still counted
  // unreachable, but says it explained itself so showError keeps the
  // explanation rather than the generic sentence.
  it("marks a gateway status with its own error message as explained", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: false,
        status: 502,
        statusText: "Bad Gateway",
        headers: new Map([["Content-Type", "application/json"]]),
        json: () => Promise.resolve({ error: "the issuer did not answer" }),
      }),
    );

    const err = await api("/api/capabilities/x/check").catch((e) => e);
    expect(err).toMatchObject({
      message: "the issuer did not answer",
      status: 502,
      explained: true,
    });
  });
});

// The conditional half: what the three-second poll of /api/tasks does
// with a list it already has (poll.js's own header for the whole
// argument, pkg/ui/task_list_cache.go for the other end of it).
describe("api, polling", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    forgetETags();
  });

  it("sends the ETag it was last given back as If-None-Match", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        jsonResponse(200, [{ id: "1" }], { ETag: '"abc"' }),
      )
      .mockResolvedValueOnce(notModified('"abc"'));
    vi.stubGlobal("fetch", fetchMock);

    await expect(api("/api/tasks", { poll: true })).resolves.toEqual([
      { id: "1" },
    ]);
    // Nothing to send back on the first poll of a fresh page.
    expect(fetchMock.mock.calls[0][1].headers["If-None-Match"]).toBeUndefined();

    await expect(api("/api/tasks", { poll: true })).resolves.toBe(UNCHANGED);
    expect(fetchMock.mock.calls[1][1].headers["If-None-Match"]).toBe('"abc"');
  });

  it("keeps the browser's own cache out of the revalidation", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse(200, [], { ETag: '"abc"' }));
    vi.stubGlobal("fetch", fetchMock);

    await api("/api/tasks", { poll: true });

    // Left to itself, fetch would revalidate against its HTTP cache and
    // hand back a 200 with the cached body -- saving the wire and none
    // of the parse, which is most of what the 304 is for.
    expect(fetchMock.mock.calls[0][1].cache).toBe("no-store");
  });

  it("follows the ETag when the answer changes", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(200, [], { ETag: '"one"' }))
      .mockResolvedValueOnce(
        jsonResponse(200, [{ id: "1" }], { ETag: '"two"' }),
      )
      .mockResolvedValueOnce(notModified('"two"'));
    vi.stubGlobal("fetch", fetchMock);

    await api("/api/tasks", { poll: true });
    await expect(api("/api/tasks", { poll: true })).resolves.toEqual([
      { id: "1" },
    ]);
    await api("/api/tasks", { poll: true });

    expect(fetchMock.mock.calls[1][1].headers["If-None-Match"]).toBe('"one"');
    expect(fetchMock.mock.calls[2][1].headers["If-None-Match"]).toBe('"two"');
  });

  it("forgets an ETag when an answer comes back without one", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(200, [], { ETag: '"abc"' }))
      .mockResolvedValueOnce(jsonResponse(200, []))
      .mockResolvedValueOnce(jsonResponse(200, []));
    vi.stubGlobal("fetch", fetchMock);

    await api("/api/tasks", { poll: true });
    await api("/api/tasks", { poll: true });
    await api("/api/tasks", { poll: true });

    // A server that has stopped sending validators is one this must stop
    // asserting about: the stored ETag describes nothing it would still
    // recognise.
    expect(fetchMock.mock.calls[2][1].headers["If-None-Match"]).toBeUndefined();
  });

  it("forgets one path's ETag for a caller that dropped its answer", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(200, [], { ETag: '"task"' }))
      .mockResolvedValueOnce(jsonResponse(200, [], { ETag: '"list"' }))
      .mockResolvedValueOnce(jsonResponse(200, []))
      .mockResolvedValueOnce(notModified('"list"'));
    vi.stubGlobal("fetch", fetchMock);

    await api("/api/tasks/1", { poll: true });
    await api("/api/tasks", { poll: true });
    forgetETag("/api/tasks/1");
    await api("/api/tasks/1", { poll: true });
    await api("/api/tasks", { poll: true });

    // The dropped answer's ETag would have the next poll told 304 about
    // a body its caller never kept (App.jsx's loadSessionTask); other
    // paths keep theirs.
    expect(fetchMock.mock.calls[2][1].headers["If-None-Match"]).toBeUndefined();
    expect(fetchMock.mock.calls[3][1].headers["If-None-Match"]).toBe('"list"');
  });

  it("asks unconditionally for a path nobody polls", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse(200, [], { ETag: '"abc"' }));
    vi.stubGlobal("fetch", fetchMock);

    await api("/api/tasks", { poll: true });
    await api("/api/tasks");

    // The ETag is shared per path, so a caller that wants the body must
    // not be handed the poll's "you already have this".
    expect(fetchMock.mock.calls[1][1].headers["If-None-Match"]).toBeUndefined();
    expect(fetchMock.mock.calls[1][1].cache).toBeUndefined();
  });

  it("does not send poll through to fetch as an option of its own", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, []));
    vi.stubGlobal("fetch", fetchMock);

    await api("/api/tasks", { poll: true });

    expect(fetchMock.mock.calls[0][1].poll).toBeUndefined();
  });

  // grain/task-1435: a sign-in page or proxy answering 200 for grain
  // used to come back as null, which App read as "no config yet" and
  // waited on forever.
  it("throws on a successful HTML answer instead of returning null", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        status: 200,
        statusText: "OK",
        headers: new Map([["Content-Type", "text/html; charset=utf-8"]]),
        json: () => Promise.reject(new Error("not JSON")),
      }),
    );

    const err = await api("/api/config").catch((e) => e);

    expect(err).toBeInstanceOf(Error);
    expect(err.notGrain).toBe(true);
    expect(err.message).toMatch(/\/api\/config answered 200 with an HTML page/);
  });

  it("still returns null for a successful answer with no body", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        status: 204,
        statusText: "No Content",
        headers: new Map(),
        json: () => Promise.reject(new Error("no body")),
      }),
    );

    await expect(api("/api/tasks/1/approve", { method: "POST" })).resolves.toBe(
      null,
    );
  });

  // pkg/ui's writeVerifierUnavailable: the daemon answering that the
  // sign-in verifier could not check this request's identity right now.
  describe("verifier_unavailable", () => {
    const body = {
      error:
        "sign-in verification could not check your identity right now; retrying shortly (ref 0123456789ab)",
      code: "verifier_unavailable",
      reason: "tonic_unreachable",
      retryAfter: 5,
      ref: "0123456789ab",
    };

    afterEach(() => {
      forgetVerifierTrouble();
      vi.useRealTimers();
    });

    it("carries the reason and retry interval, and is not unreachable", async () => {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(jsonResponse(503, body)),
      );
      const err = await api("/api/tasks", { poll: true }).catch((e) => e);
      expect(err.status).toBe(503);
      expect(err.code).toBe("verifier_unavailable");
      expect(err.reason).toBe("tonic_unreachable");
      expect(err.retryAfter).toBe(5);
      expect(err.method).toBe("GET");
      expect(isVerifierUnavailable(err)).toBe(true);
      expect(isUnreachable(err)).toBe(false);
    });

    it("leaves a generic 503 unreachable", async () => {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          jsonResponse(503, {
            error: "the service is temporarily unavailable (ref 1)",
            code: "unavailable",
          }),
        ),
      );
      const err = await api("/api/tasks").catch((e) => e);
      expect(isVerifierUnavailable(err)).toBe(false);
      expect(isUnreachable(err)).toBe(true);
      expect(verifierTroubleSince()).toBeNull();
    });

    it("times a run of refusals from the first, and ends it on the first answer", async () => {
      vi.useFakeTimers();
      const fetchMock = vi.fn().mockResolvedValue(jsonResponse(503, body));
      vi.stubGlobal("fetch", fetchMock);
      await api("/api/tasks").catch(() => {});
      const since = verifierTroubleSince();
      expect(since).toBeInstanceOf(Date);
      expect(verifierTroubleSustained()).toBe(false);

      vi.advanceTimersByTime(VERIFIER_SUSTAINED_MS - 1000);
      await api("/api/config").catch(() => {});
      expect(verifierTroubleSince()).toBe(since);
      expect(verifierTroubleSustained()).toBe(false);
      vi.advanceTimersByTime(1000);
      expect(verifierTroubleSustained()).toBe(true);
      expect(verifierSustainedReason(since)).toBe(
        "Sign-in verification has been temporarily unable to check your identity for 30s — retrying automatically.",
      );

      fetchMock.mockResolvedValue(jsonResponse(200, []));
      await api("/api/tasks");
      expect(verifierTroubleSince()).toBeNull();
      expect(verifierTroubleSustained()).toBe(false);
    });
  });
});
