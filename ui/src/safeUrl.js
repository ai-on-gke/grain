// The schemes a link or image in rendered prose or a diagram may carry.
// Owned here rather than left to react-markdown's defaultUrlTransform or
// Mermaid's sanitizer alone, so the policy that keeps `javascript:`,
// `data:` or `vbscript:` out of an href cannot widen under us with a
// dependency bump, and is narrower than either library's: nothing in a
// task body or comment needs irc: or xmpp:.
const SAFE_PROTOCOL = /^(https?|mailto)$/i;

// safeUrl returns url unchanged when it is relative or carries an
// allowed scheme, and "" otherwise. A colon only names a scheme when it
// comes before any `/`, `?` or `#`; one after them is part of a relative
// path, query or fragment.
export function safeUrl(url) {
  const value = String(url ?? "").trim();
  const colon = value.indexOf(":");
  if (colon === -1) {
    return value;
  }
  for (const delimiter of ["/", "?", "#"]) {
    const at = value.indexOf(delimiter);
    if (at !== -1 && at < colon) {
      return value;
    }
  }
  return SAFE_PROTOCOL.test(value.slice(0, colon)) ? value : "";
}
