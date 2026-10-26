// Read-only OpenCode v2 HTTP hooks for bounded access diagnostics.
// This plugin does not change the request, response, URL, headers, or toolset.
import { appendFile, writeFile } from "node:fs/promises";
import { createHash } from "node:crypto";

const hash = value => createHash("sha256").update(value).digest("hex");
const allowed = new Set(["accept", "accept-encoding", "content-type", "user-agent", "b3", "traceparent",
  "x-opencode-client", "x-opencode-project", "x-opencode-session", "x-opencode-session-id",
  "x-opencode-org-id", "x-opencode-parent-session-id", "x-session-affinity", "x-session-id", "x-parent-session-id"]);
export default {
  id: "zen-access-readonly-observer",
  async setup(ctx) {
    let capturedRequest;
    const record = row => appendFile(ctx.options.observations,
      JSON.stringify({ time: new Date().toISOString(), ...row }) + "\n", { mode: 0o600 });
    const proxyEnvironment = Object.fromEntries(["HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"].map(key => {
      let value = process.env[key] || null;
      if (value && value.includes("://")) {
        const url = new URL(value);
        value = url.protocol + "//" + url.hostname + (url.port ? ":" + url.port : "");
      }
      return [key, value];
    }));
    await record({ event: "observer_loaded", bun_version: Bun.version, proxy_environment: proxyEnvironment });
    try {
      const response = await fetch("https://www.cloudflare.com/cdn-cgi/trace", { signal: AbortSignal.timeout(10000) });
      await record({ event: "native_process_egress", status: response.status, trace: (await response.text()).slice(0, 1500) });
    } catch (error) {
      await record({ event: "native_process_egress", error: String(error) });
    }
    await ctx.session.hook("http.request", async event => {
      if (event.model.id !== "deepseek-v4.1-flash-free" || event.kind !== "primary") return;
      const request = event.request.clone();
      const body = await request.text();
      const headers = Object.fromEntries([...request.headers].filter(([key]) => allowed.has(key)));
      const authorization = request.headers.get("authorization") || "";
      const token = authorization.replace(/^Bearer\s+/i, "");
      const parsed = JSON.parse(body);
      capturedRequest = { url: request.url, method: request.method, headers: Object.fromEntries(request.headers), body };
      await record({ event: "native_http_request", url: request.url, model: event.model,
        kind: event.kind, request_sha256: hash(body), request_bytes: Buffer.byteLength(body),
        authorization_fingerprint: hash(token).slice(0, 12),
        header_names: [...request.headers.keys()], body_fields: Object.keys(parsed),
        tools: (parsed.tools || []).map(tool => tool.name || tool.function?.name) });
      if (token && body.includes(token)) throw new Error("Credential unexpectedly present in request body");
      await writeFile(ctx.options.capture,
        JSON.stringify({ url: request.url, path: new URL(request.url).pathname, headers, body }),
        { mode: 0o600 });
    });
    await ctx.session.hook("http.response", async event => {
      if (event.model.id !== "deepseek-v4.1-flash-free" || event.kind !== "primary") return;
      const headers = Object.fromEntries([...event.response.headers].filter(([key]) =>
        ["date", "retry-after", "cf-ray", "content-type", "x-request-id"].includes(key) || key.includes("ratelimit")));
      await record({ event: "native_http_response", url: event.request.url, model: event.model,
        status: event.response.status, headers });
      if (ctx.options.replay && capturedRequest) {
        const replay = capturedRequest;
        capturedRequest = undefined;
        try {
          const response = await fetch(replay.url, { method: replay.method, headers: replay.headers,
            body: replay.body, signal: AbortSignal.timeout(30000) });
          const reader = response.body.getReader();
          let text = "";
          for (;;) {
            const chunk = await reader.read();
            if (chunk.done) break;
            text += new TextDecoder().decode(chunk.value);
            if (text.length > 65536) { await reader.cancel(); break; }
          }
          await record({ event: "native_runtime_fetch_replay", url: replay.url, status: response.status,
            body_sha256: hash(replay.body), response: text.slice(0, 12000) });
        } catch (error) {
          await record({ event: "native_runtime_fetch_replay", error: String(error) });
        }
      }
    });
  }
};
