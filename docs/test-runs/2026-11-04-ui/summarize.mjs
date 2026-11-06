// Prints searchselect-walk*.json as one line per box.
// Usage: node summarize.mjs searchselect-walk.json
import fs from "node:fs";
const r = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
for (const f of r) {
  console.log(`## ${f.form} @${f.width} found=${f.found}${f.error ? " ERR " + f.error : ""}`);
  for (const b of f.boxes) {
    const p = b.pick || {};
    const e = b.escape || {};
    const n = b.enterNoMatch || {};
    console.log(
      `  [${b.box}] ${String(b.label).slice(0, 40)} opts=${b.options} axe=${(b.axe || []).join(",") || "-"}` +
        ` search=${b.search ? b.search.word + "->" + b.search.hits + (b.search.targetListed ? "" : "(target missing)") : "-"}` +
        ` pick=${JSON.stringify(p.value)}${p.chips && p.chips.length ? " chips=" + p.chips.join("|") : ""}` +
        ` listClosed=${p.listClosed} formOpen=${p.formStillOpen} writes=${(p.writes || []).join(";") || "-"}` +
        ` | esc opened=${e.opened} closed=${e.listClosed} formOpen=${e.formStillOpen}` +
        ` | clear=${b.clear ? JSON.stringify(b.clear) : "n/a"}` +
        ` | enterNoMatch shown=${n.noMatchShown} formOpen=${n.formStillOpen} url=${n.urlUnchanged} writes=${(n.writes || []).join(";") || "-"}` +
        `${b.error ? " ERR " + b.error : ""} ${b.note || ""}`,
    );
  }
}
