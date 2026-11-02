import { expect, test } from "bun:test";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { assemble, coverage, mergeCommands, schemaInputs, validateCommands, type CoverageOperation } from "../scripts/commands";
import { applyCompletion, commandSpec, parseCommand, tokenize } from "../src/commands";
import { CommandEngine, type CommandRuntime, type FloatLabApi } from "../src";

const runtime: CommandRuntime = { interactive: true, confirm: async () => false, editCompose: async () => undefined, present: async () => {} };
const api = { listStacks: async () => [{ id: "stack", name: "demo stack", primary_node: "node", state: "idle" }], listNodes: async () => [{ id: "node", name: "lab node", status: "ready" }], listContainers: async () => [{ id: "container", name: "web app", status: "running" }] } as unknown as FloatLabApi;

test("modules assemble deterministically and shared groups reject conflicts", async () => {
  expect(await assemble()).toEqual(commandSpec);
  const target = [{ name: "backup", commands: [{ name: "list" }] }];
  mergeCommands(target, [{ name: "backup", commands: [{ name: "restore" }] }]);
  expect(target[0]!.commands.map(item => item.name)).toEqual(["list", "restore"]);
  expect(() => mergeCommands(target, [{ name: "backup", description: "different", commands: [{ name: "run" }] }])).toThrow("Conflicting group");
  expect(() => mergeCommands(target, [{ name: "backup", commands: [{ name: "list" }] }])).toThrow("Duplicate executable");
  expect(() => validateCommands({ name: "root", commands: [{ name: "list", aliases: ["ls"] }, { name: "ls" }] })).toThrow("collision");
  expect(() => validateCommands({ name: "root", arguments: [{ name: "node", metadata: [{ name: "floatlab", value: { provider: "typo" } }] }] })).toThrow("Unknown provider");
});

test("missing and malformed fragments fail assembly", async () => {
  const directory = await mkdtemp(join(tmpdir(), "floatlab-contract-"));
  const source = resolve(import.meta.dir, "../../../api/opencli");
  const manifest = JSON.parse(await readFile(join(source, "manifest.json"), "utf8"));
  try {
    manifest.modules = ["stacks.json"];
    await writeFile(join(directory, "manifest.json"), JSON.stringify(manifest));
    await expect(assemble(directory)).rejects.toThrow();
    await writeFile(join(directory, "stacks.json"), JSON.stringify({ commands: [{ name: "test", metadata: [{ name: "floatlab", value: { typo: true } }] }] }));
    await expect(assemble(directory)).rejects.toThrow("additional properties");
  } finally { await rm(directory, { recursive: true }); }
});

test("contract parsing keeps defaults, aliases, arity and interactive prompts", () => {
  for (const value of ["a b", 'a"b', "a\nb", "a\\b", "$HOME;literal"]) expect(tokenize(applyCompletion("start ", { value }))).toEqual(["start", value]);
  expect(parseCommand(["alerts"]).positionals).toEqual(["list"]);
  expect(parseCommand(["ls"]).name).toBe("list");
  expect(parseCommand(["update", "host", "prepare", "lab", "host-v1", "--upstream=owner/repo"]).flags.upstream).toBe("owner/repo");
  expect(parseCommand(["create"], true).positionals).toEqual([]);
  expect(() => parseCommand(["create"])).toThrow("Usage");
  expect(() => parseCommand(["list", "--purge"])).toThrow("Unknown flag");
  expect(() => parseCommand(["stats", "demo", "--range", "bad"])).toThrow("range must be");
  expect(() => parseCommand(["logs", "demo", "--limit"])).toThrow("needs a value");
  expect(() => parseCommand(["list", "extra", "extra"])).toThrow("Usage");
  expect(() => parseCommand(["exec", "demo", "web", "echo", "--", "literal"])).toThrow("Usage");
  expect(parseCommand(["delete", "demo", "--help"]).name).toBe("help");
  expect(parseCommand(["alerts", "mute", "incident", "2h"]).positionals.at(-1)).toBe("2h");
});

test("completion shares context, tolerates quotes and keeps filesystem capability in the CLI", async () => {
  const engine = new CommandEngine(api, runtime);
  const choices = await engine.complete('start "demo s');
  expect(choices[0]?.value).toBe("demo stack");
  expect(applyCompletion('start "demo s', choices[0]!)).toBe('start "demo stack"');
  expect((await engine.complete("create demo --secondary "))[0]?.value).toBe("lab node");
  expect((await engine.complete('create demo --primary "lab node" --secondary=la'))[0]?.value).toBe("--secondary=lab node");
  expect(await engine.completeTokens(["start", "demo stack", "we", "ignored"], 2)).toEqual([{ value: "web app", description: "running" }]);
  expect(await engine.completeTokens(["exec", "demo stack", "web app", "--", ""], 4)).toEqual([]);
  expect(await engine.complete("create demo --fi")).toEqual([]);
  expect((await engine.complete("update host prepare lab tag --" )).map(choice => choice.value)).toContain("--skip-dry-run");
  const offline = new CommandEngine({ listStacks: async () => { throw new Error("unauthorized"); } } as unknown as FloatLabApi, runtime);
  expect(await offline.complete("start de")).toEqual([]);
  expect((await offline.complete("upd"))[0]?.value).toBe("update");
});

test("coverage reports new inputs and operations while rejecting stale cross-module mappings", () => {
  const operation: CoverageOperation = { module: "nodes.yaml", operationId: "example", method: "PUT", path: "/nodes/{id}", inputs: ["path/id", "body/new_field"], unsupported: [] };
  const document = { opencli: "0.1", info: { title: "test", version: "1" }, command: { name: "test", commands: [{ name: "edit", arguments: [{ name: "node" }], metadata: [{ name: "floatlab", value: { api: [{ operationId: "example", inputs: { "path/id": "argument:node" } }] } }] }] } };
  const report = coverage(document, [operation, { ...operation, operationId: "newEndpoint" }]);
  expect(report[0]?.uncovered).toEqual(["body/new_field"]);
  expect(report[0]?.module).toBe("nodes.yaml");
  expect(report[1]?.mappings).toEqual([]);
  expect(() => coverage(document, [])).toThrow("Unknown operationId");
  expect(() => coverage(document, [{ ...operation, inputs: [] }])).toThrow("Unknown API input");
  const unsupported: string[] = [];
  expect(schemaInputs({ allOf: [{ properties: { field: { type: "string" } } }, { properties: { hidden: { readOnly: true }, nested: { oneOf: [{ properties: { child: { type: "string" } } }] } } }] }, "body", unsupported)).toEqual(["body/field", "body/nested/child"]);
  expect(unsupported).toEqual([]);
});
