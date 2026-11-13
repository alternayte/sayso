import { lstatSync, readdirSync, realpathSync } from "node:fs";
import { homedir } from "node:os";
import {
  basename,
  dirname,
  isAbsolute,
  join,
  relative,
  resolve,
  sep,
} from "node:path";

/**
 * realOf resolves symlinks in p. A path that does not exist yet resolves
 * through its nearest existing parent, so a write cannot go through a
 * symlinked directory either.
 */
export function realOf(p: string): string {
  const abs = resolve(p);
  try {
    return realpathSync(abs);
  } catch {
    const parent = dirname(abs);
    if (parent === abs) return abs;
    return resolve(realOf(parent), basename(abs));
  }
}

/** inside reports whether p, with symlinks resolved, is root or below it. */
export function inside(p: string, root: string): boolean {
  const r = realOf(root);
  const x = realOf(p);
  return x === r || x.startsWith(r.endsWith(sep) ? r : r + sep);
}

/**
 * toolPaths returns each path that a pi file tool can open for an input:
 * pi drops a leading "@", expands "~", and for reads also tries spelling
 * variants of the name. A guard must check all of them.
 */
export function toolPaths(input: string, cwd: string): string[] {
  let p = input.startsWith("@") ? input.slice(1) : input;
  p = p.replace(/[\u00A0\u2000-\u200A\u202F\u205F\u3000]/g, " ");
  if (p === "~") p = homedir();
  else if (p.startsWith("~/")) p = homedir() + p.slice(1);
  const abs = isAbsolute(p) ? p : resolve(cwd, p);
  const amPm = abs.replace(/ (AM|PM)\./gi, "\u202F$1.");
  return [
    ...new Set([
      abs,
      amPm,
      abs.normalize("NFD"),
      abs.replace(/'/g, "\u2019"),
      amPm.replace(/'/g, "\u2019"),
    ]),
  ];
}

/**
 * testFiles lists the files of <head>/.sayso-tests to upload, as paths
 * relative to it. The directory and its entries come from the pull
 * request, so a symlink anywhere, also the directory itself, is skipped.
 */
export function testFiles(headDir: string, maxBytes: number): string[] {
  const dir = join(headDir, ".sayso-tests");
  let st;
  try {
    st = lstatSync(dir);
  } catch {
    return [];
  }
  if (!st.isDirectory() || !inside(dir, headDir)) return [];
  const out: string[] = [];
  const walk = (d: string) => {
    for (const n of readdirSync(d).sort()) {
      const p = join(d, n);
      const s = lstatSync(p);
      if (s.isSymbolicLink() || !inside(p, dir)) continue;
      if (s.isDirectory()) walk(p);
      else if (s.isFile() && s.size < maxBytes) out.push(relative(dir, p));
    }
  };
  walk(dir);
  return out;
}
