import { describe, expect, it } from "vitest";
import {
  RebunoError,
  NetworkError,
  APIError,
  ValidationError,
  UnauthorizedError,
  NotFoundError,
  ConflictError,
  PolicyError,
  ToolError,
} from "../src/errors.js";

describe("Error hierarchy", () => {
  it("RebunoError is base error", () => {
    const err = new RebunoError("test");
    expect(err).toBeInstanceOf(Error);
    expect(err).toBeInstanceOf(RebunoError);
    expect(err.message).toBe("test");
    expect(err.details).toEqual({});
  });

  it("NetworkError extends RebunoError", () => {
    const err = new NetworkError("connection refused");
    expect(err).toBeInstanceOf(RebunoError);
    expect(err.name).toBe("NetworkError");
  });

  it("APIError includes code and statusCode", () => {
    const err = new APIError("bad request", "INVALID", 400);
    expect(err).toBeInstanceOf(RebunoError);
    expect(err.code).toBe("INVALID");
    expect(err.statusCode).toBe(400);
    expect(err.toString()).toContain("[INVALID]");
    expect(err.toString()).toContain("HTTP 400");
  });

  it("ValidationError extends APIError", () => {
    const err = new ValidationError("invalid input", "VALIDATION", 400);
    expect(err).toBeInstanceOf(APIError);
    expect(err.statusCode).toBe(400);
  });

  it("UnauthorizedError extends APIError", () => {
    const err = new UnauthorizedError("bad token", "UNAUTHORIZED", 401);
    expect(err).toBeInstanceOf(APIError);
    expect(err.statusCode).toBe(401);
  });

  it("NotFoundError extends APIError", () => {
    const err = new NotFoundError("not found", "NOT_FOUND", 404);
    expect(err).toBeInstanceOf(APIError);
  });

  it("ConflictError extends APIError", () => {
    const err = new ConflictError("conflict", "CONFLICT", 409);
    expect(err).toBeInstanceOf(APIError);
  });

  it("PolicyError defaults to 403", () => {
    const err = new PolicyError("denied", "rule-1");
    expect(err).toBeInstanceOf(APIError);
    expect(err.statusCode).toBe(403);
    expect(err.code).toBe("policy_denied");
    expect(err.ruleId).toBe("rule-1");
  });

  it("ToolError includes tool metadata", () => {
    const err = new ToolError("failed", "web.search", "step-1", true);
    expect(err).toBeInstanceOf(RebunoError);
    expect(err.toolId).toBe("web.search");
    expect(err.stepId).toBe("step-1");
    expect(err.retryable).toBe(true);
  });
});
