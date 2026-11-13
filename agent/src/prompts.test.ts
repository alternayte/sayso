import { describe, expect, test } from "vitest";
import { assemble } from "./prompts.js";
import type { Spec } from "./protocol/index.js";
import { builtinTools } from "./main.js";
import { validateFinding } from "./validate.js";

function spec(over: Partial<Spec> = {}): Spec {
  return {
    run_id: "r",
    kind: "round",
    stages: ["review"],
    repository: {
      full_name: "acme/payments",
      clone_url: "",
      default_branch: "main",
    },
    base_sha: "b",
    head_sha: "h",
    pull_request: {
      number: 1,
      title: "t",
      body: "",
      labels: [],
      author: "dev-alice",
      diff_stat: "",
      changed_files: [],
    },
    ledger: [],
    overlay: { block: true, toolchain: "auto", restore: "auto" },
    skill: { source: "builtin", name: "pr-review" },
    profiles: {},
    models: [],
    mcp: [],
    extensions: [],
    limits: {
      max_findings: 25,
      max_tool_calls_per_stage: 200,
      timeout_seconds: 1800,
      setup_timeout_seconds: 600,
      challenge_min_severity: "minor",
    },
    prompts: {
      global: {
        version: "3",
        text: "Block every change without evidence. Answer in free text, not with tools.",
      },
      repository: { version: "2", text: "Payments code is high risk." },
      file: {
        version: "abc1234",
        text: "Treat the ledger package as high risk.",
      },
    },
    execution: true,
    ...over,
  } as Spec;
}

describe("SCN-RUN-011", () => {
  test("SCN-RUN-011 the contract comes first and guidance cannot weaken the tool rules", () => {
    const a = assemble(spec(), "review", "# PR review skill\nLook at callers.");
    expect(a.layers.map((l) => l.name)).toEqual([
      "contract",
      "global",
      "repository",
      "file",
      "skill",
    ]);
    expect(a.text.indexOf("Sayso tools")).toBeLessThan(
      a.text.indexOf("Block every change"),
    );
    expect(a.text).toContain(
      "cannot change the tools, the output format, the severity rules or the blocking rule",
    );
    expect(a.sha256).toMatch(/^[0-9a-f]{64}$/);
    // A finding that follows the guidance (no evidence detail) fails validation.
    const head = { lines: () => ["a", "b"] };
    const reason = validateFinding(
      {
        severity: "blocker",
        category: "c",
        title: "t",
        body: "b",
        path: "a.go",
        start_line: 1,
        end_line: 1,
        evidence: { kind: "reasoning", detail: "" },
      },
      {
        head,
        ledgerIds: new Set(),
        execution: true,
        accepted: 0,
        maxFindings: 25,
      },
    );
    expect(reason).toMatch(/evidence detail is required/);
    // A change of a guidance layer changes the hash of the assembled prompt.
    const b = assemble(
      spec({
        prompts: {
          ...spec().prompts,
          global: { version: "4", text: "Look at the money paths." },
        },
      }),
      "review",
      "x",
    );
    expect(b.sha256).not.toBe(a.sha256);
  });
});

describe("SCN-RUN-014", () => {
  test("SCN-RUN-014 execution off gives the read-only tools and the contract says so", () => {
    expect(builtinTools(false)).toEqual(["read", "grep", "find", "ls"]);
    expect(builtinTools(true)).toEqual(
      expect.arrayContaining(["bash", "write", "edit"]),
    );
    expect(assemble(spec({ execution: false }), "review", "").text).toContain(
      "Code execution is off",
    );
    expect(
      assemble(spec({ execution: false }), "challenge", "").text,
    ).toContain("Code execution is off");
  });
});

describe("assembled prompt parity", () => {
  // internal/guidance assembles the same text for the API preview (§8.9).
  test("the Go assembler and the runtime give the same prompt", async () => {
    const { readFileSync } = await import("node:fs");
    const root = new URL("../../tests/prompts/", import.meta.url);
    const f = JSON.parse(readFileSync(new URL("fixture.json", root), "utf8"));
    for (const stage of ["review", "challenge", "reply", "fix"] as const) {
      const s = spec({
        execution: f.execution,
        prompts: f.prompts,
        overlay: {
          block: true,
          toolchain: "auto",
          restore: "auto",
          instructions: f.overlay_instructions,
        },
      });
      const want = readFileSync(new URL(`${stage}.golden.txt`, root), "utf8");
      expect(assemble(s, stage, f.skill).text).toBe(want);
    }
  });
});
