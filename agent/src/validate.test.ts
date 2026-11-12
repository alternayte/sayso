import { describe, expect, test } from "vitest";
import {
  downgrade,
  validateAssessment,
  validateFinding,
  type FindingInput,
  type HeadFiles,
} from "./validate.js";
import { applyChallenge, challengeCandidates } from "./stages.js";
import type { Finding } from "./protocol/index.js";

const head: HeadFiles = {
  lines: (p) =>
    p === "pkg/ledger/ledger.go"
      ? ["package ledger", "", "func Post() error {", "\treturn nil", "}"]
      : undefined,
};
const ctx = {
  head,
  ledgerIds: new Set(["S-1"]),
  execution: true,
  accepted: 0,
  maxFindings: 25,
};
const valid: FindingInput = {
  severity: "major",
  category: "correctness",
  title: "Post drops the error",
  body: "The caller ignores the error.",
  path: "pkg/ledger/ledger.go",
  start_line: 3,
  end_line: 4,
  evidence: { kind: "trace", detail: "Post returns nil on every path." },
  suggestion: "func Post() error {\n\treturn errPost",
};

describe("SCN-RUN-006", () => {
  test("SCN-RUN-006 each invalid tool input returns its reason and a valid input passes", () => {
    expect(validateFinding(valid, ctx)).toBeUndefined();
    const cases: [Partial<FindingInput>, RegExp][] = [
      [{ path: "pkg/missing.go" }, /does not exist at the head commit/],
      [{ start_line: 4, end_line: 9 }, /not in range/],
      [{ start_line: 0 }, /not in range/],
      [{ title: "x".repeat(101) }, /title has 101 characters/],
      [{ body: "x".repeat(2001) }, /body has 2001 characters/],
      [
        { evidence: { kind: "hunch", detail: "x" } },
        /evidence kind must be one of/,
      ],
      [
        { suggestion: Array.from({ length: 21 }, () => "x").join("\n") },
        /suggestion has 21 lines/,
      ],
      [
        { suggestion: "func Post() error {\n\treturn nil" },
        /does not change the lines/,
      ],
      [{ same_as: "S-9" }, /S-9 is not a finding of the ledger/],
      [{ severity: "critical" }, /severity must be one of/],
    ];
    for (const [patch, want] of cases)
      expect(validateFinding({ ...valid, ...patch }, ctx)).toMatch(want);
  });

  test("SCN-RUN-009 the 26th finding returns limit_reached", () => {
    expect(validateFinding(valid, { ...ctx, accepted: 25 })).toMatch(
      /^limit_reached/,
    );
  });

  test("SCN-RUN-014 evidence kind test is refused with execution off", () => {
    const f = {
      ...valid,
      evidence: {
        kind: "test",
        detail: "fails",
        test_path: ".sayso-tests/a_test.go",
      },
    };
    expect(validateFinding(f, ctx)).toBeUndefined();
    expect(validateFinding(f, { ...ctx, execution: false })).toMatch(
      /code execution is off/,
    );
  });

  test("SCN-RUN-006 the assessment needs 3 sentences or fewer and a known risk", () => {
    const a = {
      tldr: "One. Two. Three.",
      risk: "high",
      risk_reasons: ["money"],
      where_to_look: [],
      impact: [],
    };
    expect(validateAssessment(a)).toBeUndefined();
    expect(
      validateAssessment({ ...a, tldr: "One. Two. Three. Four." }),
    ).toMatch(/4 sentences/);
    expect(validateAssessment({ ...a, risk: "extreme" })).toMatch(
      /risk must be/,
    );
  });
});

function finding(id: string, severity: Finding["severity"]): Finding {
  return {
    candidate_id: id,
    severity,
    category: "c",
    title: id,
    body: "",
    path: "a",
    start_line: 1,
    end_line: 1,
    evidence: { kind: "trace", detail: "d" },
  };
}

describe("SCN-FND-007", () => {
  test("SCN-FND-007 refuted is withdrawn, downgraded drops one level, a downgraded nit is withdrawn", () => {
    const all = [
      finding("c1", "blocker"),
      finding("c2", "major"),
      finding("c3", "minor"),
      finding("c4", "nit"),
    ];
    expect(
      challengeCandidates(all, "minor").map((f) => f.candidate_id),
    ).toEqual(["c1", "c2", "c3"]);
    expect(
      challengeCandidates(all, "blocker").map((f) => f.candidate_id),
    ).toEqual(["c1"]);
    expect(challengeCandidates([finding("c2", "major")], "blocker")).toEqual(
      [],
    );
    expect(downgrade("blocker")).toBe("major");
    expect(downgrade("minor")).toBe("nit");
    expect(downgrade("nit")).toBe("withdrawn");
    const results = new Map([
      ["c1", { outcome: "refuted" as const, reason: "wrong" }],
      ["c2", { outcome: "downgraded" as const, reason: "smaller" }],
      ["c3", { outcome: "confirmed" as const, reason: "real" }],
      ["c4", { outcome: "downgraded" as const, reason: "style only" }],
    ]);
    const out = applyChallenge(all, results);
    expect(
      out.map((f) => [f.candidate_id, f.severity, f.challenge?.outcome]),
    ).toEqual([
      ["c1", "blocker", "refuted"],
      ["c2", "minor", "downgraded"],
      ["c3", "minor", "confirmed"],
      ["c4", "nit", "refuted"],
    ]);
  });
});
