/**
 * Vendored verbatim from the grain-mark design pack, v2 (grainy glyphs),
 * with this note and nothing else added. The previous vendoring carried
 * three deviations from the pack; v2 needs none of them:
 *
 *   - `slot` -- the cycle slot the animation opens on, so it can start on
 *     the figure the still was already showing -- is the pack's own
 *     option now.
 *   - `frameWidth` set the ring's width. v2 has no ring: the circle is an
 *     invisible clip, so the shape itself is the mark.
 *   - `sizePx` picked the render tier off the CSS size rather than the
 *     backing store's, because tiering off `canvas.width` drew a
 *     different figure on a 2x display. v2 draws one figure at every
 *     size; only grain count and radius vary, and both are plain options
 *     (`count`, `radius`) the caller can set. GrainMark.jsx derives them
 *     from the CSS size, which keeps the mark the same picture at 1x and
 *     2x without the module having to know about devicePixelRatio.
 *
 * So a later revision of the mark drops in as a straight file copy.
 * See docs/brand.md for what the mark means and where grain uses it.
 */

/**
 * grain-mark.js — Grain brand mark, v2 (grainy glyph system)
 *
 * Four glyphs derived from square-plate Chladni eigenmodes, rendered as
 * region fills — either solid, or as grains that fly between glyphs on a
 * mode change. No ring: the circle is an invisible clip.
 *
 * ES module, no dependencies.
 *
 *   import { createGrainMark, renderStatic, GLYPHS } from './grain-mark.js';
 *   const mark = createGrainMark(canvas, { theme:'light' });
 *   mark.start();                       // cycle through the four glyphs
 *   mark.setMode(2);                    // fly to a glyph (index into GLYPHS)
 *   renderStatic(canvas, { slot:2 });   // static logo: 2·3 (−) rosette, grains
 *   renderStatic(icon,   { slot:2, style:'solid' });
 */

// ---------- tuned constants ----------
export const DEFAULTS = {
  snapSeconds: 1.3, // flight time between glyphs
  dwellSeconds: 0.33, // hold on each glyph
  scatter: 0.07, // fraction of frame radius grains stray mid-flight
  theme: "dark",
};

// [n, m, sign] — grid-and-loops, diamond, rosette, plus
export const GLYPHS = [
  [1, 3, 1],
  [1, 2, 1],
  [2, 3, -1],
  [2, 3, 1],
];
export const STATIC_SLOT = 2; // 2·3 (−) rosette is the static logo

export const THEMES = {
  dark: { grain: "#D9A441", grainRGB: [0xd9, 0xa4, 0x41] },
  light: { grain: "#8A6A2E", grainRGB: [0x8a, 0x6a, 0x2e] },
};

/** Grain count / radius by canvas pixel size. */
export function grainSpec(W) {
  if (W >= 300) return { count: 3200, radius: 0.0055 };
  if (W <= 28)
    return { count: Math.round(230 * (W / 28) * (W / 28)), radius: 0.85 / W };
  return { count: Math.round(140 * (W / 28) * (W / 28)), radius: 1.05 / W };
}

// ---------- glyph definitions ----------
export function fieldVal(nx, ny, n, m, sign) {
  const px = nx * Math.PI,
    py = ny * Math.PI;
  return (
    Math.cos(n * px) * Math.cos(m * py) +
    sign * Math.cos(m * px) * Math.cos(n * py)
  );
}

/** Zoom of the field relative to the frame (>1 crops in, <1 pulls out). */
export function glyphZoom(n, m, sg) {
  if (n === 2 && m === 3 && sg < 0) return 0.78; // rosette
  if (n === 2 && m === 3 && sg > 0) return 1.5; // plus fills the circle
  return 1;
}

/** Scalar whose positive region is filled. */
export function fillScalar(n, m, sg, nx, ny) {
  if (n === 1 && m === 3 && sg > 0) {
    // factors as 2ab(2a²+2b²−3): fill the closed loops; grid lines are overlay strokes
    const a = Math.cos(Math.PI * nx),
      b = Math.cos(Math.PI * ny);
    return 2 * a * a + 2 * b * b - 3;
  }
  return -fieldVal(nx, ny, n, m, sg); // inverted fill for the rest
}

/** Stroke overlay lines in field units (frame = unit circle). */
export function overlayLines(n, m, sg) {
  if (n === 1 && m === 3 && sg > 0) {
    const h = Math.sqrt(1 - 0.25);
    return [
      [-0.5, -h, -0.5, h],
      [0.5, -h, 0.5, h],
      [-h, -0.5, h, -0.5],
      [-h, 0.5, h, 0.5],
    ];
  }
  return [];
}

// ---------- geometry ----------
function frameOf(canvas) {
  const W = canvas.width,
    H = canvas.height;
  return { W, H, cx: W / 2, cy: H / 2, R: Math.min(W, H) * 0.44 };
}
const inCircle = (f, x, y) => {
  const dx = x - f.cx,
    dy = y - f.cy;
  return dx * dx + dy * dy <= f.R * f.R;
};

/** Evenly stippled points across the glyph's filled region plus its overlay lines. */
export function sampleGlyph(frame, glyph, count) {
  const [n, m, sg] = glyph;
  const Rf = frame.R * glyphZoom(n, m, sg);
  const inside = (x, y) =>
    inCircle(frame, x, y) &&
    fillScalar(n, m, sg, (x - frame.cx) / Rf, (y - frame.cy) / Rf) > 0;

  const lines = overlayLines(n, m, sg);
  const lineCount = lines.length
    ? Math.round(count * (lines.length > 2 ? 0.3 : 0.22))
    : 0;
  const areaCount = count - lineCount;

  // estimate filled area to derive Poisson spacing
  let hits = 0;
  const tries = 800;
  for (let i = 0; i < tries; i++) {
    const x = frame.cx + (Math.random() * 2 - 1) * frame.R,
      y = frame.cy + (Math.random() * 2 - 1) * frame.R;
    if (inside(x, y)) hits++;
  }
  const area = Math.max(1, (hits / tries) * 4 * frame.R * frame.R);
  const minD = Math.sqrt(area / areaCount) * 0.8;

  // grid-accelerated dart throwing
  const cell = minD / Math.SQRT2,
    cols = Math.ceil(frame.W / cell) + 1,
    rows = Math.ceil(frame.H / cell) + 1;
  const grid = new Int32Array(cols * rows).fill(-1);
  const pts = [];
  let attempts = 0;
  while (pts.length < areaCount && attempts < areaCount * 60) {
    attempts++;
    const x = frame.cx + (Math.random() * 2 - 1) * frame.R,
      y = frame.cy + (Math.random() * 2 - 1) * frame.R;
    if (!inside(x, y)) continue;
    const gx = Math.floor(x / cell),
      gy = Math.floor(y / cell);
    let ok = true;
    for (
      let yy = Math.max(0, gy - 2);
      yy <= Math.min(rows - 1, gy + 2) && ok;
      yy++
    ) {
      for (
        let xx = Math.max(0, gx - 2);
        xx <= Math.min(cols - 1, gx + 2);
        xx++
      ) {
        const idx = grid[yy * cols + xx];
        if (idx >= 0) {
          const p = pts[idx];
          if ((p.x - x) ** 2 + (p.y - y) ** 2 < minD * minD) {
            ok = false;
            break;
          }
        }
      }
    }
    if (!ok) continue;
    grid[gy * cols + gx] = pts.length;
    pts.push({ x, y });
  }

  if (lineCount) {
    const L = lines.map(([x1, y1, x2, y2]) => ({
      x1: frame.cx + x1 * Rf,
      y1: frame.cy + y1 * Rf,
      x2: frame.cx + x2 * Rf,
      y2: frame.cy + y2 * Rf,
    }));
    const lens = L.map((l) => Math.hypot(l.x2 - l.x1, l.y2 - l.y1)),
      tot = lens.reduce((a, b) => a + b, 0);
    L.forEach((l, i) => {
      const k = Math.max(2, Math.round((lineCount * lens[i]) / tot));
      for (let j = 0; j < k; j++) {
        const f = (j + 0.5) / k;
        pts.push({ x: l.x1 + (l.x2 - l.x1) * f, y: l.y1 + (l.y2 - l.y1) * f });
      }
    });
  }
  while (pts.length < count)
    pts.push({
      ...(pts[Math.floor(Math.random() * pts.length)] || {
        x: frame.cx,
        y: frame.cy,
      }),
    });
  return pts.slice(0, count);
}

function slotGlyph(slot) {
  const L = GLYPHS.length;
  return GLYPHS[((slot % L) + L) % L];
}

// ---------- static ----------
/** style: 'grains' (default) | 'solid'. Pass slot (index) or glyph ([n,m,sign]). */
export function renderStatic(canvas, opts = {}) {
  const o = { ...DEFAULTS, ...opts };
  const ctx = canvas.getContext("2d");
  const f = frameOf(canvas);
  const theme = THEMES[o.theme] || THEMES.dark;
  const glyph = o.glyph || slotGlyph(o.slot ?? STATIC_SLOT);
  const [n, m, sg] = glyph;
  ctx.clearRect(0, 0, f.W, f.H);

  if (o.style === "solid") {
    const Rf = f.R * glyphZoom(n, m, sg);
    const SS = 3,
      img = ctx.createImageData(f.W, f.H),
      d = img.data,
      rgb = theme.grainRGB;
    for (let y = 0; y < f.H; y++)
      for (let x = 0; x < f.W; x++) {
        let cover = 0;
        for (let sy = 0; sy < SS; sy++)
          for (let sx = 0; sx < SS; sx++) {
            const px = x + (sx + 0.5) / SS,
              py = y + (sy + 0.5) / SS;
            if (!inCircle(f, px, py)) continue;
            if (fillScalar(n, m, sg, (px - f.cx) / Rf, (py - f.cy) / Rf) > 0)
              cover++;
          }
        const i = (y * f.W + x) * 4;
        d[i] = rgb[0];
        d[i + 1] = rgb[1];
        d[i + 2] = rgb[2];
        d[i + 3] = Math.round((cover / 9) * 255);
      }
    ctx.putImageData(img, 0, 0);
    const lines = overlayLines(n, m, sg);
    if (lines.length) {
      ctx.save();
      ctx.beginPath();
      ctx.arc(f.cx, f.cy, f.R, 0, Math.PI * 2);
      ctx.clip();
      ctx.strokeStyle = theme.grain;
      ctx.lineWidth = Math.max(1, f.W * 0.07);
      ctx.lineCap = "round";
      for (const [x1, y1, x2, y2] of lines) {
        ctx.beginPath();
        ctx.moveTo(f.cx + x1 * Rf, f.cy + y1 * Rf);
        ctx.lineTo(f.cx + x2 * Rf, f.cy + y2 * Rf);
        ctx.stroke();
      }
      ctx.restore();
    }
    return;
  }

  const spec = grainSpec(f.W);
  const pts = sampleGlyph(f, glyph, o.count ?? spec.count);
  ctx.save();
  ctx.beginPath();
  ctx.arc(f.cx, f.cy, f.R, 0, Math.PI * 2);
  ctx.clip();
  ctx.fillStyle = theme.grain;
  const r = Math.max(0.6, f.W * (o.radius ?? spec.radius));
  for (const p of pts) {
    ctx.beginPath();
    ctx.arc(p.x, p.y, r, 0, Math.PI * 2);
    ctx.fill();
  }
  ctx.restore();
}

// ---------- animated ----------
export function createGrainMark(canvas, opts = {}) {
  const o = { ...DEFAULTS, ...opts };
  const ctx = canvas.getContext("2d");
  const f = frameOf(canvas);
  const spec = grainSpec(f.W);
  const count = o.count ?? spec.count,
    radius = o.radius ?? spec.radius;
  let theme = THEMES[o.theme] || THEMES.dark;

  const grains = [];
  for (let i = 0; i < count; i++)
    grains.push({
      x: f.cx,
      y: f.cy,
      sx: f.cx,
      sy: f.cy,
      tx: f.cx,
      ty: f.cy,
      kx: 0,
      ky: 0,
    });

  function assignTargets(glyph) {
    const targets = sampleGlyph(f, glyph, count);
    const cell = Math.max(4, f.R * 0.08),
      cols = Math.ceil(f.W / cell),
      rows = Math.ceil(f.H / cell);
    const buckets = Array.from({ length: cols * rows }, () => []);
    grains.forEach((g, i) => {
      const bx = Math.min(cols - 1, Math.max(0, Math.floor(g.x / cell))),
        by = Math.min(rows - 1, Math.max(0, Math.floor(g.y / cell)));
      buckets[by * cols + bx].push(i);
    });
    for (const t of targets.slice().sort(() => Math.random() - 0.5)) {
      const bx = Math.min(cols - 1, Math.max(0, Math.floor(t.x / cell))),
        by = Math.min(rows - 1, Math.max(0, Math.floor(t.y / cell)));
      let best = -1,
        bd = Infinity,
        bb = null,
        bp = -1;
      for (let ring = 0; ring < Math.max(cols, rows); ring++) {
        for (let yy = by - ring; yy <= by + ring; yy++) {
          if (yy < 0 || yy >= rows) continue;
          for (let xx = bx - ring; xx <= bx + ring; xx++) {
            if (xx < 0 || xx >= cols) continue;
            if (Math.abs(yy - by) !== ring && Math.abs(xx - bx) !== ring)
              continue;
            const b = buckets[yy * cols + xx];
            for (let q = 0; q < b.length; q++) {
              const g = grains[b[q]];
              const dd = (g.x - t.x) ** 2 + (g.y - t.y) ** 2;
              if (dd < bd) {
                bd = dd;
                best = b[q];
                bb = b;
                bp = q;
              }
            }
          }
        }
        if (best >= 0 && Math.sqrt(bd) < ring * cell) break;
      }
      if (best < 0) break;
      bb.splice(bp, 1);
      const g = grains[best];
      g.sx = g.x;
      g.sy = g.y;
      g.tx = t.x;
      g.ty = t.y;
      const a = Math.random() * Math.PI * 2,
        mag = (0.3 + Math.random() * 0.7) * f.R * o.scatter;
      g.kx = Math.cos(a) * mag;
      g.ky = Math.sin(a) * mag;
    }
  }
  function place(k) {
    const bulge = Math.sin(Math.PI * k);
    for (const g of grains) {
      g.x = g.sx + (g.tx - g.sx) * k + g.kx * bulge;
      g.y = g.sy + (g.ty - g.sy) * k + g.ky * bulge;
    }
  }
  function draw() {
    ctx.clearRect(0, 0, f.W, f.H);
    ctx.save();
    ctx.beginPath();
    ctx.arc(f.cx, f.cy, f.R, 0, Math.PI * 2);
    ctx.clip();
    ctx.fillStyle = theme.grain;
    const r = Math.max(0.6, f.W * radius);
    for (const g of grains) {
      ctx.beginPath();
      ctx.arc(g.x, g.y, r, 0, Math.PI * 2);
      ctx.fill();
    }
    ctx.restore();
  }

  let idx = 0,
    snapT = 1,
    dwellT = 0,
    auto = false,
    raf = null,
    last = 0;
  function setMode(i) {
    idx = ((i % GLYPHS.length) + GLYPHS.length) % GLYPHS.length;
    assignTargets(GLYPHS[idx]);
    snapT = 0;
    dwellT = 0;
    if (!raf) loop();
  }
  function loop() {
    last = performance.now();
    const step = (now) => {
      const dt = Math.min(0.05, (now - last) / 1000);
      last = now;
      if (snapT < 1) {
        snapT = Math.min(1, snapT + dt / o.snapSeconds);
        const e =
          snapT < 0.5 ? 4 * snapT ** 3 : 1 - Math.pow(-2 * snapT + 2, 3) / 2;
        place(e);
        draw();
      } else if (auto) {
        dwellT += dt;
        if (dwellT >= o.dwellSeconds) setMode(idx + 1);
      }
      if (auto || snapT < 1) raf = requestAnimationFrame(step);
      else raf = null;
    };
    raf = requestAnimationFrame(step);
  }
  const start = o.slot ?? 0;
  idx = start;
  assignTargets(GLYPHS[idx]);
  place(1);
  draw();

  return {
    start() {
      auto = true;
      if (!raf) loop();
    },
    stop() {
      auto = false;
    },
    setMode,
    next() {
      setMode(idx + 1);
    },
    get mode() {
      return idx;
    },
    setTheme(name) {
      theme = THEMES[name] || theme;
      draw();
    },
    destroy() {
      auto = false;
      if (raf) cancelAnimationFrame(raf);
      raf = null;
    },
  };
}
