"use client";

import { useState } from "react";
import { createExecution } from "@/lib/api";

export default function CreateExecution({ onCreated }: { onCreated: () => void }) {
  const [agentId, setAgentId] = useState("");
  const [input, setInput] = useState("{}");
  const [labelsText, setLabelsText] = useState("");
  const [loading, setLoading] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const [error, setError] = useState("");

  function parseLabels(text: string): Record<string, string> | null {
    const trimmed = text.trim();
    if (!trimmed) return null;
    const labels: Record<string, string> = {};
    for (const line of trimmed.split("\n")) {
      const entry = line.trim();
      if (!entry) continue;
      const eqIdx = entry.indexOf("=");
      if (eqIdx <= 0) return null;
      labels[entry.slice(0, eqIdx).trim()] = entry.slice(eqIdx + 1).trim();
    }
    return Object.keys(labels).length > 0 ? labels : null;
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError("");

    let parsed: unknown;
    try {
      parsed = JSON.parse(input);
    } catch {
      setError("Invalid JSON input");
      return;
    }

    if (labelsText.trim() && !parseLabels(labelsText)) {
      setError("Invalid labels format. Use key=value, one per line.");
      return;
    }

    setLoading(true);
    try {
      await createExecution(agentId, parsed, parseLabels(labelsText) ?? undefined);
      onCreated();
      setExpanded(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create execution");
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="border-b border-border">
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full px-4 py-3 flex items-center justify-between hover:bg-surface-2/30 transition-colors group"
      >
        <div className="flex items-center gap-2.5">
          <div className={`w-4 h-4 border border-accent/40 flex items-center justify-center transition-colors ${
            expanded ? "bg-accent/10" : "group-hover:border-accent/60"
          }`}>
            <svg className="w-2.5 h-2.5 text-accent" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2.5}>
              <path strokeLinecap="round" strokeLinejoin="round" d="M12 4.5v15m7.5-7.5h-15" />
            </svg>
          </div>
          <span className="text-[11px] font-mono text-gray-400 uppercase tracking-widest group-hover:text-gray-300 transition-colors">
            New Execution
          </span>
        </div>
        <svg
          className={`w-3 h-3 text-gray-600 transition-transform duration-200 ${expanded ? "rotate-180" : ""}`}
          fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}
        >
          <path strokeLinecap="round" strokeLinejoin="round" d="M19.5 8.25l-7.5 7.5-7.5-7.5" />
        </svg>
      </button>

      {expanded && (
        <form onSubmit={handleSubmit} className="px-4 pb-4 animate-fade-in space-y-3">
          <div>
            <label className="block text-[10px] text-gray-600 mb-1 font-mono uppercase tracking-widest">
              Agent ID
            </label>
            <input
              type="text"
              value={agentId}
              onChange={(e) => setAgentId(e.target.value)}
              className="input-field"
              placeholder="e.g. researcher"
              required
            />
          </div>
          <div>
            <label className="block text-[10px] text-gray-600 mb-1 font-mono uppercase tracking-widest">
              Input &middot; JSON
            </label>
            <textarea
              value={input}
              onChange={(e) => setInput(e.target.value)}
              rows={3}
              className="input-field resize-none"
              placeholder='e.g. {"query": "research quantum computing"}'
            />
          </div>
          <div>
            <label className="block text-[10px] text-gray-600 mb-1 font-mono uppercase tracking-widest">
              Labels <span className="normal-case tracking-normal text-gray-700">(optional, key=value per line)</span>
            </label>
            <textarea
              value={labelsText}
              onChange={(e) => setLabelsText(e.target.value)}
              rows={2}
              className="input-field resize-none"
              placeholder={"env=staging\nteam=research"}
            />
          </div>
          {error && (
            <p className="text-[11px] text-phosphor-red font-mono bg-phosphor-red/5 border border-phosphor-red/15 px-3 py-2">
              {error}
            </p>
          )}
          <button
            type="submit"
            disabled={loading}
            className="btn-primary w-full disabled:opacity-40 disabled:cursor-not-allowed"
          >
            {loading ? (
              <span className="flex items-center justify-center gap-2">
                <div className="w-1 h-1 rounded-full bg-surface-0 animate-pulse" />
                Executing...
              </span>
            ) : (
              "Execute"
            )}
          </button>
        </form>
      )}
    </div>
  );
}
