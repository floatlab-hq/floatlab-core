import { readFile, writeFile, mkdir } from "node:fs/promises";
import { resolve, basename } from "node:path";
import { createHash } from "node:crypto";
import { isDeepStrictEqual } from "node:util";
import Ajv from "ajv/dist/2020";
import { bundle, createConfig } from "@redocly/openapi-core";
import { parse } from "yaml";
import { metadata, names, type CommandDefinition, type OpenCLI } from "../src/command-schema";
import { completionScript, shells, completionFilename } from "../../../cli/src/completion-scripts";

const root = resolve(import.meta.dir, "../../..");
const json = async (file: string) => JSON.parse(await readFile(file, "utf8"));
const encode = (value: unknown) => JSON.stringify(value, null, 2) + "\n";

export function mergeCommands(target: CommandDefinition[], incoming: CommandDefinition[]): void {
  for (const item of incoming) {
    const existing = target.find(command => command.name === item.name);
    if (!existing) { target.push(structuredClone(item)); continue; }
    const { commands: oldChildren, ...oldGroup } = existing;
    const { commands: newChildren, ...newGroup } = item;
    if (!oldChildren?.length || !newChildren?.length || existing.arguments?.length || item.arguments?.length || metadata(existing).api || metadata(item).api)
      throw new Error(`Duplicate executable command: ${item.name}`);
    if (!isDeepStrictEqual(oldGroup, newGroup)) throw new Error(`Conflicting group: ${item.name}`);
    mergeCommands(oldChildren, newChildren);
  }
}

const providers = ["files", "stacks", "nodes", "containers", "images", "alerts", "active-alerts", "rules"];
export function validateCommands(command: CommandDefinition, prefix = ""): void {
  const location = `${prefix} ${command.name}`.trim();
  for (const item of [command, ...(command.arguments ?? []), ...(command.options ?? []), ...(command.options ?? []).flatMap(option => option.arguments ?? [])]) {
    if ((item.metadata?.length ?? 0) > 1) throw new Error(`Duplicate metadata namespace: ${location}`);
  }
  const checkNames = (items: { name: string; aliases?: string[] }[]) => {
    const seen = new Set<string>();
    for (const item of items) for (const name of names(item)) {
      if (seen.has(name.toLowerCase())) throw new Error(`Name/alias collision: ${location}: ${name}`);
      seen.add(name.toLowerCase());
    }
  };
  checkNames(command.commands ?? []); checkNames(command.options ?? []);
  if (metadata(command).defaultCommand && !command.commands?.some(child => child.name === metadata(command).defaultCommand)) throw new Error(`Invalid default command: ${location}`);
  const checkArgument = (arg: CommandDefinition) => {
    const m = metadata(arg);
    if (m.provider && !providers.includes(m.provider)) throw new Error(`Unknown provider: ${m.provider}`);
    if (m.dependsOn && !command.arguments?.some(item => item.name === m.dependsOn)) throw new Error(`Unknown dependency: ${location} ${m.dependsOn}`);
    if (arg.arity && arg.arity.maximum !== undefined && arg.arity.maximum < arg.arity.minimum) throw new Error(`Invalid arity: ${location}`);
  };
  for (const arg of command.arguments ?? []) checkArgument(arg);
  const args = command.arguments ?? [];
  if (new Set(args.map(arg => arg.name)).size !== args.length) throw new Error(`Duplicate argument: ${location}`);
  if (args.slice(0, -1).some(arg => arg.arity && arg.arity.maximum === undefined)) throw new Error(`Rest argument must be last: ${location}`);
  for (const option of command.options ?? []) {
    if (!/^--[a-z][a-z-]*$/.test(option.name) || (option.arguments?.length ?? 0) > 1) throw new Error(`Unsupported option: ${location} ${option.name}`);
    for (const arg of option.arguments ?? []) checkArgument(arg);
  }
  for (const child of command.commands ?? []) validateCommands(child, location);
}

export async function assemble(directory = resolve(root, "api/opencli")): Promise<OpenCLI> {
  const manifest = await json(resolve(directory, "manifest.json"));
  const { modules, schemaRevision, schemaSha256, ...document } = manifest;
  if (!/^[a-f0-9]{40}$/.test(schemaRevision) || !Array.isArray(modules) || new Set(modules).size !== modules.length) throw new Error("Invalid OpenCLI manifest");
  const commands: CommandDefinition[] = [];
  const schemaBytes = await readFile(resolve(root, "api/opencli/vendor/schema.json"));
  if (createHash("sha256").update(schemaBytes).digest("hex") !== schemaSha256) throw new Error("Pinned OpenCLI schema checksum mismatch");
  const schema = JSON.parse(schemaBytes.toString());
  const metadataSchema = {
    type: "object", additionalProperties: false,
    properties: {
      provider: { enum: providers }, dependsOn: { type: "string" }, suggestions: { type: "array", items: { type: "string" } },
      appendSpace: { type: "boolean" }, prompt: { type: "boolean" }, defaultCommand: { type: "string" }, passthrough: { type: "boolean" },
      capability: { enum: ["cli", "files", "interactive"] }, utility: { type: "boolean" },
      api: { type: "array", items: { type: "object", additionalProperties: false, required: ["operationId", "inputs"], properties: {
        operationId: { type: "string", minLength: 1 }, exclusion: { type: "string", minLength: 1 }, inputs: { type: "object", additionalProperties: { type: "string", pattern: "^(argument|option|json|editor|derived|runtime):.+" } },
      } } },
    },
  };
  schema.$defs.Metadata = { type: "object", additionalProperties: false, required: ["name", "value"], properties: { name: { const: "floatlab" }, value: metadataSchema } };
  for (const def of Object.values(schema.$defs) as any[]) if (def.type === "object") def.additionalProperties = false;
  schema.additionalProperties = false;
  const ajv = new Ajv({ strict: false, allErrors: true, validateFormats: false, unicodeRegExp: false });
  const validate = ajv.compile<OpenCLI>(schema);
  for (const module of modules) {
    if (typeof module !== "string" || !/^[a-z-]+\.json$/.test(module)) throw new Error("Invalid module path");
    if (module !== "cli.json") await readFile(resolve(root, "api/openapi", module.replace(/json$/, "yaml")));
    const fragment = await json(resolve(directory, module));
    if (Object.keys(fragment).some(key => key !== "commands") || !Array.isArray(fragment.commands)) throw new Error(`Invalid fragment: ${module}`);
    if (!validate({ ...document, command: { ...document.command, commands: fragment.commands } })) throw new Error(`${module}: ${ajv.errorsText(validate.errors)}`);
    mergeCommands(commands, fragment.commands);
  }
  document.command.commands = commands;
  if (!validate(document)) throw new Error(ajv.errorsText(validate.errors));
  validateCommands(document.command);
  return document;
}

export type CoverageOperation = { module: string; operationId: string; method: string; path: string; inputs: string[]; unsupported: string[] };
export function schemaInputs(schema: any, prefix: string, unsupported: string[], seen = new Set<any>()): string[] {
  if (!schema || schema.readOnly) return [];
  if (seen.has(schema)) { unsupported.push(`${prefix}: recursive schema`); return [prefix]; }
  const next = new Set(seen).add(schema);
  if (schema.$ref) { unsupported.push(`${prefix}: unresolved reference ${schema.$ref}`); return [prefix]; }
  const fields: string[] = [];
  for (const keyword of ["allOf", "oneOf", "anyOf"]) for (const member of schema[keyword] ?? []) fields.push(...schemaInputs(member, prefix, unsupported, next));
  for (const [name, child] of Object.entries(schema.properties ?? {})) fields.push(...schemaInputs(child, `${prefix}/${name}`, unsupported, next));
  if (schema.items) fields.push(...schemaInputs(schema.items, `${prefix}/*`, unsupported, next));
  if (schema.additionalProperties) fields.push(`${prefix}/*`);
  if (schema.not) unsupported.push(`${prefix}: not constraint`);
  if (schema.properties && !Object.keys(schema.properties).length) return [prefix];
  if (!fields.length && schema.properties) return [];
  return [...new Set(fields.length ? fields : [prefix])];
}

export async function apiOperations(): Promise<CoverageOperation[]> {
  const ref = resolve(root, "api/openapi/openapi.yaml");
  const source = parse(await readFile(ref, "utf8"));
  const result = await bundle({ ref, config: await createConfig({}), dereference: true });
  if (result.problems.some(problem => problem.severity === "error")) throw new Error(result.problems.map(problem => problem.message).join("\n"));
  const operations: CoverageOperation[] = [];
  for (const [path, item] of Object.entries(result.bundle.parsed.paths) as [string, any][]) {
    const origin = source.paths[path].$ref?.split("#")[0] ?? "openapi.yaml";
    for (const method of ["get", "put", "post", "delete", "patch", "head", "options", "trace"]) {
      const operation = item[method]; if (!operation) continue;
      const unsupported: string[] = [];
      const parameters = new Map<string, any>();
      for (const parameter of [...(item.parameters ?? []), ...(operation.parameters ?? [])]) parameters.set(`${parameter.in}/${parameter.name}`, parameter);
      const inputs = [...parameters.keys()];
      for (const media of Object.values(operation.requestBody?.content ?? {}) as any[]) inputs.push(...schemaInputs(media.schema, "body", unsupported));
      if (!operation.operationId) throw new Error(`Missing operationId: ${method} ${path}`);
      operations.push({ module: basename(origin), operationId: operation.operationId, method: method.toUpperCase(), path, inputs: [...new Set(inputs)].sort(), unsupported });
    }
  }
  if (new Set(operations.map(op => op.operationId)).size !== operations.length) throw new Error("Duplicate OpenAPI operationId");
  return operations.sort((a, b) => a.module.localeCompare(b.module) || a.operationId.localeCompare(b.operationId));
}

export function coverage(document: OpenCLI, operations: CoverageOperation[]) {
  const mappings = new Map<string, { command: string; inputs: Record<string, string>; exclusion?: string }[]>();
  const walk = (command: CommandDefinition, path: string[]) => {
    for (const mapping of metadata(command).api ?? []) {
      const operation = operations.find(op => op.operationId === mapping.operationId);
      if (!operation) throw new Error(`Unknown operationId: ${mapping.operationId}`);
      for (const [input, source] of Object.entries(mapping.inputs)) {
        if (!operation.inputs.some(field => field === input || field.startsWith(input + "/"))) throw new Error(`Unknown API input: ${mapping.operationId} ${input}`);
        const [kind, target] = [source.slice(0, source.indexOf(":")), source.slice(source.indexOf(":") + 1)];
        if (["argument", "json"].includes(kind) && !command.arguments?.some(arg => arg.name === target)) throw new Error(`Unknown command argument: ${path.join(" ")} ${target}`);
        if (kind === "option" && !command.options?.some(option => names(option).includes(target))) throw new Error(`Unknown command option: ${path.join(" ")} ${target}`);
      }
      const list = mappings.get(mapping.operationId) ?? []; list.push({ command: path.join(" "), inputs: mapping.inputs, ...(mapping.exclusion ? { exclusion: mapping.exclusion } : {}) }); mappings.set(mapping.operationId, list);
    }
    for (const child of command.commands ?? []) walk(child, [...path, child.name]);
  };
  walk(document.command, []);
  return operations.map(operation => {
    const linked = mappings.get(operation.operationId) ?? [];
    const classified = operation.inputs.map(input => ({ input, mappings: linked.flatMap(link => Object.entries(link.inputs).filter(([key]) => input === key || input.startsWith(key + "/")).map(([, source]) => ({ command: link.command, source, exposed: /^(argument|option|json|editor):/.test(source) }))) }));
    return { ...operation, mappings: linked, classified, uncovered: classified.filter(field => !field.mappings.length).map(field => field.input) };
  });
}

export async function run(mode: string) {
  if (!["generate", "check", "coverage"].includes(mode)) throw new Error("Use generate, check, or coverage");
  const document = await assemble();
  const report = coverage(document, await apiOperations());
  if (mode === "coverage") {
    const directory = resolve(root, "dist/command-coverage"); await mkdir(directory, { recursive: true });
    const lines = ["# OpenAPI → OpenCLI coverage", "", "Report only. Mappings declare coverage; handler tests verify behavior.", ""];
    for (const module of [...new Set(report.map(op => op.module))]) {
      lines.push(`## ${module}`, "", "| Operation | Commands | Uncovered inputs | Unsupported schemas |", "|---|---|---|---|");
      for (const op of report.filter(op => op.module === module)) lines.push(`| ${op.operationId} | ${op.mappings.map(item => item.exclusion ? `Excluded: ${item.exclusion}` : item.command).join(", ") || "Unmapped"} | ${op.uncovered.join(", ") || "—"} | ${op.unsupported.join(", ") || "—"} |`);
      lines.push("");
    }
    await writeFile(resolve(directory, "coverage.json"), encode(report)); await writeFile(resolve(directory, "coverage.md"), lines.join("\n"));
    console.log(`Coverage: ${report.filter(op => op.mappings.length).length}/${report.length} operations mapped. Report: dist/command-coverage/coverage.md`);
    return;
  }
  const artifacts: [string, string][] = [["packages/floatlab-client/src/commands.generated.json", encode(document)], ...shells.map(shell => [`cli/completions/${completionFilename(shell)}`, completionScript(shell)] as [string, string])];
  for (const [file, content] of artifacts) {
    const path = resolve(root, file);
    if (mode === "check") { if (await readFile(path, "utf8").catch(() => "") !== content) throw new Error(`Stale generated file: ${file}. Run pnpm commands:generate`); }
    else await writeFile(path, content);
  }
  console.log(`OpenCLI ${mode}: ${artifacts.length} artifacts verified`);
}
if (import.meta.main) await run(process.argv[2] ?? "check");
