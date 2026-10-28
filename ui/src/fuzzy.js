// fuzzyScore is how well query matches text, or -1 when it does not:
// every character of the query must appear in the text in order, case
// ignored, with spaces in the query ignored too -- so "gem flash", "g37f"
// and "flash" all find "gemini-3.7-flash". Higher is better: a match
// that is one unbroken run, that starts the text, or whose characters
// start words ("-", ".", "/", " ", "_" boundaries) beats one scattered
// through the middle of a longer name.
export function fuzzyScore(query, text) {
  const q = String(query || "")
    .toLowerCase()
    .replace(/\s+/g, "");
  const t = String(text || "").toLowerCase();
  if (!q) return 0;
  if (!t) return -1;
  const at = t.indexOf(q);
  if (at >= 0) {
    // A plain substring beats any scattered match.
    return 1000 - at * 2 - (t.length - q.length) / 100;
  }
  let score = 0;
  let ti = 0;
  let previous = -2;
  for (const ch of q) {
    const found = t.indexOf(ch, ti);
    if (found < 0) return -1;
    if (found === previous + 1) score += 5;
    if (found === 0 || /[-./ _:]/.test(t[found - 1])) score += 3;
    score -= Math.min(found - ti, 10) / 10;
    previous = found;
    ti = found + 1;
  }
  return score - t.length / 100;
}

// fuzzyFilter is the items whose text fuzzyScore matches query, best
// first; ties keep their original order. With no query it is every item,
// in order.
export function fuzzyFilter(items, query, text = (item) => item) {
  if (!String(query || "").trim()) return items;
  return items
    .map((item, index) => ({
      item,
      index,
      score: fuzzyScore(query, text(item)),
    }))
    .filter((entry) => entry.score >= 0)
    .sort((a, b) => b.score - a.score || a.index - b.index)
    .map((entry) => entry.item);
}
