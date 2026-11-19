import "./styles/tokens.css";
import "./styles/base.css";
import { mount } from "svelte";
import { registerWorker } from "./api/swRegister";
import { isStepUpTab, runStepUpTab } from "./api/stepup";

if (isStepUpTab(location)) {
  // Proxy-mode second tab (D1): it got past the edge, so say so and close. No app, no player, no storage writes here.
  runStepUpTab(document);
} else {
  void boot();
}

async function boot(): Promise<void> {
  // Who is signed in comes first (C313.1, C313.7): a different user than the one this browser last saw has its stored state
  // (queue, device id, inbox markers, media cache, offline database) cleared BEFORE any module reads it, so the app is
  // imported only now. With no answer and no stored one, the shell shows a retry instead of guessing a role.
  const target = document.getElementById("app")!;
  const { bootMe } = await import("./lib/me.svelte");
  if (await bootMe()) {
    const { default: App } = await import("./App.svelte");
    mount(App, { target });

    // Service worker (C115.1): only in a secure context; a refused registration is logged and ignored.
    void registerWorker().then((reg) => {
      if (reg) window.dispatchEvent(new CustomEvent("mm:sw-registered", { detail: reg }));
    });
  } else {
    target.innerHTML = "";
    const p = document.createElement("p");
    p.setAttribute("role", "alert");
    p.dataset.testid = "boot-failed";
    p.textContent = "Could not load your session. ";
    const b = document.createElement("button");
    b.type = "button";
    b.textContent = "Try again";
    b.onclick = () => location.reload();
    p.append(b);
    target.append(p);
  }
}
