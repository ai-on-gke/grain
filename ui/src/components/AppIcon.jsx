// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import AddIcon from "@mui/icons-material/Add";
import ArrowBackIcon from "@mui/icons-material/ArrowBack";
import ArrowDropDownIcon from "@mui/icons-material/ArrowDropDown";
import BarChartOutlinedIcon from "@mui/icons-material/BarChartOutlined";
import ChevronLeftIcon from "@mui/icons-material/ChevronLeft";
import ChevronRightIcon from "@mui/icons-material/ChevronRight";
import CloseIcon from "@mui/icons-material/Close";
import ContentCopyIcon from "@mui/icons-material/ContentCopy";
import DeleteOutlineIcon from "@mui/icons-material/DeleteOutline";
import EditOutlinedIcon from "@mui/icons-material/EditOutlined";
import ExpandMoreIcon from "@mui/icons-material/ExpandMore";
import FactCheckOutlinedIcon from "@mui/icons-material/FactCheckOutlined";
import FilterListIcon from "@mui/icons-material/FilterList";
import HelpOutlineIcon from "@mui/icons-material/HelpOutline";
import InboxOutlinedIcon from "@mui/icons-material/InboxOutlined";
import LogoutIcon from "@mui/icons-material/Logout";
import MenuIcon from "@mui/icons-material/Menu";
import MonitorHeartOutlinedIcon from "@mui/icons-material/MonitorHeartOutlined";
import MoreHorizIcon from "@mui/icons-material/MoreHoriz";
import NotificationsOutlinedIcon from "@mui/icons-material/NotificationsOutlined";
import OpenInNewIcon from "@mui/icons-material/OpenInNew";
import PersonOutlineIcon from "@mui/icons-material/PersonOutline";
import PsychologyOutlinedIcon from "@mui/icons-material/PsychologyOutlined";
import RefreshIcon from "@mui/icons-material/Refresh";
import SearchIcon from "@mui/icons-material/Search";
import SettingsOutlinedIcon from "@mui/icons-material/SettingsOutlined";
import StorageOutlinedIcon from "@mui/icons-material/StorageOutlined";
import SwapVertIcon from "@mui/icons-material/SwapVert";
import TuneIcon from "@mui/icons-material/Tune";
import ViewSidebarOutlinedIcon from "@mui/icons-material/ViewSidebarOutlined";
import ItemGlyph from "./ItemGlyph.jsx";

// Material's ViewSidebarOutlined draws the narrow rail on the right; mirror
// it horizontally so the sidebar column sits on the left to match grain's
// left navigation rail.
function LeftSidebarIcon({ sx, ...rest }) {
  return (
    <ViewSidebarOutlinedIcon
      {...rest}
      sx={{ transform: "scaleX(-1)", ...sx }}
    />
  );
}

// STANDARD_ICONS maps each chrome destination and UI control meaning to a
// single Material icon (@mui/icons-material, MIT License) imported by
// subpath so Vite tree-shakes unused icons out of the bundle. Domain item
// kinds (tasks, repos, branches, boards, schedules, templates, suites,
// processes, sandboxes, sessions, director) keep their Chladni figures in
// ItemGlyph.jsx.
export const STANDARD_ICONS = {
  settings: SettingsOutlinedIcon,
  alerts: NotificationsOutlinedIcon,
  notifications: NotificationsOutlinedIcon,
  inbox: InboxOutlinedIcon,
  search: SearchIcon,
  system: MonitorHeartOutlinedIcon,
  admin: MonitorHeartOutlinedIcon,
  health: MonitorHeartOutlinedIcon,
  ci: FactCheckOutlinedIcon,
  user: PersonOutlineIcon,
  account: PersonOutlineIcon,
  help: HelpOutlineIcon,
  signOut: LogoutIcon,
  logout: LogoutIcon,
  add: AddIcon,
  edit: EditOutlinedIcon,
  delete: DeleteOutlineIcon,
  more: MoreHorizIcon,
  close: CloseIcon,
  externalLink: OpenInNewIcon,
  copy: ContentCopyIcon,
  refresh: RefreshIcon,
  filter: FilterListIcon,
  sort: SwapVertIcon,
  expand: ExpandMoreIcon,
  collapse: ChevronRightIcon,
  chevronLeft: ChevronLeftIcon,
  chevronRight: ChevronRightIcon,
  sidebar: LeftSidebarIcon,
  back: ArrowBackIcon,
  menu: MenuIcon,
  customize: TuneIcon,
  dropdown: ArrowDropDownIcon,
  cache: StorageOutlinedIcon,
  memory: PsychologyOutlinedIcon,
  metrics: BarChartOutlinedIcon,
};

// CHROME_GLYPH_MAP routes navigation/place identifiers that represent UI
// chrome rather than domain item kinds to their standard icon meaning.
export const CHROME_GLYPH_MAP = {
  settings: "settings",
  alerts: "alerts",
  notifications: "notifications",
  inbox: "inbox",
  system: "system",
  admin: "system",
  ci: "ci",
  user: "user",
  cache: "cache",
  memory: "memory",
  metrics: "metrics",
};

// AppIcon renders one standard UI icon. Like ItemGlyph, it is decorative
// (`aria-hidden="true"`) unless `title` is supplied for a standalone
// graphic, and inherits `currentColor` from its parent button, link or
// text in both light and dark themes.
export default function AppIcon({
  name,
  size = 18,
  title,
  className,
  sx,
  ...rest
}) {
  const Icon = STANDARD_ICONS[name];
  if (!Icon) return null;
  return (
    <Icon
      className={className}
      data-icon={name}
      width={size}
      height={size}
      role={title ? "img" : undefined}
      aria-hidden={title ? undefined : true}
      aria-label={title || undefined}
      titleAccess={title || undefined}
      focusable="false"
      sx={{
        fontSize: size,
        width: size,
        height: size,
        display: "block",
        flex: "none",
        ...sx,
      }}
      {...rest}
    />
  );
}

// NavGlyph renders a standard AppIcon for navigation/chrome places
// (Settings, Alerts, Inbox, System, CI, User, Caches, Memory, Metrics)
// and keeps the Chladni ItemGlyph unchanged for domain item kinds (tasks,
// repos, branches, boards, schedules, templates, suites, processes,
// sandboxes, sessions, director).
export function NavGlyph({ kind, size = 16, title, className }) {
  if (!kind) return null;
  const standardName = CHROME_GLYPH_MAP[kind];
  if (standardName) {
    return (
      <AppIcon
        name={standardName}
        size={size}
        title={title}
        className={className}
      />
    );
  }
  return (
    <ItemGlyph kind={kind} size={size} title={title} className={className} />
  );
}

// ExternalLinkIcon is the decorative arrow-out-of-a-box trailing external
// links (`target="_blank"`), hidden from assistive technology so the link's
// accessible name stays its visible text.
export function ExternalLinkIcon({ size = 13, sx }) {
  return (
    <AppIcon
      name="externalLink"
      size={size}
      className="external-link-icon"
      sx={{
        display: "inline-block",
        verticalAlign: "-0.12em",
        ml: "0.25em",
        opacity: 0.75,
        ...sx,
      }}
    />
  );
}
