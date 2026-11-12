/** Words that UI copy does not use: marketing tone, filler and blame. */
export const bannedWords = [
  "simply",
  "just",
  "easily",
  "easy",
  "seamless",
  "seamlessly",
  "powerful",
  "awesome",
  "amazing",
  "oops",
  "whoops",
  "please",
  "sorry",
  "unfortunately",
  "obviously",
  "basically",
  "magic",
  "blazing",
  "revolutionary",
  "leverage",
  "utilize",
  "robust",
];

export const maxWords = 20;

/** lintCopy returns one message per rule that a string breaks. */
export function lintCopy(key: string, text: string): string[] {
  const out: string[] = [];
  const sentences = text.split(/(?<=[.!?])\s+/).filter((s) => s.trim() !== "");
  for (const s of sentences) {
    const words = s.trim().split(/\s+/).length;
    if (words > maxWords) out.push(`${key}: a sentence has ${words} words, the limit is ${maxWords}`);
  }
  const lower = text.toLowerCase();
  for (const w of bannedWords) {
    if (new RegExp(`\\b${w}\\b`).test(lower)) out.push(`${key}: banned word "${w}"`);
  }
  if (text.includes("!")) out.push(`${key}: no exclamation marks`);
  return out;
}
