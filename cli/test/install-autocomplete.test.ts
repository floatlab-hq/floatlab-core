import { expect, test } from "bun:test";
import { chmod, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { installAutocomplete } from "../src/install-autocomplete";
import { completionScript } from "../src/completion-scripts";

test("Bash and Fish install offline, preserve configuration and update idempotently", async () => {
  const directory = await mkdtemp(join(tmpdir(), "lab-autocomplete-"));
  const env = { ...process.env, HOME: directory, XDG_CONFIG_HOME: join(directory, "config"), SHELL: "/bin/bash" };
  try {
    await writeFile(join(directory, ".bashrc"), "# user settings without a trailing newline");
    expect(await installAutocomplete(undefined, env)).toContain("Installed bash");
    const bash = await readFile(join(directory, ".bashrc"), "utf8");
    expect(bash).toStartWith("# user settings without a trailing newline\n");
    expect(bash).toContain(completionScript("bash"));
    await installAutocomplete("bash", env);
    expect(await readFile(join(directory, ".bashrc"), "utf8")).toBe(bash);
    await writeFile(join(directory, ".bashrc"), "# before\n# >>> lab autocomplete >>>\nstale script\n# <<< lab autocomplete <<<\n# after\n");
    await installAutocomplete("bash", env);
    const updated = await readFile(join(directory, ".bashrc"), "utf8");
    expect(updated).toStartWith("# before\n"); expect(updated).toEndWith("\n# after\n"); expect(updated).not.toContain("stale script");
    const target = join(env.XDG_CONFIG_HOME, "fish/completions/lab.fish");
    await mkdir(join(env.XDG_CONFIG_HOME, "fish/completions"), { recursive: true });
    await writeFile(target, "# custom fish settings\n");
    await installAutocomplete("fish", env);
    const fish = await readFile(target, "utf8");
    expect(fish).toStartWith("# custom fish settings\n"); expect(fish).toContain(completionScript("fish"));
    await installAutocomplete("fish", env);
    expect(await readFile(target, "utf8")).toBe(fish);
    await expect(installAutocomplete("invalid", env)).rejects.toThrow("Choose a shell");
    await expect(installAutocomplete(undefined, { ...env, SHELL: "", PSModulePath: "" })).rejects.toThrow("Choose a shell");
    for (const contents of ["# >>> lab autocomplete >>>\n", "# <<< lab autocomplete <<<\n", "# >>> lab autocomplete >>>\n# <<< lab autocomplete <<<\n# <<< lab autocomplete <<<"]) {
      await writeFile(join(directory, ".bashrc"), contents);
      await expect(installAutocomplete("bash", env)).rejects.toThrow("repair its markers");
      expect(await readFile(join(directory, ".bashrc"), "utf8")).toBe(contents);
    }
  } finally { await rm(directory, { recursive: true }); }
});

test("PowerShell and Nushell query their actual startup paths without loading profiles", async () => {
  const directory = await mkdtemp(join(tmpdir(), "lab-profile-"));
  try {
    for (const binary of ["pwsh", "nu"]) {
      const file = join(directory, binary);
      await writeFile(file, '#!/bin/sh\nprintf "%s\\n" "$@" > "$LAB_TEST_ARGS"\nprintf "%s" "$LAB_TEST_PROFILE"\n');
      await chmod(file, 0o755);
    }
    for (const [shell, selected, flag] of [["ps1", "powershell", "-NoProfile"], ["nu", "nushell", "--no-config-file"]] as const) {
      const profile = join(directory, `${shell} custom`, "profile");
      const env = { ...process.env, PATH: directory, LAB_TEST_PROFILE: profile, LAB_TEST_ARGS: join(directory, "args") };
      await installAutocomplete(shell, env);
      const result = await readFile(profile, "utf8");
      expect(result).toContain(completionScript(selected));
      expect(await readFile(env.LAB_TEST_ARGS, "utf8")).toContain(flag);
      await installAutocomplete(selected, env);
      expect(await readFile(profile, "utf8")).toBe(result);
      await expect(installAutocomplete(shell, { ...env, LAB_TEST_PROFILE: "relative" })).rejects.toThrow("absolute startup file path");
      await expect(installAutocomplete(shell, { ...env, PATH: "" })).rejects.toThrow("ensure it is on PATH");
    }
  } finally { await rm(directory, { recursive: true }); }
});

test("the executable installs bundled completion without authentication or external scripts", async () => {
  const directory = await mkdtemp(join(tmpdir(), "lab-install-cli-"));
  try {
    const command = process.env.FLOATLAB_TEST_BINARY ? [process.env.FLOATLAB_TEST_BINARY] : [process.execPath, resolve(import.meta.dir, "../src/main.ts")];
    const child = Bun.spawn([...command, "install-autocomplete", "bash"], {
      cwd: directory, env: { ...process.env, HOME: directory, FLOATLAB_URL: "", FLOATLAB_TOKEN: "" }, stdout: "pipe", stderr: "pipe",
    });
    expect(await child.exited).toBe(0);
    expect(await new Response(child.stderr).text()).toBe("");
    expect(await new Response(child.stdout).text()).toContain("Restart your shell");
    expect(await readFile(join(directory, ".bashrc"), "utf8")).toContain(completionScript("bash"));
    const bash = Bun.spawn(["bash", "--noprofile", "--norc", "-c", 'source "$HOME/.bashrc"; complete -p lab'], { env: { ...process.env, HOME: directory }, stdout: "pipe", stderr: "pipe" });
    expect(await bash.exited).toBe(0);
    expect(await new Response(bash.stdout).text()).toContain("-F _lab_complete lab");
  } finally { await rm(directory, { recursive: true }); }
});
