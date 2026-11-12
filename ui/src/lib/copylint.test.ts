import { describe, expect, test } from "vitest";
import en from "@/copy/en.json";
import { lintCopy } from "./copylint";

describe("copy lint", () => {
  test("SCN-UI-003 every UI string passes the copy lint", () => {
    const problems = Object.entries(en).flatMap(([k, v]) => lintCopy(k, v));
    expect(problems).toEqual([]);
  });

  test("SCN-UI-003 a sentence of 21 words fails", () => {
    const long = Array.from({ length: 21 }, (_, i) => `word${i}`).join(" ") + ".";
    expect(lintCopy("test", long)).toHaveLength(1);
    const ok = Array.from({ length: 20 }, (_, i) => `word${i}`).join(" ") + ".";
    expect(lintCopy("test", ok)).toEqual([]);
  });

  test("SCN-UI-003 banned words fail", () => {
    expect(lintCopy("test", "Simply click the button.")).not.toEqual([]);
  });
});
