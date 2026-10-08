import type { Container, ExecResult, FloatLabApi, HostNetworkApi, HostNetworkStatus, HostNetworkChange, NetworkPool, LogLine, Node, Stack, StackMetricSeries } from "./types";

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

export function createApiClient(options: ApiClientOptions): FloatLabApi & HostNetworkApi {
  const baseUrl = normalizeApiUrl(options.baseUrl);
  const send = async <T>(path: string, init: RequestInit = {}): Promise<T> => {
    const headers = new Headers(init.headers);
    headers.set("accept", "application/json");
    if (options.token) headers.set("authorization", `Bearer ${options.token}`);
    if (init.body && !headers.has("content-type")) headers.set("content-type", "application/json");
    let response: Response;
    try { response = await (options.fetch ?? fetch)(`${baseUrl}${path}`, { ...init, headers, signal: options.signal }); }
    catch (error) { throw new ApiError(error instanceof Error ? error.message : "Network request failed", 0); }
    if (!response.ok) {
      const body = await response.json().catch(() => ({})) as { message?: string; error?: string; code?: string };
      throw new ApiError(body.message ?? body.error ?? `${response.status} ${response.statusText}`, response.status, body.code);
    }
    if (response.status === 204) return undefined as T;
    const body = await response.text(); if (!body) return undefined as T;
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
  return {
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
