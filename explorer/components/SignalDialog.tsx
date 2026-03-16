"use client";

import { useState, useRef, useEffect } from "react";
import { sendSignal } from "@/lib/api";

interface Props {
  executionId: string;
  onClose: () => void;
}

export default function SignalDialog({ executionId, onClose }: Props) {
  const [signalType, setSignalType] = useState("");
  const [payload, setPayload] = useState("{}");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  useEffect(() => {
    const handleEsc = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", handleEsc);
    return () => window.removeEventListener("keydown", handleEsc);
  }, [onClose]);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError("");

    if (!signalType.trim()) {
      setError("Signal type is required");
      return;
    }

    let parsed: unknown;
    try {
      parsed = JSON.parse(payload);
    } catch {
      setError("Invalid JSON payload");
      return;
    }

    setLoading(true);
    try {
      await sendSignal(executionId, signalType, parsed);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to send signal");
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      <div
        className="absolute inset-0 bg-black/70 backdrop-blur-sm"
        onClick={onClose}
      />

      <div className="relative bg-surface-1 border border-border w-full max-w-md mx-4 animate-fade-in shadow-2xl">
        <div className="flex items-center justify-between px-5 py-3 border-b border-border">
          <div className="flex items-center gap-2">
            <svg className="w-3.5 h-3.5 text-accent" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
              <path strokeLinecap="round" strokeLinejoin="round" d="M3.75 13.5l10.5-11.25L12 10.5h8.25L9.75 21.75 12 13.5H3.75z" />
            </svg>
            <h3 className="text-xs font-mono text-white uppercase tracking-widest">
              Send Signal
            </h3>
          </div>
          <button onClick={onClose} className="btn-ghost p-1">
            <svg className="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
              <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
            </svg>
          </button>
        </div>

        <div className="p-5">
          <div className="text-[10px] text-gray-600 mb-4 font-mono tracking-wider">
            TARGET <span className="text-gray-500">{executionId}</span>
          </div>

          <form onSubmit={handleSubmit} className="space-y-4">
            <div>
              <label className="block text-[10px] text-gray-600 mb-1.5 font-mono uppercase tracking-widest">
                Signal Type
              </label>
              <input
                ref={inputRef}
                type="text"
                value={signalType}
                onChange={(e) => setSignalType(e.target.value)}
                placeholder="human_approval"
                className="input-field"
                required
              />
            </div>

            <div>
              <label className="block text-[10px] text-gray-600 mb-1.5 font-mono uppercase tracking-widest">
                Payload &middot; JSON
              </label>
              <textarea
                value={payload}
                onChange={(e) => setPayload(e.target.value)}
                rows={4}
                className="input-field resize-none"
              />
            </div>

            {error && (
              <p className="text-[11px] text-phosphor-red font-mono bg-phosphor-red/5 border border-phosphor-red/15 px-3 py-2">
                {error}
              </p>
            )}

            <div className="flex justify-end gap-2 pt-1">
              <button type="button" onClick={onClose} className="btn-secondary">
                Cancel
              </button>
              <button
                type="submit"
                disabled={loading}
                className="btn-primary disabled:opacity-40 disabled:cursor-not-allowed"
              >
                {loading ? "Transmitting..." : "Transmit"}
              </button>
            </div>
          </form>
        </div>
      </div>
    </div>
  );
}
