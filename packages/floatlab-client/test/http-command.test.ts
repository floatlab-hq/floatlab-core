import { expect, test } from "bun:test";
import { createApiClient, ApiError } from "../src/api";
import { CommandEngine } from "../src/engine";
import { commandSpec, metadata, type CommandDefinition, type HttpOperation } from "../src/commands";
import { runHttpCommand } from "../src/http-command";
import type { CommandRuntime } from "../src/types";

const runtime = (changes: Partial<CommandRuntime> = {}): CommandRuntime => ({interactive: false, confirm: async () => true, editCompose: async () => undefined, present: async () => {}, ...changes});
const sample = (schema: Record<string, any> = {}): any => {
  if (schema.enum) return schema.enum[0];
  if (schema.default !== undefined) return schema.default;
  if (schema.example !== undefined) return schema.example;
  if (schema.allOf) return Object.assign({}, ...schema.allOf.map(sample));
  if (schema.oneOf || schema.anyOf) return sample((schema.oneOf ?? schema.anyOf)[0]);
  if (schema.type === "object" || schema.properties) {
    const value = Object.fromEntries((schema.required ?? []).map((key: string) => [key, sample(schema.properties?.[key])]));
    if (schema.minProperties && !Object.keys(value).length) value.sample = sample(schema.additionalProperties);
    return value;
  }
  if (schema.type === "array") return Array.from({length: schema.minItems ?? 0}, () => sample(schema.items));
  if (schema.type === "boolean") return true;
  if (schema.type === "integer" || schema.type === "number") return schema.minimum ?? 1;
  return schema.pattern === "^[0-9]{6}$" ? "123456" : "sample";
};

test("every generated direct operation dispatches its method, all inputs and body to HTTP", async () => {
  let count = 0;
  const visit = async (command: CommandDefinition, path: string[]) => {
    const operation = metadata(command).http;
    if (operation) {
      count++;
      const argv = [...path];
      for (const parameter of operation.parameters) {
        const value = sample(parameter.schema);
        argv.push(parameter.option, typeof value === "string" ? value : JSON.stringify(value));
      }
      const media = operation.requestBody && Object.keys(operation.requestBody.content)[0];
      if (media) {
        const schema = operation.requestBody!.content[media]!.schema;
        const value = sample(schema);
        argv.push("--body", media.includes("json") ? JSON.stringify(value) : schema?.format === "binary" ? btoa("archive") : String(value));
      }
      const requests: {url: string; init?: RequestInit}[] = [];
      const api = createApiClient({baseUrl: "https://floatlab.test", fetch: async (url, init) => { requests.push({url: String(url), init}); return new Response(null, {status: 204}); }});
      const result = await new CommandEngine(api, runtime()).dispatch(argv);
      expect(result.result, `${path.join(" ")}: ${JSON.stringify(result.result)}`).not.toHaveProperty("kind", "error");
      expect(requests, path.join(" ")).toHaveLength(1);
      expect(requests[0]!.init!.method).toBe(operation.method);
      expect(requests[0]!.url).not.toContain("{");
      const headers = new Headers(requests[0]!.init!.headers);
      for (const parameter of operation.parameters.filter(item => item.in === "header" && item.required)) expect(headers.has(parameter.name)).toBe(true);
      const url = new URL(requests[0]!.url);
      for (const parameter of operation.parameters) {
        const value = sample(parameter.schema);
        if (parameter.in === "query" && !Array.isArray(value) && (value === null || typeof value !== "object")) expect(url.searchParams.get(parameter.name), parameter.option).toBe(String(value));
        if (parameter.in === "header") expect(headers.get(parameter.name), parameter.option).toBe(String(value));
      }
      if (media) expect(headers.get("content-type")).toBe(media);
    }
    for (const child of command.commands ?? []) await visit(child, [...path, child.name]);
  };
  await visit(commandSpec.command, []);
  expect(count).toBeGreaterThan(100);
});

test("direct HTTP serializes paths, query values, headers and cookies and preserves auth overrides", async () => {
  let captured: {url: string; init?: RequestInit} | undefined;
  const api = createApiClient({baseUrl: "https://host", token: "default", fetch: async (url, init) => { captured = {url: String(url), init}; return new Response("answer", {headers: {"content-type": "application/yaml"}}); }});
  const operation: HttpOperation = {method: "GET", path: "/items/{id}", accept: "application/yaml", parameters: [
    {name: "id", in: "path", option: "--id", schema: {type: "string"}},
    {name: "names", in: "query", option: "--names", schema: {type: "array", items: {type: "string"}}, style: "form", explode: true},
    {name: "filter", in: "query", option: "--filter", schema: {type: "object"}, style: "deepObject"},
    {name: "enabled", in: "query", option: "--enabled", schema: {type: "boolean"}},
    {name: "authorization", in: "header", option: "--auth"}, {name: "session", in: "cookie", option: "--session"},
  ]};
  expect(await runHttpCommand(operation, {id: "node/a?b", names: '["one","two x"]', filter: '{"x":"a&b"}', enabled: "false", auth: "custom", session: "a;b"}, api, runtime())).toEqual({kind: "text", text: "answer"});
  expect(captured!.url).toBe("https://host/api/v1/items/node%2Fa%3Fb?names=one&names=two+x&filter%5Bx%5D=a%26b&enabled=false");
  const headers = new Headers(captured!.init!.headers);
  expect(headers.get("authorization")).toBe("custom");
  expect(headers.get("cookie")).toBe("session=a%3Bb");
  expect(headers.get("accept")).toBe("application/yaml");
});

test("validation and declined interactive mutations never reach the network", async () => {
  let calls = 0;
  const api = {request: async () => { calls++; }};
  const operation: HttpOperation = {method: "POST", path: "/items", parameters: [], requestBody: {required: true, content: {"application/json": {schema: {type: "object", required: ["name"], properties: {name: {type: "string", minLength: 1}}}}}}};
  for (const body of ["{", "null", "{}", '{"name":""}']) await expect(runHttpCommand(operation, {body}, api, runtime())).rejects.toThrow();
  expect(await runHttpCommand(operation, {body: '{"name":"x"}'}, api, runtime({interactive: true, confirm: async () => false}))).toEqual({kind: "text", text: "Request cancelled."});
  expect(calls).toBe(0);
});

test("idempotency defaults only apply to declared parameters and binary requests decode base64", async () => {
  const requests: RequestInit[] = [];
  const api = createApiClient({baseUrl: "https://host", fetch: async (_, init) => { requests.push(init!); return new Response(null, {status: 204}); }});
  const operation: HttpOperation = {method: "POST", path: "/items", parameters: []};
  await runHttpCommand(operation, {}, api, runtime());
  operation.parameters.push({name: "Idempotency-Key", in: "header", option: "--key", required: true, schema: {type: "string"}});
  await runHttpCommand(operation, {}, api, runtime());
  operation.requestBody = {content: {"application/x-tar": {schema: {type: "string", format: "binary"}}}};
  await runHttpCommand(operation, {body: btoa("archive"), key: "stable"}, api, runtime());
  expect(new Headers(requests[0]!.headers).has("idempotency-key")).toBe(false);
  expect(new Headers(requests[1]!.headers).get("idempotency-key")).toMatch(/^[\da-f-]{36}$/);
  expect(new Headers(requests[2]!.headers).get("idempotency-key")).toBe("stable");
  expect(new TextDecoder().decode(requests[2]!.body as ArrayBuffer)).toBe("archive");
  await expect(runHttpCommand(operation, {body: "invalid base64!"}, api, runtime())).rejects.toThrow("base64");
  expect(requests).toHaveLength(3);
});

test("alternate body media and response negotiation preserve raw YAML", async () => {
  let init: RequestInit | undefined;
  const api = createApiClient({baseUrl: "https://host", fetch: async (_, request) => { init = request; return new Response("saved: true", {headers: {"content-type": "application/yaml; charset=utf-8"}}); }});
  const operation: HttpOperation = {method: "PUT", path: "/config", parameters: [], requestBody: {content: {"application/json": {schema: {type: "object"}}, "application/yaml": {schema: {type: "string"}}}}};
  expect(await runHttpCommand(operation, {body: "name: web\n", "content-type": "application/yaml", accept: "application/yaml"}, api, runtime())).toEqual({kind: "text", text: "saved: true"});
  expect(init!.body).toBe("name: web\n");
  expect(new Headers(init!.headers).get("content-type")).toBe("application/yaml");
  expect(new Headers(init!.headers).get("accept")).toBe("application/yaml");
});

test("body files preserve raw binary and validate JSON before any network call", async () => {
  const requests: RequestInit[] = [];
  const api = createApiClient({baseUrl: "https://host", fetch: async (_, init) => { requests.push(init!); return new Response(null, {status: 204}); }});
  const operation: HttpOperation = {method: "POST", path: "/items", parameters: [], requestBody: {required: true, content: {"application/json": {schema: {type: "object", required: ["name"]}}, "application/x-tar": {schema: {type: "string", format: "binary"}}}}};
  const files = runtime({readBody: async path => path === "archive.tar" ? new Uint8Array([0, 255, 128, 10]) : new TextEncoder().encode(path === "valid.json" ? '{"name":"web"}' : "{")});
  await runHttpCommand(operation, {"body-file": "valid.json"}, api, files);
  expect(requests[0]!.body).toBe('{"name":"web"}');
  await runHttpCommand(operation, {"body-file": "archive.tar", "content-type": "application/x-tar"}, api, files);
  expect(new Uint8Array(requests[1]!.body as ArrayBuffer)).toEqual(new Uint8Array([0, 255, 128, 10]));
  await expect(runHttpCommand(operation, {"body-file": "invalid.json"}, api, files)).rejects.toThrow("--body-file must be valid JSON");
  await expect(runHttpCommand(operation, {body: "{}", "body-file": "valid.json"}, api, files)).rejects.toThrow("either");
  await expect(runHttpCommand(operation, {"body-file": "valid.json"}, api, runtime())).rejects.toThrow("only available in the CLI");
  await expect(runHttpCommand(operation, {}, api, files)).rejects.toThrow("required");
  expect(requests).toHaveLength(2);
});

test("transport handles JSON, empty, binary, text errors and forwards abort signals", async () => {
  const controller = new AbortController();
  const replies = [new Response('{"ok":true}', {headers: {"content-type": "application/json"}}), new Response(null, {status: 204}), new Response(new Uint8Array([0, 255]), {headers: {"content-type": "application/octet-stream"}}), new Response("unavailable", {status: 503})];
  const api = createApiClient({baseUrl: "https://host", fetch: async (_, init) => { expect(init?.signal).toBe(controller.signal); return replies.shift()!; }});
  expect(await api.request("/items", {signal: controller.signal})).toEqual({ok: true});
  expect(await api.request("/items", {signal: controller.signal})).toBeUndefined();
  expect(await api.request("/items", {signal: controller.signal})).toEqual(new Uint8Array([0, 255]));
  await expect(api.request("/items", {signal: controller.signal})).rejects.toEqual(new ApiError("unavailable", 503));
});

test("SSE is delivered incrementally and respects the command abort signal", async () => {
  const chunks: string[] = [];
  const controller = new AbortController();
  const api = createApiClient({baseUrl: "https://host", fetch: async (_, init) => {
    expect(init?.signal).toBe(controller.signal);
    return new Response(new ReadableStream({start(stream) { stream.enqueue(new TextEncoder().encode("data: one\n\n")); stream.enqueue(new TextEncoder().encode("data: two\n\n")); stream.close(); }}), {headers: {"content-type": "text/event-stream"}});
  }});
  const result = await runHttpCommand({method: "GET", path: "/events", parameters: [], accept: "text/event-stream"}, {}, api, runtime({signal: controller.signal, writeChunk: async chunk => { chunks.push(chunk); }}));
  expect(chunks).toEqual(["data: one\n\n", "data: two\n\n"]);
  expect(result).toEqual({kind: "text", text: ""});
});
