import { chmod, mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { homedir } from "node:os";
import { normalizeApiUrl } from "@floatlab/client";

export type Session = { url: string; token?: string; expires_at?: string };
export type Connection = Session & { source: "environment" | "config" };

export function configPath(env: Record<string, string | undefined> = process.env): string {
  return join(env.XDG_CONFIG_HOME || join(homedir(), ".config"), "floatlab", "config.json");
}

export async function loadSession(path = configPath()): Promise<Session | undefined> {
  try {
    const value = JSON.parse(await readFile(path, "utf8")) as Session;
    return value.url ? { ...value, url: normalizeApiUrl(value.url) } : undefined;
  } catch { return undefined; }
}

export async function saveSession(session: Session, path = configPath()): Promise<void> {
  const directory = dirname(path); await mkdir(directory, { recursive: true, mode: 0o700 }); await chmod(directory, 0o700);
  await writeFile(path, `${JSON.stringify({ url: normalizeApiUrl(session.url), token: session.token, expires_at: session.expires_at }, null, 2)}\n`, { mode: 0o600 }); await chmod(path, 0o600);
}

export const clearToken = (url: string, path = configPath()) => saveSession({ url }, path);

export const valid = (session: Session | undefined) => Boolean(session?.token && (!session.expires_at || Date.parse(session.expires_at) > Date.now()));

export async function connection(env: Record<string, string | undefined> = process.env, path = configPath()): Promise<Connection | undefined> {
  if (env.FLOATLAB_URL) {
    return { url: normalizeApiUrl(env.FLOATLAB_URL), token: env.FLOATLAB_TOKEN, source: "environment" };
  }
  const saved = await loadSession(path); return saved ? { ...saved, source: "config" } : undefined;
}

export async function login(url: string, username: string, password: string): Promise<Session> {
  const response = await fetch(`${normalizeApiUrl(url)}/auth/token`, { method: "POST", headers: { "content-type": "application/json", accept: "application/json" }, body: JSON.stringify({ username, password }) });
  const body = await response.json().catch(() => ({})) as { access_token?: string; expires_in?: number; error?: string };
  if (!response.ok || !body.access_token) throw new Error(body.error ?? `Login failed (${response.status})`);
  return { url: normalizeApiUrl(url), token: body.access_token, expires_at: new Date(Date.now() + (body.expires_in ?? 0) * 1000).toISOString() };
}
