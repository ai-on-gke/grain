import { describe, expect, it } from "vitest";
import { readNumberBox } from "./numberBox.js";

// A stand-in for the <input type="number"> a blur hands over: what the
// browser reports as its value, what it opened on, and whether the
// browser could read what was typed at all.
function box(value, { defaultValue = "500", badInput = false, min, max } = {}) {
  return {
    value,
    defaultValue,
    min,
    max,
    validity: { badInput },
    labels: [{ textContent: "CI job cache entries" }],
  };
}

describe("readNumberBox", () => {
  it("reads a whole number, an exponent included", () => {
    expect(readNumberBox(box("64"), { integer: true })).toEqual({ value: 64 });
    expect(readNumberBox(box("1e3"), { integer: true })).toEqual({
      value: 1000,
    });
  });

  it("reads a fraction where one is allowed", () => {
    expect(readNumberBox(box("0.25"))).toEqual({ value: 0.25 });
  });

  it("says a box somebody emptied is empty", () => {
    expect(readNumberBox(box("  "), { integer: true })).toEqual({
      empty: true,
    });
  });

  it("refuses a fraction where a whole number is wanted, and puts the box back", () => {
    const input = box("2.5");
    expect(readNumberBox(input, { integer: true })).toEqual({
      error: "CI job cache entries takes a whole number; 2.5 was not saved.",
    });
    expect(input.value).toBe("500");
  });

  // The browser reports a half-typed entry ("5e") as "", the same as an
  // emptied box; it used to clear the setting back to its default.
  it("refuses what the browser could not read as a number, rather than calling it empty", () => {
    const input = box("", { badInput: true });
    const read = readNumberBox(input, { integer: true });
    expect(read.empty).toBeUndefined();
    expect(read.error).toBe(
      "CI job cache entries: what was typed is not a number, so it was not saved.",
    );
    expect(input.value).toBe("500");
  });

  it("names the field by the label it is given, or says this setting", () => {
    expect(
      readNumberBox({ ...box("x"), labels: [] }, { label: "Sandbox vCPUs" })
        .error,
    ).toBe("Sandbox vCPUs: x is not a number, so it was not saved.");
    expect(readNumberBox({ ...box("x"), labels: [] }).error).toBe(
      "This setting: x is not a number, so it was not saved.",
    );
  });

  it("refuses a value below the box's min, and puts the box back", () => {
    const input = box("-1", { defaultValue: "2", min: "1" });
    expect(
      readNumberBox(input, {
        integer: true,
        label: "Max unreviewed follow-up depth",
      }),
    ).toEqual({
      error:
        "Max unreviewed follow-up depth must be at least 1; -1 was not saved.",
    });
    expect(input.value).toBe("2");

    const fromOption = box("0", { defaultValue: "4" });
    expect(
      readNumberBox(fromOption, {
        integer: true,
        min: 1,
        label: "Max worker agents",
      }),
    ).toEqual({
      error: "Max worker agents must be at least 1; 0 was not saved.",
    });
    expect(fromOption.value).toBe("4");
  });

  it("refuses a value above the box's max, and puts the box back", () => {
    const input = box("1.5", { defaultValue: "0.1", min: "0", max: "1" });
    expect(
      readNumberBox(input, { label: "Disk floor (share of each volume)" }),
    ).toEqual({
      error:
        "Disk floor (share of each volume) cannot be more than 1; 1.5 was not saved.",
    });
    expect(input.value).toBe("0.1");
  });
});
