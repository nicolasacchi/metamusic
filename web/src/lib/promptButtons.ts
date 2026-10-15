// "Copy critique prompt" / "Copy variant prompt": get_prompt rendered to plain text for the clipboard.
import type { createApi } from "../api/client";
import type { components } from "../api/schema";

type Api = ReturnType<typeof createApi>;
export type PromptName = "critique_track" | "write_variant";
export type PromptResult = { ok: true; copied: boolean; chars: number } | { ok: false; message: string };

/** The text an agent would receive: message texts and embedded resources in order. */
export function promptText(v: components["schemas"]["PromptView"]): string {
  const parts: string[] = [];
  for (const m of v.messages) {
    const t = m.content.type === "resource" ? m.content.resource?.text : m.content.text;
    if (t) parts.push(t);
  }
  return parts.join("\n\n").trimEnd();
}

export async function copyPrompt(api: Api, name: PromptName, trackId: string, clipboard: { writeText(t: string): Promise<void> } | undefined): Promise<PromptResult> {
  const r = await api.GET("/api/prompts/{name}", { params: { path: { name }, query: { track_id: trackId } } });
  if (!r.data) return { ok: false, message: r.error?.message || "Could not render the prompt" };
  const text = promptText(r.data);
  let copied = false;
  try {
    if (clipboard) {
      await clipboard.writeText(text);
      copied = true;
    }
  } catch {
    copied = false;
  }
  return { ok: true, copied, chars: text.length };
}
