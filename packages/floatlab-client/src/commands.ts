import definition from "./commands.generated.json";
import type { Completion } from "./types";

export class CommandError extends Error { constructor(message: string, readonly code = 2) { super(message); } }
import { metadata, names, type ArgumentDefinition, type OptionDefinition, type CommandDefinition, type OpenCLI } from "./command-schema";
export * from "./command-schema";
export const commandSpec = definition as OpenCLI;
export type ParsedCommand = { name: string; positionals: string[]; flags: Record<string, string | boolean>; path: string[]; command: CommandDefinition };

export function scan(input: string, tolerant = false): { words: string[]; starts: number[]; ends: number[] } {
  const words: string[] = [], starts: number[] = [], ends: number[] = [];
  let token = "", quote = "", escaped = false, start = -1;
  const push = (end: number) => { if (start >= 0) { words.push(token); starts.push(start); ends.push(end); } token = ""; start = -1; };
  for (let i = 0; i < input.length; i++) {
    const char = input[i]!;
    if (escaped) { token += char; escaped = false; continue; }
    if (char === "\\") { if (start < 0) start = i; escaped = true; continue; }
    if (quote) { if (char === quote) quote = ""; else token += char; continue; }
    if (char === "'" || char === '"') { if (start < 0) start = i; quote = char; continue; }
    if (/\s/.test(char)) { push(i); continue; }
    if ("|;&<>`$".includes(char)) throw new CommandError(`Unsupported shell operator: ${char}`);
    if (start < 0) start = i;
    token += char;
  }
  if (!tolerant && (escaped || quote)) throw new CommandError("Unterminated escape or quote");
  push(input.length);
  if (tolerant && !quote && !escaped && /\s$/.test(input)) { words.push(""); starts.push(input.length); ends.push(input.length); }
  return { words, starts, ends };
}
export const tokenize = (input: string) => scan(input).words;
export const quoteArgument = (value: string) => /^[\w./:=@,+%-]+$/.test(value) ? value : '"' + value.replaceAll("\\", "\\\\").replaceAll('"', '\\"') + '"';
export function applyCompletion(input: string, choice: Completion): string {
  const tokens = scan(input, true);
  const start = choice.replaceStart ?? tokens.starts.at(-1) ?? input.length;
  const end = choice.replaceEnd ?? input.length;
  return input.slice(0, start) + quoteArgument(choice.value) + (choice.appendSpace ? " " : "") + input.slice(end);
}

export function usage(command: CommandDefinition, path: string[]): string {
  return [...path, ...(command.commands?.length ? ["<command>"] : []), ...(command.arguments ?? []).map((arg, index, args) => {
    const label = arg.name + (arg.arity && arg.arity.maximum === undefined ? "..." : "");
    return (metadata(command).passthrough && index === args.length - 1 ? "-- " : "") + (arg.required ? `<${label}>` : `[${label}]`);
  }), ...(command.options ?? []).map(option => {
    const label = option.name + (option.arguments?.length ? ` <${option.arguments[0]!.name}>` : "");
    return option.required ? label : `[${label}]`;
  })].join(" ");
}

export function commandHelp(path: string[] = [], root = commandSpec.command): string {
  let command = root;
  for (const word of path) {
    const child = command.commands?.find(item => names(item).includes(word));
    if (!child) throw new CommandError(`Unknown command: ${path.join(" ")}`);
    command = child;
  }
  const lines = [`Usage: ${usage(command, [root.name, ...path])}`];
  const visit = (item: CommandDefinition, prefix: string[]) => {
    if (item.hidden) return;
    if (!item.commands?.length || item.arguments?.length) lines.push(`${usage(item, prefix)}${item.aliases?.length ? ` (${item.aliases.join(", ")})` : ""}`);
    for (const child of item.commands ?? []) visit(child, [...prefix, child.name]);
  };
  if (!path.length || command.commands?.length) for (const child of command.commands ?? []) visit(child, [...path, child.name]);
  return lines.join("\n");
}

export function parseCommand(words: string[], interactive = false, root = commandSpec.command): ParsedCommand {
  let command = root;
  const path: string[] = [], positionals: string[] = [], flags: Record<string, string | boolean> = {};
  let inherited: OptionDefinition[] = [];
  let passthrough = false;
  for (let index = 0; index < words.length; index++) {
    const word = words[index]!;
    if (word === "--" && metadata(command).passthrough) {
      if (!metadata(command).utility && positionals.length !== (command.arguments?.length ?? 1) - 1)
        throw new CommandError(`Usage: ${usage(command, path)}`);
      passthrough = true; flags.command = true; positionals.push(...words.slice(index + 1)); break;
    }
    if (word.startsWith("-")) {
      const [key] = word.split("="); const equal = word.indexOf("=");
      const option = [...(command.options ?? []), ...inherited].find(item => names(item).includes(key!));
      if (!option) throw new CommandError(`Unknown flag: ${word}`);
      if (option.name === "--help") return { name: "help", positionals: path, flags: {}, path: ["help"], command };
      if (option.arguments?.length) {
        const value = equal < 0 ? words[++index] : word.slice(equal + 1);
        if (!value || value.startsWith("--")) throw new CommandError(`${key} needs a value`);
        validateValue(option.arguments[0]!, value);
        flags[option.name.slice(2)] = value;
      } else {
        if (equal >= 0) throw new CommandError(`${key} does not take a value`);
        flags[option.name.slice(2)] = true;
      }
      continue;
    }
    const child = !positionals.length && command.commands?.find(item => names(item).includes(path.length ? word : word.toLowerCase()));
    if (child) {
      inherited = [...inherited, ...(command.options ?? []).filter(option => option.recursive)];
      command = child; path.push(child.name);
    } else positionals.push(word);
  }
  const fallback = metadata(command).defaultCommand;
  if (fallback && !positionals.length) { command = command.commands!.find(child => child.name === fallback)!; path.push(fallback); }
  if (!path.length) throw new CommandError(words.length ? `Unknown command: ${words[0]}. Try help.` : "Enter a command");
  const fail = () => { throw new CommandError(`Usage: ${usage(command, path)}`); };
  if (command.commands?.length && !command.arguments?.length) fail();
  let offset = 0;
  for (const arg of command.arguments ?? []) {
    const min = arg.required && !(interactive && metadata(arg).prompt) ? arg.arity?.minimum ?? 1 : 0;
    const max = arg.arity ? arg.arity.maximum ?? Infinity : 1;
    const count = Math.max(0, Math.min(positionals.length - offset, max));
    if (count < min) fail();
    for (const value of positionals.slice(offset, offset + count)) validateValue(arg, value);
    offset += count;
  }
  if (offset !== positionals.length) fail();
  if (metadata(command).passthrough && path[0] !== "completion" && !passthrough) fail();
  for (const option of command.options ?? []) if (option.required && flags[option.name.slice(2)] === undefined) fail();
  return { name: path[0]!, positionals: [...path.slice(1), ...positionals], flags, path, command };
}

function validateValue(arg: ArgumentDefinition, value: string) {
  if (arg.acceptedValues && !arg.acceptedValues.includes(value)) throw new CommandError(`${arg.name} must be ${arg.acceptedValues.join(", ")}`);
}

export type CompletionContext = { command: CommandDefinition; path: string[]; argument?: ArgumentDefinition; values: Record<string, string>; choices: Completion[]; prefix: string; inline: string };
export function completionContext(words: string[], index: number, available: (item: ArgumentDefinition) => boolean): CompletionContext | undefined {
  let command = commandSpec.command;
  const path: string[] = [], values: Record<string, string> = {};
  let position = 0, pending: ArgumentDefinition | undefined;
  let inherited: OptionDefinition[] = [];
  for (const word of words.slice(0, index)) {
    if (pending) { values[pending.name] = word; pending = undefined; continue; }
    if (word === "--" && metadata(command).passthrough) return;
    if (word.startsWith("-")) {
      const equal = word.indexOf("=");
      const option = [...inherited, ...(command.options ?? [])].find(item => names(item).includes(equal < 0 ? word : word.slice(0, equal)));
      if (!option) return;
      if (equal < 0) pending = option.arguments?.[0];
      continue;
    }
    const child = !position && command.commands?.find(item => names(item).includes(path.length ? word : word.toLowerCase()));
    if (child) { inherited = [...inherited, ...(command.options ?? []).filter(option => option.recursive)]; command = child; path.push(child.name); }
    else {
      const arg = command.arguments?.[position];
      if (!arg) return;
      values[arg.name] = word;
      if (!arg.arity || arg.arity.maximum !== undefined) position++;
    }
  }
  const current = words[index] ?? "";
  const options = [...inherited, ...(command.options ?? [])].filter(item => !item.hidden && available(item));
  const equal = current.indexOf("=");
  const inlineOption = equal > 0 ? options.find(item => names(item).includes(current.slice(0, equal))) : undefined;
  const inline = inlineOption ? current.slice(0, equal + 1) : "";
  const argument = pending ?? inlineOption?.arguments?.[0] ?? command.arguments?.[position];
  const choices: Completion[] = [];
  if (!pending && !inlineOption) {
    if (current.startsWith("-")) choices.push(...options.flatMap(item => names(item).map(value => ({ value, description: item.description, appendSpace: true }))));
    else if (!position) choices.push(...(command.commands ?? []).filter(item => !item.hidden && available(item)).flatMap(item => names(item).map(value => ({ value, description: item.description, ...(metadata(item).appendSpace ? { appendSpace: true } : {}) }))));
  }
  if ((argument && !current.startsWith("-")) || pending || inlineOption) choices.push(...(argument?.acceptedValues ?? metadata(argument ?? command).suggestions ?? []).map(value => ({ value })));
  return { command, path, argument: current.startsWith("-") && !inlineOption && !pending ? undefined : argument, values, choices, prefix: inline ? current.slice(equal + 1) : current, inline };
}
