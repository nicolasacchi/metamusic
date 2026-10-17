import "./styles/tokens.css";
import "./styles/base.css";
import { mount } from "svelte";
import App from "./App.svelte";
import { registerWorker } from "./api/swRegister";
import { isStepUpTab, runStepUpTab } from "./api/stepup";

if (isStepUpTab(location)) {
  // Proxy-mode second tab (D1): it got past the edge, so say so and close. No app, no player, no storage writes here.
  runStepUpTab(document);
} else {
  mount(App, { target: document.getElementById("app")! });

  // Service worker (C115.1): only in a secure context; a refused registration is logged and ignored.
  void registerWorker().then((reg) => {
    if (reg) window.dispatchEvent(new CustomEvent("mm:sw-registered", { detail: reg }));
  });
}
