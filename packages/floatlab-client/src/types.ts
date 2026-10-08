import type { components } from "./schema";

export type Stack = components["schemas"]["Stack"];
export type Container = components["schemas"]["Container"] & { service?: string };
export type Node = components["schemas"]["Node"];
export type LogLine = components["schemas"]["LogLine"];

export type StackMetricSeries = components["schemas"]["StackMetricSeries"];
export type ExecResult = { stdout: string; stderr: string; exit_code: number };

export type CommandResult =
  | ({ kind: "exec" } & ExecResult)
  | { kind: "text"; text: string }
  | { kind: "table"; columns: string[]; rows: string[][] }
  | { kind: "logs"; lines: LogLine[] }
  | { kind: "operation"; action: string; state?: string; operationId?: string; text?: string }
  | { kind: "error"; message: string; status?: number };

export type Completion = { value: string; description?: string };

export interface CommandRuntime {
  interactive: boolean;
  signal?: AbortSignal;
  confirm(message: string): Promise<boolean>;
  editCompose(initial: string): Promise<string | undefined>;
  present(result: CommandResult): Promise<void>;
  /** Terminal adapters use this after a successful/cancelled edit to remove their private temp file. */
  cleanupCompose?(): Promise<void>;
  readCompose?(path: string): Promise<string>;
  ask?(message: string): Promise<string | undefined>;
  choose?(message: string, choices: Completion[]): Promise<string | undefined>;
}

export interface FloatLabApi {
  listStacks(): Promise<Stack[]>;
  getStack(id: string): Promise<Stack>;
  getStackConfig(id: string): Promise<string>;
  getStackStats(id: string, range: string): Promise<StackMetricSeries[]>;
  execContainer(stackId: string, containerId: string, command: string[], idempotencyKey: string): Promise<ExecResult>;
  listNodes(): Promise<Node[]>;
  listContainers(stackId: string): Promise<Container[]>;
  validateCompose(body: { name?: string; stack_id?: string; compose_file: string }): Promise<void>;
  createStack(body: { name: string; compose_file: string }, idempotencyKey: string): Promise<Stack>;
  updateCompose(id: string, compose_file: string, idempotencyKey: string): Promise<Stack>;
  startStack(id: string, idempotencyKey: string): Promise<unknown>;
  stopStack(id: string, idempotencyKey: string): Promise<unknown>;
  startContainer(stackId: string, containerId: string, idempotencyKey: string): Promise<Container>;
  stopContainer(stackId: string, containerId: string, idempotencyKey: string): Promise<Container>;
  deleteStack(id: string, purge: boolean, idempotencyKey: string): Promise<unknown>;
  getStackLogs(id: string, query: { tail?: number; since?: string; service?: string }): Promise<LogLine[]>;
  getContainerLogs(id: string, query: { tail?: number; since?: string }): Promise<LogLine[]>;
  searchLogs(query: { query: string; limit?: number; start?: string; end?: string }): Promise<LogLine[]>;
  upgradeStack(id: string, images: Record<string, string>, idempotencyKey: string): Promise<Record<string, unknown>>;
  createSnapshot(id: string, idempotencyKey: string): Promise<unknown>;
  listSnapshots(id: string): Promise<Record<string, unknown>[]>;
}
