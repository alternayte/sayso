import { readFileSync } from "node:fs";
import path from "node:path";
import { expect, test } from "vitest";
import { designTokens, tokenDiff } from "./tokens";

const root = path.resolve(import.meta.dirname, "../..");
const design = readFileSync(path.join(root, "DESIGN.md"), "utf8");
const css = readFileSync(path.join(root, "src/index.css"), "utf8");

test("SCN-UI-004 index.css has every design token with the same value", () => {
  expect(designTokens(design).size).toBe(29);
  expect(tokenDiff(design, css)).toEqual([]);
});

test("SCN-UI-004 a changed token value fails the check", () => {
  const changed = css.replace("--accent: #00e5ff;", "--accent: #00e5fe;");
  expect(tokenDiff(design, changed)).toEqual(["--accent is #00e5fe, DESIGN.md says #00e5ff"]);
});
