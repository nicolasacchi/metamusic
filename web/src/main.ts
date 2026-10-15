import { mount } from "svelte";
import App from "./App.svelte";
import { registerWorker } from "./api/swRegister";

mount(App, { target: document.getElementById("app")! });

// Service worker (C115.1): only in a secure context; a refused registration is logged and ignored.
void registerWorker().then((reg) => {
  if (reg) window.dispatchEvent(new CustomEvent("mm:sw-registered", { detail: reg }));
});
