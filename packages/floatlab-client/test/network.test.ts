import { expect, test } from "bun:test";
import { createApiClient } from "../src/api";

test("network apply sends revision and key; recovery requires no idempotency key", async () => {
  const requests: { url: string; init: RequestInit }[] = [];
  const api = createApiClient({ baseUrl: "http://host", token: "admin", fetch: (async (input, init) => {
    requests.push({ url: String(input), init: init ?? {} });
    return new Response(JSON.stringify({ id: "change", state: "applying" }), { status: 202 });
  }) as typeof fetch });
  const config = { version: 1 as const, excluded_macs: [], bonds: [], ipv4: { mode: "dhcp" as const }, dns_servers: [] };
  await api.applyHostNetwork("node/a", "revision", config, "retry-key");
  expect(requests[0].url).toBe("http://host/api/v1/nodes/node%2Fa/settings/network");
  expect(requests[0].init.method).toBe("PUT");
  expect(new Headers(requests[0].init.headers).get("idempotency-key")).toBe("retry-key");
  expect(JSON.parse(requests[0].init.body as string)).toEqual({ revision: "revision", config });
  await api.confirmHostNetworkChange("node/a", "change");
  expect(requests[1].init.method).toBe("POST");
  expect(new Headers(requests[1].init.headers).get("idempotency-key")).toBeNull();
  expect(new Headers(requests[1].init.headers).get("authorization")).toBe("Bearer admin");
});
