import { ApiError } from "./api";
import type { CommandResult, CommandRuntime, Completion, Container, FloatLabApi, Node, Stack } from "./types";

export class CommandError extends Error { constructor(message: string, readonly code = 2) { super(message); } }

export function tokenize(input: string): string[] {
  const tokens: string[] = []; let token = ""; let quote = ""; let escaped = false; let started = false;
  const push = () => { if (started) tokens.push(token); token = ""; started = false; };
  for (const char of input) {
    if (escaped) { token += char; escaped = false; started = true; continue; }
    if (char === "\\") { escaped = true; started = true; continue; }
    if (quote) { if (char === quote) quote = ""; else token += char; started = true; continue; }
    if (char === "'" || char === '"') { quote = char; started = true; continue; }
    if (/\s/.test(char)) { push(); continue; }
    if ("|;&<>`$".includes(char)) throw new CommandError(`Unsupported shell operator: ${char}`);
    token += char; started = true;
  }
  if (escaped || quote) throw new CommandError("Unterminated escape or quote");
  push(); return tokens;
}

type Parsed = { name: string; positionals: string[]; flags: Record<string, string | boolean> };
const commands = [
  ["create", ["new", "add", "mk", "make"], "create <name> [--file path | --primary node] [--secondary node] [--start]"],
  ["config", [], "config <stack>"],
  ["stats", [], "stats <stack> [--range 1h|6h|24h|7d]"],
  ["exec", [], "exec <stack> <container> -- <command> [args...]"],
  ["edit", ["vi", "configure"], "edit <stack>"],
  ["start", [], "start <stack> [container]"], ["stop", ["shutdown"], "stop <stack> [container]"],
  ["delete", ["remove", "rm"], "delete <stack> [--purge]"],
  ["logs", [], "logs <stack> [container] [--search terms] [--since value] [--limit n]"],
  ["info", ["inspect"], "info <stack> [container]"], ["list", ["ls"], "list [stack] [--containers]"],
  ["upgrade", [], "upgrade <stack> [service=image ...]"], ["snapshot", [], "snapshot create|list <stack>"],
] as const;
const aliases = new Map<string, string>(commands.flatMap(([name, names]) => [name, ...names].map((alias) => [alias, name])));
const flagValues = new Set(["primary", "secondary", "search", "since", "limit", "file", "range"]);

function parse(input: string | string[]): Parsed {
  const words = typeof input === "string" ? tokenize(input) : input;
  const raw = words[0]?.toLowerCase();
  if (!raw) throw new CommandError("Enter a command");
  if (raw === "help") return { name: "help", positionals: [], flags: {} };
  const name = aliases.get(raw); if (!name) throw new CommandError(`Unknown command: ${raw}. Try help.`);
  const positionals: string[] = []; const flags: Record<string, string | boolean> = {};
  for (let index = 1; index < words.length; index += 1) {
    const word = words[index]!;
    if (word === "--" && name === "exec") {
      if (positionals.length !== 2 || index === words.length - 1) throw new CommandError("Usage: exec <stack> <container> -- <command> [args...]");
      flags.command = true; positionals.push(...words.slice(index + 1)); break;
    }
    if (!word.startsWith("--")) { positionals.push(word); continue; }
    const equal = word.indexOf("=");
    const key = word.slice(2, equal < 0 ? undefined : equal);
    const inline = equal < 0 ? undefined : word.slice(equal + 1);
    if (!key || !["primary", "secondary", "start", "purge", "search", "since", "limit", "containers", "file", "range"].includes(key)) throw new CommandError(`Unknown flag: ${word}`);
    if (flagValues.has(key)) {
      const value = inline ?? words[++index]; if (!value || value.startsWith("--")) throw new CommandError(`--${key} needs a value`);
      flags[key] = value;
    } else { if (inline !== undefined) throw new CommandError(`--${key} does not take a value`); flags[key] = true; }
  }
  return { name, positionals, flags };
}

const idempotencyKey = () => crypto.randomUUID();
const isMatch = (value: string, item: { id: string; name: string; service?: string }) => item.id === value || item.name === value || item.service === value || item.id.startsWith(value);
const exactOne = <T extends { id: string; name: string }>(items: T[], value: string, label: string): T => {
  const found = items.filter((item) => isMatch(value, item));
  if (found.length === 1) return found[0]!;
  throw new CommandError(found.length ? `Ambiguous ${label}: ${value}` : `Unknown ${label}: ${value}`);
};
const text = (value: unknown) => typeof value === "string" ? value : JSON.stringify(value, null, 2);

export class CommandEngine {
  private stacks?: { at: number; value: Stack[] };
  private nodes?: { at: number; value: Node[] };
  constructor(readonly api: FloatLabApi, readonly runtime: CommandRuntime, private readonly ttlMs = 10_000) {}

  async dispatch(input: string | string[]): Promise<{ code: number; result: CommandResult }> {
    try {
      const result = await this.run(parse(input)); await this.runtime.present(result); return { code: result.kind === "exec" ? result.exit_code : result.kind === "error" ? 1 : 0, result };
    } catch (error) {
      const result: CommandResult = { kind: "error", message: error instanceof Error ? error.message : "Command failed", status: error instanceof ApiError ? error.status : undefined };
      await this.runtime.present(result); return { code: error instanceof CommandError ? error.code : 1, result };
    }
  }

  async complete(input: string): Promise<Completion[]> {
    let words: string[];
    try { words = tokenize(input); } catch { return []; }
    const last = input.endsWith(" ") ? "" : words.at(-1) ?? "";
    const matching = (choices: Completion[]) => choices.filter((choice) => choice.value.toLowerCase().startsWith(last.toLowerCase()));
    if (words.length <= 1 && !input.endsWith(" ")) return matching([...commands.flatMap(([name, names, description]) => [name, ...names].map((value) => ({ value, description }))), ...["help", "exit", "quit"].map((value) => ({ value, description: "interactive command" }))]);
    const name = aliases.get(words[0] ?? ""); if (!name) return [];
    if (name === "snapshot") {
      if (words.length === 1 && input.endsWith(" ")) return ["create", "list"].map((value) => ({ value, description: "snapshot" }));
      if (words.length === 2 && !input.endsWith(" ")) return matching(["create", "list"].map((value) => ({ value, description: "snapshot" })));
      if (words.length === 2 && input.endsWith(" ") && ["create", "list"].includes(words[1]!)) return (await this.getStacks().catch(() => [])).map((stack) => ({ value: stack.name, description: stack.state }));
      if (words.length === 3 && ["create", "list"].includes(words[1]!)) return matching((await this.getStacks().catch(() => [])).map((stack) => ({ value: stack.name, description: stack.state })));
      return [];
    }
    if (["create"].includes(name) && words.some((word) => word === "--primary" || word === "--secondary")) return matching((await this.getNodes().catch(() => [])).map((node) => ({ value: node.name, description: node.status })));
    if (["config", "stats", "exec", "edit", "start", "stop", "delete", "logs", "info", "upgrade", "list"].includes(name) && words.length === 2 && !input.endsWith(" ")) return matching((await this.getStacks().catch(() => [])).map((stack) => ({ value: stack.name, description: stack.state })));
    if (["start", "stop", "logs", "info", "exec"].includes(name) && ((words.length === 2 && input.endsWith(" ")) || words.length === 3)) {
      const stacks = await this.getStacks().catch(() => []); const stack = stacks.find((item) => isMatch(words[1] ?? "", item));
      return stack ? matching((await this.api.listContainers(stack.id).catch(() => [])).map((container) => ({ value: container.name, description: `${container.status}${container.health ? `, ${container.health}` : ""}` }))) : [];
    }
    if (name === "upgrade" && ((words.length === 2 && input.endsWith(" ")) || words.length === 3)) {
      const stack = (await this.getStacks().catch(() => [])).find((item) => isMatch(words[1] ?? "", item));
      if (!stack) return [];
      const containers = await this.api.listContainers(stack.id).catch(() => []);
      return matching([...new Map(containers.map((container) => [container.service ?? container.name, container])).values()].map((container) => ({ value: `${container.service ?? container.name}=`, description: container.image })));
    }
    return [];
  }

  private async getStacks(refresh = false): Promise<Stack[]> {
    if (!refresh && this.stacks && Date.now() - this.stacks.at < this.ttlMs) return this.stacks.value;
    const value = await this.api.listStacks(); this.stacks = { at: Date.now(), value }; return value;
  }
  private async getNodes(): Promise<Node[]> {
    if (this.nodes && Date.now() - this.nodes.at < this.ttlMs) return this.nodes.value;
    const value = await this.api.listNodes(); this.nodes = { at: Date.now(), value }; return value;
  }
  private async stack(value: string): Promise<Stack> { return exactOne(await this.getStacks(), value, "stack"); }
  private async container(stack: Stack, value: string): Promise<Container> { return exactOne(await this.api.listContainers(stack.id), value, "container"); }
  private async node(value: string): Promise<Node> { return exactOne(await this.getNodes(), value, "node"); }
  private async ask(message: string): Promise<string> { const value = await this.runtime.ask?.(message); if (!value) throw new CommandError(`${message} is required`); return value; }
  private async confirm(message: string): Promise<boolean> { return !this.runtime.interactive || await this.runtime.confirm(message); }
  private changed() { this.stacks = undefined; this.nodes = undefined; }

  private async run(parsed: Parsed): Promise<CommandResult> {
    if (parsed.name === "help") return { kind: "text", text: commands.map(([, aliases, help]) => `${help}${aliases.length ? ` (${aliases.join(", ")})` : ""}`).join("\n") + "\nhelp, exit, quit" };
    const { name, positionals: args, flags } = parsed;
    if (name === "config") {
      if (args.length !== 1) throw new CommandError("Usage: config <stack>");
      return { kind: "text", text: await this.api.getStackConfig((await this.stack(args[0]!)).id) };
    }
    if (name === "stats") return this.stats(args, flags);
    if (name === "exec") {
      if (!flags.command || args.length < 3) throw new CommandError("Usage: exec <stack> <container> -- <command> [args...]");
      const stack = await this.stack(args[0]!); const container = await this.container(stack, args[1]!);
      return { kind: "exec", ...await this.api.execContainer(stack.id, container.id, args.slice(2), idempotencyKey()) };
    }
    if (name === "create") return this.create(args, flags);
    if (name === "edit") return this.edit(args);
    if (name === "start" || name === "stop") return this.lifecycle(name, args);
    if (name === "delete") return this.remove(args, Boolean(flags.purge));
    if (name === "logs") return this.logs(args, flags);
    if (name === "info") return this.info(args);
    if (name === "list") return this.list(args, Boolean(flags.containers));
    if (name === "upgrade") return this.upgrade(args);
    return this.snapshot(args);
  }

  private async create(args: string[], flags: Record<string, string | boolean>): Promise<CommandResult> {
    if (args.length > 1) throw new CommandError("Usage: create <name> [--file path | --primary node] [--secondary node] [--start]");
    const name = args[0] ?? await this.ask("Stack name");
    let compose: string | undefined;
    if (typeof flags.file === "string") {
      if (flags.primary || flags.secondary) throw new CommandError("--file uses node assignments from Compose; omit --primary and --secondary");
      if (!this.runtime.readCompose) throw new CommandError("--file is only available in the CLI");
      compose = await this.runtime.readCompose(flags.file);
      try { await this.api.validateCompose({ name, compose_file: compose }); }
      catch (error) { compose = await this.retryValidation(error) ? await this.editValidated(compose, { name }) : undefined; }
    } else {
      let primaryValue = typeof flags.primary === "string" ? flags.primary : undefined;
      if (!primaryValue && this.runtime.choose) primaryValue = await this.runtime.choose("Primary node", (await this.getNodes()).map((node) => ({ value: node.name, description: node.status })));
      const primary = await this.node(primaryValue ?? await this.ask("Primary node"));
      const secondary = typeof flags.secondary === "string" ? await this.node(flags.secondary) : undefined;
      const source = `name: ${JSON.stringify(name)}\nservices: {}\nx-fl-stack:\n  schema_version: 1\n  primary_node: ${JSON.stringify(primary.id)}${secondary ? `\n  secondary_node: ${JSON.stringify(secondary.id)}` : ""}\n  failover:\n    mode: manual\n  storage:\n    pool: ${JSON.stringify(primary.zfs_pool ?? "floatlab")}\n`;
      compose = await this.editValidated(source, { name });
    }
    if (compose === undefined) return { kind: "text", text: "Create cancelled." };
    const created = await this.api.createStack({ name, compose_file: compose }, idempotencyKey()); this.changed();
    if (!flags.start) return { kind: "text", text: `Created ${created.name}.` };
    try { await this.waitForIdle(created.id); } catch (error) { if (error instanceof CommandError && error.code === 130) throw error; return { kind: "error", message: `Created ${created.name}, but could not start it: ${error instanceof Error ? error.message : "provisioning failed"}` }; }
    if (!await this.confirm(`Start ${created.name}?`)) return { kind: "text", text: `Created ${created.name}; left stopped.` };
    try { await this.api.startStack(created.id, idempotencyKey()); } catch (error) { return { kind: "error", message: `Created ${created.name}, but could not start it: ${error instanceof Error ? error.message : "start failed"}` }; }
    this.changed(); return { kind: "operation", action: "start", state: "accepted", text: `Created ${created.name}; start requested.` };
  }

  private async edit(args: string[]): Promise<CommandResult> {
    if (args.length !== 1) throw new CommandError("Usage: edit <stack>"); const stack = await this.stack(args[0]!);
    const original = await this.api.getStackConfig(stack.id);
    if (!original) throw new CommandError("The server did not return the stack Compose file");
    const compose = await this.editValidated(original, { stack_id: stack.id });
    if (compose === undefined) return { kind: "text", text: "Edit cancelled." };
    if (compose === original) return { kind: "text", text: "Compose unchanged." };
    await this.api.updateCompose(stack.id, compose, idempotencyKey()); this.changed(); return { kind: "text", text: `Updated ${stack.name}.` };
  }

  private async editValidated(initial: string, validation: { name?: string; stack_id?: string }): Promise<string | undefined> {
    let source = initial;
    try {
      while (true) {
        const edited = await this.runtime.editCompose(source); if (edited === undefined) return undefined; source = edited;
        try { await this.api.validateCompose({ ...validation, compose_file: source }); return source; }
        catch (error) { if (!await this.retryValidation(error)) return undefined; }
      }
    } finally { await this.runtime.cleanupCompose?.(); }
  }

  private async retryValidation(error: unknown): Promise<boolean> {
    if (!this.runtime.interactive) throw error;
    await this.runtime.present({ kind: "error", message: error instanceof Error ? error.message : "Compose validation failed" });
    return this.runtime.confirm("Edit and try again?");
  }

  private async waitForIdle(id: string): Promise<void> {
    const deadline = Date.now() + 5 * 60_000;
    while (Date.now() < deadline) {
      if (this.runtime.signal?.aborted) throw new CommandError("Cancelled", 130);
      const stack = await this.api.getStack(id);
      if (stack.state === "Idle") return;
      if (stack.state === "Failed") throw new CommandError("provisioning failed");
      await new Promise<void>((resolve, reject) => {
        const signal = this.runtime.signal;
        const abort = () => { clearTimeout(timer); reject(new CommandError("Cancelled", 130)); };
        const timer = setTimeout(() => { signal?.removeEventListener("abort", abort); resolve(); }, 1_000);
        signal?.addEventListener("abort", abort, { once: true });
      });
    }
    throw new CommandError("timed out waiting for provisioning");
  }

  private async lifecycle(action: "start" | "stop", args: string[]): Promise<CommandResult> {
    if (args.length < 1 || args.length > 2) throw new CommandError(`Usage: ${action} <stack> [container]`); const stack = await this.stack(args[0]!);
    const target = args[1] ? await this.container(stack, args[1]) : undefined; const label = target ? `${stack.name}/${target.name}` : stack.name;
    if (!await this.confirm(`${action === "start" ? "Start" : "Stop"} ${label}?`)) return { kind: "text", text: "Cancelled." };
    const response = target ? action === "start" ? await this.api.startContainer(stack.id, target.id, idempotencyKey()) : await this.api.stopContainer(stack.id, target.id, idempotencyKey()) : action === "start" ? await this.api.startStack(stack.id, idempotencyKey()) : await this.api.stopStack(stack.id, idempotencyKey());
    this.changed(); return { kind: "operation", action, state: "accepted", text: text(response) };
  }

  private async remove(args: string[], purge: boolean): Promise<CommandResult> {
    if (args.length !== 1) throw new CommandError("Usage: delete <stack> [--purge]"); const stack = await this.stack(args[0]!);
    if (!await this.confirm(`Delete ${stack.name}${purge ? " and its data" : ""}?`)) return { kind: "text", text: "Cancelled." };
    await this.api.deleteStack(stack.id, purge, idempotencyKey()); this.changed(); return { kind: "operation", action: "delete", state: "accepted", text: `Deletion requested for ${stack.name}.` };
  }

  private async logs(args: string[], flags: Record<string, string | boolean>): Promise<CommandResult> {
    if (args.length < 1 || args.length > 2) throw new CommandError("Usage: logs <stack> [container] [--search terms] [--since value] [--limit n]");
    const limit = flags.limit === undefined ? undefined : Number(flags.limit); if (limit !== undefined && (!Number.isInteger(limit) || limit < 1)) throw new CommandError("--limit must be a positive integer");
    const stack = await this.stack(args[0]!); const container = args[1] ? await this.container(stack, args[1]) : undefined; const since = typeof flags.since === "string" ? flags.since : undefined;
    let lines;
    if (typeof flags.search === "string") {
      const scope = container ? `container_id:${JSON.stringify(container.id)}` : `stack_id:${JSON.stringify(stack.id)}`;
      lines = await this.api.searchLogs({ query: `${scope} _msg:${JSON.stringify(flags.search)}`, start: since, limit });
    } else lines = container ? await this.api.getContainerLogs(container.id, { since, tail: limit }) : await this.api.getStackLogs(stack.id, { since, tail: limit });
    return { kind: "logs", lines };
  }

  private async stats(args: string[], flags: Record<string, string | boolean>): Promise<CommandResult> {
    const range = typeof flags.range === "string" ? flags.range : "1h";
    if (args.length !== 1 || !["1h", "6h", "24h", "7d"].includes(range)) throw new CommandError("Usage: stats <stack> [--range 1h|6h|24h|7d]");
    const series = await this.api.getStackStats((await this.stack(args[0]!)).id, range);
    return { kind: "table", columns: ["metric", "value", "unit", "timestamp"], rows: series.map((metric) => {
      const point = metric.points.reduce<typeof metric.points[number] | undefined>((latest, item) => !latest || item.timestamp > latest.timestamp ? item : latest, undefined);
      return [metric.label, point ? String(point.value) : "no samples", metric.unit, point ? new Date(point.timestamp * 1000).toISOString() : "—"];
    }) };
  }

  private async info(args: string[]): Promise<CommandResult> {
    if (args.length < 1 || args.length > 2) throw new CommandError("Usage: info <stack> [container]"); const stack = await this.stack(args[0]!);
    if (args[1]) { const container = await this.container(stack, args[1]); return { kind: "table", columns: ["field", "value"], rows: Object.entries(container).map(([key, value]) => [key, text(value)]) }; }
    const detail = await this.api.getStack(stack.id); return { kind: "table", columns: ["field", "value"], rows: Object.entries(detail).map(([key, value]) => [key, text(value)]) };
  }

  private async list(args: string[], allContainers: boolean): Promise<CommandResult> {
    if (args.length > 1 || (args.length && allContainers)) throw new CommandError("Usage: list [stack] [--containers]");
    if (args[0]) { const stack = await this.stack(args[0]); return this.containerTable(await this.api.listContainers(stack.id)); }
    const stacks = await this.getStacks(); if (!allContainers) return { kind: "table", columns: ["name", "state", "primary", "secondary"], rows: stacks.map((stack) => [stack.name, stack.state, stack.primary_node, stack.secondary_node ?? ""]) };
    const rows = (await Promise.all(stacks.map(async (stack) => (await this.api.listContainers(stack.id)).map((container) => [stack.name, ...this.containerRow(container)])))).flat();
    return { kind: "table", columns: ["stack", "service", "container", "image", "state", "health", "active node"], rows };
  }
  private containerRow(container: Container): string[] { return [container.service ?? container.name, container.name, container.image, container.status, container.health ?? "", container.node_id ?? ""]; }
  private containerTable(containers: Container[]): CommandResult { return { kind: "table", columns: ["service", "container", "image", "state", "health", "active node"], rows: containers.map((container) => this.containerRow(container)) }; }

  private async upgrade(args: string[]): Promise<CommandResult> {
    if (args.length < 1) throw new CommandError("Usage: upgrade <stack> service=image [...]"); const stack = await this.stack(args[0]!); const containers = await this.api.listContainers(stack.id); const images: Record<string, string> = {};
    if (args.length === 1) {
      if (!this.runtime.interactive || !this.runtime.choose) throw new CommandError("Usage: upgrade <stack> service=image [...]");
      while (true) {
        const service = await this.runtime.choose("Service to upgrade", [...new Map(containers.map((container) => [container.service ?? container.name, { value: container.service ?? container.name, description: container.image }])).values(), { value: "", description: "Done" }]);
        if (!service) break; images[service] = await this.ask(`Replacement image for ${service}`);
      }
      if (!Object.keys(images).length) return { kind: "text", text: "Upgrade cancelled." };
    }
    for (const update of args.slice(1)) { const equal = update.indexOf("="); const service = update.slice(0, equal); const image = update.slice(equal + 1); if (equal < 1 || !image) throw new CommandError(`Invalid image update: ${update}`); if (!containers.some((container) => (container.service ?? container.name) === service)) throw new CommandError(`Unknown service: ${service}`); images[service] = image; }
    const operation = await this.api.upgradeStack(stack.id, images, idempotencyKey()); this.changed(); return { kind: "operation", action: "upgrade", state: String(operation.state ?? "accepted"), operationId: typeof operation.operation_id === "string" ? operation.operation_id : undefined, text: text(operation) };
  }

  private async snapshot(args: string[]): Promise<CommandResult> {
    if (args.length !== 2 || !["create", "list"].includes(args[0]!)) throw new CommandError("Usage: snapshot create|list <stack>"); const stack = await this.stack(args[1]!);
    if (args[0] === "create") { await this.api.createSnapshot(stack.id, idempotencyKey()); return { kind: "operation", action: "snapshot", state: "accepted", text: `Snapshot requested for ${stack.name}.` }; }
    const snapshots = await this.api.listSnapshots(stack.id); const hasType = snapshots.some((snapshot) => snapshot.type);
    const columns = ["name", "created_at", ...(hasType ? ["type"] : []), "space"];
    return { kind: "table", columns, rows: snapshots.map((snapshot) => [text(snapshot.name ?? ""), text(snapshot.created_at ?? ""), ...(hasType ? [text(snapshot.type ?? "")] : []), text(snapshot.used ?? snapshot.space ?? "")]) };
  }
}
