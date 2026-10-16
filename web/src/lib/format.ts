// Formatting helpers shared across pages.

export function fmtNum(n: number): string {
  if (n === undefined || n === null || isNaN(n)) return "0";
  if (Math.abs(n) >= 1_000_000) return (n / 1_000_000).toFixed(2).replace(/\.00$/, "") + "M";
  if (Math.abs(n) >= 1_000) return (n / 1_000).toFixed(1).replace(/\.0$/, "") + "k";
  return String(n);
}

export function fmtMs(ms: number): string {
  if (ms === undefined || ms === null || !Number.isFinite(ms)) return "—";
  if (ms < 1000) return `${ms}ms`;
  return `${(ms / 1000).toFixed(2)}s`;
}

export function fmtTime(iso: string): string {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

export function fmtDateTime(iso: string): string {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return d.toLocaleString([], {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

export function relTime(iso: string): string {
  const d = new Date(iso).getTime();
  if (isNaN(d)) return iso;
  const diff = Date.now() - d;
  const s = Math.floor(diff / 1000);
  if (s < 5) return "just now";
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

export function statusTone(status: number): string {
  if (status === 0) return "text-slate-400";
  if (status < 300) return "text-accent-green";
  if (status < 400) return "text-accent-cyan";
  if (status < 500) return "text-accent-amber";
  return "text-accent-red";
}

// Pretty-print JSON if it looks like JSON, else return as-is.
export function prettyJson(s: string): string {
  if (!s) return "";
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return salvageJson(s);
  }
}

// salvageJson best-effort formats truncated or slightly-damaged JSON for the
// log drawer. Stored bodies are cut at MaxBodyLogBytes (usually mid-string,
// with a trailing … marker), so JSON.parse fails exactly when the body is
// biggest and hardest to read raw. This walks the text token-aware — strings
// (with escapes) pass through untouched, structural characters get newlines +
// indentation — and stops gracefully at the cut point instead of inventing
// closing brackets. Plaintext placeholders like "[streamed]" pass through.
function salvageJson(s: string): string {
  // Bare placeholder text, not JSON with damage: leave it alone. Same for
  // anything that never opened a JSON structure — the tolerant walk below
  // only makes sense on inputs that start like JSON.
  const trimmed = s.trim();
  if (/^\[[^\[\]{}\"]+\]$/.test(trimmed)) return s;
  if (!trimmed.startsWith("{") && !trimmed.startsWith("[")) return s;
  // The backend truncation marker carries no structure: drop it, then retry
  // the canonical path in case the cut landed on a clean boundary.
  const uncut = s.replace(/…\s*$/, "");
  try {
    return JSON.stringify(JSON.parse(uncut), null, 2);
  } catch {
    /* fall through to the tolerant walk */
  }
  let out = "";
  let indent = 0;
  let inStr = false;
  let esc = false;
  const pad = () => "\n" + "  ".repeat(indent);
  for (const c of uncut) {
    if (inStr) {
      out += c;
      if (esc) esc = false;
      else if (c === "\\") esc = true;
      else if (c === '"') inStr = false;
      continue;
    }
    switch (c) {
      case '"':
        inStr = true;
        out += c;
        break;
      case "{":
      case "[":
        out += c;
        indent++;
        out += pad();
        break;
      case "}":
      case "]":
        indent = Math.max(0, indent - 1);
        out += pad() + c;
        break;
      case ",":
        out += c + pad();
        break;
      case ":":
        out += ": ";
        break;
      default:
        // Outside strings, whitespace is re-flowed and raw control
        // characters (the usual parse-killer in tool-result text) are
        // dropped; string content above passes through byte-identical.
        if (c === " " || c === "\n" || c === "\r" || c === "\t") break;
        if (c < " ") break;
        out += c;
    }
  }
  return out;
}
