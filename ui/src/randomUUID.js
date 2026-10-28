// randomUUID is crypto.randomUUID() for a page that may not be allowed
// to call it.
//
// crypto.randomUUID only exists in a secure context: https, or a
// loopback host. The Cloudtop installer advertises the UI as
// http://<host>:4242/ -- plain http on a name that is not localhost --
// and there window.isSecureContext is false, crypto.randomUUID is
// undefined, and calling it throws. That is what made Team settings'
// "New connection" do nothing at all (grain/task-4696).
// crypto.getRandomValues has no such restriction, so the fallback
// builds the same RFC 4122 version 4 UUID out of it: 16 random bytes
// with the version and variant bits set.
export function randomUUID(c = globalThis.crypto) {
  if (typeof c?.randomUUID === "function") return c.randomUUID();
  const b = c.getRandomValues(new Uint8Array(16));
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const hex = Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
