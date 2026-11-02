import { appendFile, mkdir, readFile, writeFile } from "node:fs/promises";
import { basename, dirname, isAbsolute, join } from "node:path";
import { homedir } from "node:os";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { CommandError } from "@floatlab/client";
import { completionScript } from "./completion-scripts";

const aliases = { bash: "bash", fish: "fish", ps1: "powershell", powershell: "powershell", pwsh: "powershell", nu: "nushell", nushell: "nushell" } as const;
const start = "# >>> lab autocomplete >>>", end = "# <<< lab autocomplete <<<";

export async function installAutocomplete(shell?: string, env: NodeJS.ProcessEnv = process.env): Promise<string> {
  const name = shell ?? basename(env.SHELL ?? (env.PSModulePath ? "pwsh" : ""));
  const selected = Object.hasOwn(aliases, name) ? aliases[name as keyof typeof aliases] : undefined;
  if (!selected) throw new CommandError("Choose a shell: lab install-autocomplete [bash|fish|ps1|nu]");
  const home = env.HOME || env.USERPROFILE || homedir();
  let target: string;
  if (selected === "fish") target = join(env.XDG_CONFIG_HOME || join(home, ".config"), "fish", "completions", "lab.fish");
  else if (selected === "bash") target = join(home, ".bashrc");
  else {
    const binary = selected === "powershell" ? "pwsh" : "nu";
    const args = selected === "powershell" ? ["-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "$PROFILE.CurrentUserAllHosts"] : ["--no-config-file", "-c", "$nu.config-path"];
    try { target = (await promisify(execFile)(binary, args, { env, timeout: 5_000 })).stdout.trim(); }
    catch { throw new CommandError(`Could not locate ${binary}'s startup file. Install ${binary} and ensure it is on PATH.`); }
    if (!isAbsolute(target)) throw new CommandError(`${binary} did not return an absolute startup file path`);
  }
  const script = completionScript(selected);
  await mkdir(dirname(target), { recursive: true });
  const previous = await readFile(target, "utf8").catch((error: NodeJS.ErrnoException) => {
    if (error.code !== "ENOENT") throw error;
    return "";
  });
  const block = `${start}\n${script}${end}`;
  const first = previous.indexOf(start), last = previous.indexOf(end);
  if (first < 0 && last < 0) await appendFile(target, `${previous && !previous.endsWith("\n") ? "\n" : ""}\n${block}\n`);
  else {
    if (first < 0 || last < first || previous.indexOf(start, first + start.length) >= 0 || previous.indexOf(end, last + end.length) >= 0)
      throw new CommandError(`Incomplete or duplicate lab autocomplete block in ${target}; repair its markers before reinstalling`);
    const updated = previous.slice(0, first) + block + previous.slice(last + end.length);
    if (updated !== previous) await writeFile(target, updated);
  }
  return `Installed ${name} autocomplete in ${target}. Restart your shell to activate it.`;
}
