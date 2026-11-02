import { expect, test } from "bun:test";
import { chmod, mkdtemp, rm, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { completionFilename, shells } from "../src/completion-scripts";

const bins = { bash: "bash", zsh: "zsh", fish: "fish", powershell: "pwsh", nushell: "nu" };
for (const shell of shells) {
  const binary = Bun.which(bins[shell]);
  if (process.env.REQUIRE_COMPLETION_SHELLS && !binary) throw new Error(`Required completion shell missing: ${shell}`);
  (binary ? test : test.skip)(`${shell} loads the shipped script and resolves nested and quoted resource candidates`, async () => {
    const directory = await mkdtemp(join(tmpdir(), "floatlab-shell-"));
    const file = resolve(import.meta.dir, "../completions", completionFilename(shell));
    const server = Bun.serve({ port: 0, fetch(request) {
      expect(request.method).toBe("GET");
      const path = new URL(request.url).pathname;
      if (path === "/api/v1/nodes") return Response.json([{ id: "node", name: "lab node", status: "ready" }]);
      if (path === "/api/v1/stacks") return Response.json([{ id: "stack", name: "demo stack", state: "idle", primary_node: "node" }]);
      if (path === "/api/v1/stacks/stack/containers") return Response.json([{ id: "container", name: "web app", status: "running" }]);
      return Response.json([]);
    } });
    const quote = (value: string) => "'" + value.replaceAll("'", "'\\''") + "'";
    await writeFile(join(directory, "floatlab"), `#!/bin/sh\nexec ${process.env.FLOATLAB_TEST_BINARY ? quote(process.env.FLOATLAB_TEST_BINARY) : quote(process.execPath) + " " + quote(resolve(import.meta.dir, "../src/main.ts"))} "$@"\n`);
    await chmod(join(directory, "floatlab"), 0o755);
    const scripts = {
      bash: `source "$FLOATLAB_COMPLETION_FILE"
COMP_LINE='floatlab update host prepare'; COMP_POINT=23; COMP_WORDS=(floatlab update host prepare); COMP_CWORD=3; _floatlab_complete; printf '%s\\n' "\${COMPREPLY[@]}"
unset COMP_LINE; COMP_WORDS=(floatlab start de); COMP_CWORD=2; _floatlab_complete; printf '%s\\n' "\${COMPREPLY[@]}"
COMP_WORDS=(floatlab start '"demo stack"' we); COMP_CWORD=3; _floatlab_complete; printf '%s\\n' "\${COMPREPLY[@]}"
COMP_WORDS=(floatlab create demo --primary = la); COMP_CWORD=5; _floatlab_complete; printf '%s\\n' "\${COMPREPLY[@]}"`,
      zsh: `autoload -Uz compinit; compinit -D
source "$FLOATLAB_COMPLETION_FILE"
compadd() { while (( $# )); do if [[ "$1" == -- ]]; then shift; print -r -- "$@"; return; fi; shift; done }
PREFIX=pr; words=(floatlab update host prepare); CURRENT=4; _floatlab; unset PREFIX
words=(floatlab start de); CURRENT=3; _floatlab
words=(floatlab start '"demo stack"' we); CURRENT=4; _floatlab`,
      fish: `source "$FLOATLAB_COMPLETION_FILE"
complete -C 'floatlab update host pr'
complete -C 'floatlab start de'
complete -C 'floatlab start "demo stack" we'`,
      powershell: `. $env:FLOATLAB_COMPLETION_FILE
foreach ($line in @('floatlab update host pr', 'floatlab start de', 'floatlab start "demo stack" we')) {
 (TabExpansion2 $line $line.Length).CompletionMatches | ForEach-Object { $_.CompletionText }
}`,
      nushell: `source "${file}"
do $env.config.completions.external.completer [floatlab update host pr] | to json --raw | print
do $env.config.completions.external.completer [floatlab start de] | to json --raw | print
do $env.config.completions.external.completer [floatlab start 'demo stack' we] | to json --raw`,
    };
    try {
      const args = shell === "powershell" ? ["-NoLogo", "-NoProfile", "-NonInteractive", "-Command"] : shell === "fish" ? ["--no-config", "-c"] : shell === "nushell" ? ["--no-config-file", "-c"] : shell === "zsh" ? ["-f", "-c"] : ["--noprofile", "--norc", "-c"];
      const child = Bun.spawn([binary!, ...args, scripts[shell]], { env: { ...process.env, PATH: directory + ":" + process.env.PATH, FLOATLAB_COMPLETION_FILE: file, FLOATLAB_URL: server.url.origin, FLOATLAB_TOKEN: "fixture", XDG_CONFIG_HOME: directory }, stdout: "pipe", stderr: "pipe" });
      const [stdout, stderr, code] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited]);
      expect({ code, stderr }).toEqual({ code: 0, stderr: "" });
      const readable = stdout.replaceAll("\\ ", " ");
      expect(readable).toContain("prepare"); expect(readable).toContain("demo stack"); expect(readable).toContain("web app");
      if (shell === "bash") { expect(readable).toContain("lab node"); expect(readable).not.toContain("--primary="); }
    } finally { server.stop(true); await rm(directory, { recursive: true }); }
  });
}
