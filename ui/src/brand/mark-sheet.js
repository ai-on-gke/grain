// The pre-rendered animation: the contract between the exporter that
// writes the sheets and the component that plays them, so the two cannot
// disagree about a frame count or a file name.
//
// Every mark small enough to have a sheet plays one instead of running
// the particle system. The frames are not an approximation of what the
// module paints -- scripts/export-mark-sheets.mjs captures them out of a
// real browser running the module itself, on a stubbed clock, so they
// are exactly the frames a live mark would have drawn.
//
// The sheet is an **alpha mask**, not a picture. Every pixel of the mark
// is the same colour at a varying alpha, so the frames carry only the
// alpha and the colour comes from CSS -- which is what makes one file
// serve both themes, and lets it follow a theme change with no reload.
// It is a grayscale+alpha PNG rather than a plain grayscale one for the
// same reason: a CSS mask reads the alpha channel, and an image without
// one masks nothing at all.
//
// Frames are stacked vertically rather than in a row. 43% of them are
// held (the crisp dwells), and stacked those become repeated scanlines
// that PNG's Up filter collapses to almost nothing; in a row they would
// be repeated *columns*, which it cannot.

/** Device pixels per CSS pixel the sheets are rendered at. */
export const SHEET_DPR = 2;

/** Frames a second. */
export const SHEET_FPS = 24;

// The mark moves slowly relative to its own size: at the peak of a
// flight a grain covers about half a CSS pixel per frame at 24fps, so
// the quantization is invisible and there is nothing to gain from more.
// Frames are the entire cost of the sheet, so this is the dial to reach
// for if that ever stops being true.

/**
 * The sizes a sheet is rendered for. Any other size runs the live
 * renderer instead, which is what the hero does and what any new call
 * site gets until a sheet is rendered for it.
 */
export const SHEET_SIZES = [20, 32];

/** Frames in a sheet -- one full cycle of all four glyphs. */
export const SHEET_FRAMES = 204;

/** How long that cycle takes, in ms. */
export const SHEET_LOOP_MS = Math.round((SHEET_FRAMES / SHEET_FPS) * 1000);

export const sheetHref = (cssPx) => `/grain-mark-${cssPx}.png`;

export const hasSheet = (cssPx) => SHEET_SIZES.includes(cssPx);
