#!/usr/bin/env bun
import { ApiError, CommandEngine, createApiClient, type FloatLabApi } from "@floatlab/client";
import { commandInput, credentials, terminalRuntime } from "./terminal";
import { clearToken, connection, login, saveSession, valid } from "./session";

const version = typeof __FLOATLAB_VERSION__ === "string" ? __FLOATLAB_VERSION__ : "dev";
declare const __FLOATLAB_VERSION__: string;

async function acquire(interactive: boolean) {
  let current = await connection();
  if ((!current?.url || !valid(current)) && !interactive) throw new Error("No valid FloatLab session. Set FLOATLAB_URL and FLOATLAB_TOKEN, or run floatlab in a terminal to log in.");
  if (!current?.url) {
    const { input } = await import("@inquirer/prompts"); const url = await input({ message: "FloatLab URL" }); current = { url, source: "config" }; await saveSession(current);
  }
  if (!valid(current)) {
    const auth = await credentials(); const session = await login(current.url, auth.username, auth.password); await saveSession(session); current = { ...session, source: "config" };
  }
  return current;
}

async function execute(input: string | string[], interactive: boolean): Promise<number> {
  let current = await acquire(interactive); const runtime = terminalRuntime(interactive);
  const run = async () => new CommandEngine(createApiClient({ baseUrl: current.url, token: current.token, signal: runtime.signal }), runtime).dispatch(input);
  let result = await run();
  if (result.result.kind === "error" && result.result.status === 401 && current.source === "config" && interactive) {
    await clearToken(current.url); const auth = await credentials(); const session = await login(current.url, auth.username, auth.password); await saveSession(session); current = { ...session, source: "config" }; result = await run();
  }
  return result.code;
}

async function interactive(): Promise<number> {
  let current = await acquire(true); const controller = new AbortController(); process.once("SIGINT", () => controller.abort()); const runtime = terminalRuntime(true, controller.signal);
  let engine = new CommandEngine(createApiClient({ baseUrl: current.url, token: current.token, signal: runtime.signal }), runtime); const history: string[] = [];
  while (true) {
    const line = await commandInput(engine.complete.bind(engine), history); if (!line) continue; if (["exit", "quit"].includes(line)) return 0;
    history.push(line); let result = await engine.dispatch(line); if (result.code === 130) return 130;
    if (result.result.kind === "error" && result.result.status === 401 && current.source === "config") {
      await clearToken(current.url); const auth = await credentials(); const session = await login(current.url, auth.username, auth.password); await saveSession(session);
      current = { ...session, source: "config" }; engine = new CommandEngine(createApiClient({ baseUrl: current.url, token: current.token, signal: runtime.signal }), runtime); result = await engine.dispatch(line); if (result.code === 130) return 130;
    }
  }
}

async function main(): Promise<number> {
  const args = process.argv.slice(2);
  if (args[0] === "--version") { console.log(version); return 0; }
  if (args[0] === "--help" || args[0] === "-h") return (await new CommandEngine({} as FloatLabApi, terminalRuntime(false)).dispatch(["help"])).code;
  if (args.length) return execute(args, false);
  if (!process.stdin.isTTY) throw new Error("No command supplied; use `floatlab --help`.");
  return interactive();
}

main().then((code) => { process.exitCode = code; }).catch((error) => { console.error(error instanceof ApiError ? error.message : error instanceof Error ? error.message : String(error)); process.exitCode = 1; });
