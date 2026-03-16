"use client";

import { useEffect, useState, useCallback } from "react";
import { listExecutions, type Execution } from "@/lib/api";
import { EXECUTION_LIST_POLL_INTERVAL } from "@/lib/constants";
import StatusBadge from "@/components/StatusBadge";

interface Props {
  statusFilter: string;
  refreshKey: number;
  selectedId: string | null;
  onSelect: (id: string) => void;
}

export default function ExecutionList({ statusFilter, refreshKey, selectedId, onSelect }: Props) {
  const [executions, setExecutions] = useState<Execution[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [cursor, setCursor] = useState<string | undefined>();
  const [hasMore, setHasMore] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);

  const buildParams = useCallback(
    (extra?: { cursor: string }) => {
      const params: { status?: string; cursor?: string } = {};
      if (statusFilter) params.status = statusFilter;
      if (extra?.cursor) params.cursor = extra.cursor;
      return params;
    },
    [statusFilter]
  );

  const load = useCallback(async () => {
    try {
      const data = await listExecutions(buildParams());
      setExecutions(data.executions);
      setHasMore(!!data.next_cursor);
      setCursor(data.next_cursor || undefined);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load executions");
      console.error("Failed to load executions:", e);
    } finally {
      setLoading(false);
    }
  }, [buildParams]);

  const loadMore = useCallback(async () => {
    if (!cursor || loadingMore) return;
    setLoadingMore(true);
    try {
      const data = await listExecutions(buildParams({ cursor }));
      setExecutions((prev) => [...prev, ...data.executions]);
      setHasMore(!!data.next_cursor);
      setCursor(data.next_cursor || undefined);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load more executions");
      console.error("Failed to load more executions:", e);
    } finally {
      setLoadingMore(false);
    }
  }, [buildParams, cursor, loadingMore]);

  useEffect(() => {
    setLoading(true);
    load();
    const timer = setInterval(load, EXECUTION_LIST_POLL_INTERVAL);
    return () => clearInterval(timer);
  }, [load, refreshKey]);

  if (loading) {
    return (
      <div className="flex-1 flex items-center justify-center p-8">
        <div className="flex items-center gap-2 text-gray-600">
          <div className="w-1 h-1 rounded-full bg-accent animate-pulse" />
          <span className="text-[10px] font-mono uppercase tracking-widest">Loading</span>
        </div>
      </div>
    );
  }

  if (error) {
    return (
      <div className="flex-1 flex items-center justify-center p-8">
        <div className="text-center space-y-3">
          <p className="text-[11px] text-phosphor-red font-mono bg-phosphor-red/5 border border-phosphor-red/15 px-3 py-2">
            {error}
          </p>
          <button
            onClick={() => { setLoading(true); load(); }}
            className="btn-secondary py-1 px-3 text-[10px]"
          >
            Retry
          </button>
        </div>
      </div>
    );
  }

  if (!executions.length) {
    return (
      <div className="flex-1 flex items-center justify-center p-8">
        <div className="text-[10px] font-mono text-gray-600 uppercase tracking-widest">
          No executions
        </div>
      </div>
    );
  }

  return (
    <div className="flex-1 overflow-y-auto">
      {executions.map((exec) => {
        const isSelected = selectedId === exec.id;
        return (
          <button
            key={exec.id}
            onClick={() => onSelect(exec.id)}
            className={`w-full text-left px-4 py-3 border-b border-border/40 transition-all duration-100 group ${
              isSelected
                ? "bg-accent/[0.06] border-l-2 border-l-accent"
                : "hover:bg-surface-2/40 border-l-2 border-l-transparent"
            }`}
          >
            <div className="flex items-start justify-between gap-2 mb-1.5">
              <code className={`text-[11px] font-mono truncate flex-1 ${
                isSelected ? "text-accent" : "text-gray-400 group-hover:text-gray-300"
              }`}>
                {exec.id}
              </code>
              <StatusBadge status={exec.status} />
            </div>
            <div className="flex items-center gap-2 text-[10px] text-gray-600 font-mono">
              <span className="flex items-center gap-1">
                <svg className="w-2.5 h-2.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                  <path strokeLinecap="round" strokeLinejoin="round" d="M15.75 6a3.75 3.75 0 11-7.5 0 3.75 3.75 0 017.5 0zM4.501 20.118a7.5 7.5 0 0114.998 0A17.933 17.933 0 0112 21.75c-2.676 0-5.216-.584-7.499-1.632z" />
                </svg>
                {exec.agent_id}
              </span>
              <span className="text-gray-700">/</span>
              <span>{new Date(exec.created_at).toLocaleTimeString("en-US", { hour12: false })}</span>
            </div>
          </button>
        );
      })}
      {hasMore && (
        <div className="px-4 py-3 border-b border-border/40">
          <button
            onClick={loadMore}
            disabled={loadingMore}
            className="btn-secondary w-full py-1.5 text-[10px] disabled:opacity-40 disabled:cursor-not-allowed"
          >
            {loadingMore ? "Loading..." : "Load More"}
          </button>
        </div>
      )}
    </div>
  );
}
