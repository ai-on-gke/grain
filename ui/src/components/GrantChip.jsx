import { useEffect, useState } from "react";
import { Box, Chip, Tooltip } from "@mui/material";
import { describeGrant, parseGrant } from "../capabilityArgs.js";
import { capabilityMethods } from "./capabilityMethods.js";

// GrantChip is one of a task's capability grants as a chip, the same
// wherever a grant is listed -- task rows and cards, the task pane's
// grants, the new-task form, schedules and templates. A stored grant can
// be a long <ref>=<json> string; the chip reads short instead
// (describeGrant: "workspace › open", then only the arguments that
// differ from their defaults, cut at about 32 characters) and its
// tooltip, on hover and on keyboard focus, has the whole of it: the
// capability id, the method and every argument. The tooltip describes
// the chip (aria-describedby) rather than renaming it, so the chip's
// accessible name is still its short label.
//
// method is the method listing's entry for a server grant's ref when the
// caller has the listing (GrantsPicker); left undefined the chip reads
// the shared, cached listing itself so its defaults can be left out.
// null means the caller looked and there is none. note goes above the
// grant in the tooltip (a warning), ariaSuffix after the label in the
// accessible name; anything else is the Chip's own props.
export default function GrantChip({
  grant,
  config,
  method,
  note,
  ariaSuffix = "",
  sx,
  ...chipProps
}) {
  const fetched = useListedMethod(grant, method === undefined);
  const d = describeGrant(grant, {
    method: method === undefined ? fetched : method,
    config,
  });
  const label = d.args ? `${d.label} · ${d.args}` : d.label;
  const tip = (
    <Box
      component="span"
      sx={{ display: "block", whiteSpace: "pre-line", wordBreak: "break-word" }}
      data-grant-tooltip
    >
      {note ? `${note}\n\n` : ""}
      {d.lines.join("\n")}
    </Box>
  );
  return (
    <Tooltip title={tip} describeChild>
      <Chip
        size="small"
        tabIndex={chipProps.onClick || chipProps.onDelete ? undefined : 0}
        data-grant-chip={d.kind}
        {...chipProps}
        label={label}
        aria-label={`${label}${ariaSuffix}`}
        sx={{ maxWidth: "100%", ...sx }}
      />
    </Tooltip>
  );
}

// useListedMethod is the method listing's entry for a server grant's
// ref, read from the listing every grant editor on the page shares, or
// undefined until (or unless) it has one.
function useListedMethod(grant, wanted) {
  const { kind } = describeGrant(grant);
  const ref = kind === "method" ? parseGrant(grant).ref : "";
  const [found, setFound] = useState(undefined);
  useEffect(() => {
    if (!wanted || !ref) return undefined;
    let live = true;
    capabilityMethods()
      .then((list) => {
        if (!live || !Array.isArray(list)) return;
        setFound(list.find((m) => m?.ref === ref));
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [wanted, ref]);
  return found;
}
