// The /human/subsonic create form: state <-> create_subsonic_client body.
import type { components } from "../api/schema";

type Body = components["schemas"]["CreateSubsonicClientInBody"];

export interface ClientForm {
  name: string;
  username: string;
  usernameEdited: boolean;
  apiKey: boolean;
  password: boolean;
  write: boolean;
  defaultFormat: "raw" | "mp3";
}

export function emptyClientForm(): ClientForm {
  return { name: "", username: "", usernameEdited: false, apiKey: true, password: true, write: true, defaultFormat: "raw" };
}

export function slugUsername(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40);
}

export function effectiveUsername(f: ClientForm): string {
  return f.usernameEdited ? f.username.trim() : slugUsername(f.name);
}

export function canSubmit(f: ClientForm): boolean {
  return f.name.trim() !== "" && (f.apiKey || f.password) && effectiveUsername(f) !== "";
}

export function buildClientBody(f: ClientForm): Body {
  return {
    name: f.name.trim(),
    username: effectiveUsername(f),
    api_key: f.apiKey,
    password: f.password,
    write: f.write,
    default_format: f.defaultFormat,
  };
}
