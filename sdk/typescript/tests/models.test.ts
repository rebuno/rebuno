import { describe, expect, it } from "vitest";
import { ExecutionStatus, StepStatus } from "../src/models.js";

describe("ExecutionStatus", () => {
  it("has correct values", () => {
    expect(ExecutionStatus.Pending).toBe("pending");
    expect(ExecutionStatus.Running).toBe("running");
    expect(ExecutionStatus.Blocked).toBe("blocked");
    expect(ExecutionStatus.Completed).toBe("completed");
    expect(ExecutionStatus.Failed).toBe("failed");
    expect(ExecutionStatus.Cancelled).toBe("cancelled");
  });
});

describe("StepStatus", () => {
  it("has correct values", () => {
    expect(StepStatus.Pending).toBe("pending");
    expect(StepStatus.Dispatched).toBe("dispatched");
    expect(StepStatus.Running).toBe("running");
    expect(StepStatus.Succeeded).toBe("succeeded");
    expect(StepStatus.Failed).toBe("failed");
    expect(StepStatus.TimedOut).toBe("timed_out");
    expect(StepStatus.Cancelled).toBe("cancelled");
  });
});
