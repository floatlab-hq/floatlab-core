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

test("config prints the saved Compose source", async () => {
  const source = "name: demo\nservices: {}\n";
  const configured = { ...api, getStackConfig: async () => source } as FloatLabApi;
  expect((await new CommandEngine(configured, runtime).dispatch("config demo")).result).toEqual({ kind: "text", text: source });
});

test("file creation validates the file and never opens an editor or asks for nodes", async () => {
  let created = "";
  const source = "name: demo\nx-fl-stack:\n  primary_node: node\nservices: {}";
  const configured = { ...api, validateCompose: async (body: { compose_file: string }) => { expect(body.compose_file).toBe(source); }, createStack: async (body: { compose_file: string }) => { created = body.compose_file; return { name: "demo" }; } } as unknown as FloatLabApi;
  const fileRuntime = { ...runtime, readCompose: async (path: string) => { expect(path).toBe("compose.yaml"); return source; }, editCompose: async () => { throw new Error("unexpected editor"); } };
  expect((await new CommandEngine(configured, fileRuntime).dispatch("create demo --file compose.yaml")).code).toBe(0);
  expect(created).toBe(source);
  expect((await new CommandEngine(configured, fileRuntime).dispatch("create demo --file compose.yaml --primary node")).code).toBe(2);
});

test("invalid file creation fails without mutation or prompting", async () => {
  const configured = { ...api, validateCompose: async () => { throw new Error("invalid Compose"); }, createStack: async () => { throw new Error("unexpected create"); } } as unknown as FloatLabApi;
  const fileRuntime = { ...runtime, readCompose: async () => "invalid", confirm: async () => { throw new Error("unexpected prompt"); } };
  const result = await new CommandEngine(configured, fileRuntime).dispatch("create demo --file compose.yaml");
  expect(result.code).toBe(1); expect(result.result).toMatchObject({ kind: "error", message: "invalid Compose" });
});

for (const retry of [true, false]) test(`invalid interactive Compose asks before retrying (${retry})`, async () => {
  let edits = 0; let saved = ""; const sources: string[] = [];
  const configured = { ...api, getStackConfig: async () => "original", validateCompose: async (body: { compose_file: string }) => { if (body.compose_file === "rejected") throw new Error("invalid Compose"); }, updateCompose: async (_id: string, source: string) => { saved = source; } } as unknown as FloatLabApi;
  const editRuntime = { ...runtime, interactive: true, editCompose: async (source: string) => { sources.push(source); return ++edits === 1 ? "rejected" : "fixed"; }, confirm: async (message: string) => { expect(message).toBe("Edit and try again?"); expect(presented.at(-1)).toMatchObject({ kind: "error", message: "invalid Compose" }); return retry; } };
  expect((await new CommandEngine(configured, editRuntime).dispatch("edit demo")).code).toBe(0);
  expect(sources).toEqual(retry ? ["original", "rejected"] : ["original"]);
  expect(saved).toBe(retry ? "fixed" : "");
});

test("stats shows the newest sample and identifies missing samples", async () => {
  const configured = { ...api, getStackStats: async (_id: string, range: string) => { expect(range).toBe("6h"); return [{ label: "mem", unit: "bytes", points: [{ timestamp: 2, value: 12 }, { timestamp: 1, value: 9 }] }, { label: "cpu", unit: "%", points: [] }]; } } as FloatLabApi;
  expect((await new CommandEngine(configured, runtime).dispatch("stats demo --range 6h")).result).toMatchObject({ kind: "table", rows: [["mem", "12", "bytes", "1970-01-01T00:00:02.000Z"], ["cpu", "no samples", "%", "—"]] });
  expect((await new CommandEngine(configured, runtime).dispatch("stats demo --range invalid")).code).toBe(2);
});

test("exec preserves argv after the delimiter and propagates exit status", async () => {
  const command = ["sh", "-c", "printf '%s' '$HOME;literal'", "", "--limit", "a=b"];
  const configured = { ...api, listContainers: async () => [{ id: "container", name: "demo-web-1", service: "web" }], execContainer: async (_stack: string, container: string, args: string[]) => { expect(container).toBe("container"); expect(args).toEqual(command); return { stdout: "out", stderr: "err", exit_code: 7 }; } } as unknown as FloatLabApi;
  const engine = new CommandEngine(configured, runtime);
  const result = await engine.dispatch(["exec", "demo", "web", "--", ...command]);
  expect(result.code).toBe(7); expect(result.result).toEqual({ kind: "exec", stdout: "out", stderr: "err", exit_code: 7 });
  expect((await engine.dispatch("exec demo web sh")).code).toBe(2);
});

test("interactive file validation offers editing the rejected file", async () => {
  let saved = ""; let edited = "";
  const configured = { ...api, validateCompose: async (body: { compose_file: string }) => { if (body.compose_file === "invalid") throw new Error("invalid Compose"); }, createStack: async (body: { compose_file: string }) => { saved = body.compose_file; return { name: "demo" }; } } as unknown as FloatLabApi;
  const fileRuntime = { ...runtime, interactive: true, readCompose: async () => "invalid", confirm: async () => true, editCompose: async (source: string) => { edited = source; return "fixed"; } };
  expect((await new CommandEngine(configured, fileRuntime).dispatch("create demo --file compose.yaml")).code).toBe(0);
  expect(edited).toBe("invalid"); expect(saved).toBe("fixed");
});
