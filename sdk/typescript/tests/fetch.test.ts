import { describe, expect, it } from "vitest";
import { camelKeys, snakeKeys } from "../src/internal/fetch.js";

describe("snakeKeys", () => {
  it("converts camelCase keys to snake_case", () => {
    expect(snakeKeys({ agentId: "a", executionId: "b" })).toEqual({
      agent_id: "a",
      execution_id: "b",
    });
  });

  it("handles nested objects", () => {
    expect(snakeKeys({ outer: { innerKey: "v" } })).toEqual({
      outer: { inner_key: "v" },
    });
  });

  it("handles arrays", () => {
    expect(snakeKeys([{ myKey: 1 }, { myKey: 2 }])).toEqual([
      { my_key: 1 },
      { my_key: 2 },
    ]);
  });

  it("passes through primitives", () => {
    expect(snakeKeys("hello")).toBe("hello");
    expect(snakeKeys(42)).toBe(42);
    expect(snakeKeys(null)).toBe(null);
    expect(snakeKeys(undefined)).toBe(undefined);
  });
});

describe("camelKeys", () => {
  it("converts snake_case keys to camelCase", () => {
    expect(camelKeys({ agent_id: "a", execution_id: "b" })).toEqual({
      agentId: "a",
      executionId: "b",
    });
  });

  it("handles nested objects", () => {
    expect(camelKeys({ outer: { inner_key: "v" } })).toEqual({
      outer: { innerKey: "v" },
    });
  });

  it("handles arrays", () => {
    expect(camelKeys([{ my_key: 1 }])).toEqual([{ myKey: 1 }]);
  });

  it("roundtrips with snakeKeys", () => {
    const original = { agentId: "a", executionId: "b", labels: { myKey: "v" } };
    expect(camelKeys(snakeKeys(original))).toEqual(original);
  });
});
