import { expect, test } from "bun:test";
import { createApiClient } from "../src/api";
import { CommandEngine } from "../src/engine";
import type { CommandRuntime, FloatLabApi, LabApi } from "../src/types";

const runtime: CommandRuntime = { interactive: false, confirm: async () => true, editCompose: async () => undefined, present: async () => undefined };

test("lab transport preserves explicit removal and rotation operation identities", async () => {
  const requests: { url: string; body: unknown }[] = [];
  const api = createApiClient({ baseUrl: "https://node", token: "admin", fetch: (async (url, init) => {
    expect(new Headers(init?.headers).get("authorization")).toBe("Bearer admin");
    requests.push({ url: String(url), body: init?.body ? JSON.parse(String(init.body)) : undefined });
    return Response.json({});
  }) as typeof fetch });
  await api.removeLabMember("node/a", "remove-1");
  await api.rotateLabIdentity("rotate-1");
  await api.releaseLabPeerAddress("node/a", "release-1");
  expect(requests).toEqual([
    { url: "https://node/api/v1/lab/members/node%2Fa/remove", body: { confirm_node_id: "node/a", operation_id: "remove-1" } },
    { url: "https://node/api/v1/lab/identity/rotate", body: { operation_id: "rotate-1" } },
    { url: "https://node/api/v1/lab/members/node%2Fa/release-peer-address", body: { confirm_node_id: "node/a", operation_id: "release-1", confirm_fenced: true, confirm_reset: true } },
  ]);
});

test("lab commands preview removal, preserve retry ID, and require reset confirmations", async () => {
  const calls: string[] = [];
  const api = {
    listNodes: async () => [{ id: "node-1", name: "host" }],
    getLabStatus: async () => ({ quorum: false }),
    previewLabRemoval: async (node: string) => { calls.push(`preview:${node}`); return { stale: true }; },
    removeLabMember: async (node: string, operation: string) => { calls.push(`remove:${node}:${operation}`); return {}; },
    releaseLabPeerAddress: async () => { calls.push("release"); return {}; },
  } as unknown as FloatLabApi & LabApi;
  const engine = new CommandEngine(api, runtime);
  expect((await engine.dispatch("lab status")).code).toBe(0);
  expect((await engine.dispatch("lab remove host remove-1")).code).toBe(0);
  expect(calls).toEqual(["preview:node-1", "remove:node-1:remove-1"]);
  expect((await engine.dispatch("lab release-peer-address host release-1")).code).toBe(2);
  expect((await engine.dispatch("lab release-peer-address host release-1 --confirm-fenced --confirm-reset")).code).toBe(0);
  expect(calls.at(-1)).toBe("release");
  expect(await engine.complete("lab sta")).toEqual([{ value: "status", description: "status" }]);
});

test("duplicate stack names need a node-qualified selection", async () => {
  const api = {
    listNodes: async () => [{ id: "a", name: "north" }, { id: "b", name: "south" }],
    listStacks: async () => [{ id: "stack-a", name: "web", primary_node: "a" }, { id: "stack-b", name: "web", primary_node: "b" }],
    getStackConfig: async (id: string) => id,
  } as unknown as FloatLabApi;
  const engine = new CommandEngine(api, runtime);
  expect((await engine.dispatch("config web")).code).toBe(2);
  expect((await engine.dispatch("config south/web")).result).toEqual({ kind: "text", text: "stack-b" });
});

test("stack lists disclose cached remote observations", async () => {
  const api = { listStacks: async () => [{ id: "a", name: "web", state: "Idle", primary_node: "a" }, { id: "b", name: "web", state: "RunningPrimary", primary_node: "b", replica: true, observed_revision: 42 }] } as unknown as FloatLabApi;
  const result = (await new CommandEngine(api, runtime).dispatch("list")).result;
  expect(result.kind).toBe("table");
  if (result.kind === "table") {
    expect(result.columns.at(-1)).toBe("view");
    expect(result.rows.map(row => row.at(-1))).toEqual(["local", "cached revision 42"]);
  }
});
