import { gunzipSync } from "node:zlib";
import { expect, test, vi } from "vitest";
import { EventStream } from "./api.js";
import type { Event } from "./protocol/index.js";

test("SCN-RUN-008 120 events reach the API in 3 batches or more, in order", async () => {
  const batches: Event[][] = [];
  const s = new EventStream(async (b) => void batches.push(b));
  for (let i = 0; i < 120; i++)
    s.emit({
      type: "tool_call",
      name: `t${i}`,
      preview: "x".repeat(600),
      payload: { i },
    });
  await s.flush();
  expect(batches.length).toBeGreaterThanOrEqual(3);
  expect(batches.every((b) => b.length <= 50)).toBe(true);
  const all = batches.flat();
  expect(all.map((e) => e.seq)).toEqual(
    Array.from({ length: 120 }, (_, i) => i + 1),
  );
  expect(all[0]!.preview!.length).toBe(500);
  expect(
    JSON.parse(gunzipSync(Buffer.from(all[7]!.payload!, "base64")).toString()),
  ).toEqual({ i: 7 });
});

test("SCN-RUN-008 a partial batch goes out after 2 seconds", async () => {
  vi.useFakeTimers();
  const batches: Event[][] = [];
  const s = new EventStream(async (b) => void batches.push(b));
  s.emit({ type: "note", name: "one" });
  expect(batches.length).toBe(0);
  await vi.advanceTimersByTimeAsync(2000);
  expect(batches.length).toBe(1);
  vi.useRealTimers();
});
