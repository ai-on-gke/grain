import React, { useMemo, useRef, useState } from "react";
import {
  Box,
  Button,
  ButtonBase,
  Chip,
  Collapse,
  Divider,
  FormControl,
  IconButton,
  InputLabel,
  List,
  ListItem,
  ListItemText,
  MenuItem,
  Select,
  Stack,
  Switch,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import ArrowDownwardIcon from "@mui/icons-material/ArrowDownward";
import ArrowUpwardIcon from "@mui/icons-material/ArrowUpward";
import DeleteOutlineIcon from "@mui/icons-material/DeleteOutline";
import AddIcon from "@mui/icons-material/Add";
import PaletteOutlinedIcon from "@mui/icons-material/PaletteOutlined";
import CheckIcon from "@mui/icons-material/Check";
import FormatColorResetIcon from "@mui/icons-material/FormatColorReset";
import ItemGlyph from "./ItemGlyph.jsx";
import Overlay from "./Overlay.jsx";
import {
  CHLADNI_ICONS,
  DEFAULT_PINNED_ITEMS,
  PINNED_COLOR_OPTIONS,
  getPinnedItemInfo,
  pickerIconId,
  pinFromTarget,
  pinTargets,
  samePin,
} from "../pinnedNav.js";
import { usePinnedNav } from "../PinnedNavContext.jsx";
import {
  SIDEBAR_SECTIONS,
  saveHiddenSections,
  useHiddenSections,
} from "../sidebarSections.js";

// CustomizeSidebarOverlay is the left panel's one Customize control:
// which sections it shows (sidebarSections.js) and what is pinned at its
// top (pinnedNav.js), both this browser's own. Nothing is applied until
// Save, and Reset to default puts both back.
export default function CustomizeSidebarOverlay({
  open = true,
  onClose,
  sessions = [],
  boards = [],
  onSave: propOnSave,
}) {
  const { pinnedItems: currentPinned, setPinnedItems } = usePinnedNav();
  const currentHidden = useHiddenSections();
  const [draft, setDraft] = useState(() => currentPinned);
  const [draftHidden, setDraftHidden] = useState(() => currentHidden);
  const [editingIndex, setEditingIndex] = useState(null);
  const upRefs = useRef([]);
  const downRefs = useRef([]);
  const removeRefs = useRef([]);
  const resetBtnRef = useRef(null);
  const isDirty = () =>
    JSON.stringify(draft) !== JSON.stringify(currentPinned) ||
    draftHidden.join() !== currentHidden.join();

  const toggleSection = (id) => {
    const hidden = draftHidden.includes(id);
    setDraftHidden(
      SIDEBAR_SECTIONS.map((s) => s.id).filter((sid) =>
        sid === id ? !hidden : draftHidden.includes(sid),
      ),
    );
  };

  const [addKind, setAddKind] = useState("view");
  const [addId, setAddId] = useState("");
  const [addIcon, setAddIcon] = useState("");
  const [addColor, setAddColor] = useState("");

  const availableOptions = useMemo(() => {
    if (addKind === "view") {
      return pinTargets();
    }
    if (addKind === "session") {
      return sessions.map((s) => ({ id: s.id, label: s.title || s.id }));
    }
    if (addKind === "board") {
      return boards.map((b) => ({ id: b.id, label: b.name }));
    }
    return [];
  }, [addKind, sessions, boards]);

  const moveItem = (index, delta) => {
    const toIndex = index + delta;
    if (toIndex < 0 || toIndex >= draft.length) return;
    const next = [...draft];
    const [moved] = next.splice(index, 1);
    next.splice(toIndex, 0, moved);
    setDraft(next);
    if (editingIndex === index) {
      setEditingIndex(toIndex);
    } else if (editingIndex === toIndex) {
      setEditingIndex(index);
    }
    requestAnimationFrame(() => {
      if (toIndex === 0) downRefs.current[0]?.focus();
      else if (toIndex === next.length - 1) upRefs.current[toIndex]?.focus();
      else (delta < 0 ? upRefs : downRefs).current[toIndex]?.focus();
    });
  };

  const removeItem = (index) => {
    const remaining = draft.length - 1;
    setDraft(draft.filter((_, i) => i !== index));
    if (editingIndex === index) {
      setEditingIndex(null);
    } else if (editingIndex !== null && editingIndex > index) {
      setEditingIndex(editingIndex - 1);
    }
    requestAnimationFrame(() => {
      const next =
        remaining > 0
          ? removeRefs.current[Math.min(index, remaining - 1)]
          : resetBtnRef.current;
      next?.focus();
    });
  };

  const updateItem = (index, patch) => {
    setDraft((prev) => {
      const next = [...prev];
      const target = { ...next[index] };
      if ("icon" in patch) {
        if (patch.icon && patch.icon.trim()) {
          target.icon = patch.icon.trim();
        } else {
          delete target.icon;
          delete target.glyph;
        }
      }
      if ("color" in patch) {
        if (patch.color && patch.color.trim()) {
          target.color = patch.color.trim();
        } else {
          delete target.color;
        }
      }
      next[index] = target;
      return next;
    });
  };

  const candidatePin = addId
    ? addKind === "view"
      ? pinFromTarget(addId)
      : { kind: addKind, id: addId }
    : null;

  const addItem = () => {
    if (!candidatePin || draft.some((it) => samePin(it, candidatePin))) return;
    const newItem = { ...candidatePin };
    if (addIcon && addIcon.trim()) {
      newItem.icon = addIcon.trim();
    }
    if (addColor && addColor.trim()) {
      newItem.color = addColor.trim();
    }
    setDraft([...draft, newItem]);
    setAddId("");
    setAddIcon("");
    setAddColor("");
  };

  const handleReset = () => {
    setDraft(DEFAULT_PINNED_ITEMS);
    setDraftHidden([]);
    setEditingIndex(null);
  };

  const handleSave = () => {
    setPinnedItems(draft);
    saveHiddenSections(draftHidden);
    if (propOnSave) propOnSave(draft);
    if (onClose) onClose();
  };

  if (!open) return null;

  return (
    <Overlay onClose={onClose} isDirty={isDirty} wide label="Customize sidebar">
      <Typography variant="h6" component="h2" sx={{ mb: 0.5 }}>
        Customize sidebar
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        Choose which sections the left panel shows, and pin, reorder and color
        the shortcuts at its top. A hidden section still opens from its URL, and
        anything pinned stays under Pinned. These choices are kept in this
        browser.
      </Typography>

      <Typography
        variant="subtitle2"
        sx={{ mb: 1 }}
        id="sidebar-sections-heading"
      >
        Sections
      </Typography>
      <List
        disablePadding
        aria-labelledby="sidebar-sections-heading"
        sx={{
          border: 1,
          borderColor: "divider",
          borderRadius: 1.5,
          mb: 3,
          display: "grid",
          gridTemplateColumns: { xs: "1fr", sm: "1fr 1fr" },
        }}
      >
        {SIDEBAR_SECTIONS.map((section) => {
          const shown = !draftHidden.includes(section.id);
          return (
            <ListItem
              key={section.id}
              dense
              secondaryAction={
                <Switch
                  edge="end"
                  size="small"
                  checked={shown}
                  onChange={() => toggleSection(section.id)}
                  inputProps={{
                    role: "switch",
                    "aria-label": `Show ${section.label}`,
                  }}
                />
              }
            >
              <ListItemText
                primary={section.label}
                primaryTypographyProps={{ fontSize: "0.85rem" }}
              />
            </ListItem>
          );
        })}
      </List>

      <Typography variant="subtitle2" sx={{ mb: 1 }}>
        Pinned items ({draft.length})
      </Typography>

      <List
        disablePadding
        sx={{
          border: 1,
          borderColor: "divider",
          borderRadius: 1.5,
          mb: 3,
          maxHeight: 360,
          overflowY: "auto",
        }}
      >
        {draft.length === 0 ? (
          <ListItem>
            <ListItemText
              primary="No items pinned"
              secondary="Add an item below or click 'Reset to defaults'."
            />
          </ListItem>
        ) : (
          draft.map((item, index) => {
            const info = getPinnedItemInfo(item, { sessions, boards });
            const isEditing = editingIndex === index;
            const iconStyle = item.color ? { color: item.color } : undefined;

            return (
              <React.Fragment
                key={`${item.kind}:${item.id}:${item.tab || item.section || ""}`}
              >
                <ListItem
                  divider={index < draft.length - 1 || isEditing}
                  secondaryAction={
                    <Stack direction="row" spacing={0.5} alignItems="center">
                      <IconButton
                        size="small"
                        aria-label={`Customize ${info.label}`}
                        title="Customize icon & color"
                        color={isEditing ? "primary" : "default"}
                        onClick={() =>
                          setEditingIndex(isEditing ? null : index)
                        }
                      >
                        <PaletteOutlinedIcon fontSize="small" />
                      </IconButton>
                      <IconButton
                        ref={(el) => {
                          upRefs.current[index] = el;
                        }}
                        size="small"
                        aria-label="Move up"
                        title="Move up"
                        disabled={index === 0}
                        onClick={() => moveItem(index, -1)}
                      >
                        <ArrowUpwardIcon fontSize="small" />
                      </IconButton>
                      <IconButton
                        ref={(el) => {
                          downRefs.current[index] = el;
                        }}
                        size="small"
                        aria-label="Move down"
                        title="Move down"
                        disabled={index === draft.length - 1}
                        onClick={() => moveItem(index, 1)}
                      >
                        <ArrowDownwardIcon fontSize="small" />
                      </IconButton>
                      <IconButton
                        ref={(el) => {
                          removeRefs.current[index] = el;
                        }}
                        size="small"
                        aria-label={`Unpin ${info.label}`}
                        title="Unpin"
                        color="error"
                        onClick={() => removeItem(index)}
                      >
                        <DeleteOutlineIcon fontSize="small" />
                      </IconButton>
                    </Stack>
                  }
                >
                  <Box
                    sx={{
                      display: "flex",
                      alignItems: "center",
                      gap: 1.2,
                      minWidth: 0,
                    }}
                  >
                    <span className="nav-icon" style={iconStyle}>
                      {info.glyph ? (
                        <ItemGlyph kind={info.glyph} />
                      ) : (
                        <span
                          className="dot dot-all"
                          style={
                            item.color
                              ? {
                                  backgroundColor: item.color,
                                  borderColor: item.color,
                                }
                              : undefined
                          }
                        />
                      )}
                    </span>
                    <ListItemText
                      primary={info.label}
                      primaryTypographyProps={{
                        noWrap: true,
                        fontSize: "0.9rem",
                        fontWeight: 500,
                      }}
                    />
                    <Chip
                      label={info.typeLabel}
                      size="small"
                      variant="outlined"
                      sx={{ height: 20, fontSize: "0.72rem" }}
                    />
                    {item.color && (
                      <Box
                        sx={{
                          width: 12,
                          height: 12,
                          borderRadius: "50%",
                          bgcolor: item.color,
                          border: 1,
                          borderColor: "divider",
                        }}
                        title={`Color: ${item.color}`}
                      />
                    )}
                  </Box>
                </ListItem>

                <Collapse in={isEditing} timeout="auto" unmountOnExit>
                  <Box
                    sx={{
                      p: 2,
                      bgcolor: "action.hover",
                      borderBottom: 1,
                      borderColor: "divider",
                      display: "flex",
                      flexDirection: "column",
                      gap: 1.5,
                    }}
                  >
                    <Box
                      sx={{
                        display: "flex",
                        alignItems: "center",
                        gap: 2,
                        flexWrap: "wrap",
                      }}
                    >
                      <FormControl size="small" sx={{ minWidth: 220, flex: 1 }}>
                        <InputLabel id={`item-icon-label-${index}`}>
                          Icon
                        </InputLabel>
                        <Select
                          labelId={`item-icon-label-${index}`}
                          id={`item-icon-select-${index}`}
                          value={pickerIconId(item.icon || item.glyph || "")}
                          label="Icon"
                          onChange={(e) =>
                            updateItem(index, { icon: e.target.value })
                          }
                        >
                          <MenuItem value="">
                            <Typography variant="body2" color="text.secondary">
                              Default icon
                            </Typography>
                          </MenuItem>
                          {CHLADNI_ICONS.map((chladni) => (
                            <MenuItem key={chladni.id} value={chladni.id}>
                              <Box
                                sx={{
                                  display: "flex",
                                  alignItems: "center",
                                  gap: 1,
                                }}
                              >
                                <ItemGlyph kind={chladni.id} size={16} />
                                <Typography variant="body2">
                                  {chladni.label}
                                </Typography>
                              </Box>
                            </MenuItem>
                          ))}
                        </Select>
                      </FormControl>
                    </Box>

                    <Box>
                      <Typography
                        variant="caption"
                        color="text.secondary"
                        sx={{ display: "block", mb: 0.8 }}
                      >
                        Color
                      </Typography>
                      <Box
                        sx={{
                          display: "flex",
                          flexWrap: "wrap",
                          alignItems: "center",
                          gap: 0.8,
                        }}
                      >
                        {PINNED_COLOR_OPTIONS.map((c) => {
                          const isSelected = (item.color || "") === c.value;
                          return (
                            <Tooltip
                              key={c.label}
                              title={c.label}
                              arrow
                              placement="top"
                            >
                              <ButtonBase
                                aria-label={`Color ${c.label}`}
                                onClick={() =>
                                  updateItem(index, { color: c.value })
                                }
                                sx={{
                                  width: 24,
                                  height: 24,
                                  borderRadius: "50%",
                                  bgcolor: c.value || "background.paper",
                                  border: isSelected ? 2 : 1,
                                  borderColor: isSelected
                                    ? "primary.main"
                                    : "divider",
                                  display: "flex",
                                  alignItems: "center",
                                  justifyContent: "center",
                                  boxShadow: isSelected ? 1 : 0,
                                  "&:hover": { opacity: 0.85 },
                                }}
                              >
                                {isSelected && (
                                  <CheckIcon
                                    sx={{
                                      fontSize: 14,
                                      color: c.value ? "#fff" : "text.primary",
                                      filter:
                                        c.value === "#d9a441" ||
                                        c.value === "#f59e0b" ||
                                        c.value === "#84cc16"
                                          ? "drop-shadow(0 0 1px #000)"
                                          : undefined,
                                    }}
                                  />
                                )}
                                {!c.value && !isSelected && (
                                  <FormatColorResetIcon
                                    sx={{
                                      fontSize: 12,
                                      color: "text.secondary",
                                    }}
                                  />
                                )}
                              </ButtonBase>
                            </Tooltip>
                          );
                        })}

                        <Divider
                          orientation="vertical"
                          flexItem
                          sx={{ mx: 0.5, height: 20 }}
                        />

                        <input
                          type="color"
                          value={
                            item.color?.startsWith("#") &&
                            item.color.length === 7
                              ? item.color
                              : "#d9a441"
                          }
                          onChange={(e) =>
                            updateItem(index, { color: e.target.value })
                          }
                          title="Pick custom color"
                          style={{
                            width: 26,
                            height: 26,
                            padding: 0,
                            border: "1px solid rgba(128,128,128,0.3)",
                            borderRadius: "50%",
                            cursor: "pointer",
                          }}
                        />

                        <TextField
                          size="small"
                          placeholder="#d9a441"
                          label="Custom Hex"
                          value={item.color || ""}
                          onChange={(e) =>
                            updateItem(index, { color: e.target.value })
                          }
                          sx={{
                            width: 130,
                            "& .MuiInputBase-input": {
                              py: 0.5,
                              px: 1,
                              fontSize: "0.8rem",
                            },
                          }}
                        />
                      </Box>
                    </Box>
                  </Box>
                </Collapse>
              </React.Fragment>
            );
          })
        )}
      </List>

      <Typography variant="subtitle2" sx={{ mb: 1 }}>
        Add item to pinned
      </Typography>

      <Box
        sx={{
          display: "flex",
          gap: 1.5,
          alignItems: "center",
          mb: 3,
          flexWrap: "wrap",
        }}
      >
        <FormControl size="small" sx={{ minWidth: 110 }}>
          <InputLabel id="add-kind-label">Type</InputLabel>
          <Select
            labelId="add-kind-label"
            id="add-kind-select"
            value={addKind}
            label="Type"
            onChange={(e) => {
              setAddKind(e.target.value);
              setAddId("");
            }}
          >
            <MenuItem value="view">View</MenuItem>
            <MenuItem value="session">Session</MenuItem>
            <MenuItem value="board">Board</MenuItem>
          </Select>
        </FormControl>

        <FormControl size="small" sx={{ minWidth: 160, flex: 1 }}>
          <InputLabel id="add-item-label">Item</InputLabel>
          <Select
            labelId="add-item-label"
            id="add-item-select"
            value={addId}
            label="Item"
            onChange={(e) => setAddId(e.target.value)}
          >
            {availableOptions.map((opt) => (
              <MenuItem key={opt.id} value={opt.id}>
                {opt.label}
              </MenuItem>
            ))}
          </Select>
        </FormControl>

        <FormControl size="small" sx={{ minWidth: 150 }}>
          <InputLabel id="add-icon-label">Icon (optional)</InputLabel>
          <Select
            labelId="add-icon-label"
            id="add-icon-select"
            value={addIcon}
            label="Icon (optional)"
            onChange={(e) => setAddIcon(e.target.value)}
          >
            <MenuItem value="">Default icon</MenuItem>
            {CHLADNI_ICONS.map((chladni) => (
              <MenuItem key={chladni.id} value={chladni.id}>
                <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
                  <ItemGlyph kind={chladni.id} size={16} />
                  <Typography variant="body2">{chladni.label}</Typography>
                </Box>
              </MenuItem>
            ))}
          </Select>
        </FormControl>

        <Button
          variant="outlined"
          startIcon={<AddIcon />}
          onClick={addItem}
          disabled={
            !candidatePin || draft.some((it) => samePin(it, candidatePin))
          }
        >
          Pin item
        </Button>
      </Box>

      <Divider sx={{ mb: 2 }} />

      <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
        <Button
          ref={resetBtnRef}
          variant="text"
          color="inherit"
          onClick={handleReset}
        >
          Reset to default
        </Button>
        <Box sx={{ flex: 1 }} />
        <Button variant="outlined" onClick={onClose}>
          Cancel
        </Button>
        <Button variant="contained" onClick={handleSave}>
          Save
        </Button>
      </Box>
    </Overlay>
  );
}
