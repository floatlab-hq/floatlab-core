import { expect, test } from "bun:test";
import { PassThrough } from "node:stream";
import { configPath, valid } from "../src/session";
import { commandInput, format, replaceLastToken } from "../src/terminal";

test("session paths use XDG config and expiry is enforced", () => {
  expect(configPath({ XDG_CONFIG_HOME: "/tmp/config" })).toBe("/tmp/config/floatlab/config.json");
  expect(valid({ url: "https://example.test", token: "token", expires_at: "2000-01-01T00:00:00Z" })).toBe(false);
});

test("tables stay pipeable outside the pager", () => {
  expect(format({ kind: "table", columns: ["name"], rows: [["demo"]] })).toContain("demo");
});

test("completion replaces only the active token", () => {
  expect(replaceLastToken("start pl", "plex")).toBe("start plex");
});

test("interactive input submits the typed command", async () => {
  const input = new PassThrough() as PassThrough & { isTTY: boolean; setRawMode(value: boolean): void };
  const output = new PassThrough() as PassThrough & { columns: number };
  input.isTTY = true; input.setRawMode = () => {}; output.columns = 80;
  const answer = commandInput(async () => [], [], { input, output });
  await Bun.sleep(0); input.write("exit\r");
  expect(await answer).toBe("exit");
});

test("login asks for a second factor and sends the supplied factor", async () => {
  const server = Bun.serve({ port: 0, async fetch(request) {
    const body = await request.json() as { username: string; password: string; totp_code?: string; recovery_code?: string };
    if (!body.totp_code && !body.recovery_code) return Response.json({ error: "second factor required", second_factor_required: true }, { status: 401 });
    expect(body.username).toBe("admin");
    expect(body.totp_code ?? body.recovery_code).toBe("123456");
    return Response.json({ access_token: "test-token", expires_in: 60 });
  } });
  const { login, SecondFactorRequired } = await import("../src/session");
  try {
    await expect(login(server.url.href, "admin", "password")).rejects.toBeInstanceOf(SecondFactorRequired);
    expect((await login(server.url.href, "admin", "password", { totp_code: "123456" })).token).toBe("test-token");
    expect((await login(server.url.href, "admin", "password", { recovery_code: "123456" })).token).toBe("test-token");
  } finally { server.stop(true); }
});
