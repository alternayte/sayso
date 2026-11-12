import en from "@/copy/en.json";

export type CopyKey = keyof typeof en;

/** t returns the UI string for a key and fills {name} placeholders. */
export function t(key: CopyKey, vars?: Record<string, string | number>): string {
  let s: string = en[key];
  if (vars) for (const [k, v] of Object.entries(vars)) s = s.replaceAll(`{${k}}`, String(v));
  return s;
}
