import { test, expect } from "bun:test";
import { normalize, Sessions, makeHandler, guardStream, MODEL } from "./gateway.mjs";
const body = { model: MODEL, messages: [{ role: "system", content: "CLIENT_POLICY_ONE" },
  { role: "system", content: "CLIENT_POLICY_TWO" }, { role: "user", content: "task" }],
  tools: [{ type: "function", function: { name: "Write", parameters: { type: "object" } } }], max_tokens: 100 };

test("late stream failure keeps committed output once and emits an explicit error without replay", async () => {
  let reads = 0, failures = 0;
  const original = new Response(new ReadableStream({ pull(c) {
    if (reads++ === 0) c.enqueue(new TextEncoder().encode('data: {"choices":[{"delta":{"content":"once"}}]}\n\n'));
    else c.error(new Error("fixture connection reset"));
  } }), { headers: { "content-type": "text/event-stream" } });
  const output = await guardStream(original, new AbortController().signal, () => failures++).text();
  expect(output.match(/once/g)?.length).toBe(1);
  expect(output).toContain('"error":{"type":"api_error"');
  expect(output).not.toContain("[DONE]");
  expect(failures).toBe(1);
});

test("healthy SSE passes unchanged and cancellation does not report network failure", async () => {
  let failures = 0;
  const data = 'data: {"choices":[]}\n\ndata: [DONE]\n\n';
  const response = new Response(data, { headers: { "content-type": "text/event-stream" } });
  expect(await guardStream(response, new AbortController().signal, () => failures++).text()).toBe(data);
  expect(failures).toBe(0);
});

test("compatibility keeps client instructions, tools and history and is idempotent", () => {
  const result = JSON.parse(normalize(JSON.stringify(body)));
  expect(result.messages[0].content).toContain("CLIENT_POLICY_ONE\n\nCLIENT_POLICY_TWO");
  expect(result.messages.slice(1)).toEqual(body.messages.slice(2));
  expect(result.tools).toEqual(body.tools);
  expect(result.max_tokens).toBe(100);
  expect(normalize(JSON.stringify(result))).toBe(JSON.stringify(result));
  expect(() => normalize(JSON.stringify({ ...body, model: "other" }))).toThrow();
});

test("tool turns keep affinity; credentials are isolated; protocol time refresh is bounded", () => {
  const sessions = new Sessions();
  const a = new Headers({ authorization: "Bearer first", "x-opencode-session": "conversation" });
  const id = sessions.get(a, 1000000);
  expect(sessions.get(a, 1001000)).toBe(id);
  expect(sessions.get(new Headers({ authorization: "Bearer second", "x-opencode-session": "conversation" }), 1001000)).not.toBe(id);
  expect(sessions.get(a, 1240000)).not.toBe(id);
});

test("network rejection fails over with the same credential/model, and pins the successful route", async () => {
  const calls = [];
  const handler = makeHandler({ routes: [{ name: "phone", proxy: "phone" }, { name: "direct", proxy: false }],
    fetcher: async (_url, init) => {
      calls.push({ proxy: init.proxy, key: init.headers.get("authorization"), model: JSON.parse(init.body).model });
      return init.proxy ? Response.json({ error: { type: "FreeUsageLimitError" } }, { status: 429 }) : new Response("data: real reply\n\n");
    } });
  const request = () => new Request("http://localhost/v1/chat/completions", { method: "POST", headers: { authorization: "Bearer fixture" }, body: JSON.stringify(body) });
  expect(await (await handler(request())).text()).toContain("real reply");
  await (await handler(request())).text();
  expect(calls.map(c => c.proxy)).toEqual(["phone", false, false]);
  expect(calls.every(c => c.key === "Bearer fixture" && c.model === MODEL)).toBe(true);
});

test("auth errors do not rotate routes; genuine all-route quota responses keep Retry-After", async () => {
  for (const status of [401, 403, 429]) {
    let calls = 0, exhausted = 0;
    const handler = makeHandler({ routes: [{ name: "one", proxy: false }, { name: "two", proxy: false }],
      onExhausted: () => exhausted++, fetcher: async () => { calls++; return Response.json({ error: { type: status === 429 ? "FreeUsageLimitError" : "Unauthorized" } }, { status, headers: { "Retry-After": "123", "Content-Encoding": "gzip" } }); } });
    const result = await handler(new Request("http://localhost/v1/chat/completions", { method: "POST", headers: { authorization: "Bearer fixture" }, body: JSON.stringify(body) }));
    expect(result.status).toBe(status);
    expect(result.headers.get("Retry-After")).toBe("123");
    expect(result.headers.get("Content-Encoding")).toBe(null);
    expect(calls).toBe(status === 429 ? 2 : 1);
    expect(exhausted).toBe(status === 429 ? 1 : 0);
    await result.text();
  }
});
