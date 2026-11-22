import { expect, test } from "bun:test";
import { CommandEngine, createApiClient, type CommandRuntime } from "../src";

test("failover shows the replica age and requires explicit confirmation of its recovery point", async () => {
  const requests: { url: string; init?: RequestInit }[] = [];
  const api = createApiClient({ baseUrl: "https://host", token: "admin", fetch: async (input, init) => {
    const url = String(input);
    requests.push({ url, init });
    if (url.endsWith("/stacks")) return Response.json([{ id: "stack/a", name: "db", secondary_node: "node/b" }]);
    if (init?.method === "POST") return Response.json({ operation_id: "recover", stack_id: "clone", status: "pending" }, { status: 202 });
    return Response.json({ recovery_point: "verified-point", potential_data_loss_seconds: 60, mode: "clone" });
  } });
  const runtime: CommandRuntime = { interactive: false, confirm: async () => true, editCompose: async () => undefined, present: async () => {} };
  const engine = new CommandEngine(api, runtime);
  const preview = await engine.dispatch(["failover", "preview", "db"]);
  expect(preview).toMatchObject({code: 0, result: {kind: "text", text: expect.stringContaining('"potential_data_loss_seconds": 60')}});
  expect(requests.at(-1)?.url).toBe("https://host/api/v1/failover/stack%2Fa/preview?destination_node_id=node%2Fb");
  expect((await engine.dispatch(["failover", "trigger", "db", "--recovery-point", "verified-point"])).code).toBe(2);
  expect((await engine.dispatch(["failover", "trigger", "db", "--primary-fenced"])).code).toBe(2);
  expect(requests.some(({init}) => init?.method === "POST")).toBe(false);
  const accepted = await engine.dispatch(["failover", "trigger", "db", "--primary-fenced", "--recovery-point", "verified-point", "--name", "recovery-db", "--network-pool", "recovery"]);
  expect(accepted).toMatchObject({code: 0, result: {kind: "operation", state: "pending", operationId: "recover"}});
  const mutation = requests.at(-1)!;
  expect(mutation.url).toBe("https://host/api/v1/failover/stack%2Fa/trigger");
  expect(new Headers(mutation.init?.headers).get("idempotency-key")).toBeTruthy();
  expect(JSON.parse(String(mutation.init?.body))).toEqual({destination_node_id: "node/b", primary_fenced: true, recovery_point: "verified-point", name: "recovery-db", network_pool: "recovery"});
});
