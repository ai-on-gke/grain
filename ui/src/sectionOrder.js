import { childMove, childPlaces } from "./childOrder.js";
import { sectionRow } from "./report.js";

export const sectionPlaces = childPlaces;

export function sectionMove(sections, key, beforeKey) {
  return childMove(sections, key, beforeKey, sectionRow);
}
