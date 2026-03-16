"use client";

import { useEffect, useState, useRef, useCallback } from "react";
import { getEvents, cancelExecution, type Event } from "@/lib/api";
import { EVENT_STREAM_POLL_INTERVAL } from "@/lib/constants";

interface Props {
  executionId: string;
  onSendSignal: () => void;
  onClose: () => void;
}

const categoryStyle: Record<string, { border: string; bg: string; dot: string; label: string }> = {
  execution: {
    border: "border-l-accent",
    bg: "bg-accent/[0.03]",
    dot: "bg-accent",
    label: "text-accent",
  },
  step: {
    border: "border-l-phosphor-amber",
    bg: "bg-phosphor-amber/[0.03]",
    dot: "bg-phosphor-amber",
    label: "text-phosphor-amber",
  },
  "intent.accepted": {
    border: "border-l-phosphor-green",
    bg: "bg-phosphor-green/[0.03]",
    dot: "bg-phosphor-green",
    label: "text-phosphor-green",
  },
  "intent.denied": {
    border: "border-l-phosphor-red",
    bg: "bg-phosphor-red/[0.03]",
    dot: "bg-phosphor-red",
    label: "text-phosphor-red",
  },
  signal: {
    border: "border-l-phosphor-green",
    bg: "bg-phosphor-green/[0.03]",
    dot: "bg-phosphor-green",
    label: "text-phosphor-green",
  },
};

function getCategory(type: string): string {
  if (type === "intent.accepted" || type === "intent.denied") return type;
  if (type.startsWith("step.")) return "step";
  if (type.startsWith("signal.")) return "signal";
  return "execution";
}

export default function EventStream({ executionId, onSendSignal, onClose }: Props) {
  const [events, setEvents] = useState<Event[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [autoScroll, setAutoScroll] = useState(true);
  const [expandedPayloads, setExpandedPayloads] = useState<Set<number>>(new Set());
  const [latestSeq, setLatestSeq] = useState(0);
  const [confirmingCancel, setConfirmingCancel] = useState(false);
  const [cancelError, setCancelError] = useState<string | null>(null);
  const containerRef = useRef<HTMLDivElement>(null);

  const loadEvents = useCallback(async () => {
    try {
      const data = await getEvents(executionId, latestSeq);
      if (data.events.length > 0) {
        setEvents((prev) => {
          if (latestSeq === 0) return data.events;
          const existingSeqs = new Set(prev.map((e) => e.sequence));
          const newEvents = data.events.filter((e) => !existingSeqs.has(e.sequence));
          return newEvents.length > 0 ? [...prev, ...newEvents] : prev;
        });
        setLatestSeq(data.latest_sequence);
      }
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load events");
      console.error("Failed to load events:", e);
    } finally {
      setLoading(false);
    }
  }, [executionId, latestSeq]);

  useEffect(() => {
    setEvents([]);
    setLoading(true);
    setExpandedPayloads(new Set());
    setLatestSeq(0);
    setError(null);
    setCancelError(null);
    setConfirmingCancel(false);
  }, [executionId]);

  useEffect(() => {
    loadEvents();
    const timer = setInterval(loadEvents, EVENT_STREAM_POLL_INTERVAL);
    return () => clearInterval(timer);
  }, [loadEvents]);

  useEffect(() => {
    if (autoScroll && containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
    }
  }, [events, autoScroll]);

  function handleScroll() {
    if (!containerRef.current) return;
    const { scrollTop, scrollHeight, clientHeight } = containerRef.current;
    setAutoScroll(scrollHeight - scrollTop - clientHeight < 50);
  }

  function togglePayload(seq: number) {
    setExpandedPayloads((prev) => {
      const next = new Set(prev);
      if (next.has(seq)) next.delete(seq);
      else next.add(seq);
      return next;
    });
  }

  async function handleCancel() {
    setCancelError(null);
    try {
      await cancelExecution(executionId);
      setConfirmingCancel(false);
      loadEvents();
    } catch (err) {
      setCancelError(err instanceof Error ? err.message : "Failed to cancel");
    }
  }

  function scrollToBottom() {
    setAutoScroll(true);
    if (containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
    }
  }

  function renderEventContent(): React.ReactNode {
    if (loading) {
      return (
        <div className="flex-1 flex items-center justify-center">
          <div className="flex items-center gap-2 text-gray-600">
            <div className="w-1 h-1 rounded-full bg-accent animate-pulse" />
            <span className="text-[10px] font-mono uppercase tracking-widest">Loading Events</span>
          </div>
        </div>
      );
    }

    if (error) {
      return (
        <div className="flex-1 flex items-center justify-center">
          <div className="text-center space-y-3">
            <p className="text-[11px] text-phosphor-red font-mono bg-phosphor-red/5 border border-phosphor-red/15 px-3 py-2">
              {error}
            </p>
            <button
              onClick={() => { setLoading(true); loadEvents(); }}
              className="btn-secondary py-1 px-3 text-[10px]"
            >
              Retry
            </button>
          </div>
        </div>
      );
    }

    if (events.length === 0) {
      return (
        <div className="flex-1 flex items-center justify-center">
          <div className="text-center">
            <div className="text-[10px] font-mono text-gray-600 uppercase tracking-widest">
              Awaiting events
            </div>
            <div className="mt-2 flex justify-center gap-1">
              <div className="w-0.5 h-0.5 rounded-full bg-gray-700 animate-pulse" />
              <div className="w-0.5 h-0.5 rounded-full bg-gray-700 animate-pulse [animation-delay:300ms]" />
              <div className="w-0.5 h-0.5 rounded-full bg-gray-700 animate-pulse [animation-delay:600ms]" />
            </div>
          </div>
        </div>
      );
    }

    return (
      <div className="flex-1 relative min-h-0">
        <div
          ref={containerRef}
          onScroll={handleScroll}
          className="absolute inset-0 overflow-y-auto p-3 space-y-px"
        >
          {events.map((evt, i) => {
            const style = categoryStyle[getCategory(evt.type)];
            const hasPayload = evt.payload && Object.keys(evt.payload).length > 0;
            const isExpanded = expandedPayloads.has(evt.sequence);

            return (
              <div
                key={evt.sequence}
                className={`border-l-2 px-4 py-2.5 animate-slide-up ${style.border} ${style.bg} hover:bg-surface-2/20 transition-colors`}
                style={{ animationDelay: `${Math.min(i * 15, 300)}ms` }}
              >
                <div className="flex items-center gap-3 flex-wrap">
                  <span className="text-xs text-gray-700 font-mono w-8 text-right shrink-0 tabular-nums">
                    {String(evt.sequence).padStart(3, "0")}
                  </span>

                  <span className="text-xs text-gray-600 font-mono tabular-nums">
                    {new Date(evt.timestamp).toLocaleTimeString("en-US", {
                      hour12: false,
                      hour: "2-digit",
                      minute: "2-digit",
                      second: "2-digit",
                      fractionalSecondDigits: 3,
                    })}
                  </span>

                  <span className="flex items-center gap-1.5">
                    <span className={`w-1.5 h-1.5 rounded-full ${style.dot}`} />
                    <span className={`text-sm font-mono font-medium ${style.label}`}>
                      {evt.type}
                    </span>
                  </span>

                  {hasPayload && (
                    <button
                      onClick={() => togglePayload(evt.sequence)}
                      className="text-xs text-gray-600 hover:text-gray-400 transition-colors ml-auto font-mono uppercase tracking-wider"
                      aria-label={isExpanded ? "Collapse payload" : "Expand payload"}
                    >
                      {isExpanded ? "[-]" : "[+]"}
                    </button>
                  )}
                </div>

                {hasPayload && isExpanded && (
                  <pre className="mt-2 text-xs text-phosphor-green/70 font-mono bg-surface-0/60 p-3 border border-border/30 whitespace-pre-wrap break-all overflow-hidden">
                    {JSON.stringify(evt.step_id ? { step_id: evt.step_id, ...evt.payload } : evt.payload, null, 2)}
                  </pre>
                )}
              </div>
            );
          })}
        </div>

        {!autoScroll && (
          <button
            onClick={scrollToBottom}
            aria-label="Scroll to bottom"
            className="absolute bottom-4 right-4 btn-secondary shadow-xl flex items-center gap-1.5 border-accent/20"
          >
            <svg className="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
              <path strokeLinecap="round" strokeLinejoin="round" d="M19.5 13.5L12 21m0 0l-7.5-7.5M12 21V3" />
            </svg>
            <span className="font-mono text-[10px] uppercase tracking-wider">Bottom</span>
          </button>
        )}
      </div>
    );
  }

  return (
    <div className="flex-1 flex flex-col min-h-0">
      <div className="h-11 px-4 flex items-center justify-between border-b border-border bg-surface-1/50 shrink-0">
        <div className="flex items-center gap-3">
          <button onClick={onClose} className="btn-ghost p-1" title="Close" aria-label="Close event stream">
            <svg className="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
              <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
            </svg>
          </button>
          <div className="h-3 w-px bg-border" />
          <code className="text-[11px] text-gray-500 font-mono">{executionId}</code>
          <span className="text-[10px] text-gray-600 font-mono tabular-nums">
            {events.length} evt{events.length !== 1 ? "s" : ""}
          </span>
        </div>

        <div className="flex items-center gap-2">
          <button onClick={onSendSignal} className="btn-secondary py-1 px-3">
            <span className="flex items-center gap-1.5">
              <svg className="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                <path strokeLinecap="round" strokeLinejoin="round" d="M3.75 13.5l10.5-11.25L12 10.5h8.25L9.75 21.75 12 13.5H3.75z" />
              </svg>
              Signal
            </span>
          </button>
          {confirmingCancel ? (
            <div className="flex items-center gap-1.5">
              <span className="text-[10px] text-phosphor-red font-mono">Cancel?</span>
              <button
                onClick={handleCancel}
                className="btn-danger py-1 px-2 text-[10px]"
              >
                Yes
              </button>
              <button
                onClick={() => { setConfirmingCancel(false); setCancelError(null); }}
                className="btn-secondary py-1 px-2 text-[10px]"
              >
                No
              </button>
            </div>
          ) : (
            <button onClick={() => setConfirmingCancel(true)} className="btn-danger py-1 px-3">
              Abort
            </button>
          )}
        </div>
      </div>

      {cancelError && (
        <div className="px-4 py-2 bg-phosphor-red/5 border-b border-phosphor-red/15">
          <p className="text-[11px] text-phosphor-red font-mono flex items-center justify-between">
            <span>{cancelError}</span>
            <button
              onClick={() => setCancelError(null)}
              className="text-phosphor-red/60 hover:text-phosphor-red ml-2"
            >
              Dismiss
            </button>
          </p>
        </div>
      )}

      {renderEventContent()}
    </div>
  );
}
