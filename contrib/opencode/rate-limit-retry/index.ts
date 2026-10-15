import { createHash } from "node:crypto";
import { join } from "node:path";
import { homedir } from "node:os";
import { CooldownStore } from "./store";
import { classify, cooldownFor, decide, limitFreeInputs, options, type Cooldown } from "./policy";

export function scopeFor(request: Request, model: any): string {
  const identity = [request.headers.get("authorization"), request.headers.get("x-api-key"),
    request.headers.get("api-key"), request.headers.get("x-opencode-org-id")];
  return createHash("sha256").update(JSON.stringify([
    new URL(request.url).origin, model.providerID, model.id, identity,
  ])).digest("hex");
}

async function errorBody(response: Response): Promise<string | undefined> {
  const copy = response.clone();
  const reader = copy.body?.getReader();
  if (!reader) return;
  const chunks: Uint8Array[] = [];
  let length = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    const deadline = new Promise<never>((_, reject) => {
      timer = setTimeout(() => reject(new Error("Error-body read timed out")), 1_000);
    });
    while (true) {
      const part = await Promise.race([reader.read(), deadline]);
      if (part.done) break;
      length += part.value.length;
      if (length > 32_768) return;
      chunks.push(part.value);
    }
    return Buffer.concat(chunks).toString("utf8");
  } catch { return; }
  finally {
    if (timer) clearTimeout(timer);
    // Awaiting cancellation of one tee branch can deadlock until the original
    // response is consumed. Leave original bytes to OpenCode's protocol reader.
    void reader.cancel().catch(() => {});
  }
}

export function makePlugin(deps: { now?: () => number; random?: () => number; store?: CooldownStore } = {}) {
  return {
    id: "local.rate-limit-retry",
    setup: async (ctx: any) => {
      const raw = ctx.options ?? {};
      if ([false, "false", "0"].includes(raw.enabled)) return;
      if (typeof ctx.session?.hook !== "function") throw new Error("rate-limit-retry requires OpenCode v2 native session hooks");
      const opts = options(raw);
      const now = deps.now ?? Date.now;
      const store = deps.store ?? new CooldownStore(raw.stateDir ?? process.env.RATE_LIMIT_RETRY_STATE_DIR ??
        join(process.env.XDG_STATE_HOME ?? join(homedir(), ".local/state"), "opencode", "rate-limit-retry-v2"));
      const requests = new Map<string, { scope: string; gated: boolean; time: number }>();
      const keyFor = (event: any) => JSON.stringify([event.sessionID, event.model.providerID, event.model.id]);
      const registrations: { dispose: () => Promise<void> }[] = [];
      try {
        registrations.push(await ctx.session.hook("http.request", async (event: any) => {
          const scope = scopeFor(event.request, event.model);
          const key = keyFor(event);
          if (requests.size > 1_024) requests.delete(requests.keys().next().value!);
          requests.set(key, { scope, gated: false, time: now() });
          const record = await store.read(scope);
          if (record && record.until > now()) {
            requests.get(key)!.gated = true;
            throw new Error(`Provider cooldown active until ${new Date(record.until).toISOString()}; no upstream request sent. ` +
              (record.authoritative ? "Deadline supplied by upstream." : "Estimated cooldown; upstream supplied no Retry-After."));
          }
        }));
        registrations.push(await ctx.session.hook("http.response", async (event: any) => {
          const status = event.response.status;
          if (status !== 429 && status !== 529 && !(status >= 500 && event.response.headers.has("retry-after"))) return;
          const scope = scopeFor(event.request, event.model);
          const body = await errorBody(event.response);
          const kind = classify({ status, response: { body } });
          await store.update(scope, old => cooldownFor(kind, event.response.headers.get("retry-after"), old, now(), opts));
        }));
        registrations.push(await ctx.session.hook("retry", async (event: any) => {
          try {
            const last = requests.get(keyFor(event));
            const record: Cooldown | undefined = last ? await store.read(last.scope) : undefined;
            event.decision = decide(event, record, now(), opts, deps.random, last?.gated);
            if (!event.decision.retry && record && record.until > now()) {
              console.log(`[rate-limit-retry] retry stopped; cooldown until ${new Date(record.until).toISOString()}`);
            }
          } catch {
            event.decision = { retry: false };
            console.error("[rate-limit-retry] cooldown storage unavailable; retry suppressed");
          }
        }));
        if (ctx.model?.transform) registrations.push(await ctx.model.transform((editor: any) => limitFreeInputs(editor, opts.freeInputBudget)));
      } catch (error) {
        await Promise.allSettled(registrations.map(registration => registration.dispose()));
        throw error;
      }
      console.log(`[rate-limit-retry] native policy loaded; max retries=${opts.maxRetries}, free input budget=${opts.freeInputBudget}`);
      return async () => {
        await Promise.allSettled(registrations.map(registration => registration.dispose()));
        requests.clear();
      };
    },
  };
}

export default makePlugin();
