/** designTokens reads the token table of DESIGN.md: colours, effects, shape and motion. */
export function designTokens(design: string): Map<string, string> {
  const block = design.slice(design.indexOf("```yaml"), design.indexOf("```", design.indexOf("```yaml") + 7));
  const out = new Map<string, string>();
  for (const m of block.matchAll(/([a-z-]+): "([^"]+)"/g)) {
    if (m[1] !== "sans" && m[1] !== "mono") out.set(m[1]!, m[2]!);
  }
  const shape = block.match(/shape:\s+radius (\S+), radius-control (\S+), radius-pill (\S+)/);
  if (shape) {
    out.set("radius", shape[1]!);
    out.set("radius-control", shape[2]!);
    out.set("radius-pill", shape[3]!);
  }
  const motion = block.match(
    /motion:\s+fast (\S+), base (\S+), slow (\S+)\s+ease-out (cubic-bezier\([^)]+\)), ease-in-out (cubic-bezier\([^)]+\))/,
  );
  if (motion) {
    out.set("fast", motion[1]!);
    out.set("base", motion[2]!);
    out.set("slow", motion[3]!);
    out.set("ease-out", motion[4]!);
    out.set("ease-in-out", motion[5]!);
  }
  return out;
}

/** rootTokens reads the custom properties of the first :root block of a stylesheet. */
export function rootTokens(css: string): Map<string, string> {
  const start = css.indexOf(":root {");
  const block = css.slice(start, css.indexOf("}", start));
  const out = new Map<string, string>();
  for (const m of block.matchAll(/--([a-z-]+):\s*([^;]+);/g)) out.set(m[1]!, m[2]!.trim());
  return out;
}

/** normalize makes two spellings of one CSS value equal: spaces, case and 0.10 against 0.1. */
export function normalize(v: string): string {
  return v
    .toLowerCase()
    .replace(/\s+/g, "")
    .replace(/(\d\.\d*?)0+(?=[^\d]|$)/g, "$1")
    .replace(/\.(?=[^\d])/g, "");
}

/** tokenDiff lists each design token that the stylesheet lacks or changes. */
export function tokenDiff(design: string, css: string): string[] {
  const want = designTokens(design);
  const got = rootTokens(css);
  const out: string[] = [];
  for (const [k, v] of want) {
    const g = got.get(k);
    if (g === undefined) out.push(`--${k} is missing`);
    else if (normalize(g) !== normalize(v)) out.push(`--${k} is ${g}, DESIGN.md says ${v}`);
  }
  return out;
}
