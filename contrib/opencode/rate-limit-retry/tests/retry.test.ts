import { afterEach, describe, expect, test } from "bun:test";
import { mkdtemp, readFile, stat, writeFile, mkdir, rm, readdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { classify, retryAfter, cooldownFor, decide, defaults, limitFreeInputs } from "../policy";
import { CooldownStore } from "../store";
import { makePlugin, scopeFor } from "../index";

const directories: string[] = [];
async function directory() { const dir = await mkdtemp(join(tmpdir(), "opencode-retry-test-")); directories.push(dir); return dir; }
afterEach(async () => { for (const dir of directories.splice(0)) await rm(dir, { recursive: true, force: true }); });
const model = { providerID: "opencode", id: "space-bunny-free" };
const event = (error: any, attempt = 2, decision: any = { retry: true, delay: 100 }) => ({ error, attempt, decision });
const quota = { status: 429, type: "provider.quota", response: { body: JSON.stringify({ error: { type: "FreeUsageLimitError" } }) } };

test("429 status and structured bodies, not answer substrings, identify errors", () => {
  expect(classify(quota)).toBe("quota");
  expect(classify({ status: 200, message: "DURING-429" })).toBe("permanent");
  expect(classify({ status: 403, message: "Free tier rate limit", type: "FreeTierError" })).toBe("permanent");
  expect(classify({ status: 429 })).toBe("rate");
  expect(classify({ status: 503 })).toBe("transient");
  expect(classify({ status: 400, message: "encrypted_content was not issued to this caller" })).toBe("permanent");
});

test("Retry-After accepts seconds, fractional seconds, and HTTP dates", () => {
  const now = Date.parse("2026-10-15T12:00:00Z");
  expect(retryAfter("2", now)).toBe(2_000);
  expect(retryAfter("0.5", now)).toBe(500);
  expect(retryAfter("Thu, 15 Oct 2026 12:00:10 GMT", now)).toBe(10_000);
  for (const value of [null, "", "-1", "NaN", "garbage"]) expect(retryAfter(value, now)).toBeUndefined();
});

test("daily quota without a deadline stops instead of immediate retries", () => {
  const record = cooldownFor("quota", null, undefined, 100, defaults);
  expect(record.until).toBe(900_100);
  expect(record.authoritative).toBe(false);
  expect(decide(event(quota), record, 100, defaults, () => 0)).toEqual({ retry: false });
});

test("authoritative long quota is preserved rather than clamped to a short retry", () => {
  const record = cooldownFor("quota", "52638", undefined, 1_000, defaults);
  expect(record.until).toBe(52_639_000);
  expect(decide(event(quota), record, 1_000, defaults, () => 0)).toEqual({ retry: false });
});

test("short 429 deadline overrides quota classification and native no-retry safely", () => {
  const record = cooldownFor("quota", "4", undefined, 1_000, defaults);
  const result = decide(event(quota, 2, { retry: false }), record, 1_200, defaults, () => 0);
  expect(result).toEqual({ retry: true, delay: 3_800 });
});

test("jitter and native backoff can lengthen, never shorten, Retry-After", () => {
  const record = cooldownFor("rate", "10", undefined, 0, defaults);
  for (const random of [0, 0.5, 1]) {
    const result = decide(event({ status: 429 }), record, 100, defaults, () => random);
    expect(result.retry).toBe(true);
    if (result.retry) expect(result.delay).toBeGreaterThanOrEqual(9_900);
  }
});

test("no deadline yields increasing bounded fallback, not an eight-second IP-reset loop", () => {
  const one = cooldownFor("rate", null, undefined, 0, defaults);
  const two = cooldownFor("rate", null, one, 1_000, defaults);
  expect(one.until).toBe(30_000);
  expect(two.until).toBe(61_000);
  expect(decide(event({ status: 429 }, 5), two, 1_000, defaults)).toEqual({ retry: false });
});

test("partial-output transport failures are never made replayable by this plugin", () => {
  expect(decide(event({ type: "provider.transport", status: 200 }, 2, { retry: false }), undefined, 0, defaults)).toEqual({ retry: false });
});

test("a shorter concurrent response does not erase an existing longer deadline", () => {
  const old = cooldownFor("quota", "100", undefined, 0, defaults);
  const result = cooldownFor("rate", "2", old, 1_000, defaults);
  expect(result.until).toBe(old.until);
  expect(result.kind).toBe("quota");
});

test("scope separates accounts and models without writing credential values", () => {
  const request = new Request("https://opencode.ai/inference/v1/chat/completions", { headers: { Authorization: "Bearer PRIVATE-KEY-A" } });
  const sameOriginRoute = new Request("https://opencode.ai/inference/openai/v1/chat/completions", { headers: request.headers });
  const a = scopeFor(request, model);
  expect(a).toMatch(/^[a-f0-9]{64}$/);
  expect(scopeFor(sameOriginRoute, model)).toBe(a);
  expect(scopeFor(new Request(request.url, { headers: { Authorization: "Bearer PRIVATE-KEY-B" } }), model)).not.toBe(a);
  expect(scopeFor(request, { ...model, id: "another-free" })).not.toBe(a);
});

test("concurrent stores preserve the maximum deadline across instances", async () => {
  const dir = await directory(); const a = new CooldownStore(dir); const b = new CooldownStore(dir); const scope = "a".repeat(64);
  await Promise.all([a.update(scope, old => cooldownFor("quota", "100", old, 0, defaults)),
    b.update(scope, old => cooldownFor("rate", "2", old, 0, defaults))]);
  expect((await a.read(scope))?.until).toBe(100_000);
  expect((await stat(join(dir, scope + ".json"))).mode & 0o777).toBe(0o600);
  expect(await new CooldownStore(dir).read(scope)).toEqual(await a.read(scope));
});

test("corrupt persisted state fails closed", async () => {
  const dir = await directory(); const scope = "a".repeat(64);
  await writeFile(join(dir, scope + ".json"), "not json");
  await expect(new CooldownStore(dir).read(scope)).rejects.toThrow();
});

test("dead storage-lock owner can be recovered", async () => {
  const dir = await directory(); const scope = "a".repeat(64); const lock = join(dir, scope + ".json.lock");
  await mkdir(lock); await writeFile(join(lock, "pid"), "2147483647");
  const store = new CooldownStore(dir);
  await store.update(scope, () => cooldownFor("rate", "2", undefined, 0, defaults));
  expect((await store.read(scope))?.until).toBe(2_000);
});

test("free input limits trigger native compaction earlier without reducing paid models or smaller windows", () => {
  const models = [
    { providerID: "opencode", id: "space-bunny-free", limit: { context: 1_048_576, input: 524_288, output: 524_288 } },
    { providerID: "opencode", id: "small-free", limit: { context: 32_000, input: 16_000 } },
    { providerID: "opencode", id: "paid", limit: { context: 1_048_576 } },
    { providerID: "other", id: "other-free", limit: { context: 1_048_576 } },
  ];
  const before = JSON.stringify(models.slice(2));
  limitFreeInputs({ list: () => models, update: (p: string, m: string, fn: any) => fn(models.find(x => x.providerID === p && x.id === m)) }, 128_000);
  expect(models[0].limit.input).toBe(128_000);
  expect(models[0].limit.context).toBe(1_048_576);
  expect(models[0].limit.output).toBe(524_288);
  expect(models[1].limit.input).toBe(16_000);
  expect(JSON.stringify(models.slice(2))).toBe(before);
});

async function fixture(now: () => number = () => 0) {
  const hooks = new Map<string, any>(); const disposed: string[] = []; let prompts = 0;
  const store = new CooldownStore(await directory());
  const cleanup = await makePlugin({ now, random: () => 0, store }).setup({
    session: {
      hook: async (name: string, callback: any) => { hooks.set(name, callback); return { dispose: async () => { disposed.push(name); hooks.delete(name); } }; },
      prompt: () => { prompts++; throw new Error("Synthetic prompts are forbidden"); },
    },
    event: { subscribe: () => { throw new Error("Global event retries are forbidden"); } },
  });
  const request = new Request("http://127.0.0.1:1234/v1/chat/completions", { method: "POST", headers: { Authorization: "Bearer SECRET" }, body: "original input" });
  return { hooks, cleanup, store, disposed, prompts: () => prompts, request, base: { request, model, sessionID: "ses_test", kind: "primary" } };
}

test("original error body and headers survive classification; retry adds zero user messages", async () => {
  let now = 0; const f = await fixture(() => now);
  await f.hooks.get("http.request")(f.base);
  const text = JSON.stringify({ error: { type: "FreeUsageLimitError" } });
  const response = new Response(text, { status: 429, headers: { "Retry-After": "4" } });
  await f.hooks.get("http.response")({ ...f.base, response });
  expect(await response.text()).toBe(text);
  const e = { ...f.base, ...event(quota, 2, { retry: false }) };
  await f.hooks.get("retry")(e);
  expect(e.decision).toEqual({ retry: true, delay: 4_000 });
  now = 4_001;
  await f.hooks.get("http.request")(f.base);
  expect(await f.request.clone().text()).toBe("original input");
  expect(f.prompts()).toBe(0);
  await f.cleanup?.();
  expect(f.hooks.size).toBe(0);
});

test("new sessions sharing an exhausted account/model are gated; other models still run", async () => {
  const f = await fixture(); await f.hooks.get("http.request")(f.base);
  await f.hooks.get("http.response")({ ...f.base, response: new Response(JSON.stringify({ error: { type: "FreeUsageLimitError" } }), { status: 429, headers: { "Retry-After": "3600" } }) });
  await expect(f.hooks.get("http.request")({ ...f.base, sessionID: "ses_another" })).rejects.toThrow("cooldown active");
  await f.hooks.get("http.request")({ ...f.base, model: { ...model, id: "other-free" } });
  expect(f.prompts()).toBe(0);
  await f.cleanup?.();
});

test("healthy in-flight response cannot clear a later authoritative cooldown", async () => {
  const f = await fixture(); await f.hooks.get("http.request")(f.base);
  await f.hooks.get("http.response")({ ...f.base, response: new Response("{}", { status: 429, headers: { "Retry-After": "3600" } }) });
  await f.hooks.get("http.response")({ ...f.base, response: new Response("{}", { status: 200 }) });
  expect((await f.store.read(scopeFor(f.request, model)))?.until).toBe(3_600_000);
  await f.cleanup?.();
});

test("state failure cannot silently become an upstream retry", async () => {
  const f = await fixture(); await f.hooks.get("http.request")(f.base);
  await writeFile(join(f.store.directory, scopeFor(f.request, model) + ".json"), "corrupt");
  const e = { ...f.base, ...event({ status: 429 }) }; await f.hooks.get("retry")(e);
  expect(e.decision).toEqual({ retry: false });
  await f.cleanup?.();
});

test("disabled plugin registers no hooks", async () => {
  const cleanup = await makePlugin().setup({ options: { enabled: false } });
  expect(cleanup).toBeUndefined();
});
