import { execFile } from "node:child_process";
import { existsSync, mkdirSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";
import type { NewEvent } from "./api.js";
import type { SpecExtension } from "./protocol/index.js";

const run = promisify(execFile);

/** Where the agent image keeps the baked extension packages (§14.3). */
export const bakedDir = "/opt/sayso/extensions/node_modules";

/** packageName strips the version of a package spec: @scope/name@1.2.3 gives @scope/name. */
export function packageName(spec: string): string {
  const at = spec.lastIndexOf("@");
  return at > 0 ? spec.slice(0, at) : spec;
}

/**
 * loadExtensions returns the folders of the enabled extensions and sets their
 * environment values. A package that the image did not bake installs from
 * the registry now and is cached nowhere.
 */
export async function loadExtensions(
  exts: SpecExtension[],
  emit: (e: NewEvent) => void,
): Promise<string[]> {
  const paths: string[] = [];
  for (const e of exts) {
    Object.assign(process.env, e.env ?? {});
    const name = packageName(e.package);
    let dir = join(bakedDir, name);
    if (!existsSync(dir)) {
      const prefix = join(homedir(), ".sayso", "extensions");
      mkdirSync(prefix, { recursive: true });
      try {
        await run(
          "npm",
          [
            "install",
            "--no-audit",
            "--no-fund",
            "--ignore-scripts",
            "--prefix",
            prefix,
            e.package,
          ],
          { timeout: 300_000 },
        );
        dir = join(prefix, "node_modules", name);
      } catch (err) {
        emit({
          type: "error",
          name: `extension ${e.name}`,
          status: "failed",
          preview: `npm install ${e.package} failed: ${String((err as { stderr?: string }).stderr ?? err).slice(0, 300)}`,
        });
        continue;
      }
    }
    paths.push(dir);
    emit({
      type: "note",
      name: `extension ${e.name}`,
      status: "loaded",
      preview: `${e.package} from ${dir}`,
    });
  }
  return paths;
}
