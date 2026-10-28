import { describe, expect, it } from "vitest";
import { randomUUID } from "./randomUUID.js";

const V4 =
  /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

describe("randomUUID", () => {
  it("uses crypto.randomUUID where the page has it", () => {
    expect(randomUUID({ randomUUID: () => "native" })).toBe("native");
  });

  // What a plain-http, non-localhost origin hands the page: a crypto
  // with getRandomValues and no randomUUID.
  it("falls back to getRandomValues outside a secure context", () => {
    const insecure = {
      getRandomValues: (a) => globalThis.crypto.getRandomValues(a),
    };
    const ids = new Set();
    for (let i = 0; i < 100; i++) {
      const id = randomUUID(insecure);
      expect(id).toMatch(V4);
      ids.add(id);
    }
    expect(ids.size).toBe(100);
  });

  it("sets the version and variant bits whatever the bytes were", () => {
    const ones = { getRandomValues: (a) => a.fill(0xff) };
    expect(randomUUID(ones)).toBe("ffffffff-ffff-4fff-bfff-ffffffffffff");
    const zeros = { getRandomValues: (a) => a.fill(0) };
    expect(randomUUID(zeros)).toBe("00000000-0000-4000-8000-000000000000");
  });
});
