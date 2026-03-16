const statusConfig: Record<string, { border: string; text: string; dot: string }> = {
  pending: {
    border: "border-phosphor-blue/30",
    text: "text-phosphor-blue",
    dot: "bg-phosphor-blue",
  },
  running: {
    border: "border-accent/30",
    text: "text-accent",
    dot: "bg-accent animate-pulse",
  },
  blocked: {
    border: "border-phosphor-purple/30",
    text: "text-phosphor-purple",
    dot: "bg-phosphor-purple animate-pulse-slow",
  },
  completed: {
    border: "border-phosphor-green/30",
    text: "text-phosphor-green",
    dot: "bg-phosphor-green",
  },
  failed: {
    border: "border-phosphor-red/30",
    text: "text-phosphor-red",
    dot: "bg-phosphor-red",
  },
  cancelled: {
    border: "border-gray-600/30",
    text: "text-gray-500",
    dot: "bg-gray-500",
  },
};

export default function StatusBadge({ status }: { status: string }) {
  const config = statusConfig[status] || statusConfig.pending;

  return (
    <span
      className={`label-tag border ${config.border} ${config.text}`}
    >
      <span className={`w-1 h-1 rounded-full ${config.dot}`} />
      {status}
    </span>
  );
}
