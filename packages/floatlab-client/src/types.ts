import type { components } from "./schema";

export type Stack = components["schemas"]["Stack"];
export type Container = components["schemas"]["Container"] & { service?: string };
export type Node = components["schemas"]["Node"];
export type NodeStats = components["schemas"]["NodeStats"];
export type StorageStats = components["schemas"]["StorageStats"];
export type Notification = components["schemas"]["Notification"];
export type RaftHealth = components["schemas"]["RaftHealth"];
export type LoginRequest = components["schemas"]["LoginRequest"];
export type TokenResponse = components["schemas"]["TokenResponse"];
export type Alert = components["schemas"]["Alert"];
export type AlertRule = components["schemas"]["AlertRule"];
export type AlertRuleUpdate = Partial<components["schemas"]["AlertRuleUpdate"]>;
export interface AlertsApi {
  listAlerts(state?: Alert["state"]): Promise<Alert[]>;
  getAlert(id: string): Promise<Alert>;
  silenceAlert(id: string, until: string, key: string): Promise<Alert>;
  listAlertRules(): Promise<AlertRule[]>;
  getAlertRule(id: string): Promise<AlertRule>;
  updateAlertRule(id: string, changes: AlertRuleUpdate, key: string): Promise<AlertRule>;
}

export interface DashboardApi {
  createAuthToken(credentials: LoginRequest): Promise<TokenResponse>;
  getRaftHealth(): Promise<RaftHealth>;
  listNotifications(): Promise<Notification[]>;
  listAuditLog(): Promise<AuditEntry[]>;
  getNodeStats(id: string): Promise<NodeStats>;
  getStorageStats(id: string): Promise<StorageStats>;
  getNodeLogs(id: string): Promise<LogLine[]>;
}
export type LogLine = components["schemas"]["LogLine"];
export type AuditEntry = components["schemas"]["AuditEntry"] & { outcome?: string; details?: string | null };

export type StackMetricSeries = components["schemas"]["StackMetricSeries"];
export type ExecResult = { stdout: string; stderr: string; exit_code: number };

export type CommandResult =
  | { kind: "binary"; data: Uint8Array; contentType: string }
  | ({ kind: "exec" } & ExecResult)
  | { kind: "text"; text: string }
  | { kind: "table"; columns: string[]; rows: string[][] }
  | { kind: "logs"; lines: LogLine[] }
  | { kind: "operation"; action: string; state?: string; operationId?: string; text?: string }
  | { kind: "error"; message: string; status?: number };

export type Completion = { value: string; description?: string; appendSpace?: boolean; replaceStart?: number; replaceEnd?: number; kind?: "file" | "directory" };

export type HttpRequest = RequestInit & { onChunk?(text: string): Promise<void> };
export interface HttpApi { request(path: string, init?: HttpRequest): Promise<unknown>; }

export interface CommandRuntime {
  readBody?(path: string): Promise<Uint8Array>;
  writeChunk?(chunk: string): Promise<void>;
  interactive: boolean;
  cli?: boolean;
  completeFiles?(prefix: string): Promise<Completion[]>;
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

export type HostNetworkConfig = components["schemas"]["HostNetworkConfig"];
export type HostNetworkStatus = components["schemas"]["HostNetworkStatus"];
export type HostNetworkChange = components["schemas"]["HostNetworkChange"];
export type NetworkPool = components["schemas"]["NetworkPool"];
export type NetworkPoolWrite = components["schemas"]["NetworkPoolWrite"];

export interface HostNetworkApi {
  getHostNetwork(nodeId: string): Promise<HostNetworkStatus>;
  applyHostNetwork(nodeId: string, revision: string, config: HostNetworkConfig, idempotencyKey: string): Promise<HostNetworkChange>;
  getHostNetworkChange(nodeId: string, changeId: string): Promise<HostNetworkChange>;
  confirmHostNetworkChange(nodeId: string, changeId: string): Promise<HostNetworkChange>;
  rollbackHostNetworkChange(nodeId: string, changeId: string): Promise<HostNetworkChange>;
  listNetworkPools(): Promise<NetworkPool[]>;
  createNetworkPool(pool: NetworkPoolWrite, idempotencyKey: string): Promise<NetworkPool>;
  updateNetworkPool(id: string, pool: NetworkPoolWrite, idempotencyKey: string): Promise<NetworkPool>;
  deleteNetworkPool(id: string, idempotencyKey: string): Promise<void>;
}

export type UpdateComponent = "management" | "host";
export type UpdateAction = "start" | "prepare" | "activate" | "commit" | "rollback" | "cleanup";
export type UpdateRelease = components["schemas"]["UpdateRelease"];
export type UpdateStatus = NonNullable<components["schemas"]["UpdateStatus"]>;
export interface UpdateApi {
  listUpdateReleases(nodeId: string, component: UpdateComponent, upstream?: string): Promise<UpdateRelease[]>;
  getUpdateStatus(nodeId: string, component: UpdateComponent, operationId?: string): Promise<UpdateStatus | null>;
  runUpdateAction(nodeId: string, component: UpdateComponent, action: UpdateAction, request: { release?: string; operation_id?: string; upstream?: string; skip_dry_run?: boolean }, idempotencyKey: string): Promise<UpdateStatus>;
}

export type LabInvitation = { lab_id: string; bootstrap_url: string; token: string; fingerprint: string; expires_at: string };
export type LabJoinRequest = { invitation: LabInvitation; peer_url: string; raft_address: string; rqlite_address: string };
export interface LabApi {
  getLabStatus(): Promise<Record<string, unknown>>;
  listLabMembers(): Promise<Record<string, unknown>>;
  createLabInvitation(): Promise<LabInvitation>;
  joinLab(request: LabJoinRequest): Promise<Record<string, unknown>>;
  previewLabRemoval(nodeId: string): Promise<Record<string, unknown>>;
  removeLabMember(nodeId: string, operationId: string): Promise<Record<string, unknown>>;
  rotateLabIdentity(operationId: string): Promise<Record<string, unknown>>;
  releaseLabPeerAddress(nodeId: string, operationId: string): Promise<Record<string, unknown>>;
}

export type BackupTarget = { stack_id: string; dataset: string; node_id: string; source_node_id: string; schedule: string; local_policy: string; enabled: boolean; cleanup_pending: boolean; error?: string };
export interface BackupApi {
 listBackupTargets(stackId: string): Promise<BackupTarget[]>;
 putBackupTarget(stackId: string, target: {dataset: string; node_id: string; schedule: string}, key: string): Promise<BackupTarget>;
 removeBackupTarget(stackId: string, target: {dataset: string; node_id: string}, key: string): Promise<unknown>;
 triggerReplication(stackId: string, nodeId: string | undefined, dataset: string | undefined, key: string): Promise<unknown>;
 listReplication(stackId: string): Promise<Record<string, unknown>[]>;
}

export type BackupSnapshot = components["schemas"]["Snapshot"];
export type BackupRestoreRequest = {
 name: string;
 source_node_id: string;
 backup_node_id: string;
 destination_node_id: string;
 compose_file: string;
 snapshots: {dataset: string; snapshot: string}[];
};
export interface BackupRestoreApi {
 listBackups(query: {node_id: string; source_node_id: string; dataset: string}): Promise<BackupSnapshot[]>;
 getBackupConfig(stackId: string): Promise<string>;
 restoreBackup(request: BackupRestoreRequest, key: string): Promise<{operation_id?: string; stack_id?: string; state?: string}>;
}
