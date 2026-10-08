import { expect, test } from "bun:test";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

test("CLI subprocesses create from a file and keep output pipeable", async () => {
  const directory = await mkdtemp(join(tmpdir(), "floatlab-cli-test-"));
  const compose = "name: demo\nservices: {}\n";
  let created = false;
  const server = Bun.serve({ port: 0, async fetch(request) {
    expect(request.headers.get("authorization")).toBe("Bearer test-token");
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/stacks" && request.method === "GET") return Response.json([{ id: "stack", name: "demo", state: "RunningPrimary", primary_node: "node1" }]);
    if (path === "/api/v1/stacks/stack/config") return new Response(compose);
    if (path === "/api/v1/stacks/stack/containers") return Response.json([{ id: "container", name: "demo-web-1", service: "web", status: "running" }]);
    if (path === "/api/v1/stacks/validate") { expect(await request.json()).toEqual({ name: "demo", compose_file: compose }); return new Response(null, { status: 204 }); }
    if (path === "/api/v1/stacks" && request.method === "POST") { created = true; expect(await request.json()).toEqual({ name: "demo", compose_file: compose }); expect(request.headers.get("idempotency-key")).toBeTruthy(); return Response.json({ id: "stack", name: "demo" }); }
    if (path === "/api/v1/stats/stacks/stack") return Response.json([{ label: "mem", unit: "bytes", points: [{ timestamp: 1, value: 123 }] }]);
    if (path === "/api/v1/stacks/stack/containers/container/exec") { expect(await request.json()).toEqual({ command: ["echo", "literal $HOME; a b", "--flag"] }); return Response.json({ stdout: "out", stderr: "err", exit_code: 7 }); }
    return new Response("unexpected request", { status: 404 });
  } });
  const run = async (args: string[]) => {
    const child = Bun.spawn([process.execPath, join(import.meta.dir, "../src/main.ts"), ...args], { env: { ...process.env, FLOATLAB_URL: server.url.origin, FLOATLAB_TOKEN: "test-token", XDG_CONFIG_HOME: directory, EDITOR: "must-not-run" }, stdin: "ignore", stdout: "pipe", stderr: "pipe" });
    const [stdout, stderr, code] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited]);
    return { stdout, stderr, code };
  };
  try {
    const file = join(directory, "compose=fixture.yaml"); await writeFile(file, compose);
    expect(await run(["create", "demo", `--file=${file}`])).toEqual({ stdout: "Created demo.\n", stderr: "", code: 0 });
    expect(created).toBe(true);
    expect((await run(["list"])).stdout).toContain("RunningPrimary");
    expect((await run(["config", "demo"])).stdout.trimEnd()).toBe(compose.trimEnd());
    expect((await run(["stats", "demo"])).stdout).toContain("123");
    expect(await run(["exec", "demo", "web", "--", "echo", "literal $HOME; a b", "--flag"])).toEqual({ stdout: "out", stderr: "err", code: 7 });
  } finally { server.stop(true); await rm(directory, { recursive: true, force: true }); }
});
