import { existsSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";

const HERE = import.meta.dirname;

/** `play/harness` */
export const HARNESS_DIR = resolve(HERE, "..");
/** Repository root: the bd workspace and the gqlcli config live here. */
export const REPO_ROOT = resolve(HERE, "../../..");

/** bd runs against the repository's workspace, whatever the conversation's cwd is. */
export const BD_CWD = REPO_ROOT;

export const AGENTS_DIR = join(HARNESS_DIR, "agents");
export const STORAGE_PATH = join(AGENTS_DIR, "agents.sqlite");

/** The provider pi-ai ships for OpenCode Go (`opencodeGoProvider()`). */
export const PROVIDER = "opencode-go";
/** Model id as listed by `OPENCODE_GO_MODELS`; 1M context. */
export const MODEL_ID = "deepseek-v4.1-flash";
export const MODEL = { provider: PROVIDER, modelId: MODEL_ID } as const;

/**
 * The key file is 0600-ish local state at the repository root and is gitignored.
 * `OPENCODE_API_KEY` wins when set, so a rotated key needs no file edit.
 */
export function loadApiKey(): string {
	const fromEnv = process.env.OPENCODE_API_KEY?.trim();
	if (fromEnv) return fromEnv;
	const path = join(REPO_ROOT, ".opencodego-api-key");
	if (!existsSync(path)) throw new Error(`no OPENCODE_API_KEY set and ${path} does not exist`);
	const key = readFileSync(path, "utf8").trim();
	if (!key) throw new Error(`${path} is empty`);
	return key;
}
