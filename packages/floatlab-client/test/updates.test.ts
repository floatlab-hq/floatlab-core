import { expect, test } from "bun:test";
import { createApiClient } from "../src/api";
import { CommandEngine } from "../src/engine";
import type { CommandRuntime, FloatLabApi, UpdateApi } from "../src/types";

test("update requests preserve node escaping, identity and recovery operation", async () => {
  const requests: { url: string; init: RequestInit }[] = [];
  const api = createApiClient({ baseUrl: "http://host", token: "admin", fetch: (async (input, init) => {
    requests.push({ url: String(input), init: init ?? {} });
    return new Response(JSON.stringify({ operation_id: "trial", state: "running", phase: "prepare" }), { status: 202 });
  }) as typeof fetch });
  await api.runUpdateAction("node/a", "host", "prepare", { release: "host-v1.2.3" }, "prepare-key");
  expect(requests[0]!.url).toBe("http://host/api/v1/nodes/node%2Fa/updates/host/prepare");
  expect(JSON.parse(requests[0]!.init.body as string)).toEqual({ release: "host-v1.2.3" });
  expect(new Headers(requests[0]!.init.headers).get("authorization")).toBe("Bearer admin");
  expect(new Headers(requests[0]!.init.headers).get("idempotency-key")).toBe("prepare-key");
  await api.runUpdateAction("node", "host", "rollback", { operation_id: "trial" }, "rollback-key");
  expect(JSON.parse(requests[1]!.init.body as string)).toEqual({ operation_id: "trial" });
  await api.getUpdateStatus("node", "host", "trial/a");
  expect(requests[2]!.url).toBe("http://host/api/v1/nodes/node/updates/host/status?operation_id=trial%2Fa");
  await api.listUpdateReleases("node", "management");
  expect(requests[3]!.url).toBe("http://host/api/v1/nodes/node/updates/management/releases");
});

test("appliance command resolves nodes, requires targets and keeps workload upgrade separate", async () => {
  let calls = 0;
  const api = {
    listNodes: async () => [{ id: "local", name: "lab" }],
    listUpdateReleases: async () => [{ tag_name: "manage-v1.2.3" }],
    getUpdateStatus: async () => null,
    runUpdateAction: async (node: string, component: string, action: string, request: unknown) => {
      calls++;
      expect([node, component, action, request]).toEqual(["local", "management", "start", { release: "manage-v1.2.3" }]);
      return { operation_id: "update", state: "running", phase: "preflight" };
    },
  } as unknown as FloatLabApi & UpdateApi;
  const runtime: CommandRuntime = { interactive: false, confirm: async () => true, editCompose: async () => undefined, present: async () => undefined };
  const engine = new CommandEngine(api, runtime);
  expect((await engine.dispatch("update management releases lab")).result.kind).toBe("table");
  expect((await engine.dispatch("update management status lab")).code).toBe(0);
  expect((await engine.dispatch("update management start lab")).code).toBe(2);
  expect((await engine.dispatch("update management commit lab update")).code).toBe(2);
  expect((await engine.dispatch("update management start lab manage-v1.2.3")).code).toBe(0);
  expect(calls).toBe(1);
  expect(await engine.complete("update host ac")).toEqual([{ value: "activate" }]);
  expect(await engine.complete("update management start l")).toEqual([{ value: "lab", description: undefined }]);
});
