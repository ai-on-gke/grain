// remarkMermaid turns fenced ```mermaid blocks in an mdast tree into
// block-level diagram elements, and pairs consecutive Before / After
// Mermaid blocks into a single comparison element so architectural state
// transitions on tasks and implementation plans can be read side by side
// or flipped in place.
//
// Everything stays valid GitHub-flavoured Markdown on the wire: a task
// comment or design doc synced to GitHub still uses ordinary ```mermaid
// fences, with Before / After named either in the fence info string
// (` ```mermaid before ` / ` ```mermaid after `) or in the heading or bold
// label immediately above each block (`### Before` / `### After`). Raw
// `<mermaid-diagram>` HTML in prose is never promoted here -- remark-parse
// emits that as an `html` node, which react-markdown drops without
// rehype-raw.

const BEFORE_PREFIX = /^before(?:$|[\s:\-–—(])/i;
const AFTER_PREFIX = /^after(?:$|[\s:\-–—(])/i;

function isMermaidCode(node) {
  return (
    node?.type === "code" &&
    String(node.lang ?? "")
      .trim()
      .toLowerCase() === "mermaid"
  );
}

// Extracts plain text from an mdast inline subtree (headings, strong
// labels, inlineCode inside headings).
function mdastText(node) {
  if (!node) return "";
  if (typeof node.value === "string") return node.value;
  if (Array.isArray(node.children)) {
    return node.children.map(mdastText).join("");
  }
  return "";
}

// Strips `title="..."` / `title='...'` wrapper when present in fence meta,
// leaving bare text (`Request pipeline`, `before: Monolith`) as-is.
function unwrapMetaTitle(meta) {
  const raw = String(meta ?? "").trim();
  if (!raw) return "";
  const quoted = raw.match(/^title\s*=\s*(?:"([^"]*)"|'([^']*)')$/i);
  if (quoted) {
    return (quoted[1] ?? quoted[2] ?? "").trim();
  }
  const unquoted = raw.match(/^title\s*=\s*(\S.*)$/i);
  if (unquoted) {
    return unquoted[1].trim();
  }
  return raw;
}

function normalizeStateLabel(text, fallback) {
  const trimmed = text.replace(/\s*:\s*$/, "").trim();
  if (!trimmed) return fallback;
  if (trimmed.toLowerCase() === fallback.toLowerCase()) {
    return fallback;
  }
  // Capitalize leading `before` / `after` when written lowercase in fence
  // metadata (`before: monolith` -> `Before: monolith`).
  return trimmed.charAt(0).toUpperCase() + trimmed.slice(1);
}

// Reads a ```mermaid fence's meta string (` ```mermaid before: Monolith `
// or ` ```mermaid System overview `). Returns `{ role, label, title }`.
export function parseMermaidFenceMeta(meta) {
  const clean = unwrapMetaTitle(meta);
  if (!clean) {
    return { role: null, label: "", title: "" };
  }
  if (BEFORE_PREFIX.test(clean)) {
    return {
      role: "before",
      label: normalizeStateLabel(clean, "Before"),
      title: clean,
    };
  }
  if (AFTER_PREFIX.test(clean)) {
    return {
      role: "after",
      label: normalizeStateLabel(clean, "After"),
      title: clean,
    };
  }
  return { role: null, label: "", title: clean };
}

// Recognises a short heading (`### Before`, `### Before: Direct writes`)
// or standalone bold label paragraph (`**Before:**`) immediately preceding
// a Mermaid fence. `kind` encodes heading depth so `## Before` only pairs
// with `## After` and never swallows a mismatched subsection.
export function classifyBeforeAfterHeader(node) {
  if (!node) return null;

  if (node.type === "heading") {
    const text = mdastText(node).trim();
    if (!text || text.length > 140) return null;
    if (BEFORE_PREFIX.test(text)) {
      return {
        kind: `heading:${node.depth}`,
        role: "before",
        label: normalizeStateLabel(text, "Before"),
      };
    }
    if (AFTER_PREFIX.test(text)) {
      return {
        kind: `heading:${node.depth}`,
        role: "after",
        label: normalizeStateLabel(text, "After"),
      };
    }
    return null;
  }

  if (
    node.type === "paragraph" &&
    Array.isArray(node.children) &&
    node.children.length > 0 &&
    node.children[0].type === "strong"
  ) {
    const text = mdastText(node).trim();
    if (!text || text.length > 140 || text.includes("\n")) return null;
    // Require that the paragraph is a label rather than a full sentence
    // that happens to open with "**Before** we deploy, check...".
    const strongText = mdastText(node.children[0]).trim();
    const rest = text.slice(strongText.length).trim();
    if (
      rest &&
      !/^[:\-–—(]/.test(rest) &&
      !BEFORE_PREFIX.test(strongText) &&
      !AFTER_PREFIX.test(strongText)
    ) {
      return null;
    }
    // Reject prose sentences (`**Before** we migrate the store, ...`) where
    // unbolded words follow `**Before**` without a colon or dash separator.
    if (
      /^(before|after)$/i.test(strongText) &&
      rest &&
      !/^[:\-–—(]/.test(rest)
    ) {
      return null;
    }
    if (BEFORE_PREFIX.test(text)) {
      return {
        kind: "label",
        role: "before",
        label: normalizeStateLabel(text, "Before"),
      };
    }
    if (AFTER_PREFIX.test(text)) {
      return {
        kind: "label",
        role: "after",
        label: normalizeStateLabel(text, "After"),
      };
    }
  }

  return null;
}

function makeDiagramNode(codeNode) {
  const { title } = parseMermaidFenceMeta(codeNode.meta);
  return {
    type: "mermaidDiagram",
    data: {
      hName: "mermaid-diagram",
      hProperties: {
        "data-code": String(codeNode.value ?? ""),
        ...(title ? { "data-title": title } : {}),
      },
    },
  };
}

function makeComparisonNode(beforeCode, afterCode, beforeLabel, afterLabel) {
  return {
    type: "mermaidComparison",
    data: {
      hName: "mermaid-comparison",
      hProperties: {
        "data-before-code": String(beforeCode ?? ""),
        "data-after-code": String(afterCode ?? ""),
        "data-before-label": beforeLabel || "Before",
        "data-after-label": afterLabel || "After",
      },
    },
  };
}

function transformChildren(parent) {
  if (!parent || !Array.isArray(parent.children)) return;

  const out = [];
  const children = parent.children;
  let i = 0;

  while (i < children.length) {
    // 1. Heading / bold-label Before + mermaid + After + mermaid pair
    if (i + 3 < children.length) {
      const firstHeader = classifyBeforeAfterHeader(children[i]);
      const secondHeader = classifyBeforeAfterHeader(children[i + 2]);
      if (
        firstHeader &&
        secondHeader &&
        firstHeader.role === "before" &&
        secondHeader.role === "after" &&
        firstHeader.kind === secondHeader.kind &&
        isMermaidCode(children[i + 1]) &&
        isMermaidCode(children[i + 3])
      ) {
        out.push(
          makeComparisonNode(
            children[i + 1].value,
            children[i + 3].value,
            firstHeader.label,
            secondHeader.label,
          ),
        );
        i += 4;
        continue;
      }
    }

    // 2. Two consecutive ```mermaid blocks carrying `before` and `after`
    //    in their fence metadata.
    if (
      i + 1 < children.length &&
      isMermaidCode(children[i]) &&
      isMermaidCode(children[i + 1])
    ) {
      const firstMeta = parseMermaidFenceMeta(children[i].meta);
      const secondMeta = parseMermaidFenceMeta(children[i + 1].meta);
      if (firstMeta.role === "before" && secondMeta.role === "after") {
        out.push(
          makeComparisonNode(
            children[i].value,
            children[i + 1].value,
            firstMeta.label,
            secondMeta.label,
          ),
        );
        i += 2;
        continue;
      }
    }

    // 3. Standalone ```mermaid block -> block-level <mermaid-diagram>
    if (isMermaidCode(children[i])) {
      out.push(makeDiagramNode(children[i]));
      i += 1;
      continue;
    }

    transformChildren(children[i]);
    out.push(children[i]);
    i += 1;
  }

  parent.children = out;
}

export default function remarkMermaid() {
  return (tree) => {
    transformChildren(tree);
  };
}
