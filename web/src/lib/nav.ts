// Secure (/human/*) pages are ordinary SPA routes (D1): an in-app click renders them without a document load, so the
// music keeps playing. Nothing here gates anything: their data calls hit /human/api/*, which the server gates on its own,
// and a DOCUMENT request to /human/* (typed URL, notification link) still goes through the edge and the step-up check.

import { APP_ROUTES } from "./router";

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
  { id: "new", label: "New", path: "/" },
  { id: "library", label: "Library", path: "/library" },
  { id: "queue", label: "Play queue", path: "/queue" },
  { id: "judge", label: "Judge", path: "/judge" },
  { id: "more", label: "More", path: "/more" },
];

const TAB_OF: Record<string, Tab> = {
  new: "new",
  home: "library",
  track: "library",
  lineage: "library",
  patch: "library",
  song: "library",
  sounds: "library",
  atlas: "library",
  records: "library",
  record: "library",
  crates: "library",
  crate: "library",
  playlists: "library",
  playlist: "library",
  queue: "queue",
  judge: "judge",
  callbacks: "judge",
  callback: "judge",
  duels: "judge",
  duel: "judge",
  ladder: "judge",
  compare: "judge",
  critics: "judge",
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
  transforms: "more",
  "transform-score": "more",
  desk: "more",
  plan: "more",
  engines: "more",
  "engine-trial": "more",
  suite: "library",
  batch: "more",
  "human-generate": "more",
  "human-confirm": "more",
  mandates: "more",
  mandate: "more",
  "night-shift": "more",
  "human-mandate-new": "more",
  "human-night-shift": "more",
  // Library tools, Settings
  export: "more",
  import: "more",
  offline: "more",
  diction: "more",
  settings: "more",
  "settings-backups": "more",
  "settings-audit": "more",
  receipts: "more",
  "settings-curation": "more",
  // Secure
  human: "more",
  "human-tokens": "more",
  "human-devices": "more",
  "human-spend": "more",
  "human-connectors": "more",
  "human-subsonic": "more",
  "human-record-publish": "more",
  "human-curation": "more",
};

/** The tab a route belongs to; an unknown route (404) sits under More. */
export function tabFor(routeName: string | null | undefined): Tab {
  return (routeName && TAB_OF[routeName]) || "more";
}

/** Route names that have a tab (for the one-tab-per-route test). */
export function mappedRouteNames(): string[] {
  return Object.keys(TAB_OF);
}

/** The /human/* pages: SPA routes whose data is gated server-side (/human/api, edge two-factor or local step-up). */
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
  { label: "Takes", path: "/library", tab: "library" },
  { label: "Sounds", path: "/sounds", tab: "library" },
  { label: "Atlas", path: "/atlas", tab: "library" },
  { label: "Records", path: "/records", tab: "library" },
  { label: "Crates", path: "/crates", tab: "library" },
  { label: "Playlists", path: "/playlists", tab: "library" },
  { label: "One by one", path: "/judge", tab: "judge" },
  { label: "Callbacks", path: "/judge/callbacks", tab: "judge" },
  { label: "Duels", path: "/duels", tab: "judge" },
  { label: "Ladder", path: "/ladder", tab: "judge" },
  { label: "Critics", path: "/critics", tab: "judge" },
  { label: "Board", path: "/board", tab: "more", group: "Make" },
  { label: "Backlog", path: "/backlog", tab: "more", group: "Make" },
  { label: "Jobs", path: "/jobs", tab: "more", group: "Make" },
  { label: "Transforms", path: "/transforms", tab: "more", group: "Make" },
  { label: "Render score", path: "/transforms/score", tab: "more", group: "Make" },
  { label: "Desk", path: "/desk", tab: "more", group: "Make", testid: "nav-desk" },
  { label: "Engines", path: "/engines", tab: "more", group: "Make" },
  { label: "Mandates", path: "/mandates", tab: "more", group: "Make", testid: "nav-mandates" },
  { label: "Night shift", path: "/night-shift", tab: "more", group: "Make", testid: "nav-night-shift" },
  { label: "Generate", path: "/human/generate", tab: "more", group: "Make", secure: true },
  { label: "Import", path: "/import", tab: "more", group: "Library tools" },
  { label: "Export", path: "/export", tab: "more", group: "Library tools" },
  { label: "Diction Lab", path: "/diction", tab: "more", group: "Library tools", testid: "nav-diction" },
  { label: "Offline", path: "/offline", tab: "more", group: "Library tools", testid: "nav-offline" },
  { label: "Settings", path: "/settings", tab: "more", group: "Settings" },
  { label: "Backups", path: "/settings/backups", tab: "more", group: "Settings" },
  { label: "Audit", path: "/settings/audit", tab: "more", group: "Settings" },
  { label: "Receipts", path: "/receipts", tab: "more", group: "Settings" },
  { label: "Curation", path: "/settings/curation", tab: "more", group: "Settings" },
  { label: "Approvals", path: "/human/", tab: "more", group: "Secure", secure: true, testid: "approvals-nav" },
  { label: "Spend", path: "/human/spend", tab: "more", group: "Secure", secure: true },
  { label: "Tokens", path: "/human/tokens", tab: "more", group: "Secure", secure: true },
  { label: "Devices", path: "/human/devices", tab: "more", group: "Secure", secure: true },
  { label: "Connectors", path: "/human/connectors", tab: "more", group: "Secure", secure: true },
  { label: "Subsonic", path: "/human/subsonic", tab: "more", group: "Secure", secure: true },
  { label: "Opt-ins", path: "/human/curation", tab: "more", group: "Secure", secure: true },
];

/** The sub-views a tab shows as a segmented row (Library, Judge). */
export function subLinks(tab: Tab, can?: (op: string) => boolean): NavLink[] {
  const all = tab === "library" || tab === "judge" ? NAV_LINKS.filter((l) => l.tab === tab) : [];
  return can ? all.filter((l) => linkVisible(l, can)) : all;
}

/** Whether a sub-view is the current page. */
export function subActive(l: NavLink, routeName: string | null | undefined): boolean {
  switch (l.path) {
    case "/library":
      return routeName === "home" || routeName === "track" || routeName === "lineage" || routeName === "song";
    case "/sounds":
      return routeName === "sounds";
    case "/atlas":
      return routeName === "atlas";
    case "/records":
      return routeName === "records" || routeName === "record";
    case "/crates":
      return routeName === "crates" || routeName === "crate";
    case "/playlists":
      return routeName === "playlists" || routeName === "playlist";
    case "/judge":
      return routeName === "judge";
    case "/judge/callbacks":
      return routeName === "callbacks" || routeName === "callback";
    case "/duels":
      return routeName === "duels" || routeName === "duel";
    case "/ladder":
      return routeName === "ladder" || routeName === "compare";
    case "/critics":
      return routeName === "critics";
    default:
      return false;
  }
}

/** Routes that are only reached from a page (an id, a form, query arguments), not from a tab. */
export const CONTEXTUAL_ROUTES = new Set([
  "track", "muse", "song", "draft-new", "draft-upload", "draft", "playlist", "duel", "compare", "job", "batch", "lineage", "human-confirm",
  "record", "crate", "human-record-publish", "callback", "patch", "plan", "mandate", "human-mandate-new", "human-night-shift",
]);

const TITLES: Record<string, string> = {
  new: "New",
  home: "Library",
  track: "Take",
  song: "Song",
  sounds: "Sounds",
  atlas: "Atlas",
  records: "Records",
  record: "Record",
  crates: "Crates",
  crate: "Crate",
  playlists: "Playlists",
  playlist: "Playlist",
  lineage: "Lineage",
  queue: "Play queue",
  judge: "Judge",
  callbacks: "Callbacks",
  callback: "Callback",
  patch: "Song PR",
  receipts: "Receipts",
  duels: "Duels",
  duel: "Duel",
  ladder: "Ladder",
  compare: "Compare",
  critics: "Critics",
  mandates: "Mandates",
  mandate: "Mandate",
  "human-mandate-new": "New mandate",
  "night-shift": "Night shift",
  "human-night-shift": "Night shift settings",
  more: "More",
  board: "Board",
  muse: "Muse",
  backlog: "Backlog",
  "draft-new": "New draft",
  "draft-upload": "Upload draft",
  draft: "Draft",
  jobs: "Jobs",
  job: "Job",
  transforms: "Transforms",
  "transform-score": "Render score",
  desk: "Desk",
  plan: "Plan",
  engines: "Engines",
  "engine-trial": "Engine trial",
  suite: "Suite",
  batch: "Batch",
  export: "Export",
  import: "Import",
  offline: "Offline",
  diction: "Diction Lab",
  settings: "Settings",
  "settings-backups": "Backups",
  "settings-audit": "Audit",
  "settings-curation": "Curation",
  "human-curation": "Curation",
  "human-record-publish": "Publish",
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
  return !routeName || ["new", "home", "sounds", "atlas", "records", "crates", "playlists", "queue", "judge", "callbacks", "duels", "ladder", "critics", "more"].includes(routeName);
}

/**
 * The operation a route's page is built on (its x-mm.ui op, U1 C313.3); null: every signed-in caller may open it. A caller
 * for whom `can(op)` is false gets "no access" instead of the page, and no nav link or tab to it. Cosmetic: the server
 * refuses the page's calls on its own. Every route has an entry (nav.test.ts), and every op id exists in openapi.json.
 */
export const ROUTE_OPS: Record<string, string | null> = {
  new: "search_tracks",
  home: "search_tracks",
  track: "get_track",
  lineage: "get_lineage",
  patch: "get_patch",
  song: "get_song",
  sounds: "search_sounds",
  atlas: "get_song_atlas",
  records: "list_records",
  record: "get_record",
  crates: "list_crates",
  crate: "get_crate",
  playlists: "list_playlists",
  playlist: "get_playlist",
  queue: null,
  judge: "next_duel",
  callbacks: "list_callbacks",
  callback: "get_callback",
  duels: "list_duels",
  duel: "get_duel",
  ladder: "get_ladder",
  compare: "compare_tracks",
  critics: "get_critic_bench",
  more: null,
  board: "get_muse_board",
  muse: "get_muse",
  backlog: "list_drafts",
  "draft-new": "create_draft",
  "draft-upload": "upload_batch_jsonl",
  draft: "get_draft",
  jobs: "list_jobs",
  job: "get_job",
  transforms: "list_transforms",
  "transform-score": "create_transform",
  desk: "list_plans",
  plan: "get_plan",
  engines: "list_engines",
  "engine-trial": "list_engines",
  suite: "get_lineage",
  batch: "get_batch",
  "human-generate": "estimate_generation",
  "human-confirm": "approve_batch",
  mandates: "list_mandates",
  mandate: "get_mandate",
  "human-mandate-new": "create_mandate",
  "night-shift": "get_night_shift",
  "human-night-shift": "update_night_shift",
  export: "list_export_sources",
  import: "list_import_runs",
  offline: "get_offline_set",
  diction: "get_diction_lexicon",
  settings: null, // every user picks this device's playback quality here; its Owner-only sections gate themselves (U1-INT-W)
  "settings-backups": "list_backups",
  "settings-audit": "get_audit",
  receipts: "list_ledger",
  "settings-curation": "get_curation_settings",
  human: "approve_batch",
  "human-tokens": "list_tokens",
  "human-devices": "list_devices",
  "human-spend": "get_spend",
  "human-connectors": "list_connector_grants",
  "human-subsonic": "list_subsonic_clients",
  "human-record-publish": "publish_record",
  "human-curation": "set_curation_settings",
};

/** The page's primary op; undefined for a route with no entry (NotFound). */
export function routeOp(routeName: string | null | undefined): string | null | undefined {
  return routeName ? ROUTE_OPS[routeName] : undefined;
}

/** May the caller open this route? Routes without an op (queue, More, 404) are open to everyone signed in. */
export function routeAllowed(routeName: string | null | undefined, can: (op: string) => boolean): boolean {
  const op = routeOp(routeName);
  return !op || can(op);
}

const NAME_OF_PATH = new Map(APP_ROUTES.map((r) => [r.pattern, r.name]));

/** The route a static nav link points at. */
export function linkRoute(l: NavLink): string | undefined {
  return NAME_OF_PATH.get(l.path.replace(/\/$/, "") || "/");
}

/** Whether a nav link is shown to this caller. */
export function linkVisible(l: NavLink, can: (op: string) => boolean): boolean {
  return routeAllowed(linkRoute(l), can);
}

/** A tab is shown when its landing page is open to the caller or one of its sub-views / rows is. More always shows (About). */
export function tabVisible(t: TabDef, can: (op: string) => boolean): boolean {
  if (t.id === "more" || t.id === "queue") return true;
  const landing = NAME_OF_PATH.get(t.path);
  if (routeAllowed(landing, can)) return true;
  return NAV_LINKS.some((l) => l.tab === t.id && linkVisible(l, can));
}

/** The tabs this caller sees. */
export function visibleTabs(can: (op: string) => boolean): TabDef[] {
  return TABS.filter((t) => tabVisible(t, can));
}
