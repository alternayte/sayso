import { gzipSync } from "node:zlib";
import type { Complete, Event, HeartbeatBody, Spec } from "./protocol/index.js";

/** Batch limits of the event stream (§8.8). */
export const batchSize = 50;
export const batchIntervalMs = 2000;

type Fetch = typeof fetch;

/** RunnerClient calls the runner API with the run token (Appendix B). */
export class RunnerClient {
  constructor(
    private readonly apiURL: string,
    private readonly token: string,
    private readonly http: Fetch = fetch,
  ) {}

  private async call(method: string, path: string, body?: string | Buffer, contentType = "application/json"): Promise<Response> {
    const headers: Record<string, string> = { Authorization: `Bearer ${this.token}` };
    if (body !== undefined) headers["Content-Type"] = contentType;
    let last: unknown;
    for (let attempt = 0; attempt < 4; attempt++) {
      try {
        const res = await this.http(`${this.apiURL}/api/runner/v1${path}`, { method, headers, body });
        if (res.status >= 500) {
          last = new Error(`${method} ${path}: status ${res.status}`);
        } else {
          if (!res.ok) throw new RunnerError(res.status, `${method} ${path}: status ${res.status}: ${await res.text()}`);
          return res;
        }
      } catch (e) {
        if (e instanceof RunnerError) throw e;
        last = e;
      }
      await new Promise((r) => setTimeout(r, 250 * 2 ** attempt));
    }
    throw last;
  }

  async spec(): Promise<Spec> {
    return (await (await this.call("GET", "/spec")).json()) as Spec;
  }

  async events(events: Event[]): Promise<void> {
    await this.call("POST", "/events", JSON.stringify({ events }));
  }

  async artifact(name: string, data: Uint8Array | string, contentType = "application/octet-stream"): Promise<void> {
    await this.call("PUT", `/artifacts/${name}`, typeof data === "string" ? data : Buffer.from(data), contentType);
  }

  async heartbeat(): Promise<HeartbeatBody> {
    return (await (await this.call("POST", "/heartbeat")).json()) as HeartbeatBody;
  }

  async complete(c: Complete): Promise<void> {
    await this.call("POST", "/complete", JSON.stringify(c));
  }
}

export class RunnerError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

export type NewEvent = Omit<Event, "seq" | "at" | "payload"> & { payload?: unknown };

/**
 * EventStream numbers events and sends them in batches of at most 50, or
 * every 2 seconds, whichever comes first.
 */
export class EventStream {
  private seq = 0;
  private queue: Event[] = [];
  private timer: NodeJS.Timeout | undefined;
  private sending: Promise<void> = Promise.resolve();
  private failure: unknown;

  constructor(
    private readonly send: (events: Event[]) => Promise<void>,
    private readonly now: () => Date = () => new Date(),
  ) {}

  emit(e: NewEvent): void {
    const { payload, ...rest } = e;
    const ev: Event = { ...rest, seq: ++this.seq, at: this.now().toISOString() };
    if (ev.preview && ev.preview.length > 500) ev.preview = ev.preview.slice(0, 500);
    if (payload !== undefined) ev.payload = gzipSync(JSON.stringify(payload)).toString("base64");
    this.queue.push(ev);
    if (this.queue.length >= batchSize) this.flushSoon();
    else if (!this.timer) this.timer = setTimeout(() => this.flushSoon(), batchIntervalMs);
  }

  private flushSoon(): void {
    if (this.timer) clearTimeout(this.timer);
    this.timer = undefined;
    while (this.queue.length > 0) {
      const batch = this.queue.splice(0, batchSize);
      this.sending = this.sending.then(() => this.send(batch)).catch((err: unknown) => {
        this.failure = err;
      });
    }
  }

  /** flush sends the remaining events and waits for every batch. */
  async flush(): Promise<void> {
    this.flushSoon();
    await this.sending;
    if (this.failure) throw this.failure;
  }
}
