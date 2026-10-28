#!/usr/bin/env python3
"""Writes the SVG assets for the three options: a refreshed mark / app
icon, a status icon set and a navigation icon set, in the style each
option calls for. Reuses the brand's own rosette path (ui/public/
grain-mark-light.svg) so the mark stays the mark.

  python3 icons.py            # writes option-{1,2,3}/*.svg next to tools/
"""
import os, re, sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, "../../../.."))
OUT = os.path.join(HERE, "..")

with open(os.path.join(ROOT, "ui/public/grain-mark-light.svg")) as f:
    ROSETTE = re.search(r'<path[^>]*\sd="([^"]+)"', f.read()).group(1)

# ---- style per option -------------------------------------------------
STYLES = {
    1: dict(stroke=1.5, cap="round", radius=6, light="#8a6a2e", dark="#d9a441", ink="#24221d", paper="#f3ecdb", plate="#17130a",
            states=dict(light=dict(running="#8a6a2e", queued="#6e6a60", blocked="#55657a", merging="#0b5fc0", merged="#15803d",
                                   failed="#c03a2f", awaiting="#7e22ce", proposed="#a35406", deferred="#55657a", submit="#c2410c"),
                        dark=dict(running="#d9a441", queued="#a39f94", blocked="#98a8bd", merging="#6fb1ff", merged="#4cc466",
                                  failed="#f0786d", awaiting="#c89bfc", proposed="#f5a524", deferred="#98a8bd", submit="#fb9a4a"))),
    2: dict(stroke=1.5, cap="round", radius=5, light="#7a5a22", dark="#e0b45c", ink="#1b1c1e", paper="#ffffff", plate="#121417",
            states=dict(light=dict(running="#7a5a22", queued="#6b6e75", blocked="#475569", merging="#1d4ed8", merged="#15803d",
                                   failed="#c81e1e", awaiting="#7c3aed", proposed="#b4530a", deferred="#475569", submit="#c2410c"),
                        dark=dict(running="#e0b45c", queued="#9aa0a8", blocked="#94a3b8", merging="#60a5fa", merged="#4ade80",
                                  failed="#f87171", awaiting="#c084fc", proposed="#f5a524", deferred="#94a3b8", submit="#fb923c"))),
    3: dict(stroke=2, cap="round", radius=8, light="#7d5a1c", dark="#e3b65a", ink="#1b1916", paper="#f4f0e8", plate="#1b1916",
            states=dict(light=dict(running="#7d5a1c", queued="#6e675a", blocked="#4b5a6e", merging="#1a56c4", merged="#167a3c",
                                   failed="#b3261e", awaiting="#6d28d9", proposed="#a8530a", deferred="#4b5a6e", submit="#b83a0b"),
                        dark=dict(running="#e3b65a", queued="#a09a8c", blocked="#9aa9bd", merging="#6fb1ff", merged="#4fcf72",
                                  failed="#f4776c", awaiting="#c99cff", proposed="#f6a623", deferred="#9aa9bd", submit="#fb9a4a"))),
}

def svg(body, size=16, vb=None, extra=""):
    vb = vb or f"0 0 {size} {size}"
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{vb}" width="{size}" height="{size}" '
            f'fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" {extra}>{body}</svg>\n')

# ---- status icons (16px grid, drawn as strokes in currentColor) --------
def status_icons(sw):
    c = 'cx="8" cy="8" r="6"'
    return {
        "running":  f'<path d="M14 8a6 6 0 1 1-1.8-4.3" stroke-width="{sw}"/><path d="M14 3.5V8h-4.5" stroke-width="{sw}"/>',
        "queued":   f'<circle {c} stroke-width="{sw}" stroke-dasharray="2.4 2.2"/><circle cx="8" cy="8" r="1.4" fill="currentColor" stroke="none"/>',
        "proposed": f'<circle {c} stroke-width="{sw}"/><path d="M8 5v3.5l2 1.5" stroke-width="{sw}"/>',
        "awaiting": f'<path d="M3 3.5h10v7H7.5L4.5 13v-2.5H3z" stroke-width="{sw}"/><path d="M6.6 6.4a1.5 1.5 0 1 1 2.1 1.4c-.5.2-.7.5-.7 1" stroke-width="{sw}"/><circle cx="8" cy="10.3" r=".6" fill="currentColor" stroke="none"/>',
        "submit":   f'<circle cx="4" cy="4" r="1.6" stroke-width="{sw}"/><circle cx="4" cy="12" r="1.6" stroke-width="{sw}"/><circle cx="12" cy="8" r="1.6" stroke-width="{sw}"/><path d="M4 5.6v4.8M5.4 4.6l5.2 2.6M5.4 11.4l5.2-2.6" stroke-width="{sw}"/>',
        "merging":  f'<circle cx="4" cy="3.5" r="1.6" stroke-width="{sw}"/><circle cx="4" cy="12.5" r="1.6" stroke-width="{sw}"/><circle cx="12" cy="7" r="1.6" stroke-width="{sw}"/><path d="M4 5.1v5.8M4 9c0-2.5 3-3.5 6.4-3.5" stroke-width="{sw}"/>',
        "merged":   f'<circle {c} stroke-width="{sw}"/><path d="M5.2 8.2l2 2 3.8-4.2" stroke-width="{sw}"/>',
        "failed":   f'<circle {c} stroke-width="{sw}"/><path d="M5.8 5.8l4.4 4.4M10.2 5.8l-4.4 4.4" stroke-width="{sw}"/>',
        "blocked":  f'<circle {c} stroke-width="{sw}"/><path d="M4 12L12 4" stroke-width="{sw}"/>',
        "deferred": f'<circle {c} stroke-width="{sw}"/><path d="M6.3 5.5v5M9.7 5.5v5" stroke-width="{sw}"/>',
        "closed":   f'<circle {c} stroke-width="{sw}"/><path d="M5 8h6" stroke-width="{sw}"/>',
        "interactive": f'<path d="M2.5 4h11v6.5h-5l-2.5 2.2v-2.2h-3.5z" stroke-width="{sw}"/><path d="M5.5 7h5" stroke-width="{sw}"/>',
    }

# ---- navigation icons (16px grid) -------------------------------------
def nav_icons(sw):
    return {
        "inbox":    f'<path d="M2.5 9.5l1.8-5.5h7.4l1.8 5.5v3h-11z" stroke-width="{sw}"/><path d="M2.5 9.5h3.2l.8 1.6h3l.8-1.6h3.2" stroke-width="{sw}"/>',
        "tasks":    f'<path d="M3 4.5h1.5M3 8h1.5M3 11.5h1.5M6.5 4.5H13M6.5 8H13M6.5 11.5H13" stroke-width="{sw}"/>',
        "board":    f'<rect x="2.5" y="3" width="3.2" height="10" rx=".8" stroke-width="{sw}"/><rect x="6.4" y="3" width="3.2" height="7" rx=".8" stroke-width="{sw}"/><rect x="10.3" y="3" width="3.2" height="4.5" rx=".8" stroke-width="{sw}"/>',
        "sessions": f'<path d="M2.5 3.5h11v7H8l-3 2.5v-2.5H2.5z" stroke-width="{sw}"/>',
        "processes": f'<circle cx="8" cy="8" r="2.2" stroke-width="{sw}"/><path d="M8 2.5v2M8 11.5v2M2.5 8h2M11.5 8h2M4.1 4.1l1.4 1.4M10.5 10.5l1.4 1.4M4.1 11.9l1.4-1.4M10.5 5.5l1.4-1.4" stroke-width="{sw}"/>',
        "ci":       f'<path d="M3 11.5l3-7 2 4.5 1.5-2.5 3.5 5z" stroke-width="{sw}"/><path d="M2.5 13h11" stroke-width="{sw}"/>',
        "repos":    f'<path d="M4 2.5h8.5v11H4a1.5 1.5 0 0 1 0-3h8.5" stroke-width="{sw}"/><path d="M4 2.5v8" stroke-width="{sw}"/>',
        "metrics":  f'<path d="M2.5 13V3M2.5 13H13.5" stroke-width="{sw}"/><path d="M4.5 10.5l2.5-3 2.5 2 3.5-5" stroke-width="{sw}"/>',
        "settings": f'<circle cx="8" cy="8" r="2" stroke-width="{sw}"/><path d="M8 2.5v1.6M8 11.9v1.6M2.5 8h1.6M11.9 8h1.6M4.1 4.1l1.1 1.1M10.8 10.8l1.1 1.1M4.1 11.9l1.1-1.1M10.8 5.2l1.1-1.1" stroke-width="{sw}"/>',
        "system":   f'<rect x="2.5" y="3" width="11" height="7" rx="1.2" stroke-width="{sw}"/><path d="M6 13h4M8 10v3" stroke-width="{sw}"/>',
        "alerts":   f'<path d="M8 2.5a3.5 3.5 0 0 0-3.5 3.5v2.3L3.2 10.5h9.6l-1.3-2.2V6A3.5 3.5 0 0 0 8 2.5z" stroke-width="{sw}"/><path d="M6.7 12.5a1.4 1.4 0 0 0 2.6 0" stroke-width="{sw}"/>',
        "new":      f'<path d="M8 3v10M3 8h10" stroke-width="{sw}"/>',
    }

# ---- nodal-line navigation icons ------------------------------------------
# Chladni-like without the blob problem: every destination is a round plate
# (a faint 1px circle) carrying its own *nodal lines* -- the thin curves
# sand settles on -- and each figure has a different topology (parallel
# lines, columns, a fork, a wave, radial spokes, nested rings), which is
# what makes them tellable apart at 16px where the filled figures were not.
def nodal_icons(sw):
    plate = '<circle cx="8" cy="8" r="7" stroke-width="1" opacity=".45"/>'
    sw2 = sw
    return {
        "inbox":     plate + f'<path d="M3.2 9.2h2.6l.9 1.6h2.6l.9-1.6h2.6" stroke-width="{sw2}"/><path d="M8 3.2v3.6" stroke-width="{sw2}"/>',
        "tasks":     plate + f'<path d="M4.3 5.6h7.4M4.3 8h7.4M4.3 10.4h7.4" stroke-width="{sw2}"/>',
        "board":     plate + f'<path d="M5.6 3.6v8.8M8 3.6v6M10.4 3.6v4.4" stroke-width="{sw2}"/>',
        "sessions":  plate + f'<path d="M4.6 6.6a3.4 3.4 0 0 1 6.8 0 3.4 3.4 0 0 1-3.4 3.4H6.2L4.8 11.6V9.4" stroke-width="{sw2}"/>',
        "processes": plate + f'<path d="M8 2.6v2.4M8 11v2.4M2.6 8H5M11 8h2.4M4.2 4.2l1.7 1.7M10.1 10.1l1.7 1.7M4.2 11.8l1.7-1.7M10.1 5.9l1.7-1.7" stroke-width="{sw2}"/><circle cx="8" cy="8" r="1.8" stroke-width="{sw2}"/>',
        "ci":        plate + f'<path d="M2.8 9.4h2.4l1.4-4.2 1.6 6.2 1.4-4.4 1 2.4h2.6" stroke-width="{sw2}"/>',
        "repos":     plate + f'<path d="M8 13V8.2M8 8.2L5 5.2M8 8.2l3-3" stroke-width="{sw2}"/><circle cx="5" cy="4.4" r="1.1" stroke-width="{sw2}"/><circle cx="11" cy="4.4" r="1.1" stroke-width="{sw2}"/>',
        "metrics":   plate + f'<path d="M4 11.6V9.4M6.7 11.6V6.4M9.3 11.6V8.2M12 11.6V4.6" stroke-width="{sw2}"/>',
        "settings":  plate + f'<circle cx="8" cy="8" r="2.6" stroke-width="{sw2}"/><path d="M8 2.6v1.6M8 11.8v1.6M2.6 8h1.6M11.8 8h1.6" stroke-width="{sw2}"/>',
        "system":    plate + f'<rect x="4.4" y="4.4" width="7.2" height="7.2" rx="1" stroke-width="{sw2}"/><path d="M4.4 8h7.2M8 4.4v7.2" stroke-width="{sw2}" opacity=".7"/>',
        "alerts":    plate + f'<circle cx="8" cy="8.6" r="1.6" stroke-width="{sw2}"/><path d="M8 3.2v1.6M4.2 5.4l1.1 1.1M11.8 5.4l-1.1 1.1" stroke-width="{sw2}"/><path d="M5.4 12.2h5.2" stroke-width="{sw2}"/>',
        "new":       plate + f'<path d="M8 4.2v7.6M4.2 8h7.6" stroke-width="{sw2}"/>',
        "director":  plate + f'<path d="M8 3.4l3.6 2.1v4.2L8 11.8 4.4 9.7V5.5z" stroke-width="{sw2}"/><circle cx="8" cy="7.8" r="1.3" stroke-width="{sw2}"/>',
        "boards":    plate + f'<path d="M5.6 3.6v8.8M8 3.6v6M10.4 3.6v4.4" stroke-width="{sw2}"/>',
        "staging":   plate + f'<path d="M4.4 10.6l3.6-6 3.6 6z" stroke-width="{sw2}"/>',
    }

# ---- marks ---------------------------------------------------------------
def mark(option, theme):
    s = STYLES[option]
    fill = s["light"] if theme == "light" else s["dark"]
    if option == 1:
        # Brand mark unchanged; the deliverable is an app-icon tile.
        ground = s["paper"] if theme == "light" else s["plate"]
        return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 128 128" width="128" height="128">'
                f'<rect width="128" height="128" rx="28" fill="{ground}"/>'
                f'<g transform="translate(64 64) scale(.78) translate(-64 -64)">'
                f'<path fill="{fill}" fill-rule="evenodd" d="{ROSETTE}"/></g></svg>\n')
    if option == 2:
        # Monoline rosette: the same shape as a 3px stroke, the ledger's
        # own line weight, so it sits beside line icons without shouting.
        # 7 units of 128 is 1.75px at the rail's 32px, the ledger's own
        # line weight; the first cut at 3.5 units vanished at that size.
        return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 128 128" width="128" height="128">'
                f'<defs><clipPath id="c"><circle cx="64" cy="64" r="58"/></clipPath></defs>'
                f'<g clip-path="url(#c)"><path fill="none" stroke="{fill}" stroke-width="7" stroke-linejoin="round" '
                f'fill-rule="evenodd" d="{ROSETTE}"/></g></svg>\n')
    # option 3: the plate. A rounded square of ink with the rosette in
    # wheat; on a light ground the plate is still ink, which is the point.
    # The plate is a step lighter than the rail it sits on, with a faint
    # wheat edge, so the tile reads on the ink rail as well as on paper.
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 128 128" width="128" height="128">'
            f'<rect x="2" y="2" width="124" height="124" rx="24" fill="#2b2723" stroke="#e3b65a" stroke-opacity=".45" stroke-width="4"/>'
            f'<g transform="translate(64 64) scale(.8) translate(-64 -64)">'
            f'<path fill="#e3b65a" fill-rule="evenodd" d="{ROSETTE}"/></g>'
            f'<g fill="#e3b65a" opacity=".55"><circle cx="18" cy="18" r="2.5"/><circle cx="110" cy="18" r="2.5"/>'
            f'<circle cx="18" cy="110" r="2.5"/><circle cx="110" cy="110" r="2.5"/></g></svg>\n')

def sheet(option, theme, icons, kind):
    """A contact sheet of the icon set on the option's ground, for the README."""
    s = STYLES[option]
    ground = (s["paper"] if theme == "light" else s["plate"])
    if option == 3 and kind == "nav":
        ground = s["plate"]  # the rail is ink in both themes
    colour = s["ink"] if (theme == "light" and not (option == 3 and kind == "nav")) else "#ece7dc"
    states = s["states"][theme]
    cells = []
    x = 12
    for name, body in icons.items():
        col = states.get(name, colour) if kind == "status" else colour
        cells.append(f'<g transform="translate({x} 12) scale(2)" color="{col}" stroke="{col}">{body}</g>'
                     f'<text x="{x+16}" y="60" font-family="Inter, IBM Plex Sans, sans-serif" font-size="9" text-anchor="middle" fill="{colour}">{name}</text>')
        x += 56
    w = x + 4
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {w} 72" width="{w}" height="72" fill="none" '
            f'stroke-linecap="round" stroke-linejoin="round"><rect width="{w}" height="72" rx="8" fill="{ground}"/>{"".join(cells)}</svg>\n')

def main():
    for option, s in STYLES.items():
        d = os.path.join(OUT, f"option-{option}")
        os.makedirs(d, exist_ok=True)
        sw = s["stroke"]
        for name, body in status_icons(sw).items():
            with open(os.path.join(d, f"status-{name}.svg"), "w") as f:
                f.write(svg(body))
        for name, body in nav_icons(sw).items():
            with open(os.path.join(d, f"nav-{name}.svg"), "w") as f:
                f.write(svg(body))
        nodal = nodal_icons(sw)
        for name, body in nodal.items():
            with open(os.path.join(d, f"nodal-{name}.svg"), "w") as f:
                f.write(svg(body))
        import json
        with open(os.path.join(d, "nodal-icons.json"), "w") as f:
            json.dump({k: svg(v) for k, v in nodal.items()}, f)
        for theme in ("light", "dark"):
            with open(os.path.join(d, f"nodal-icons-{theme}.svg"), "w") as f:
                f.write(sheet(option, theme, nodal, "nav"))
            with open(os.path.join(d, f"mark-{theme}.svg"), "w") as f:
                f.write(mark(option, theme))
            with open(os.path.join(d, f"status-icons-{theme}.svg"), "w") as f:
                f.write(sheet(option, theme, status_icons(sw), "status"))
            with open(os.path.join(d, f"nav-icons-{theme}.svg"), "w") as f:
                f.write(sheet(option, theme, nav_icons(sw), "nav"))
        print("wrote", d)

if __name__ == "__main__":
    main()
