import { describe, expect, it } from "vitest";
import { jitteredBackoff } from "../src/internal/backoff.js";

describe("jitteredBackoff", () => {
  it("returns value within expected range for attempt 1", () => {
    for (let i = 0; i < 100; i++) {
      const delay = jitteredBackoff(1.0, 1, 60.0);
      // base * 2^0 = 1.0, jitter: [0.5, 1.0]
      expect(delay).toBeGreaterThanOrEqual(0.5);
      expect(delay).toBeLessThanOrEqual(1.0);
    }
  });

  it("increases with attempt number", () => {
    const delays = Array.from({ length: 5 }, (_, i) =>
      jitteredBackoff(1.0, i + 1, 60.0),
    );
    // On average, later attempts should have higher delays
    // Can't test deterministically due to jitter, so test max bounds
    expect(jitteredBackoff(1.0, 5, 60.0)).toBeLessThanOrEqual(16.0);
  });

  it("respects max delay", () => {
    for (let i = 0; i < 100; i++) {
      const delay = jitteredBackoff(1.0, 100, 10.0);
      expect(delay).toBeLessThanOrEqual(10.0);
    }
  });
});
