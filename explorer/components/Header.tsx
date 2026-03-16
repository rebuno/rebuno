"use client";

import { useEffect, useState } from "react";
import { checkHealth } from "@/lib/api";
import { HEALTH_POLL_INTERVAL } from "@/lib/constants";

interface ConnectionDisplay {
  dot: string;
  text: string;
  label: string;
}

function getConnectionDisplay(connected: boolean | null): ConnectionDisplay {
  if (connected === null) {
    return { dot: "bg-accent animate-pulse", text: "text-accent", label: "Init" };
  }
  if (connected) {
    return { dot: "bg-phosphor-green", text: "text-phosphor-green", label: "Online" };
  }
  return { dot: "bg-phosphor-red animate-pulse", text: "text-phosphor-red", label: "Offline" };
}

export default function Header() {
  const [connected, setConnected] = useState<boolean | null>(null);
  const [time, setTime] = useState("");

  useEffect(() => {
    async function check() {
      setConnected(await checkHealth());
    }
    check();
    const timer = setInterval(check, HEALTH_POLL_INTERVAL);
    return () => clearInterval(timer);
  }, []);

  useEffect(() => {
    function tick() {
      setTime(
        new Date().toLocaleTimeString("en-US", {
          hour12: false,
          hour: "2-digit",
          minute: "2-digit",
          second: "2-digit",
        })
      );
    }
    tick();
    const timer = setInterval(tick, 1000);
    return () => clearInterval(timer);
  }, []);

  const conn = getConnectionDisplay(connected);

  return (
    <header className="h-11 border-b border-border bg-surface-1 flex items-center justify-between px-4 shrink-0">
      <div className="flex items-center gap-3">
        <span className="text-[15px] font-medium text-accent tracking-[-0.5px]" style={{ fontFamily: "var(--font-dm-mono), monospace" }}>
          rebuno
        </span>
      </div>

      <div className="flex items-center gap-4">
        <span className="text-[11px] font-mono text-gray-500 tabular-nums tracking-wider">
          {time}
        </span>

        <div className="h-3 w-px bg-border" />

        <div className="flex items-center gap-2">
          <div className={`w-1.5 h-1.5 rounded-full ${conn.dot}`} />
          <span className={`text-[10px] font-mono uppercase tracking-widest ${conn.text}`}>
            {conn.label}
          </span>
        </div>
      </div>
    </header>
  );
}
