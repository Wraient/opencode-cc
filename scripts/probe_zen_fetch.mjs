// Run with a selected Bun release to compare fetch transports on an unchanged request.
import { createHash } from "node:crypto";
const fixture = await Bun.file(process.env.ZEN_PROBE_FIXTURE).json();
const headers = Object.fromEntries(Object.entries(fixture.headers).filter(([key]) =>
  !["host", "content-length", "authorization", "connection", "accept-encoding"].includes(key.toLowerCase())));
if (process.env.ZEN_PROBE_KEY) headers.authorization = "Bearer " + process.env.ZEN_PROBE_KEY;
const start = performance.now();
const row = { event: "standalone_bun_fetch", bun_version: Bun.version,
  request_sha256: createHash("sha256").update(fixture.body).digest("hex"),
  credential_fingerprint: createHash("sha256").update(process.env.ZEN_PROBE_KEY || "").digest("hex").slice(0, 12) };
try {
  const response = await fetch(process.env.ZEN_PROBE_URL || fixture.url, { method: "POST", headers, body: fixture.body,
    proxy: process.env.ZEN_PROBE_PROXY || false, signal: AbortSignal.timeout(40000) });
  row.status = response.status;
  row.headers = Object.fromEntries([...response.headers].filter(([key]) =>
    ["date", "retry-after", "cf-ray", "content-type", "x-request-id"].includes(key) || key.includes("ratelimit")));
  let raw = "";
  const reader = response.body.getReader();
  for (;;) {
    const chunk = await reader.read();
    if (chunk.done) break;
    raw += new TextDecoder().decode(chunk.value);
    if (raw.length > 65536) { await reader.cancel(); throw new Error("Diagnostic response exceeded 64 KiB"); }
  }
  row.response = raw.slice(0, 16000);
} catch (error) {
  row.error = String(error);
}
row.seconds = (performance.now() - start) / 1000;
row.time = new Date().toISOString();
console.log(JSON.stringify(row));
