import { confirm, input, password, select } from "@inquirer/prompts";
import { createPrompt, isDownKey, isEnterKey, isTabKey, isUpKey, useKeypress, useState } from "@inquirer/core";
import { chmod, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { tokenize, type CommandResult, type CommandRuntime, type Completion } from "@floatlab/client";

export const format = (result: CommandResult): string => {
  if (result.kind === "text") return result.text;
  if (result.kind === "error") return result.message;
  if (result.kind === "operation") return result.text ?? `${result.action}: ${result.state ?? "accepted"}`;
  if (result.kind === "logs") return result.lines.map((line) => `${line.ts} ${line.stream} ${line.msg}`).join("\n");
  const widths = result.columns.map((column, index) => Math.max(column.length, ...result.rows.map((row) => (row[index] ?? "").length)));
  const row = (values: string[]) => values.map((value, index) => value.padEnd(widths[index]!)).join("  ");
  return [row(result.columns), row(widths.map((width) => "-".repeat(width))), ...result.rows.map(row)].join("\n");
};

export const replaceLastToken = (input: string, value: string) => input.replace(/\S*$/, value);

async function page(content: string): Promise<boolean> {
  try {
    const process = Bun.spawn(["less", "-R"], { stdin: "pipe", stdout: "inherit", stderr: "inherit" });
    await process.stdin.write(content); await process.stdin.end(); return (await process.exited) === 0;
  } catch { return false; }
}

export function composeEditor() {
  let temporary: { directory: string; file: string } | undefined;
  return {
    async edit(initial: string): Promise<string | undefined> {
      temporary ??= { directory: await mkdtemp(join(tmpdir(), "floatlab-")), file: "" }; temporary.file ||= join(temporary.directory, "compose.yaml");
      await writeFile(temporary.file, initial, { mode: 0o600 }); await chmod(temporary.file, 0o600);
      const editor = tokenize(process.env.VISUAL || process.env.EDITOR || "vi");
      const child = Bun.spawn([...editor, temporary.file], { stdin: "inherit", stdout: "inherit", stderr: "inherit" });
      return await child.exited === 0 ? readFile(temporary.file, "utf8") : undefined;
    },
    async cleanup() { if (temporary) await rm(temporary.directory, { recursive: true, force: true }); temporary = undefined; },
  };
}

export function terminalRuntime(interactive: boolean, signal?: AbortSignal): CommandRuntime {
  const editor = composeEditor();
  return {
    interactive,
    signal,
    confirm: (message) => confirm({ message, default: false }),
    ask: (message) => input({ message }),
    choose: (message, choices) => select({ message, choices: choices.map((choice) => ({ name: `${choice.value}${choice.description ? ` — ${choice.description}` : ""}`, value: choice.value })) }),
    editCompose: editor.edit,
    cleanupCompose: editor.cleanup,
    async present(result) {
      const output = format(result);
      if (interactive && (result.kind === "logs" || result.kind === "table")) {
        if (!await page(output)) console.warn("less is unavailable; printing output.");
        else return;
      }
      (result.kind === "error" ? console.error : console.log)(output);
    },
  };
}

type CommandInputConfig = { complete(value: string): Promise<Completion[]>; history: string[] };
const commandPrompt = createPrompt<string, CommandInputConfig>((config, done) => {
  const [value, setValue] = useState(""); const [choices, setChoices] = useState<Completion[]>([]); const [selected, setSelected] = useState(0); const [historyIndex, setHistoryIndex] = useState(config.history.length);
  useKeypress(async (key, rl) => {
    const next = rl.line;
    const replaceLine = (line: string) => { rl.clearLine(0); rl.write(line); setValue(line); };
    if (isEnterKey(key)) { done(value.trim()); return; }
    if (isUpKey(key)) { if (choices.length) setSelected((selected + choices.length - 1) % choices.length); else { const index = Math.max(0, historyIndex - 1); setHistoryIndex(index); replaceLine(config.history[index] ?? ""); } return; }
    if (isDownKey(key)) { if (choices.length) setSelected((selected + 1) % choices.length); else { const index = Math.min(config.history.length, historyIndex + 1); setHistoryIndex(index); replaceLine(config.history[index] ?? ""); } return; }
    if (isTabKey(key) && choices[selected]) { replaceLine(replaceLastToken(value, choices[selected]!.value)); return; }
    setValue(next); const matches = await config.complete(next); setChoices(matches); setSelected(0);
  });
  const suggestion = choices.slice(0, 6).map((choice, index) => `${index === selected ? ">" : " "} ${choice.value}${choice.description ? ` — ${choice.description}` : ""}`).join("\n");
  return [`? floatlab ${value}`, suggestion || undefined];
});

export async function commandInput(complete: (value: string) => Promise<Completion[]>, history: string[], context?: Parameters<typeof commandPrompt>[1]): Promise<string | undefined> {
  const value = await commandPrompt({ complete, history }, context); return value || undefined;
}

export async function credentials(): Promise<{ username: string; password: string }> {
  return { username: await input({ message: "Username" }), password: await password({ message: "Password", mask: "*" }) };
}
