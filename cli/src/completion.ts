import { readdir } from "node:fs/promises";
import { dirname, basename, join } from "node:path";
import { CommandError, CommandEngine, createApiClient, parseCommand, scan, type Completion, type FloatLabApi } from "@floatlab/client";
import { connection, valid } from "./session";
import { completionScript, shells, type Shell } from "./completion-scripts";

export async function completeFiles(prefix: string): Promise<Completion[]> {
  const directory = prefix.endsWith("/") ? prefix : dirname(prefix);
  const start = prefix.endsWith("/") ? "" : basename(prefix);
  try {
    return (await readdir(directory || ".", { withFileTypes: true }))
      .filter(item => item.name.startsWith(start) && (!item.name.startsWith(".") || start.startsWith(".")))
      .sort((a, b) => a.name.localeCompare(b.name))
      .map(item => ({ value: (directory === "." && !prefix.startsWith("./") ? item.name : join(directory, item.name)) + (item.isDirectory() ? "/" : ""), kind: item.isDirectory() ? "directory" : "file" }));
  } catch { return []; }
}

const powershellQuote = (value: string) => "'" + value.replaceAll("'", "''") + "'";
export function formatCompletions(choices: Completion[], shell: Shell): string {
  const clean = (value: string) => value.replace(/[\r\n\t\x00-\x1f\x7f]/g, " ");
  if (shell === "powershell") return JSON.stringify(choices.map(item => ({ ...item, completionText: powershellQuote(item.value) })));
  if (shell === "nushell") return JSON.stringify(choices.map(item => ({ value: item.value, description: item.description ?? "" })));
  const lines = choices.filter(item => !/[\r\n\x00]/.test(item.value)).map(item => {
    if (shell === "bash" || shell === "zsh") return `${clean(item.value)}\t${item.appendSpace ? "space" : "none"}`;
    return `${clean(item.value)}\t${clean(item.description ?? "")}`;
  }).join("\n");
  return lines ? lines + "\n" : "";
}

export async function runCompletion(args: string[]): Promise<string> {
  const parsed = parseCommand(args);
  if (parsed.name !== "completion") throw new CommandError("Expected completion command");
  if (parsed.path.length === 1) {
    const shell = parsed.positionals[0];
    if (!shell || !shells.includes(shell as Shell)) throw new CommandError(`Choose a shell: ${shells.join(", ")}`);
    return completionScript(shell as Shell);
  }
  const shell = parsed.flags.shell as Shell;
  const words = parsed.positionals.slice(1).map(word => parsed.flags["raw-words"] ? scan(word, true).words.join(" ") : word);
  const index = Number(parsed.flags.index);
  if (!/^\d+$/.test(String(parsed.flags.index)) || index > words.length) throw new CommandError("Invalid completion word index");
  const signal = AbortSignal.timeout(1_000);
  // Acquire credentials lazily: static completion never reads session state.
  let client: ReturnType<typeof createApiClient> | undefined;
  const api = new Proxy({} as FloatLabApi, {
    get: (_target, method) => async (...params: unknown[]) => {
      if (!client) {
        const current = await connection();
        if (!valid(current) || !current) return [];
        client = createApiClient({ baseUrl: current.url, token: current.token, signal });
      }
      const fn = client[method as keyof typeof client] as (...params: unknown[]) => Promise<unknown>;
      return fn(...params);
    },
  });
  const runtime = { interactive: false, cli: true, completeFiles, readCompose: async () => "", confirm: async () => false, editCompose: async () => undefined, present: async () => {} };
  const engine = new CommandEngine(api, runtime);
  const choices = await engine.completeTokens(words, index).catch(() => []);
  return formatCompletions(choices, shell);
}
