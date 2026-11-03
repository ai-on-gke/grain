import { chromium } from "playwright";
const [BASE, TOK] = process.argv.slice(2);
const b = await chromium.launch();
const p = await (await b.newContext({ extraHTTPHeaders: { Authorization: `Bearer ${TOK}` } })).newPage();
const out = {};
for (const path of ["/repos/acme/widgets", "/repos/acme/widgets/settings/merge-queue", "/repos/acme/widgets/settings/advanced", "/repos/acme/widgets/settings/ci", "/merge-queue", "/settings/repositories"]) {
  await p.goto(BASE + path); await p.waitForLoadState("networkidle"); await p.waitForTimeout(800);
  const t = await p.locator("body").innerText();
  out[path] = t.split("\n").filter((l) => /re-?check|batch|gather/i.test(l)).slice(0, 8);
}
console.log(JSON.stringify(out, null, 1));
await b.close();
