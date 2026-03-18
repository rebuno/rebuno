export function jitteredBackoff(
  baseDelay: number,
  attempt: number,
  maxDelay: number,
): number {
  const delay = Math.min(baseDelay * 2 ** (attempt - 1), maxDelay);
  return delay * (0.5 + Math.random() * 0.5);
}
