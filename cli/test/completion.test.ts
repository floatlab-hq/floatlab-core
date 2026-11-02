import { expect, test } from "bun:test";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { completeFiles, formatCompletions, runCompletion } from "../src/completion";
import { completionFilename, completionScript, shells } from "../src/completion-scripts";

const main = join(import.meta.dir, "../src/main.ts");
async function run(args: string[], env: Record<string, string | undefined>) {
  const child = Bun.spawn([...(process.env.FLOATLAB_TEST_BINARY ? [process.env.FLOATLAB_TEST_BINARY] : [process.execPath, main]), ...args], { env: { ...process.env, ...env }, stdin: "ignore", stdout: "pipe", stderr: "pipe" });
  const [stdout, stderr, code] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited]);
  return { stdout, stderr, code };
}

test("shell scripts match the generated artifacts and ship without authentication", async () => {
  for (const shell of shells) {
    const result = await run(["completion", shell], { FLOATLAB_URL: "", FLOATLAB_TOKEN: "" });
    expect(result.code).toBe(0); expect(result.stderr).toBe("");
    expect(result.stdout).toBe(completionScript(shell));
    expect(result.stdout).toBe(await readFile(join(import.meta.dir, "../completions", completionFilename(shell)), "utf8"));
  }
  expect((await run(["help"], { FLOATLAB_URL: "" })).stdout).toContain("snapshot create");
  expect((await run(["update", "host", "--help"], { FLOATLAB_URL: "" })).stdout).toContain("prepare");
  expect((await run(["completion", "bad"], {})).code).toBe(2);
});

test("resolver remains read-only, offline-capable, and bounds unavailable resources", async () => {
  const directory = await mkdtemp(join(tmpdir(), "floatlab-completion-"));
  let gets = 0, writes = 0;
  const server = Bun.serve({ port: 0, fetch(request) {
    if (request.method !== "GET") { writes++; return new Response("forbidden", { status: 405 }); }
    gets++;
    if (new URL(request.url).pathname === "/api/v1/stacks") return Response.json([{ id: "one", name: "demo stack", state: "idle" }]);
    return new Response("unauthorized", { status: 401 });
  } });
  const env = { FLOATLAB_URL: server.url.origin, FLOATLAB_TOKEN: "token", XDG_CONFIG_HOME: directory };
  try {
    const staticResult = await run(["completion", "resolve", "--shell", "powershell", "--index", "0", "--", "upd"], env);
    expect(JSON.parse(staticResult.stdout)[0].value).toBe("update"); expect(gets).toBe(0);
    const dynamic = await run(["completion", "resolve", "--shell", "nushell", "--index", "1", "--", "start", "de"], env);
    expect(JSON.parse(dynamic.stdout)[0].value).toBe("demo stack"); expect(gets).toBe(1); expect(writes).toBe(0);
    const unauthorized = await run(["completion", "resolve", "--shell", "powershell", "--index", "1", "--", "create", "--primary="], env);
    expect(unauthorized.stderr).toBe(""); expect(unauthorized.code).toBe(0);
    const offline = await run(["completion", "resolve", "--shell", "nushell", "--index", "1", "--", "start", "de"], { ...env, FLOATLAB_TOKEN: "" });
    expect(offline.stdout).toBe("[]"); expect(offline.stderr).toBe("");
    expect(await Bun.file(join(directory, "floatlab/config.json")).exists()).toBe(false);
  } finally { server.stop(true); await rm(directory, { recursive: true }); }
});

test("file candidates and shell encoders preserve spaces and literal shell characters", async () => {
  const directory = await mkdtemp(join(tmpdir(), "floatlab-files-"));
  try {
    await writeFile(join(directory, "compose file.yaml"), ""); await mkdir(join(directory, "sub directory"));
    const files = await completeFiles(directory + "/");
    expect(files.map(item => item.value)).toEqual([join(directory, "compose file.yaml"), join(directory, "sub directory/")]);
    expect(files.at(-1)?.kind).toBe("directory");
    expect(await completeFiles(join(directory, "missing/"))).toEqual([]);
    const value = "demo '; $HOME";
    expect(JSON.parse(formatCompletions([{ value }], "powershell"))[0].completionText).toBe("'demo ''; $HOME'");
    expect(formatCompletions([{ value: "a\nb" }], "bash")).toBe("");
    expect(formatCompletions([{ value: "update", appendSpace: true }], "zsh")).toBe("update\tspace\n");
  } finally { await rm(directory, { recursive: true }); }
  await expect(runCompletion(["completion", "resolve", "--shell", "bash", "--index", "-1", "--", ""])).rejects.toThrow("index");
});

test("resolver cancels a slow API within its request budget", async () => {
  const directory = await mkdtemp(join(tmpdir(), "floatlab-timeout-"));
  const server = Bun.serve({ port: 0, async fetch() { await Bun.sleep(2_000); return Response.json([]); } });
  try {
    const start = Date.now();
    const result = await run(["completion", "resolve", "--shell", "nushell", "--index", "1", "--", "start", ""], { FLOATLAB_URL: server.url.origin, FLOATLAB_TOKEN: "token", XDG_CONFIG_HOME: directory });
    expect(result).toEqual({ stdout: "[]", stderr: "", code: 0 });
    expect(Date.now() - start).toBeLessThan(1_800);
  } finally { server.stop(true); await rm(directory, { recursive: true }); }
});
