import { realpathSync } from "node:fs";
import { basename, dirname, resolve, sep } from "node:path";

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
