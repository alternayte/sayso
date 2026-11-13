// Review round 2, findings 1 and 2: bypasses of the round 1 confinement.
import { mkdirSync, mkdtempSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { testFiles } from "../../../agent/src/paths.js";
import { confine } from "../../../agent/src/session.js";

function work() {
  const root = mkdtempSync(join(tmpdir(), "sayso-r2-"));
  const head = join(root, "head");
  mkdirSync(head);
  const outside = mkdtempSync(join(tmpdir(), "sayso-r2-out-"));
  writeFileSync(join(outside, "a.txt"), "SAYSO_SECRET_KEY=secret");
  return { root, head, outside };
}

describe("review round 2", () => {
  it("refuses the @ and ~ forms that pi expands", () => {
    const w = work();
    const home = process.env.HOME;
    process.env.HOME = w.outside;
    try {
      for (const path of ["@/etc/passwd", "@../../x", "~/a.txt", "~"])
        expect(
          confine({ toolName: "read", input: { path } }, w.head, w.root),
          path,
        ).toBeDefined();
    } finally {
      process.env.HOME = home;
    }
  });

  it("refuses a spelling variant of the name that is a symlink out", () => {
    const w = work();
    symlinkSync(join(w.outside, "a.txt"), join(w.head, "it’s"));
    expect(
      confine({ toolName: "read", input: { path: "it's" } }, w.head, w.root),
    ).toBeDefined();
  });

  it("uploads nothing when .sayso-tests itself is a symlink", () => {
    const w = work();
    symlinkSync(w.outside, join(w.head, ".sayso-tests"));
    expect(testFiles(w.head, 1 << 20)).toEqual([]);
  });

  it("uploads the plain files of .sayso-tests", () => {
    const w = work();
    mkdirSync(join(w.head, ".sayso-tests", "go"), { recursive: true });
    writeFileSync(join(w.head, ".sayso-tests", "go", "out.txt"), "ok");
    expect(testFiles(w.head, 1 << 20)).toEqual(["go/out.txt"]);
  });
});
