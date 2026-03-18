export interface SSEEvent {
  type: string;
  data: string;
  id: string;
}

class SSEAccumulator {
  private eventType = "";
  private eventId = "";
  private dataLines: string[] = [];

  feed(rawLine: string): SSEEvent | null {
    const line = rawLine.replace(/[\r\n]+$/, "");

    if (line.startsWith("event:")) {
      this.eventType = line.slice(6).trim();
    } else if (line.startsWith("data:")) {
      const value = line.slice(5);
      this.dataLines.push(value.startsWith(" ") ? value.slice(1) : value);
    } else if (line.startsWith("id:")) {
      this.eventId = line.slice(3).trim();
    } else if (line.startsWith("retry:") || line.startsWith(":")) {
      // Ignored per SSE spec
    } else if (line === "") {
      return this.flush();
    }
    return null;
  }

  flush(): SSEEvent | null {
    const hasEvent = this.eventType && this.dataLines.length > 0;
    const event: SSEEvent | null = hasEvent
      ? { type: this.eventType, data: this.dataLines.join("\n"), id: this.eventId }
      : null;
    this.eventType = "";
    this.eventId = "";
    this.dataLines = [];
    return event;
  }
}

export async function* parseSSEStream(
  body: ReadableStream<Uint8Array>,
): AsyncGenerator<SSEEvent> {
  const reader = body
    .pipeThrough(new TextDecoderStream() as unknown as TransformStream<Uint8Array, string>)
    .getReader();

  const acc = new SSEAccumulator();
  let buffer = "";

  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;

      buffer += value;
      const lines = buffer.split("\n");
      buffer = lines.pop()!;

      for (const line of lines) {
        const event = acc.feed(line);
        if (event) yield event;
      }
    }

    if (buffer) {
      const event = acc.feed(buffer);
      if (event) yield event;
    }

    const trailing = acc.flush();
    if (trailing) yield trailing;
  } finally {
    reader.releaseLock();
  }
}
