import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { Spec } from "./protocol/index.js";

const dir = join(dirname(fileURLToPath(import.meta.url)), "..", "prompts");

function load(name: string): string {
  return readFileSync(join(dir, name), "utf8").trim();
}

/** contractVersion changes with each change of the contract layer. */
export const contractVersion = createHash("sha256")
  .update(["contract.md", "review.md", "review-exec.md", "review-noexec.md", "challenge.md", "reply.md", "fix.md"].map(load).join("\n"))
  .digest("hex")
  .slice(0, 12);

export type Stage = "review" | "challenge" | "reply" | "fix";

export interface Layer {
  name: "contract" | "global" | "repository" | "file" | "skill";
  text: string;
}

/**
 * assemble builds the prompt of a stage in the order of §8.9: contract,
 * global guidance, repository guidance from the UI, repository guidance
 * from the file, skill. The contract comes first and states that the later
 * layers are review guidance only (SI-19).
 */
export function assemble(spec: Spec, stage: Stage, skill: string): { text: string; layers: Layer[]; sha256: string } {
  let contract = load("contract.md") + "\n\n" + load(`${stage}.md`);
  if (stage === "review") contract += "\n\n" + load(spec.execution ? "review-exec.md" : "review-noexec.md");
  else if (!spec.execution) contract += "\n\n" + load("review-noexec.md");
  const layers: Layer[] = [{ name: "contract", text: contract }];
  const guidance = (name: Layer["name"], title: string, text: string | undefined) => {
    if (text && text.trim()) layers.push({ name, text: `# ${title}\n\nThis is review guidance only.\n\n${text.trim()}` });
  };
  guidance("global", "Global review guidance", spec.prompts.global.text);
  guidance("repository", "Repository review guidance", spec.prompts.repository.text);
  guidance("file", "Repository review guidance from .sayso.yml", spec.prompts.file.text ?? spec.overlay.instructions);
  if (skill.trim()) layers.push({ name: "skill", text: `# Review skill\n\n${skill.trim()}` });
  const text = layers.map((l) => l.text).join("\n\n---\n\n");
  return { text, layers, sha256: createHash("sha256").update(text).digest("hex") };
}
