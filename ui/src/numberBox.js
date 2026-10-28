// readNumberBox reads what a settings number box holds when it loses
// focus, for the blur-to-save boxes in Admin and Team settings.
//
// Those boxes used to read themselves with parseInt, which takes the
// longest number-looking prefix and drops the rest without a word: a
// vCPU count of 1.5 was saved as 1, and 1e3 -- a number the browser's
// own number box accepts as a thousand -- as 1. Worse, a box holding
// something the browser cannot read as a number at all ("5e", "1-",
// "--3") reports its value as "", the same as a box somebody emptied
// on purpose, so a half-typed entry cleared the setting back to
// grain's default (2026-10-07 deployment-operations walkthrough).
//
// So the answer is one of three things, and the caller does nothing it
// was not told to:
//
//   - { empty: true }: the box is really empty -- the setting goes back
//     to its default, as before;
//   - { value }: a number, a whole one where `integer` asks for it, and
//     within `min`..`max` (read from the option or the input's own
//     attribute);
//   - { error }: a sentence naming the field and what was wrong with
//     it. Nothing is saved, and the box has already been put back to
//     the value it opened on (its defaultValue, which is the stored
//     setting: these boxes are uncontrolled and keyed by it).
export function readNumberBox(
  input,
  { integer = false, min, max, label = "" } = {},
) {
  const name = label || boxLabel(input) || "This setting";
  if (input.validity?.badInput) {
    return refuse(
      input,
      `${name}: what was typed is not a number, so it was not saved.`,
    );
  }
  const raw = String(input.value ?? "").trim();
  if (raw === "") return { empty: true };
  const value = Number(raw);
  if (!Number.isFinite(value)) {
    return refuse(
      input,
      `${name}: ${raw} is not a number, so it was not saved.`,
    );
  }
  if (integer && !Number.isInteger(value)) {
    return refuse(input, `${name} takes a whole number; ${raw} was not saved.`);
  }
  const lower = bound(min ?? input.min ?? input.getAttribute?.("min"));
  if (lower !== null && value < lower) {
    return refuse(
      input,
      `${name} must be at least ${lower}; ${raw} was not saved.`,
    );
  }
  const upper = bound(max ?? input.max ?? input.getAttribute?.("max"));
  if (upper !== null && value > upper) {
    return refuse(
      input,
      `${name} cannot be more than ${upper}; ${raw} was not saved.`,
    );
  }
  return { value };
}

function bound(raw) {
  if (raw === undefined || raw === null || raw === "") return null;
  const n = Number(raw);
  return Number.isFinite(n) ? n : null;
}

function refuse(input, error) {
  input.value = input.defaultValue ?? "";
  return { error };
}

// The visible label of a MUI TextField's input, without the asterisk a
// required field adds.
function boxLabel(input) {
  const text = input.labels?.[0]?.textContent || "";
  return text.replace(/\s*\*\s*$/, "").trim();
}
