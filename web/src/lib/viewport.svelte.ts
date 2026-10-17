// Reactive phone/desktop switch for the shell (<768 px is the phone layout; the same breakpoint as the CSS).
export const PHONE_QUERY = "(max-width: 767px)";

class Viewport {
  phone = $state(typeof matchMedia === "function" ? matchMedia(PHONE_QUERY).matches : false);

  constructor() {
    if (typeof matchMedia !== "function") return;
    const mq = matchMedia(PHONE_QUERY);
    mq.addEventListener("change", (e) => (this.phone = e.matches));
  }
}

export const viewport = new Viewport();
