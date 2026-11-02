import { expect, test } from "bun:test";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

test("direct HTTP commands allow public requests and preserve bodies, binary output, and streams", async () => {
  const directory = await mkdtemp(join(tmpdir(), "lab-http-"));
  const body = JSON.stringify({ username: "admin", password: "literal $HOME; a b" });
  const bytes = new Uint8Array([0, 255, 13, 10, 42]);
  const archive = new Uint8Array(200_000).map((_, index) => index % 256);
  const server = Bun.serve({ port: 0, async fetch(request) {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/token") {
      expect(request.headers.get("authorization")).toBeNull();
      expect(await request.text()).toBe(body);
      return Response.json({ access_token: "fixture", expires_in: 60 });
    }
    if (path === "/api/v1/health") {
      expect(request.headers.get("authorization")).toBeNull();
      return new Response(bytes, { headers: { "content-type": "application/octet-stream" } });
    }
    if (path === "/api/v1/events") return new Response("data: first\n\ndata: second\n\n", { headers: { "content-type": "text/event-stream" } });
    if (path === "/api/v1/stacks/stack/start") {
      expect(request.headers.get("content-type")).toBe("application/x-tar");
      expect(new Uint8Array(await request.arrayBuffer())).toEqual(archive);
      return new Response(null, { status: 202 });
    }
    return new Response("unexpected", { status: 404 });
  } });
  const run = async (args: string[], token = "", url = server.url.origin) => {
    const child = Bun.spawn([...(process.env.FLOATLAB_TEST_BINARY ? [process.env.FLOATLAB_TEST_BINARY] : [process.execPath, join(import.meta.dir, "../src/main.ts")]), ...args], {
      env: { ...process.env, XDG_CONFIG_HOME: directory, FLOATLAB_URL: url, FLOATLAB_TOKEN: token }, stdin: "ignore", stdout: "pipe", stderr: "pipe",
    });
    const [stdout, stderr, code] = await Promise.all([new Response(child.stdout).arrayBuffer(), new Response(child.stderr).text(), child.exited]);
    return { stdout: new Uint8Array(stdout), stderr, code };
  };
  try {
    const auth = await run(["api", "auth", "createAuthToken", "--body", body]);
    expect(auth.code).toBe(0); expect(auth.stderr).toBe(""); expect(JSON.parse(new TextDecoder().decode(auth.stdout)).access_token).toBe("fixture");
    const jsonFile = join(directory, "credentials.json"); await writeFile(jsonFile, body);
    const authFile = await run(["api", "auth", "createAuthToken", "--body-file", jsonFile]);
    expect(authFile.code).toBe(0); expect(authFile.stderr).toBe(""); expect(JSON.parse(new TextDecoder().decode(authFile.stdout)).access_token).toBe("fixture");
    const archiveFile = join(directory, "stack.tar"); await writeFile(archiveFile, archive);
    const upload = await run(["api", "stacks", "startStack", "--path-id", "stack", "--content-type", "application/x-tar", "--body-file", archiveFile], "fixture");
    expect(upload.code).toBe(0); expect(upload.stderr).toBe("");
    expect(await run(["api", "health", "getHealth"])).toEqual({ stdout: bytes, stderr: "", code: 0 });
    const stream = await run(["api", "openapi", "streamEvents"], "fixture");
    expect(stream.code).toBe(0); expect(stream.stderr).toBe(""); expect(new TextDecoder().decode(stream.stdout)).toBe("data: first\n\ndata: second\n\n");
    const missing = await run(["api", "health", "getHealth"], "", "");
    expect(missing.code).toBe(1); expect(missing.stderr).toContain("Set FLOATLAB_URL");
  } finally { server.stop(true); await rm(directory, { recursive: true }); }
});
