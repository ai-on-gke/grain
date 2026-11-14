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

// The twin of ui/src/wholeNumber.js, for /next/'s Task priority boxes.

// wholeNumber reads what a number box holds as the whole number it means,
// or NaN when it holds none. Not parseInt: a number input accepts "1e3",
// and parseInt reads that as 1.
export function wholeNumber(value: unknown): number {
  const s = String(value ?? "").trim();
  if (s === "") return NaN;
  const n = Number(s);
  return Number.isInteger(n) ? n : NaN;
}

// taskPriority reads a Task priority box as the priority it files: a
// whole number from 1 up (no further than Number.MAX_SAFE_INTEGER, past
// which the number filed is no longer the one typed), or NaN.
export function taskPriority(value: unknown): number {
  const n = wholeNumber(value);
  return n >= 1 && Number.isSafeInteger(n) ? n : NaN;
}

// PRIORITY_REFUSAL is what every Task priority box says under a value it
// will not save.
export const PRIORITY_REFUSAL =
  "a whole number, 1 or more; default 5, higher numbers run first";
