// App-wide toast list. Kinds: "error" (default, 6 s, the judging failures), "info" (2 s, queue edits) and
// "success". A toast may carry one action ("Undo", "Back"); the region in App.svelte is a polite live region.
export type ToastKind = "info" | "success" | "error";

export interface ToastAction {
  label: string;
  run(): void;
}

export interface Toast {
  id: number;
  message: string;
  kind: ToastKind;
  action?: ToastAction;
}

export interface ToastOptions {
  kind?: ToastKind;
  /** Auto-dismiss delay in ms. */
  ms?: number;
  action?: ToastAction;
}

/** Info toasts disappear after 2 s (queue edits); everything else after 6 s. */
export const INFO_MS = 2000;
export const TOAST_MS = 6000;

/** App-wide toast list; mutated in place (never reassigned). */
export const toasts = $state<Toast[]>([]);
let toastSeq = 0;

export function dismissToast(id: number) {
  const i = toasts.findIndex((t) => t.id === id);
  if (i >= 0) toasts.splice(i, 1);
}

export function pushToast(message: string, o: ToastOptions = {}): number {
  const kind = o.kind ?? "error";
  // A burst of queue edits replaces the previous plain info toast instead of stacking a column of them.
  if (kind === "info" && !o.action) {
    for (let i = toasts.length - 1; i >= 0; i--) {
      const t = toasts[i]!;
      if (t.kind === "info" && !t.action) toasts.splice(i, 1);
    }
  }
  const id = ++toastSeq;
  toasts.push({ id, message, kind, ...(o.action ? { action: o.action } : {}) });
  setTimeout(() => dismissToast(id), o.ms ?? (kind === "info" ? INFO_MS : TOAST_MS));
  return id;
}
