import { expect, test } from "bun:test";
import { createApiClient } from "../src/api";

test("dashboard requests use the contract routes and bearer authentication", async () => {
  const requests: { url: string; init: RequestInit }[] = [];
  const api = createApiClient({ baseUrl: "https://host", token: "admin", fetch: async (input, init) => {
    requests.push({ url: String(input), init: init ?? {} });
    return new Response("{}");
  } });
  await api.createAuthToken({ username: "admin", password: "secret" });
  await api.getRaftHealth();
  await api.listNotifications();
  await api.listAuditLog();
  await api.getNodeStats("node/a");
  await api.getStorageStats("node/a");
  await api.getNodeLogs("node/a");
  expect(requests.map(request => request.url)).toEqual([
    "https://host/api/v1/auth/token", "https://host/api/v1/health/raft", "https://host/api/v1/notifications?limit=50",
    "https://host/api/v1/logs/audit?limit=50",
    "https://host/api/v1/stats/nodes/node%2Fa?range=1h", "https://host/api/v1/stats/storage/node%2Fa?range=1h", "https://host/api/v1/logs/nodes/node%2Fa?tail=100",
  ]);
  expect(requests[0].init.method).toBe("POST");
  expect(JSON.parse(requests[0].init.body as string)).toEqual({ username: "admin", password: "secret" });
  for (const request of requests) expect(new Headers(request.init.headers).get("authorization")).toBe("Bearer admin");
});

test("stack moves use a keyed request and poll the accepted operation", async () => {
  const requests: { url: string; init: RequestInit }[] = [];
  const api = createApiClient({ baseUrl: "https://host", token: "admin", fetch: async (input, init) => {
    requests.push({ url: String(input), init: init ?? {} });
    return Response.json(init?.method === "POST" ? { operation_id: "move/one" } : { state: "running", checkpoint: "final-sync" });
  } });
  const request = { destination_node_id: "node-b", network_pool: "node-b-pool" };
  const accepted = await api.moveStack("stack/a", request, "move-key");
  const status = await api.getOperation(accepted.operation_id);
  expect(status.checkpoint).toBe("final-sync");
  expect(requests.map(({url}) => url)).toEqual([
    "https://host/api/v1/stacks/stack%2Fa/move", "https://host/api/v1/operations/move%2Fone",
  ]);
  expect(requests[0]!.init.method).toBe("POST");
  expect(new Headers(requests[0]!.init.headers).get("idempotency-key")).toBe("move-key");
  expect(requests[0]!.init.body).toBe(JSON.stringify(request));
  expect(new Headers(requests[1]!.init.headers).get("authorization")).toBe("Bearer admin");
});
