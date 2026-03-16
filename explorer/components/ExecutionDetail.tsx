"use client";

import { useEffect, useState, useCallback } from "react";
import { getExecution, type ExecutionDetail as ExecutionDetailType } from "@/lib/api";
import { EXECUTION_LIST_POLL_INTERVAL } from "@/lib/constants";
import StatusBadge from "@/components/StatusBadge";

interface Props {
  executionId: string;
}

function JsonDetails({ label, value, colorClass }: { label: string; value: unknown; colorClass: string }): React.ReactNode {
  return (
    <details className="group">
      <summary className="text-[10px] text-gray-600 font-mono uppercase tracking-wider cursor-pointer hover:text-gray-400 transition-colors">
        {label}
      </summary>
      <pre className={`mt-1 text-xs ${colorClass} font-mono bg-surface-0/60 p-2 border border-border/30 whitespace-pre-wrap break-all overflow-hidden max-h-40 overflow-y-auto`}>
        {JSON.stringify(value, null, 2)}
      </pre>
    </details>
  );
}

export default function ExecutionDetail({ executionId }: Props) {
  const [detail, setDetail] = useState<ExecutionDetailType | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const data = await getExecution(executionId);
      setDetail(data);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to load execution detail");
      console.error("Failed to load execution detail:", e);
    } finally {
      setLoading(false);
    }
  }, [executionId]);

  useEffect(() => {
    setLoading(true);
    setDetail(null);
    setError(null);
    load();
    const timer = setInterval(load, EXECUTION_LIST_POLL_INTERVAL);
    return () => clearInterval(timer);
  }, [load]);

  if (loading) {
    return (
      <div className="px-4 py-3 border-b border-border bg-surface-1/30">
        <div className="flex items-center gap-2 text-gray-600">
          <div className="w-1 h-1 rounded-full bg-accent animate-pulse" />
          <span className="text-[10px] font-mono uppercase tracking-widest">Loading detail</span>
        </div>
      </div>
    );
  }

  if (error) {
    return (
      <div className="px-4 py-3 border-b border-border bg-surface-1/30">
        <div className="flex items-center justify-between">
          <p className="text-[11px] text-phosphor-red font-mono">{error}</p>
          <button
            onClick={() => { setLoading(true); load(); }}
            className="btn-secondary py-0.5 px-2 text-[10px]"
          >
            Retry
          </button>
        </div>
      </div>
    );
  }

  if (!detail) return null;

  return (
    <div className="px-4 py-3 border-b border-border bg-surface-1/30 space-y-2">
      <div className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <StatusBadge status={detail.status} />
          <span className="text-[10px] text-gray-500 font-mono">
            agent: <span className="text-gray-400">{detail.agent_id}</span>
          </span>
        </div>
        <div className="flex items-center gap-3 text-[10px] text-gray-600 font-mono">
          <span>created {new Date(detail.created_at).toLocaleString("en-US", { hour12: false })}</span>
          <span>updated {new Date(detail.updated_at).toLocaleString("en-US", { hour12: false })}</span>
        </div>
      </div>

      {detail.labels && Object.keys(detail.labels).length > 0 && (
        <div className="flex items-center gap-2 flex-wrap">
          <span className="text-[10px] text-gray-600 font-mono uppercase tracking-wider">Labels:</span>
          {Object.entries(detail.labels).map(([k, v]) => (
            <span key={k} className="text-[10px] font-mono text-gray-400 bg-surface-2 border border-border px-1.5 py-0.5">
              {k}={v}
            </span>
          ))}
        </div>
      )}

      {detail.input != null && (
        <JsonDetails label="Input" value={detail.input} colorClass="text-phosphor-blue/70" />
      )}

      {detail.output != null && (
        <JsonDetails label="Output" value={detail.output} colorClass="text-phosphor-green/70" />
      )}
    </div>
  );
}
