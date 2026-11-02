import type { Container, ExecResult, FloatLabApi, HostNetworkApi, HostNetworkStatus, HostNetworkChange, NetworkPool, LogLine, Node, Stack, StackMetricSeries, UpdateApi, UpdateRelease, UpdateStatus } from "./types";
import type { AuditEntry, DashboardApi, TokenResponse, RaftHealth, Notification, NodeStats, StorageStats } from "./types";
import type { LabApi, BackupApi, BackupRestoreApi } from "./types";
import type { AlertsApi } from "./types";
import type { HttpApi, HttpRequest } from "./types";

export class ApiError extends Error {
  constructor(message: string, readonly status: number, readonly code?: string) { super(message); }
}

export type ApiClientOptions = { baseUrl: string; token?: string; fetch?: typeof fetch; signal?: AbortSignal };

export function normalizeApiUrl(url: string): string {
  const parsed = new URL(url);
  if (parsed.search || parsed.hash) throw new Error("FloatLab URL cannot include a query or fragment");
  const path = parsed.pathname.replace(/\/+$/, "");
  if (path && path !== "/api/v1") throw new Error("FloatLab URL must be an origin or end in /api/v1");
  parsed.pathname = "/api/v1";
  return parsed.toString().replace(/\/$/, "");
}

export function createApiClient(options: ApiClientOptions): FloatLabApi & HostNetworkApi & DashboardApi & UpdateApi & LabApi & BackupApi & BackupRestoreApi & AlertsApi & HttpApi {
  const baseUrl = normalizeApiUrl(options.baseUrl);
  const send = async <T>(path: string, init: HttpRequest = {}, negotiated = false): Promise<T> => {
    const headers = new Headers(init.headers);
    if (!headers.has("accept")) headers.set("accept", "application/json");
    if (options.token && !headers.has("authorization")) headers.set("authorization", `Bearer ${options.token}`);
    if (init.body && !headers.has("content-type")) headers.set("content-type", "application/json");
    let response: Response;
    const { onChunk, ...request } = init;
    try { response = await (options.fetch ?? fetch)(`${baseUrl}${path}`, { ...request, headers, signal: init.signal ?? options.signal }); }
    catch (error) { throw new ApiError(error instanceof Error ? error.message : "Network request failed", 0); }
    if (!response.ok) {
      const raw = await response.text();
      let body: { message?: string; error?: string; code?: string } = {};
      try { body = JSON.parse(raw) ?? {}; } catch { /* Plain-text errors are preserved below. */ }
      throw new ApiError(body.message ?? body.error ?? (raw || `${response.status} ${response.statusText}`), response.status, body.code);
    }
    if (response.status === 204) return undefined as T;
    const contentType = response.headers.get("content-type")?.split(";")[0]?.trim() ?? "";
    if (contentType === "text/event-stream" && response.body && onChunk) {
      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      try {
        while (true) {
          const chunk = await reader.read();
          if (chunk.done) break;
          const value = decoder.decode(chunk.value, {stream: true});
          if (value) await onChunk(value);
        }
        const remaining = decoder.decode();
        if (remaining) await onChunk(remaining);
      } finally { reader.releaseLock(); }
      return undefined as T;
    }
    if (contentType === "application/octet-stream" || contentType === "application/x-tar") return new Uint8Array(await response.arrayBuffer()) as T;
    const body = await response.text(); if (!body) return undefined as T;
    if (negotiated && contentType && contentType !== "application/json" && !contentType.endsWith("+json")) return body as T;
    try { return JSON.parse(body) as T; } catch { throw new ApiError("Invalid JSON response from FloatLab", response.status); }
  };
  const mutation = <T>(path: string, method: string, key: string, body?: unknown) => send<T>(path, {
    method, headers: { "idempotency-key": key }, body: body === undefined ? undefined : JSON.stringify(body),
  });
  const query = (values: Record<string, string | number | undefined>) => {
    const params = new URLSearchParams();
    for (const [key, value] of Object.entries(values)) if (value !== undefined && value !== "") params.set(key, String(value));
    const text = params.toString(); return text ? `?${text}` : "";
  };
  const hostNetworkPath = (node: string) => `/nodes/${encodeURIComponent(node)}/settings/network`;
  const changePath = (node: string, change: string) => `${hostNetworkPath(node)}/changes/${encodeURIComponent(change)}`;
  const updatePath = (node: string, component: string) => `/nodes/${encodeURIComponent(node)}/updates/${component}`;
  return {
    request: (path, init) => send(path, init, true),
    listAlerts: state => send(`/alerts${query({state})}`),
    getAlert: id => send(`/alerts/${encodeURIComponent(id)}`),
    silenceAlert: (id, until, key) => mutation(`/alerts/${encodeURIComponent(id)}/silence`, "POST", key, {until}),
    listAlertRules: () => send('/alert-rules'),
    getAlertRule: id => send(`/alert-rules/${encodeURIComponent(id)}`),
    updateAlertRule: (id, changes, key) => mutation(`/alert-rules/${encodeURIComponent(id)}`, "PUT", key, changes),
    listBackups: values => send(`/storage/backups${query(values)}`),
    getBackupConfig: async stack => {
      let response: Response;
      try { response = await (options.fetch ?? fetch)(`${baseUrl}/storage/backups/config${query({stack_id: stack})}`, {headers: {accept: "application/yaml", ...(options.token ? {authorization: `Bearer ${options.token}`} : {})}, signal: options.signal}); }
      catch (error) { throw new ApiError(error instanceof Error ? error.message : "Network request failed", 0); }
      if (!response.ok) throw new ApiError(await response.text() || `${response.status} ${response.statusText}`, response.status);
      return response.text();
    },
    restoreBackup: (request, key) => mutation("/storage/backups/restore", "POST", key, request),
    listBackupTargets: stack => send(`/storage/replication/${encodeURIComponent(stack)}/targets`),
 putBackupTarget: (stack, target, key) => mutation(`/storage/replication/${encodeURIComponent(stack)}/targets`, "PUT", key, target),
 removeBackupTarget: (stack, target, key) => mutation(`/storage/replication/${encodeURIComponent(stack)}/targets`, "DELETE", key, target),
 triggerReplication: (stack, node, dataset, key) => mutation(`/storage/replication/${encodeURIComponent(stack)}/trigger${query({dest_node_id:node,dataset})}`, "POST", key),
 listReplication: stack => send(`/storage/replication${query({stack_id:stack})}`),
 getLabStatus: () => send("/lab/status"),
    listLabMembers: () => send("/lab/members"),
    createLabInvitation: () => send("/lab/invitations", { method: "POST" }),
    joinLab: request => send("/lab/join", { method: "POST", body: JSON.stringify(request) }),
    previewLabRemoval: node => send(`/lab/members/${encodeURIComponent(node)}/removal`),
    removeLabMember: (node, operationId) => send(`/lab/members/${encodeURIComponent(node)}/remove`, { method: "POST", body: JSON.stringify({ confirm_node_id: node, operation_id: operationId }) }),
    rotateLabIdentity: operationId => send("/lab/identity/rotate", { method: "POST", body: JSON.stringify({ operation_id: operationId }) }),
    releaseLabPeerAddress: (node, operationId) => send(`/lab/members/${encodeURIComponent(node)}/release-peer-address`, { method: "POST", body: JSON.stringify({ confirm_node_id: node, operation_id: operationId, confirm_fenced: true, confirm_reset: true }) }),
    listUpdateReleases: (node, component, upstream) => send<UpdateRelease[]>(`${updatePath(node, component)}/releases${query({ upstream })}`),
    getUpdateStatus: (node, component, operationId) => send<UpdateStatus | null>(`${updatePath(node, component)}/status${query({ operation_id: operationId })}`),
    runUpdateAction: (node, component, action, request, key) => mutation<UpdateStatus>(`${updatePath(node, component)}/${action}`, "POST", key, request),
    createAuthToken: (credentials) => send<TokenResponse>("/auth/token", { method: "POST", body: JSON.stringify(credentials) }),
    getRaftHealth: () => send<RaftHealth>("/health/raft"),
    listNotifications: () => send<Notification[]>("/notifications?limit=50"),
    listAuditLog: () => send<AuditEntry[]>("/logs/audit?limit=50"),
    getNodeStats: (id) => send<NodeStats>(`/stats/nodes/${encodeURIComponent(id)}?range=1h`),
    getStorageStats: (id) => send<StorageStats>(`/stats/storage/${encodeURIComponent(id)}?range=1h`),
    getNodeLogs: (id) => send<LogLine[]>(`/logs/nodes/${encodeURIComponent(id)}?tail=100`),
    getHostNetwork: (node) => send<HostNetworkStatus>(hostNetworkPath(node)),
    applyHostNetwork: (node, revision, config, key) => mutation<HostNetworkChange>(hostNetworkPath(node), "PUT", key, { revision, config }),
    getHostNetworkChange: (node, change) => send<HostNetworkChange>(changePath(node, change)),
    confirmHostNetworkChange: (node, change) => send<HostNetworkChange>(`${changePath(node, change)}/confirm`, { method: "POST" }),
    rollbackHostNetworkChange: (node, change) => send<HostNetworkChange>(`${changePath(node, change)}/rollback`, { method: "POST" }),
    listNetworkPools: () => send<NetworkPool[]>("/settings/network-pools"),
    createNetworkPool: (pool, key) => mutation<NetworkPool>("/settings/network-pools", "POST", key, pool),
    updateNetworkPool: (id, pool, key) => mutation<NetworkPool>(`/settings/network-pools/${encodeURIComponent(id)}`, "PUT", key, pool),
    deleteNetworkPool: (id, key) => mutation<void>(`/settings/network-pools/${encodeURIComponent(id)}`, "DELETE", key),
    listStacks: () => send<Stack[]>("/stacks"), getStack: (id) => send<Stack>(`/stacks/${encodeURIComponent(id)}`),
    getStackConfig: async (id) => {
      let response: Response;
      try { response = await (options.fetch ?? fetch)(`${baseUrl}/stacks/${encodeURIComponent(id)}/config`, { headers: { accept: "application/yaml", ...(options.token ? { authorization: `Bearer ${options.token}` } : {}) }, signal: options.signal }); }
      catch (error) { throw new ApiError(error instanceof Error ? error.message : "Network request failed", 0); }
      if (!response.ok) throw new ApiError(await response.text() || `${response.status} ${response.statusText}`, response.status);
      return response.text();
    },
    getStackStats: (id, range) => send<StackMetricSeries[]>(`/stats/stacks/${encodeURIComponent(id)}${query({ range })}`),
    execContainer: (stack, container, command, key) => mutation<ExecResult>(`/stacks/${encodeURIComponent(stack)}/containers/${encodeURIComponent(container)}/exec`, "POST", key, { command }),
    listNodes: () => send<Node[]>("/nodes"), listContainers: (id) => send<Container[]>(`/stacks/${encodeURIComponent(id)}/containers`),
    validateCompose: (body) => send<void>("/stacks/validate", { method: "POST", body: JSON.stringify(body) }),
    createStack: (body, key) => mutation<Stack>("/stacks", "POST", key, body),
    updateCompose: (id, compose_file, key) => mutation<Stack>(`/stacks/${encodeURIComponent(id)}/compose`, "PUT", key, { compose_file }),
    startStack: (id, key) => mutation(`/stacks/${encodeURIComponent(id)}/start`, "POST", key),
    stopStack: (id, key) => mutation(`/stacks/${encodeURIComponent(id)}/stop`, "POST", key),
    startContainer: (stack, container, key) => mutation<Container>(`/stacks/${encodeURIComponent(stack)}/containers/${encodeURIComponent(container)}/start`, "POST", key),
    stopContainer: (stack, container, key) => mutation<Container>(`/stacks/${encodeURIComponent(stack)}/containers/${encodeURIComponent(container)}/stop`, "POST", key),
    deleteStack: (id, purge, key) => mutation(`/stacks/${encodeURIComponent(id)}${query({ purge: purge ? "true" : undefined })}`, "DELETE", key),
    getStackLogs: (id, values) => send<LogLine[]>(`/logs/stacks/${encodeURIComponent(id)}${query({ tail: values.tail, since: values.since, service: values.service })}`),
    getContainerLogs: (id, values) => send<LogLine[]>(`/logs/containers/${encodeURIComponent(id)}${query({ tail: values.tail, since: values.since })}`),
    searchLogs: (values) => send<LogLine[]>(`/logs/search${query(values)}`),
    upgradeStack: (id, images, key) => mutation<Record<string, unknown>>(`/stacks/${encodeURIComponent(id)}/upgrade`, "POST", key, { images }),
    createSnapshot: (id, key) => mutation(`/stacks/${encodeURIComponent(id)}/snapshots`, "POST", key),
    listSnapshots: (id) => send<Record<string, unknown>[]>(`/stacks/${encodeURIComponent(id)}/snapshots`),
  };
}
