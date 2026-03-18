import { describe, expect, it } from "vitest";
import { parseSSEStream } from "../src/sse.js";

function toStream(text: string): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();
  return new ReadableStream({
    start(controller) {
      controller.enqueue(encoder.encode(text));
      controller.close();
    },
  });
}

describe("parseSSEStream", () => {
  it("parses a single event", async () => {
    const stream = toStream(
      "event: test\ndata: {\"key\": \"value\"}\n\n",
    );
    const events = [];
    for await (const event of parseSSEStream(stream)) {
      events.push(event);
    }
    expect(events).toHaveLength(1);
    expect(events[0].type).toBe("test");
    expect(events[0].data).toBe('{"key": "value"}');
  });

  it("parses multiple events", async () => {
    const stream = toStream(
      "event: first\ndata: one\n\nevent: second\ndata: two\n\n",
    );
    const events = [];
    for await (const event of parseSSEStream(stream)) {
      events.push(event);
    }
    expect(events).toHaveLength(2);
    expect(events[0].type).toBe("first");
    expect(events[0].data).toBe("one");
    expect(events[1].type).toBe("second");
    expect(events[1].data).toBe("two");
  });

  it("handles multi-line data", async () => {
    const stream = toStream(
      "event: multi\ndata: line1\ndata: line2\n\n",
    );
    const events = [];
    for await (const event of parseSSEStream(stream)) {
      events.push(event);
    }
    expect(events).toHaveLength(1);
    expect(events[0].data).toBe("line1\nline2");
  });

  it("captures event id", async () => {
    const stream = toStream(
      "event: test\nid: 42\ndata: hello\n\n",
    );
    const events = [];
    for await (const event of parseSSEStream(stream)) {
      events.push(event);
    }
    expect(events[0].id).toBe("42");
  });

  it("ignores comment lines", async () => {
    const stream = toStream(
      ": this is a comment\nevent: test\ndata: hello\n\n",
    );
    const events = [];
    for await (const event of parseSSEStream(stream)) {
      events.push(event);
    }
    expect(events).toHaveLength(1);
    expect(events[0].type).toBe("test");
  });

  it("skips events without type or data", async () => {
    const stream = toStream(
      "event: \ndata: \n\nevent: real\ndata: value\n\n",
    );
    const events = [];
    for await (const event of parseSSEStream(stream)) {
      events.push(event);
    }
    expect(events).toHaveLength(1);
    expect(events[0].type).toBe("real");
  });
});
