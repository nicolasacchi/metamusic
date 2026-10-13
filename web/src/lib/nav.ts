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
