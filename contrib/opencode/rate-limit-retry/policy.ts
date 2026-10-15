export type FailureKind = "quota" | "rate" | "transient" | "permanent";
export type Cooldown = {
  until: number;
  updated: number;
  failures: number;
  kind: FailureKind;
  authoritative: boolean;
};
export type Options = {
  maxRetries: number;
  baseDelayMs: number;
  maxDelayMs: number;
  maxAutomaticWaitMs: number;
  quotaFallbackMs: number;
  rateFallbackMs: number;
  freeInputBudget: number;
};
export const defaults: Options = {
  maxRetries: 3,
  baseDelayMs: 2_000,
  maxDelayMs: 30_000,
  maxAutomaticWaitMs: 120_000,
  quotaFallbackMs: 15 * 60_000,
  rateFallbackMs: 30_000,
  freeInputBudget: 128_000,
};

export function options(raw: Record<string, unknown> = {}): Options {
  const result = { ...defaults };
  for (const name of Object.keys(result) as (keyof Options)[]) {
    const value = raw[name];
    if (typeof value === "number" && Number.isSafeInteger(value) && value >= 0) result[name] = value;
  }
  result.maxRetries = Math.min(result.maxRetries, 10);
  result.maxAutomaticWaitMs = Math.min(result.maxAutomaticWaitMs, 15 * 60_000);
  result.baseDelayMs = Math.max(result.baseDelayMs, 1);
  result.maxDelayMs = Math.max(result.maxDelayMs, result.baseDelayMs);
  result.quotaFallbackMs = Math.max(result.quotaFallbackMs, 60_000);
  result.rateFallbackMs = Math.max(result.rateFallbackMs, 1);
  return result;
}

export function retryAfter(value: string | null, now: number): number | undefined {
  if (value === null || value.trim() === "") return;
  const text = value.trim();
  const milliseconds = /^\d+(?:\.\d+)?$/.test(text)
    ? Number(text) * 1_000
    : Date.parse(text) - now;
  if (Number.isFinite(milliseconds) && milliseconds >= 0 && Number.isSafeInteger(Math.ceil(now + milliseconds))) {
    return Math.ceil(milliseconds);
  }
}

function responseCode(error: any): string {
  const body = error?.response?.body ?? error?.data?.responseBody;
  let decoded: any;
  try { decoded = typeof body === "string" ? JSON.parse(body) : body; } catch {}
  return String(decoded?.error?.type ?? decoded?.error?.code ?? decoded?.type ?? error?.code ?? "").toLowerCase();
}

export function classify(error: any): FailureKind {
  const status = error?.status;
  const code = responseCode(error);
  const type = String(error?.type ?? "").toLowerCase();
  const message = String(error?.message ?? "").toLowerCase();
  if ([400, 401, 403, 404, 422].includes(status) ||
      /encrypted_content|not issued to this caller|aborted|cancelled/.test(message)) return "permanent";
  if (/freeusagelimiterror|insufficient_quota|usage_not_included|budgetexceeded|spendinglimit|quota_exceeded/.test(code) ||
      /insufficient balance|billing|spending limit/.test(message)) return "quota";
  if (status === 429 || status === 529) return "rate";
  if (type === "provider.quota") return "quota";
  if ((typeof status === "number" && status >= 500) || type === "provider.transport") return "transient";
  return "permanent";
}

export function cooldownFor(kind: FailureKind, header: string | null, old: Cooldown | undefined,
                            now: number, opts: Options): Cooldown {
  const failures = old && now - old.updated < 5 * 60_000 ? old.failures + 1 : 1;
  const supplied = retryAfter(header, now);
  const fallback = kind === "quota" ? opts.quotaFallbackMs
    : Math.min(opts.rateFallbackMs * 2 ** Math.min(failures - 1, 10), 15 * 60_000);
  const until = now + (supplied ?? fallback);
  // A concurrent healthy or shorter response must never erase a future deadline.
  if (old && old.until > until) return { ...old, failures, updated: now };
  return { until, failures, updated: now, kind, authoritative: supplied !== undefined };
}

export type Decision = { retry: false } | { retry: true; delay: number };
export function decide(event: any, record: Cooldown | undefined, now: number,
                       opts: Options, random = Math.random, gated = false): Decision {
  const kind = gated && record ? record.kind : classify(event.error);
  if (kind === "permanent" || event.attempt > opts.maxRetries + 1) return { retry: false };
  if (kind === "quota" && !record?.authoritative) return { retry: false };
  // Never invent a retry for a partially streamed response that the engine
  // considers unsafe to replay. Pre-request cooldown rejection has no output.
  if (!gated && event.error?.status === 200 && !event.decision?.retry) return { retry: false };
  if (!gated && kind === "transient" && !event.decision?.retry && !(event.error?.status >= 500)) return { retry: false };
  const remaining = Math.max(0, (record?.until ?? now) - now);
  const exponential = Math.min(opts.baseDelayMs * 2 ** Math.max(0, event.attempt - 2), opts.maxDelayMs);
  const native = event.decision?.retry ? event.decision.delay : 0;
  const delay = Math.ceil(Math.max(remaining, exponential, Number.isFinite(native) ? native : 0)
    + Math.max(0, Math.min(1, random())) * exponential * 0.25);
  // Preserve the true deadline on disk; do not shorten it to the auto-wait cap.
  return delay <= opts.maxAutomaticWaitMs ? { retry: true, delay } : { retry: false };
}

export function limitFreeInputs(editor: any, budget: number) {
  if (!budget) return;
  for (const model of editor.list()) {
    if (!["opencode", "opencode-cc"].includes(String(model.providerID)) ||
        !(String(model.id).includes("-free") || model.id === "big-pickle")) continue;
    editor.update(model.providerID, model.id, (draft: any) => {
      const current = draft.limit.input || draft.limit.context;
      draft.limit.input = current > 0 ? Math.min(current, budget) : budget;
    });
  }
}
