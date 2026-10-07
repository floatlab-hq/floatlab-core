import { expect, test } from "bun:test";
import { CommandEngine, normalizeApiUrl, tokenize, type CommandResult, type CommandRuntime, type FloatLabApi } from "../src";

const presented: CommandResult[] = [];
const runtime: CommandRuntime = { interactive: false, confirm: async () => true, editCompose: async (value) => value, present: async (result) => { presented.push(result); } };
const api = { listStacks: async () => [{ id: "one", name: "demo", state: "Idle", primary_node: "node" }], listNodes: async () => [], listContainers: async () => [] } as unknown as FloatLabApi;

test("tokenizer preserves quoted arguments and rejects shell syntax", () => {
  expect(tokenize('logs demo --search "out of memory"')).toEqual(["logs", "demo", "--search", "out of memory"]);
  expect(() => tokenize("list | less")).toThrow("Unsupported shell operator");
});

test("API URLs accept an origin or canonical API path only", () => {
  expect(normalizeApiUrl("https://floatlab.test")).toBe("https://floatlab.test/api/v1");
  expect(normalizeApiUrl("https://floatlab.test/api/v1/")).toBe("https://floatlab.test/api/v1");
  expect(() => normalizeApiUrl("https://floatlab.test/other")).toThrow("must be an origin");
});

test("aliases dispatch through the same registry", async () => {
  const result = await new CommandEngine(api, runtime).dispatch("ls");
  expect(result.code).toBe(0); expect(result.result.kind).toBe("table");
});

test("completion resolves containers beneath the selected stack", async () => {
  const scoped = { ...api, listContainers: async () => [{ id: "container", name: "web", image: "nginx", status: "running" }] } as FloatLabApi;
  await expect(new CommandEngine(scoped, runtime).complete("start demo ")).resolves.toEqual([{ value: "web", description: "running" }]);
});

test("nested and image-update completions use the shared resources", async () => {
  const scoped = { ...api, listContainers: async () => [{ id: "container", name: "web", image: "nginx", status: "running" }] } as FloatLabApi;
  const engine = new CommandEngine(scoped, runtime);
  await expect(engine.complete("snapshot ")).resolves.toEqual([{ value: "create", description: "snapshot" }, { value: "list", description: "snapshot" }]);
  await expect(engine.complete("upgrade demo ")).resolves.toEqual([{ value: "web=", description: "nginx" }]);
  await expect(engine.complete("snapshot cr")).resolves.toEqual([{ value: "create", description: "snapshot" }]);
  await expect(engine.complete("start de")).resolves.toEqual([{ value: "demo", description: "Idle" }]);
  await expect(engine.complete("start demo w")).resolves.toEqual([{ value: "web", description: "running" }]);
});

test("direct lifecycle invocations skip confirmation and mutate once", async () => {
  let stops = 0;
  const lifecycle = { ...api, stopStack: async () => { stops += 1; return { accepted: true }; } } as FloatLabApi;
  const result = await new CommandEngine(lifecycle, runtime).dispatch("shutdown demo");
  expect(result.code).toBe(0); expect(stops).toBe(1);
});

test("invalid flags are reported as usage errors", async () => {
  const result = await new CommandEngine(api, runtime).dispatch("logs demo --limit nope");
  expect(result.code).toBe(2); expect(result.result.kind).toBe("error");
});

test("failed edits clean up their temporary editor state", async () => {
  let cleaned = false;
  const failing = { ...api, getStackConfig: async () => "name: demo", validateCompose: async () => { throw new Error("invalid compose"); } } as FloatLabApi;
  const result = await new CommandEngine(failing, { ...runtime, editCompose: async (value) => value, cleanupCompose: async () => { cleaned = true; } }).dispatch("edit demo");
  expect(result.code).toBe(1); expect(cleaned).toBe(true);
});

test("search terms are emitted as one escaped LogsQL literal", async () => {
  let query = "";
  const searchable = { ...api, searchLogs: async (value: { query: string }) => { query = value.query; return []; } } as FloatLabApi;
  await new CommandEngine(searchable, runtime).dispatch(["logs", "demo", "--search", 'a"\\b']);
  expect(query).toBe('stack_id:"one" _msg:"a\\"\\\\b"');
});

test("an aborted create-start exits without polling", async () => {
  const controller = new AbortController(); controller.abort(); let polled = false;
  const creating = {
    ...api,
    listNodes: async () => [{ id: "node", name: "node", zfs_pool: "pool" }],
    validateCompose: async () => undefined,
    createStack: async () => ({ id: "created", name: "demo", state: "Provisioning", primary_node: "node", compose_file: "", dataset_path: "", failover_mode: "manual", created_at: "", updated_at: "" }),
    getStack: async () => { polled = true; throw new Error("should not poll"); },
  } as unknown as FloatLabApi;
  const result = await new CommandEngine(creating, { ...runtime, signal: controller.signal }).dispatch("create demo --primary node --start");
  expect(result.code).toBe(130); expect(polled).toBe(false);
});
