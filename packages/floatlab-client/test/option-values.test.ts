import { expect, test } from "bun:test";
import { parseCommand } from "../src/commands";

test("value-taking options preserve empty and option-like string values", () => {
  expect(parseCommand(["api", "logs", "searchLogs", "--query-query="]).flags["query-query"]).toBe("");
  expect(parseCommand(["api", "logs", "searchLogs", "--query-query=--literal"]).flags["query-query"]).toBe("--literal");
  expect(() => parseCommand(["api", "logs", "searchLogs", "--query-query"])).toThrow("needs a value");
});
