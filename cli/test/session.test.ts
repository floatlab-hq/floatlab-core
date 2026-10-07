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
