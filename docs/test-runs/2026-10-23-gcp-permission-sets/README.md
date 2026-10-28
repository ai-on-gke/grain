# gcp-key permission sets in Settings (2026-10-23, task 5059)

`ui/e2e/gcp-permission-sets.spec.js`, run against `grain demo` with the frontend built from this branch (`npm run build`), Chromium:

1. Settings → Credentials → Capability servers → the demo's gcp server (`https://capabilities.demo.grain.example/gcp`) → Configure.
2. Add an admin `e2e-admin` (credential source `ambient`, project `e2e-project`, role ceiling `roles/storage.objectViewer`) and a permission set `e2e-reader` under it with that role; save.
3. Test: the report shows `admin e2e-admin (ambient) is you@demo.grain.example; has every permission it needs` and `set e2e-reader (e2e-project, roles/storage.objectViewer): 0 of 3 account(s), 0 key(s) held now`.
4. New task → Grant a server method → `gcp-key` → Permission set dropdown → `e2e-reader` → Grant → Create task; the filed task holds one `…/gcp#gcp-key` grant whose arguments carry `"permission_set":"e2e-reader"`.

Result: passed (15.5 s). `capability-server-settings`, `capability-grant-arguments`, `repo-ci-capabilities` and the `settings*` specs also pass with the second demo server (18 passed).

**Stub:** GCP itself. The demo's gcp server is grain's real in-tree `gcp` capability (`intree.NewConfigurableGCP`) served over HTTP and attached by the real registry, but its `Connector` is `gcpfake` — an in-memory IAM / Resource Manager that answers every admin's identity as `you@demo.grain.example` and grants every permission. Nothing of grain's (API, React, registry, protocol) is stubbed.

**Found and fixed on the way:** the grant/config form refused to draw `credential_source` ("looks like a secret … cannot be granted from the UI") because its name contains "credential". `isSecretLooking` (`ui/src/capabilityArgs.js`) now exempts a string confined to an `enum`, since its values are the schema's own fixed list; a field marked `writeOnly`/`format: password` is still a secret.

Screenshots: `config-form-desktop.png` (the configure form with the new admin, 1280×900, light) and `contact-sheet.png` (`make ui-screenshots PAGES=/settings/credentials`, desktop and phone, light and dark). The nested array-of-objects form is #5049's renderer and unchanged here; on the shot its per-item remove button sits in the right gutter beside the item's card — noted, not changed.
