// U1-W2: the devices pages by role. The Owner's heading and Tokens link are unchanged; others get "My devices".
import { afterEach, describe, expect, test } from "bun:test";
import { render } from "svelte/server";
import HumanDevices from "./HumanDevices.svelte";
import HumanSubsonic from "./HumanSubsonic.svelte";
import { me } from "../lib/me.svelte";

const as = (allowed: string[]) => {
  me.allowed = allowed;
  me.loaded = true;
};
afterEach(() => {
  me.allowed = [];
  me.loaded = false;
});

describe("devices page shells", () => {
  test("Owner: Devices heading and the Tokens link", () => {
    as(["list_devices", "list_tokens", "list_connector_grants"]);
    const h = render(HumanDevices, { props: {} } as never).body;
    expect(h).toMatch(/<h1>Devices<\/h1>/);
    expect(h).toContain('href="/human/tokens"');
    expect(h).toContain('data-testid="pairing-form"');
    const s = render(HumanSubsonic, { props: {} } as never).body;
    expect(s).toContain('href="/human/connectors"');
  });
  test("Listener: My devices, pairing form, no Tokens or Connectors link", () => {
    as(["list_devices", "create_pairing_code", "list_subsonic_clients"]);
    const h = render(HumanDevices, { props: {} } as never).body;
    expect(h).toMatch(/<h1>My devices<\/h1>/);
    expect(h).not.toContain("/human/tokens");
    expect(h).toContain('data-testid="pairing-form"');
    const s = render(HumanSubsonic, { props: {} } as never).body;
    expect(s).not.toContain("/human/connectors");
    expect(s).toContain('data-testid="subsonic-form"');
  });
});
