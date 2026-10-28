// Shared page script for the mockups (run inside the page by
// tools/shoot.mjs after the option's CSS is injected). Swaps the brand
// mark for the option's, collapses the run of identical capability
// chips on every list row into one "+N" chip, and gives the rail's
// nav items the option's icon set where a glyph of the same name
// exists. Throwaway: nothing here ships.
(() => {
  const mock = window.__mock || {};
  // 1. The mark: only the rail's / app bar's mark, not the 20px marks
  // the task list uses for the repair indicator and the page header.
  if (mock.mark) {
    const uri = "data:image/svg+xml;utf8," + encodeURIComponent(mock.mark);
    for (const img of document.querySelectorAll('aside img[src*="grain-mark"], header.MuiAppBar-root img[src*="grain-mark"]')) {
      img.src = uri;
    }
    for (const c of document.querySelectorAll("canvas")) {
      // The animated mark draws on a canvas; replace with a still.
      if (c.width <= 64 && c.height <= 64) {
        const img = document.createElement("img");
        img.src = uri;
        img.width = c.clientWidth || 32;
        img.height = c.clientHeight || 32;
        c.replaceWith(img);
      }
    }
  }
  // 1b. Navigation icons: the nodal-line set, keyed by the rail label.
  if (mock.icons) {
    const byLabel = {
      inbox: "inbox", tasks: "tasks", boards: "board", sessions: "sessions", director: "director",
      processes: "processes", ci: "ci", repos: "repos", metrics: "metrics", settings: "settings",
      system: "system", alerts: "alerts", staging: "staging", cache: "system", caches: "system",
    };
    const items = document.querySelectorAll("aside .MuiListItemButton-root, .MuiDrawer-root .MuiListItemButton-root");
    for (const item of items) {
      const label = (item.querySelector(".MuiListItemText-root")?.textContent || "").trim().toLowerCase();
      const name = byLabel[label];
      const slot = item.querySelector(".nav-icon");
      if (!name || !slot || !mock.icons[name] || !slot.querySelector("svg")) continue;
      slot.innerHTML = mock.icons[name];
      const svg = slot.querySelector("svg");
      svg.setAttribute("width", mock.option === 3 ? 18 : 16);
      svg.setAttribute("height", mock.option === 3 ? 18 : 16);
      svg.style.display = "block";
    }
    // The page header's icon too, when it is one of the brand figures.
    const title = (document.querySelector(".page-header-title")?.textContent || "").trim().toLowerCase();
    const hname = byLabel[title.split(/\s/)[0]];
    const hslot = document.querySelector(".page-header .header-icon");
    if (hname && hslot && mock.icons[hname] && hslot.querySelector("svg")) {
      hslot.innerHTML = mock.icons[hname];
      const svg = hslot.querySelector("svg");
      svg.setAttribute("width", 20); svg.setAttribute("height", 20); svg.style.display = "block";
    }
  }
  // 2. Chips: on list rows keep the first field chip and the first
  // non-field chip, fold the rest into "+N".
  const collapse = mock.collapseChips !== false && mock.option !== 1;
  if (collapse) {
    for (const strip of document.querySelectorAll(".task-row .chips, .board-card .chips")) {
      const chips = [...strip.querySelectorAll(".MuiChip-root")];
      if (chips.length <= 3) continue;
      const keep = 2;
      const hidden = chips.slice(keep);
      for (const c of hidden) c.style.display = "none";
      const more = document.createElement("span");
      more.className = "mock-more-chip";
      more.textContent = `+${hidden.length}`;
      more.title = hidden.map((c) => c.textContent).join(", ");
      strip.appendChild(more);
    }
  }
  // 3. Sentence-case the few strings the source itself lowercases.
  if (mock.option !== 1) {
    for (const el of document.querySelectorAll(".page-header-title .MuiTypography-root, h1, h2, h3")) {
      const t = el.textContent;
      if (t && t === t.toLowerCase() && /^[a-z]/.test(t)) {
        el.textContent = t.replace(/^ci\b/, "CI").replace(/^task (\d+)/, "Task $1").replace(/^[a-z]/, (m) => m.toUpperCase());
      }
    }
  }
})();
