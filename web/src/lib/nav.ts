// Navigation to /human/* is always a full-page load (C40): the request must reach the server so the
// edge two-factor rule or the local step-up check runs before the page shell is served.
export function isHumanPath(path: string): boolean {
  const p = path.split(/[?#]/, 1)[0] ?? "";
  return p === "/human" || p.startsWith("/human/");
}

export interface NavDeps {
  assign(url: string): void;
  /** The client router's navigate. */
  route(to: string): void;
}

export function createNav(deps: NavDeps) {
  return {
    navigate(to: string) {
      if (isHumanPath(to)) deps.assign(to);
      else deps.route(to);
    },
    /** Click handler for an <a href>; returns true when it took over. */
    onLinkClick(e: MouseEvent, href: string): boolean {
      if (!isHumanPath(href)) return false;
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return false;
      e.preventDefault();
      deps.assign(href);
      return true;
    },
  };
}

// App shell (wave 2): five tabs, listen-first. Every route belongs to exactly one tab (tabFor); the phone tab bar and the
// desktop top nav render the same groups. Make (Board, Backlog, Jobs, Generate) lives under More.
export type Tab = "new" | "library" | "queue" | "judge" | "more";

export interface TabDef {
  id: Tab;
  label: string;
  /** Where the tab lands. */
  path: string;
}

export const TABS: TabDef[] = [
  { id: "new", label: "New", path: "/new" },
  { id: "library", label: "Library", path: "/" },
  { id: "queue", label: "Play queue", path: "/queue" },
  { id: "judge", label: "Judge", path: "/duels" },
  { id: "more", label: "More", path: "/more" },
];

const TAB_OF: Record<string, Tab> = {
  new: "new",
  home: "library",
  track: "library",
  lineage: "library",
  song: "library",
  sounds: "library",
  playlists: "library",
  playlist: "library",
  queue: "queue",
  duels: "judge",
  duel: "judge",
  ladder: "judge",
  compare: "judge",
  more: "more",
  // Make
  board: "more",
  muse: "more",
  backlog: "more",
  "draft-new": "more",
  "draft-upload": "more",
  draft: "more",
  jobs: "more",
  job: "more",
  batch: "more",
  "human-generate": "more",
  "human-confirm": "more",
  // Library tools, Settings
  export: "more",
  import: "more",
  offline: "more",
  settings: "more",
  "settings-backups": "more",
  "settings-audit": "more",
  // Secure
  human: "more",
  "human-tokens": "more",
  "human-devices": "more",
  "human-spend": "more",
  "human-connectors": "more",
  "human-subsonic": "more",
};

/** The tab a route belongs to; an unknown route (404) sits under More. */
export function tabFor(routeName: string | null | undefined): Tab {
  return (routeName && TAB_OF[routeName]) || "more";
}

/** Route names that have a tab (for the one-tab-per-route test). */
export function mappedRouteNames(): string[] {
  return Object.keys(TAB_OF);
}

/** The /human/* pages: a full-page load through the server's second-factor / step-up check (secure). */
export function isSecureRoute(routeName: string | null | undefined): boolean {
  return !!routeName && (routeName === "human" || routeName.startsWith("human-"));
}

export interface NavLink {
  label: string;
  path: string;
  tab: Tab;
  /** More-page group. */
  group?: "Make" | "Library tools" | "Settings" | "Secure";
  secure?: boolean;
  testid?: string;
}

/** Every static destination reachable from its tab: sub-views (Library, Judge) and the More page rows. */
export const NAV_LINKS: NavLink[] = [
  { label: "Takes", path: "/", tab: "library" },
  { label: "Sounds", path: "/sounds", tab: "library" },
  { label: "Playlists", path: "/playlists", tab: "library" },
  { label: "Duels", path: "/duels", tab: "judge" },
  { label: "Ladder", path: "/ladder", tab: "judge" },
  { label: "Board", path: "/board", tab: "more", group: "Make" },
  { label: "Backlog", path: "/backlog", tab: "more", group: "Make" },
  { label: "Jobs", path: "/jobs", tab: "more", group: "Make" },
  { label: "Generate", path: "/human/generate", tab: "more", group: "Make", secure: true },
  { label: "Import", path: "/import", tab: "more", group: "Library tools" },
  { label: "Export", path: "/export", tab: "more", group: "Library tools" },
  { label: "Offline", path: "/offline", tab: "more", group: "Library tools", testid: "nav-offline" },
  { label: "Settings", path: "/settings", tab: "more", group: "Settings" },
  { label: "Backups", path: "/settings/backups", tab: "more", group: "Settings" },
  { label: "Audit", path: "/settings/audit", tab: "more", group: "Settings" },
  { label: "Approvals", path: "/human/", tab: "more", group: "Secure", secure: true, testid: "approvals-nav" },
  { label: "Spend", path: "/human/spend", tab: "more", group: "Secure", secure: true },
  { label: "Tokens", path: "/human/tokens", tab: "more", group: "Secure", secure: true },
  { label: "Devices", path: "/human/devices", tab: "more", group: "Secure", secure: true },
  { label: "Connectors", path: "/human/connectors", tab: "more", group: "Secure", secure: true },
  { label: "Subsonic", path: "/human/subsonic", tab: "more", group: "Secure", secure: true },
];

/** The sub-views a tab shows as a segmented row (Library, Judge). */
export function subLinks(tab: Tab): NavLink[] {
  return tab === "library" || tab === "judge" ? NAV_LINKS.filter((l) => l.tab === tab) : [];
}

/** Whether a sub-view is the current page. */
export function subActive(l: NavLink, routeName: string | null | undefined): boolean {
  switch (l.path) {
    case "/":
      return routeName === "home" || routeName === "track" || routeName === "lineage" || routeName === "song";
    case "/sounds":
      return routeName === "sounds";
    case "/playlists":
      return routeName === "playlists" || routeName === "playlist";
    case "/duels":
      return routeName === "duels" || routeName === "duel";
    case "/ladder":
      return routeName === "ladder" || routeName === "compare";
    default:
      return false;
  }
}

/** Routes that are only reached from a page (an id, a form, query arguments), not from a tab. */
export const CONTEXTUAL_ROUTES = new Set([
  "track", "muse", "song", "draft-new", "draft-upload", "draft", "playlist", "duel", "compare", "job", "batch", "lineage", "human-confirm",
]);

const TITLES: Record<string, string> = {
  new: "New",
  home: "Library",
  track: "Take",
  song: "Song",
  sounds: "Sounds",
  playlists: "Playlists",
  playlist: "Playlist",
  lineage: "Lineage",
  queue: "Play queue",
  duels: "Duels",
  duel: "Duel",
  ladder: "Ladder",
  compare: "Compare",
  more: "More",
  board: "Board",
  muse: "Muse",
  backlog: "Backlog",
  "draft-new": "New draft",
  "draft-upload": "Upload draft",
  draft: "Draft",
  jobs: "Jobs",
  job: "Job",
  batch: "Batch",
  export: "Export",
  import: "Import",
  offline: "Offline",
  settings: "Settings",
  "settings-backups": "Backups",
  "settings-audit": "Audit",
  human: "Approvals",
  "human-tokens": "Tokens",
  "human-devices": "Devices",
  "human-spend": "Spend",
  "human-connectors": "Connectors",
  "human-subsonic": "Subsonic",
  "human-generate": "Generate",
  "human-confirm": "Confirm",
};

/** The phone top bar title of a route. */
export function titleFor(routeName: string | null | undefined): string {
  return (routeName && TITLES[routeName]) || "Not found";
}

/** A route that is the root of its tab (no Back button on the phone top bar). */
export function isTabRoot(routeName: string | null | undefined): boolean {
  return !routeName || ["new", "home", "sounds", "playlists", "queue", "duels", "ladder", "more"].includes(routeName);
}
