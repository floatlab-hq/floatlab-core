import Ajv from "ajv";
import { v4 as idempotencyKey } from "uuid";
import { CommandError } from "./commands";
import type { HttpOperation, HttpParameter } from "./command-schema";
import type { CommandResult, CommandRuntime, HttpApi } from "./types";

// OpenAPI formats are annotations; structural constraints are validated here and the API validates semantic formats.
const validator = new Ajv({ strict: false, validateFormats: false, allErrors: true });
function validate(schema: Record<string, unknown> | undefined, value: unknown, label: string) {
  if (!schema) return;
  const check = validator.compile(schema);
  if (!check(value)) throw new CommandError(`${label}: ${validator.errorsText(check.errors)}`);
}

function input(raw: string, schema: Record<string, unknown> | undefined, label: string): unknown {
  const type = schema?.type;
  let value: unknown = raw;
  if (type === "integer" || type === "number" || type === "boolean" || type === "array" || type === "object") {
    try { value = JSON.parse(raw); } catch { throw new CommandError(`${label} must be valid JSON (${type})`); }
  }
  validate(schema, value, label);
  return value;
}

const encode = (value: unknown) => encodeURIComponent(String(value)).replace(/[!'()*]/g, char => `%${char.charCodeAt(0).toString(16).toUpperCase()}`);
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);

function pairs(parameter: HttpParameter, value: unknown): [string, string][] {
  const { name, style = "form", explode = style === "form" } = parameter;
  if (Array.isArray(value)) {
    if (explode) return value.map(item => [name, String(item)]);
    return [[name, value.join(style === "spaceDelimited" ? " " : style === "pipeDelimited" ? "|" : ",")]];
  }
  if (object(value)) {
    if (style === "deepObject") return Object.entries(value).map(([key, item]) => [`${name}[${key}]`, String(item)]);
    if (explode) return Object.entries(value).map(([key, item]) => [key, String(item)]);
    return [[name, Object.entries(value).flat().join(",")]];
  }
  return [[name, String(value)]];
}

function simple(value: unknown, explode = false, escaped = false): string {
  const atom = escaped ? encode : String;
  if (Array.isArray(value)) return value.map(atom).join(",");
  if (object(value)) return Object.entries(value).map(([key, item]) => `${atom(key)}${explode ? "=" : ","}${atom(item)}`).join(",");
  return atom(value);
}

function pathValue(parameter: HttpParameter, value: unknown): string {
  const { name, style = "simple", explode = false } = parameter;
  if (style === "label") {
    if (Array.isArray(value) && explode) return "." + value.map(encode).join(".");
    if (object(value) && explode) return "." + Object.entries(value).map(([key, item]) => `${encode(key)}=${encode(item)}`).join(".");
    return "." + simple(value, explode, true);
  }
  if (style === "matrix") {
    if (Array.isArray(value) && explode) return value.map(item => `;${encode(name)}=${encode(item)}`).join("");
    if (object(value) && explode) return Object.entries(value).map(([key, item]) => `;${encode(key)}=${encode(item)}`).join("");
    return `;${encode(name)}=${simple(value, false, true)}`;
  }
  return simple(value, explode, true);
}

export async function runHttpCommand(operation: HttpOperation, flags: Record<string, string | boolean>, api: HttpApi, runtime: CommandRuntime): Promise<CommandResult> {
  let path = operation.path;
  const query = new URLSearchParams(), headers = new Headers(), cookies: string[] = [];
  for (const parameter of operation.parameters) {
    let raw = flags[parameter.option.replace(/^--/, "")];
    if (raw === undefined && parameter.in === "header" && parameter.name.toLowerCase() === "idempotency-key") raw = idempotencyKey();
    if (raw === undefined) {
      if (parameter.required || parameter.in === "path") throw new CommandError(`${parameter.option} is required`);
      continue;
    }
    const value = input(String(raw), parameter.schema, parameter.option);
    if (parameter.in === "path") path = path.replaceAll(`{${parameter.name}}`, pathValue(parameter, value));
    else if (parameter.in === "query") for (const [key, item] of pairs(parameter, value)) query.append(key, item);
    else if (parameter.in === "header") headers.set(parameter.name, simple(value, parameter.explode));
    else cookies.push(...pairs(parameter, value).map(([key, item]) => `${encode(key)}=${encode(item)}`));
  }
  if (cookies.length) headers.set("cookie", cookies.join("; "));
  let body: string | ArrayBuffer | undefined;
  if (operation.requestBody) {
    if (flags.body !== undefined && flags["body-file"] !== undefined) throw new CommandError("Use either --body or --body-file");
    let file: Uint8Array | undefined;
    if (flags["body-file"] !== undefined) {
      if (!runtime.readBody) throw new CommandError("--body-file is only available in the CLI");
      file = await runtime.readBody(String(flags["body-file"]));
    }
    const label = file ? "--body-file" : "--body";
    const media = typeof flags["content-type"] === "string" ? flags["content-type"] : Object.keys(operation.requestBody.content)[0]!;
    const definition = operation.requestBody.content[media];
    if (!definition) throw new CommandError(`Unsupported content type: ${media}`);
    if (flags.body === undefined && file === undefined) {
      if (operation.requestBody.required) throw new CommandError("--body or --body-file is required");
    } else {
      if (definition.schema?.format === "binary") {
        try { body = file ? new Uint8Array(file).buffer : Uint8Array.from(atob(String(flags.body)), char => char.charCodeAt(0)).buffer; }
        catch { throw new CommandError("--body must be base64 for binary media"); }
      } else {
        body = file ? new TextDecoder().decode(file) : String(flags.body);
        if (media === "application/json" || media.endsWith("+json")) {
          let value: unknown;
          try { value = JSON.parse(body); } catch { throw new CommandError(`${label} must be valid JSON`); }
          validate(definition.schema, value, label);
        } else validate(definition.schema, body, label);
      }
      headers.set("content-type", media);
    }
  }
  const accept = typeof flags.accept === "string" ? flags.accept : operation.accept ?? "application/json";
  headers.set("accept", accept);
  if (!["GET", "HEAD", "OPTIONS"].includes(operation.method.toUpperCase()) && runtime.interactive && !await runtime.confirm(`${operation.method.toUpperCase()} ${path}?`)) return {kind: "text", text: "Request cancelled."};
  const suffix = query.toString();
  let streamed = "";
  const result = await api.request(path + (suffix ? `?${suffix}` : ""), {
    method: operation.method, headers, body, signal: runtime.signal,
    onChunk: async chunk => {
      if (runtime.writeChunk) await runtime.writeChunk(chunk);
      else { streamed += chunk; await runtime.present({kind: "text", text: streamed}); }
    },
  });
  if (result instanceof Uint8Array) return {kind: "binary", data: result, contentType: accept};
  return {kind: "text", text: streamed || (result === undefined ? "" : typeof result === "string" ? result : JSON.stringify(result, null, 2))};
}
