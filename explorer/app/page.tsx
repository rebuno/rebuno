"use client";

import { useState } from "react";
import Header from "@/components/Header";
import ExecutionList from "@/components/ExecutionList";
import CreateExecution from "@/components/CreateExecution";
import EventStream from "@/components/EventStream";
import ExecutionDetail from "@/components/ExecutionDetail";
import SignalDialog from "@/components/SignalDialog";

export default function Home() {
  const [selectedExecId, setSelectedExecId] = useState<string | null>(null);
  const [statusFilter, setStatusFilter] = useState("");
  const [refreshKey, setRefreshKey] = useState(0);
  const [signalDialogOpen, setSignalDialogOpen] = useState(false);
  const [sidebarOpen, setSidebarOpen] = useState(true);

  const refresh = () => setRefreshKey((k) => k + 1);

  return (
    <div className="min-h-screen flex flex-col">
      <Header />

      <div className="flex-1 flex flex-col md:flex-row">
        <div className="md:hidden border-b border-border bg-surface-1/50 px-4 py-2">
          <button
            onClick={() => setSidebarOpen((o) => !o)}
            className="btn-ghost py-1 px-2 text-[10px] font-mono uppercase tracking-widest flex items-center gap-1.5"
          >
            <svg
              className={`w-3 h-3 transition-transform duration-200 ${sidebarOpen ? "rotate-180" : ""}`}
              fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}
            >
              <path strokeLinecap="round" strokeLinejoin="round" d="M19.5 8.25l-7.5 7.5-7.5-7.5" />
            </svg>
            {sidebarOpen ? "Hide Sidebar" : "Show Sidebar"}
          </button>
        </div>

        <aside className={`w-full md:w-[380px] border-r border-border flex flex-col bg-surface-1/30 ${
          sidebarOpen ? "" : "hidden md:flex"
        }`}>
          <CreateExecution onCreated={refresh} />

          <div className="flex-1 flex flex-col min-h-0">
            <div className="px-4 py-2.5 border-b border-border flex items-center justify-between">
              <h2 className="text-[10px] font-mono text-gray-500 uppercase tracking-[0.2em]">
                Executions
              </h2>
              <div className="flex items-center gap-2">
                <select
                  value={statusFilter}
                  onChange={(e) => setStatusFilter(e.target.value)}
                  className="text-[10px] bg-surface-2 border border-border px-2 py-1 text-gray-400 font-mono uppercase tracking-wider focus:outline-none focus:ring-1 focus:ring-accent/30 focus:border-accent/50 cursor-pointer"
                >
                  <option value="">All</option>
                  <option value="pending">Pending</option>
                  <option value="running">Running</option>
                  <option value="blocked">Blocked</option>
                  <option value="completed">Completed</option>
                  <option value="failed">Failed</option>
                  <option value="cancelled">Cancelled</option>
                </select>
                <button
                  onClick={refresh}
                  className="btn-ghost p-1.5 group"
                  title="Refresh"
                  aria-label="Refresh executions"
                >
                  <svg
                    className="w-3 h-3 group-hover:text-accent transition-colors"
                    fill="none"
                    viewBox="0 0 24 24"
                    stroke="currentColor"
                    strokeWidth={2}
                  >
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
                    />
                  </svg>
                </button>
              </div>
            </div>

            <ExecutionList
              statusFilter={statusFilter}
              refreshKey={refreshKey}
              selectedId={selectedExecId}
              onSelect={setSelectedExecId}
            />
          </div>
        </aside>

        <main className="flex-1 flex flex-col min-h-0 bg-surface-0">
          {selectedExecId ? (
            <>
              <ExecutionDetail executionId={selectedExecId} />
              <EventStream
                executionId={selectedExecId}
                onSendSignal={() => setSignalDialogOpen(true)}
                onClose={() => setSelectedExecId(null)}
              />
            </>
          ) : (
            <div className="flex-1 flex items-center justify-center">
              <div className="text-center">
                <div className="mb-5 flex justify-center">
                  <div className="relative">
                    <div className="w-12 h-12 border border-border rotate-45" />
                    <div className="absolute inset-0 flex items-center justify-center">
                      <div className="w-4 h-4 border border-accent/30 rotate-45" />
                    </div>
                    <div className="absolute inset-0 flex items-center justify-center">
                      <div className="w-1 h-1 bg-accent/40 rotate-45" />
                    </div>
                  </div>
                </div>
                <p className="text-[10px] text-gray-600 font-mono uppercase tracking-[0.3em]">
                  Select execution to monitor
                </p>
              </div>
            </div>
          )}
        </main>
      </div>

      {signalDialogOpen && selectedExecId && (
        <SignalDialog
          executionId={selectedExecId}
          onClose={() => setSignalDialogOpen(false)}
        />
      )}
    </div>
  );
}
