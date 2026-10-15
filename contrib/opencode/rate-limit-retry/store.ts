import { mkdir, readFile, writeFile, rename, rm } from "node:fs/promises";
import { join } from "node:path";
import { randomUUID } from "node:crypto";
import type { Cooldown } from "./policy";

export class CooldownStore {
  constructor(readonly directory: string) {}
  private file(scope: string) {
    if (!/^[a-f0-9]{64}$/.test(scope)) throw new Error("Invalid cooldown scope");
    return join(this.directory, scope + ".json");
  }
  async read(scope: string): Promise<Cooldown | undefined> {
    try {
      const record = JSON.parse(await readFile(this.file(scope), "utf8"));
      if (!Number.isSafeInteger(record.until) || !Number.isSafeInteger(record.updated) ||
          !Number.isSafeInteger(record.failures) || !["quota", "rate", "transient"].includes(record.kind) ||
          typeof record.authoritative !== "boolean") throw new Error("Invalid persisted cooldown");
      return record;
    } catch (error: any) {
      if (error.code === "ENOENT") return;
      throw error; // Never fail open into an exhausted upstream.
    }
  }
  async update(scope: string, change: (old: Cooldown | undefined) => Cooldown): Promise<Cooldown> {
    const file = this.file(scope);
    await mkdir(this.directory, { recursive: true, mode: 0o700 });
    const lock = file + ".lock";
    let acquired = false;
    for (let attempt = 0; attempt < 100; attempt++) {
      try {
        await mkdir(lock, { mode: 0o700 });
        acquired = true;
        await writeFile(join(lock, "pid"), String(process.pid), { mode: 0o600 });
        break;
      } catch (error: any) {
        if (error.code !== "EEXIST") {
          if (acquired) await rm(lock, { recursive: true, force: true });
          throw error;
        }
        try {
          const pid = Number(await readFile(join(lock, "pid"), "utf8"));
          if (Number.isSafeInteger(pid) && pid > 0) {
            try { process.kill(pid, 0); }
            catch (cause: any) { if (cause.code === "ESRCH") await rm(lock, { recursive: true, force: true }); }
          }
        } catch {}
        await new Promise(resolve => setTimeout(resolve, 10));
      }
    }
    if (!acquired) throw new Error("Cooldown storage is busy; upstream retry suppressed");
    const temporary = file + "." + randomUUID() + ".tmp";
    try {
      const record = change(await this.read(scope));
      await writeFile(temporary, JSON.stringify(record), { mode: 0o600, flag: "wx" });
      await rename(temporary, file);
      return record;
    } finally {
      await rm(temporary, { force: true });
      await rm(lock, { recursive: true, force: true });
    }
  }
}
