// Reactive phone/desktop switch for the shell (<768 px is the phone layout; the same breakpoint as the CSS).
export const PHONE_QUERY = "(max-width: 767px)";
/** Comfortable density: a touch device or a narrow window (rows of 64 px, sheets instead of forms). */
export const COMPACT_QUERY = "(pointer: coarse), (max-width: 767px)";

class Viewport {
  phone = $state(typeof matchMedia === "function" ? matchMedia(PHONE_QUERY).matches : false);
  /** Touch or narrow: lists replace tables, filters collapse into a sheet. */
  touch = $state(typeof matchMedia === "function" ? matchMedia(COMPACT_QUERY).matches : false);

  constructor() {
    if (typeof matchMedia !== "function") return;
    const mq = matchMedia(PHONE_QUERY);
    mq.addEventListener("change", (e) => (this.phone = e.matches));
    const tq = matchMedia(COMPACT_QUERY);
    tq.addEventListener("change", (e) => (this.touch = e.matches));
  }
}

export const viewport = new Viewport();
