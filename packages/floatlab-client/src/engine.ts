import { ApiError } from "./api";
import { v4 as idempotencyKey } from "uuid";
import type { AlertsApi, AlertRuleUpdate } from "./types";
import { backupRestoreDatasets } from "./backups";
import type { CommandResult, CommandRuntime, Completion, Container, FloatLabApi, Node, Stack, UpdateApi, UpdateAction, UpdateComponent, LabApi, LabJoinRequest, BackupApi, BackupRestoreApi } from "./types";

import { CommandError, parseCommand, tokenize, scan, completionContext, metadata, commandHelp, type ArgumentDefinition, type ParsedCommand } from "./commands";
export { CommandError, tokenize } from "./commands";

export { idempotencyKey };
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
  constructor(readonly api: FloatLabApi & Partial<UpdateApi & LabApi & BackupApi & BackupRestoreApi & AlertsApi>, readonly runtime: CommandRuntime, private readonly ttlMs = 10_000) {}

  async dispatch(input: string | string[]): Promise<{ code: number; result: CommandResult }> {
    try {
      const result = await this.run(parseCommand(typeof input === "string" ? tokenize(input) : input, this.runtime.interactive)); await this.runtime.present(result); return { code: result.kind === "exec" ? result.exit_code : result.kind === "error" ? 1 : 0, result };
    } catch (error) {
      const result: CommandResult = { kind: "error", message: error instanceof Error ? error.message : "Command failed", status: error instanceof ApiError ? error.status : undefined };
      await this.runtime.present(result); return { code: error instanceof CommandError ? error.code : 1, result };
    }
  }

  async complete(input: string): Promise<Completion[]> {
    try {
      const { words, starts, ends } = scan(input, true);
      const index = Math.max(0, words.length - 1);
      const choices = await this.completeTokens(words, index);
      const start = starts[index] ?? input.length;
      if (/\s/.test(input.slice(start).trim()) || /[\\'"]/.test(input.slice(start)))
        return choices.map(choice => ({ ...choice, replaceStart: start, replaceEnd: ends[index] ?? input.length }));
      return choices;
    } catch { return []; }
  }

  async completeTokens(words: string[], index: number): Promise<Completion[]> {
    if (!Number.isInteger(index) || index < 0 || index > words.length) return [];
    const context = completionContext(words, index, item => this.available(item));
    if (!context) return [];
    const { argument, values, prefix, inline } = context;
    const choices = [...context.choices];
    if (argument) {
      const details = metadata(argument);
      try { choices.push(...await this.suggest(details.provider, values[details.dependsOn ?? ""], prefix)); } catch { /* Offline and unauthorized resources have no suggestions. */ }
      if (details.appendSpace) for (const choice of choices) choice.appendSpace = true;
    }
    return choices.filter(choice => choice.value.toLowerCase().startsWith(prefix.toLowerCase())).map(choice => inline ? { ...choice, value: inline + choice.value } : choice);
  }

  private available(item: ArgumentDefinition): boolean {
    const capability = metadata(item).capability;
    return !capability || capability === "files" && !!this.runtime.readCompose || capability === "cli" && !!this.runtime.cli || capability === "interactive" && this.runtime.interactive;
  }

  private async suggest(provider: string | undefined, dependency: string | undefined, prefix: string): Promise<Completion[]> {
    if (provider === "files") return this.runtime.completeFiles?.(prefix) ?? [];
    if (provider === "stacks") return (await this.getStacks()).map(stack => ({ value: stack.name, description: stack.state }));
    if (provider === "nodes") return (await this.getNodes()).map(node => ({ value: node.name, description: node.status }));
    if (provider === "alerts" || provider === "active-alerts") return (await this.api.listAlerts?.() ?? []).filter(alert => provider !== "active-alerts" || alert.state !== "resolved").map(alert => ({ value: alert.id, description: `${alert.state}: ${alert.message}` }));
    if (provider === "rules") return (await this.api.listAlertRules?.() ?? []).map(rule => ({ value: rule.id, description: rule.name }));
    if ((provider === "containers" || provider === "images") && dependency) {
      const stack = await this.stack(dependency);
      const containers = await this.api.listContainers(stack.id);
      if (provider === "containers") return containers.map(container => ({ value: container.name, description: `${container.status}${container.health ? `, ${container.health}` : ""}` }));
      return [...new Map(containers.map(container => [container.service ?? container.name, container])).values()].map(container => ({ value: `${container.service ?? container.name}=`, description: container.image }));
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
  private async stack(value: string): Promise<Stack> {
    const separator = value.indexOf("/");
    if (separator < 0) return exactOne(await this.getStacks(), value, "stack");
    const node = await this.node(value.slice(0, separator));
    return exactOne((await this.getStacks()).filter(stack => stack.primary_node === node.id), value.slice(separator + 1), "stack");
  }
  private async container(stack: Stack, value: string): Promise<Container> { return exactOne(await this.api.listContainers(stack.id), value, "container"); }
  private async node(value: string): Promise<Node> { return exactOne(await this.getNodes(), value, "node"); }
  private async ask(message: string): Promise<string> { const value = await this.runtime.ask?.(message); if (!value) throw new CommandError(`${message} is required`); return value; }
  private async confirm(message: string): Promise<boolean> { return !this.runtime.interactive || await this.runtime.confirm(message); }
  private changed() { this.stacks = undefined; this.nodes = undefined; }

  private async run(parsed: ParsedCommand): Promise<CommandResult> {
    if (parsed.name === "help") return { kind: "text", text: commandHelp(parsed.positionals) };
    const { name, positionals: args, flags } = parsed;
    if (name === "alerts" || name === "alert-rules") {
      if (Object.keys(flags).length) throw new CommandError("Alert commands do not accept flags");
      return name === "alerts" ? this.alerts(args) : this.alertRules(args);
    }
    if (name === "lab") return this.lab(args, flags);
 if(name === "backup") return args[0] === "restore" ? this.restoreBackup(args.slice(1), flags) : this.backup(args);
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
    if (name === "update") return this.update(args, flags);
    return this.snapshot(args);
  }

  private async restoreBackup(args: string[], flags: Record<string, string | boolean>): Promise<CommandResult> {
    if (args.length !== 5) throw new CommandError("Usage: backup restore <stack> <new-name> <backup-node> <destination-node> <snapshot> [--source node] [--file compose.yml]");
    const api = this.api;
    if (!api.listBackups || !api.restoreBackup || !api.getBackupConfig) throw new CommandError("Backup restore API unavailable");
    const stack = await this.stack(args[0]!);
    const backup = await this.node(args[2]!);
    const destination = await this.node(args[3]!);
    const source = typeof flags.source === "string" ? (await this.node(flags.source)).id : stack.primary_node;
    let compose: string;
    if (typeof flags.file === "string") {
      if (!this.runtime.readCompose) throw new CommandError("--file is only available in the CLI");
      compose = await this.runtime.readCompose(flags.file);
    } else compose = await api.getBackupConfig(stack.id);
    const datasets = backupRestoreDatasets(compose);
    for (const dataset of datasets) {
      const snapshots = await api.listBackups({node_id: backup.id, source_node_id: source, dataset});
      if (!snapshots.some(snapshot => snapshot.name === args[4])) throw new CommandError(`Backup ${dataset}@${args[4]} is unavailable`);
    }
    if (!await this.confirm(`Restore and start ${args[1]} on ${destination.name} from ${backup.name}?`)) return {kind: "text", text: "Restore cancelled."};
    const result = await api.restoreBackup({name: args[1]!, source_node_id: source, backup_node_id: backup.id, destination_node_id: destination.id, compose_file: compose, snapshots: datasets.map(dataset => ({dataset, snapshot: args[4]!}))}, idempotencyKey());
    this.changed();
    return {kind: "operation", action: "backup restore", state: result.state ?? "accepted", operationId: result.operation_id, text: text(result)};
  }

  private async backup(args: string[]): Promise<CommandResult> {
 const [action, stackName, dataset, nodeName, policy] = args;
 const api=this.api;
 if(!api.listBackupTargets || !api.putBackupTarget || !api.removeBackupTarget || !api.triggerReplication || !api.listReplication) throw new CommandError("Backup API unavailable");
 if(!stackName || !["list","history","set","remove","run"].includes(action ?? "")) throw new CommandError("Usage: backup list|history|set|remove|run <stack> [dataset node policy]");
 const stack=await this.stack(stackName);
 if(action === "list" || action === "history") return {kind:"text", text: text(action === "list" ? await api.listBackupTargets(stack.id) : await api.listReplication(stack.id))};
 if(!dataset || !nodeName || (action === "set" && !policy)) throw new CommandError("Dataset, node and policy (for set) required");
 const node=await this.node(nodeName);
 if(action === "set") await api.putBackupTarget(stack.id,{dataset,node_id:node.id,schedule:policy!},idempotencyKey());
 else if(action === "remove") await api.removeBackupTarget(stack.id,{dataset,node_id:node.id},idempotencyKey());
 else await api.triggerReplication(stack.id,node.id,dataset,idempotencyKey());
 return {kind:"text",text: action === "remove" ? "Backup target disabled; source hold cleanup and pruning queued." : "Backup request accepted."};
 }

  private async lab(args: string[], flags: Record<string, string | boolean>): Promise<CommandResult> {
    const [action, target, operation] = args;
    const api = this.api;
    let value: unknown;
    if (args.length === 1 && action === "status" && api.getLabStatus) value = await api.getLabStatus();
    else if (args.length === 1 && action === "members" && api.listLabMembers) value = await api.listLabMembers();
    else if (args.length === 1 && action === "invite" && api.createLabInvitation) value = await api.createLabInvitation();
    else if (args.length === 2 && action === "join" && api.joinLab) {
      const request = JSON.parse(target!) as LabJoinRequest;
      if (!request.invitation?.token || !request.peer_url || !request.raft_address || !request.rqlite_address) throw new CommandError("Join requires invitation, peer_url, raft_address and rqlite_address");
      if (!await this.confirm("Join this empty appliance to the invited lab?")) return { kind: "text", text: "Join cancelled." };
      value = await api.joinLab(request); this.changed();
    } else if (args.length === 2 && action === "preview" && api.previewLabRemoval) value = await api.previewLabRemoval((await this.node(target!)).id);
    else if (args.length === 3 && action === "remove" && api.removeLabMember && api.previewLabRemoval) {
      const node = await this.node(target!);
      await this.runtime.present({ kind: "text", text: text(await api.previewLabRemoval(node.id)) });
      if (!await this.confirm(`Remove ${node.id}? Its workloads may still be running and its addresses remain reserved.`)) return { kind: "text", text: "Removal cancelled." };
      value = await api.removeLabMember(node.id, operation!); this.changed();
    } else if (args.length === 2 && action === "rotate" && api.rotateLabIdentity) value = await api.rotateLabIdentity(target!);
    else if (args.length === 3 && action === "release-peer-address" && flags["confirm-fenced"] && flags["confirm-reset"] && api.releaseLabPeerAddress) {
      const node = await this.node(target!);
      if (!await this.confirm(`Confirm ${node.id} is fenced and reset to an empty appliance before releasing its peer address?`)) return { kind: "text", text: "Address release cancelled." };
      value = await api.releaseLabPeerAddress(node.id, operation!);
    } else throw new CommandError("Invalid or unsupported lab command. See help; removal and rotation require a stable operation ID.");
    return { kind: "text", text: text(value) };
  }

  private async alerts(args: string[]): Promise<CommandResult> {
    const api = this.api;
    if (!api.listAlerts || !api.getAlert || !api.silenceAlert) throw new CommandError("Alert commands are unavailable in this client");
    const action = args[0] ?? "list";
    if (action === "list" && args.length <= 2) {
      const state = args[1] ?? "all";
      if (!["all", "active", "silenced", "resolved"].includes(state)) throw new CommandError("Alert state must be active, silenced, resolved, or all");
      const alerts = await api.listAlerts(state === "all" ? undefined : state as "active" | "silenced" | "resolved");
      return {kind: "table", columns: ["id", "rule", "state", "severity", "message", "muted until"], rows: alerts.map(alert => [alert.id, alert.rule_id, alert.state, alert.severity, alert.message, alert.silenced_until ?? ""])};
    }
    if (!["show", "mute"].includes(action) || args.length !== (action === "mute" ? 3 : 2)) throw new CommandError("Usage: alerts list [state] | alerts show <id> | alerts mute <id> <30m|1h|1d>");
    const alert = exactOne((await api.listAlerts()).map(alert => ({...alert, name: alert.id})), args[1]!, "alert");
    if (action === "show") return {kind: "text", text: text(await api.getAlert(alert.id))};
    if (alert.state === "resolved") throw new CommandError("Only active alerts can be muted");
    const duration = /^([1-9]\d*)(m|h|d)$/.exec(args[2]!);
    if (!duration) throw new CommandError("Mute duration must be positive minutes, hours, or days, such as 30m, 1h, or 1d");
    const until = new Date(Date.now() + Number(duration[1]) * {m: 60_000, h: 3_600_000, d: 86_400_000}[duration[2] as "m" | "h" | "d"]);
    if (!Number.isFinite(until.getTime())) throw new CommandError("Mute duration is too large");
    if (!await this.confirm(`Mute alert ${alert.id} until ${until.toISOString()}? Evaluation will continue.`)) return {kind: "text", text: "Mute cancelled."};
    const muted = await api.silenceAlert(alert.id, until.toISOString(), idempotencyKey());
    return {kind: "text", text: `Muted ${muted.id} until ${muted.silenced_until}.`};
  }

  private async alertRules(args: string[]): Promise<CommandResult> {
    const api = this.api;
    if (!api.listAlertRules || !api.getAlertRule || !api.updateAlertRule) throw new CommandError("Alert rule commands are unavailable in this client");
    const action = args[0] ?? "list";
    if (action === "list" && args.length <= 1) return {kind: "table", columns: ["id", "name", "enabled", "severity", "evaluations", "condition"], rows: (await api.listAlertRules()).map(rule => [rule.id, rule.name, String(rule.enabled ?? true), rule.severity, [...new Set((rule.evaluations ?? []).map(evaluation => evaluation.state))].join(", ") || "not evaluated", rule.condition])};
    if (!["show", "enable", "disable", "edit"].includes(action) || args.length !== (action === "edit" ? 3 : 2)) throw new CommandError("Usage: alert-rules list | alert-rules show|enable|disable <rule> | alert-rules edit <rule> '<JSON changes>'");
    const rule = exactOne(await api.listAlertRules(), args[1]!, "alert rule");
    if (action === "show") return {kind: "text", text: text(await api.getAlertRule(rule.id))};
    let changes: AlertRuleUpdate = {enabled: action === "enable"};
    if (action === "edit") {
      try { changes = JSON.parse(args[2]!); } catch { throw new CommandError("Rule changes must be a JSON object"); }
      if (!changes || typeof changes !== "object" || Array.isArray(changes) || !Object.keys(changes).length) throw new CommandError("Rule changes must be a nonempty JSON object");
      const fields = ["name", "condition", "severity", "for_duration", "freshness_query", "recovery_condition", "recovery_duration", "max_sample_age", "annotations", "enabled"];
      if (Object.keys(changes).some(key => !fields.includes(key))) throw new CommandError(`Editable rule fields: ${fields.join(", ")}`);
    }
    if (!await this.confirm(`${action} rule ${rule.name}? Changes: ${JSON.stringify(changes)}`)) return {kind: "text", text: "Rule change cancelled."};
    return {kind: "text", text: text(await api.updateAlertRule(rule.id, changes, idempotencyKey()))};
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
    const stacks = await this.getStacks();
    if (!allContainers) {
      const peerViews = stacks.some(stack => stack.replica);
      return { kind: "table", columns: ["name", "state", "primary", "secondary", ...(peerViews ? ["view"] : [])], rows: stacks.map(stack => [stack.name, stack.state, stack.primary_node, stack.secondary_node ?? "", ...(peerViews ? [stack.replica ? `cached revision ${stack.observed_revision ?? "unknown"}` : "local"] : [])]) };
    }
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
 const protectedSnapshots=snapshots.some(snapshot => Array.isArray(snapshot.hold_targets) && snapshot.hold_targets.length || Array.isArray(snapshot.transfer_targets) && snapshot.transfer_targets.length);
 const nodes=protectedSnapshots ? await this.getNodes() : [];
 const names=(value:unknown) => Array.isArray(value) ? value.map(id => nodes.find(node => node.id === id)?.name ?? String(id)).join(", ") : "";
    const columns = ["name", "created_at", ...(hasType ? ["type"] : []), "space", ...(protectedSnapshots ? ["held for", "transferring to"] : [])];
    return { kind: "table", columns, rows: snapshots.map((snapshot) => [text(snapshot.name ?? ""), text(snapshot.created_at ?? ""), ...(hasType ? [text(snapshot.type ?? "")] : []), text(snapshot.used ?? snapshot.space ?? ""), ...(protectedSnapshots ? [names(snapshot.hold_targets), names(snapshot.transfer_targets)] : [])]) };
  }

  private async update(args: string[], flags: Record<string, string | boolean>): Promise<CommandResult> {
    const [component, action, nodeValue, target] = args;
    const actions = component === "host" ? ["releases", "status", "prepare", "activate", "commit", "rollback", "cleanup"] : ["releases", "status", "start", "rollback", "cleanup"];
    if ((component !== "management" && component !== "host") || !action || !actions.includes(action) || !nodeValue || args.length > 4) throw new CommandError("Usage: update management|host <action> <node> [release|operation] [--skip-dry-run] [--upstream owner/repo]");
    if (!this.api.listUpdateReleases || !this.api.getUpdateStatus || !this.api.runUpdateAction) throw new CommandError("Appliance updates are unavailable in this client");
    const initial = action === "start" || action === "prepare";
    if (Object.keys(flags).some(key => key !== "upstream" && key !== "skip-dry-run")) throw new CommandError("Update commands accept only --upstream and --skip-dry-run");
    if (flags["skip-dry-run"] && !initial) throw new CommandError("--skip-dry-run requires start or prepare");
    if (flags.upstream && !initial && action !== "releases") throw new CommandError("--upstream requires releases, start or prepare");
    const upstream = typeof flags.upstream === "string" ? flags.upstream : undefined;
    if (upstream && !/^[A-Za-z0-9][A-Za-z0-9-]{0,38}\/[A-Za-z0-9_][A-Za-z0-9_.-]{0,99}$/.test(upstream)) throw new CommandError("--upstream must be a GitHub owner/repo");
    const node = await this.node(nodeValue);
    if (action === "releases") {
      if (target) throw new CommandError("Usage: update management|host releases <node>");
      const releases = await this.api.listUpdateReleases(node.id, component, upstream);
      return { kind: "table", columns: ["release", "name", "published", "prerelease"], rows: releases.map(item => [item.tag_name, item.name ?? "", item.published_at ?? "", String(item.prerelease ?? false)]) };
    }
    if (action === "status") return { kind: "text", text: text(await this.api.getUpdateStatus(node.id, component, target)) };
    if (!target) throw new CommandError(`A ${initial ? "release tag" : "transaction ID"} is required for ${action}`);
    const consequence = action === "activate" ? " This reboots the appliance." : action === "rollback" ? " This discards management changes since the update checkpoint; workload changes are not undone." : "";
    if (!await this.confirm(`${action} ${component} update on ${node.name}?${flags["skip-dry-run"] ? " Skipping the isolated preflight trial." : ""}${upstream ? ` Using releases from ${upstream}.` : ""}${consequence}`)) return { kind: "text", text: "Update cancelled." };
    const operation = await this.api.runUpdateAction(node.id, component as UpdateComponent, action as UpdateAction, initial ? { release: target, ...(upstream ? { upstream } : {}), ...(flags["skip-dry-run"] ? { skip_dry_run: true } : {}) } : { operation_id: target }, idempotencyKey());
    return { kind: "operation", action: `update ${component} ${action}`, operationId: operation.operation_id, state: operation.state, text: text(operation) };
  }
}
