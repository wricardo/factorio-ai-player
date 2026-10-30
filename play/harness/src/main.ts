import { mkdir } from "node:fs/promises";
import { readFileSync, rmSync, writeFileSync, writeSync } from "node:fs";
import { join } from "node:path";
import { randomUUID } from "node:crypto";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { type AssistantMessage } from "@earendil-works/pi-ai";
import { createModels } from "@earendil-works/pi-ai/models";
import { opencodeGoProvider } from "@earendil-works/pi-ai/providers/opencode-go";
import {
	AssistantEntry,
	type Conversation,
	type ConversationId,
	type EntryId,
	createRegistry,
	defineDoc,
	defineExtension,
	Harness,
	LiveDoc,
} from "@earendil-works/pi-durable";
import { NodeExecutionEnv } from "@earendil-works/pi-durable/env/node";
import { openNodeSqliteStorage } from "@earendil-works/pi-durable/storage/sqlite/node";
import { createReadTool } from "@earendil-works/pi-durable/tools";
import { bdExtension } from "./bd.ts";
import { AGENTS_DIR, BD_CWD, loadApiKey, MODEL, STORAGE_PATH } from "./config.ts";
import { dispatcher, ensureDispatcher } from "./dispatcher.ts";
import { factoryRead, factoryWrite, laws } from "./factory.ts";
import { workerExtension, Workers } from "./workers.ts";
import { chat } from "./chat.ts";
import { ui } from "./web.ts";

const context = BACKGROUND_CONTEXT;

/** The foreman outlives restarts; its conversation id is session state, not a name to guess. */
const ForemanDoc = defineDoc<{ conversationId?: ConversationId }>({
	kind: "app.foreman",
	version: 1,
	scope: "session",
	initial: () => ({}),
	checkpointWhen: () => false,
});

const readOnly = defineExtension({ name: "read", tools: [createReadTool()] });

/** What a spawned worker conversation selects: it can mutate the world and comment, never close a ticket. */
const workerExtensions = [factoryWrite, bdExtension("worker"), laws.worker, readOnly];

/**
 * Pi Durable allows one writer per storage. Two harness processes on one SQLite file corrupt entry
 * ids and kill both sessions, so the lock is taken before the storage is opened and checked for a
 * live owner.
 */
function claimWriterLock(): string {
	const path = join(AGENTS_DIR, "writer.lock");
	try {
		writeFileSync(path, String(process.pid), { flag: "wx" });
	} catch {
		const owner = Number(readFileSync(path, "utf8").trim());
		let alive = false;
		try {
			process.kill(owner, 0);
			alive = owner !== process.pid;
		} catch {
			alive = false;
		}
		if (alive) {
			throw new Error(
				`another harness process (pid ${owner}) owns ${STORAGE_PATH}. One writer per storage.` +
					" Stop it with `just agents-down`, or use the console it already serves." ,
			);
		}
		writeFileSync(path, String(process.pid));
	}
	process.on("exit", () => {
		try {
			if (readFileSync(path, "utf8").trim() === String(process.pid)) rmSync(path);
		} catch {
			// already gone
		}
	});
	return path;
}

async function openHarness() {
	process.env.OPENCODE_API_KEY = loadApiKey();
	await mkdir(AGENTS_DIR, { recursive: true });
	claimWriterLock();

	const models = createModels();
	models.setProvider(opencodeGoProvider());

	// Every extension a conversation may select must be installed: a conversation stores names, and an
	// uninstalled name silently contributes nothing.
	const plannerExtensions = [factoryRead, bdExtension("planner"), laws.planner, readOnly];
	const foremanExtensions = [
		factoryRead,
		bdExtension("foreman"),
		laws.foreman,
		workerExtension({ extensions: workerExtensions }),
		dispatcher,
		readOnly,
	];
	const registry = createRegistry();
	for (const extension of [...plannerExtensions, ...foremanExtensions, ...workerExtensions]) registry.install(extension);

	const storage = await openNodeSqliteStorage(STORAGE_PATH);
	const harness = await Harness.open(storage, {
		models,
		registry,
		env: () => new NodeExecutionEnv({ cwd: BD_CWD }),
		// A crash loop or a task error must be visible in the operator's terminal, not silent.
		onReport: (error, info) => {
			const detail = error instanceof Error ? error.message : String(error);
			console.error(`report ${info.type}${"reason" in info ? ` (${info.reason})` : ""}: ${detail}`);
		},
		settings: {
			// Nothing is selected by default: each conversation gets exactly what it is configured with.
			extensions: [],
			compaction: { reserveTokens: 16384, keepRecentTokens: 20000, backgroundTokens: 32768 },
		},
	}, context);

	harness.closed.then((end) => {
		if (end.reason !== "failed") return;
		// Synchronous write: `process.exit` below would truncate a buffered console.error.
		writeSync(2, `harness failed: ${end.error instanceof Error ? (end.error.stack ?? end.error.message) : String(end.error)}\n`);
		process.exitCode = 1;
	});

	// Last resort: a fatal error must reach the supervisor's log even if the process exits at once.
	process.on("uncaughtException", (error) => {
		writeSync(2, `uncaught: ${error instanceof Error ? (error.stack ?? error.message) : String(error)}\n`);
		process.exit(1);
	});
	process.on("unhandledRejection", (error) => {
		writeSync(2, `unhandled rejection: ${error instanceof Error ? (error.stack ?? error.message) : String(error)}\n`);
		process.exit(1);
	});

	const planner = await harness.root(context, { agent: { model: MODEL } });
	await planner.configure({ model: MODEL, extensions: plannerExtensions }, context);

	const foremanId = (await harness.snapshot(ForemanDoc, context))?.conversationId;
	const existingForeman = foremanId === undefined ? undefined : await harness.conversation(foremanId, context);
	const foreman =
		existingForeman ??
		(await harness.createConversation(
			{ ownership: { kind: "ownerless" }, agent: { model: MODEL }, init: undefined },
			context,
		));
	if (existingForeman === undefined) {
		await harness.commit(async (tx) => {
			(await tx.doc(ForemanDoc)).conversationId = foreman.id;
		}, context);
	}
	await foreman.configure({ model: MODEL, extensions: foremanExtensions }, context);
	// Nothing runs unprompted without this: it turns `bd ready` into messages the foreman acts on.
	await ensureDispatcher(harness, foreman, context);

	return { harness, planner, foreman };
}

async function answer(conversation: Conversation, text: string): Promise<string> {
	const submission = await conversation.submit({ type: "input", content: text, requestId: randomUUID() }, context);
	const settled = await submission.wait(context);
	if (settled.status !== "done") return `[${settled.status}: ${settled.reason}]`;
	if (settled.answer === undefined) return "(no answer entry)";
	const entry = await conversation.commit((tx) => tx.entry(AssistantEntry, settled.answer as EntryId), context);
	const message = entry?.model?.[0] as AssistantMessage | undefined;
	return (message?.content ?? []).flatMap((part) => (part.type === "text" ? [part.text] : [])).join("").trim() || "(no text)";
}

async function main(argv: string[]) {
	const [command, ...rest] = argv;
	const { harness, planner, foreman } = await openHarness();
	harness.resume();
	try {
		switch (command) {
			case "ask": {
				if (rest.length === 0) throw new Error("ask needs a message");
				console.log(await answer(planner, rest.join(" ")));
				return;
			}
			case "foreman": {
				if (rest.length === 0) throw new Error("foreman needs a message");
				console.log(await answer(foreman, rest.join(" ")));
				return;
			}
			case "auto": {
				// Ask, then stay alive until nothing is runnable or sleeping: reporter tasks keep
				// delivering answers to the foreman after the first turn is answered.
				const [role, ...message] = rest;
				const conversation = role === "foreman" ? foreman : planner;
				if (role !== "foreman" && role !== "planner") throw new Error("auto needs a role: planner|foreman");
				if (message.length === 0) throw new Error("auto needs a message");
				console.log(await answer(conversation, message.join(" ")));
				for (;;) {
					const inspection = await harness.inspect(context);
					const runnable = inspection.tasks.some((task) => {
						const status = task.record.state.status;
						return status !== "terminal" && status !== "sleeping";
					});
					// Workers are conversations too: a run there is work in flight, whatever its owner task is doing.
					const workers = (await harness.snapshot(Workers, foreman.id, context))?.workers ?? {};
					const conversations = [planner.id, foreman.id, ...Object.values(workers).map((w) => w.conversationId)];
					let busy = false;
					for (const id of conversations) {
						if ((await harness.snapshot(LiveDoc, id, context))?.run !== undefined) busy = true;
					}
					if (!runnable && !busy) return;
					await new Promise((resolve) => setTimeout(resolve, 1_000));
				}
			}
			case "ui": {
				const flag = rest.indexOf("--port");
				const port = flag === -1 ? 8787 : Number(rest[flag + 1] ?? 8787);
				const server = await ui(harness, { planner, foreman }, context, port);
				// A failed session cannot serve anything: drop connections and exit for a clean restart.
				harness.closed.then((end) => {
					if (end.reason !== "failed") return;
					server.closeAllConnections?.();
					server.close();
					setTimeout(() => process.exit(1), 100);
				});
				// Keep the process (and therefore the storage owner) alive for the browser.
				await new Promise<void>((resolve) => process.on("SIGINT", () => resolve()));
				return;
			}
			case "chat": {
				const role = rest[0] === "foreman" ? "foreman" : "planner";
				await chat(harness, { planner, foreman }, role, context);
				return;
			}
			case "status": {
				const plannerLive = await harness.snapshot(LiveDoc, planner.id, context);
				const foremanLive = await harness.snapshot(LiveDoc, foreman.id, context);
				const usage = await harness.usage(context);
				console.log(`storage   ${STORAGE_PATH}`);
				console.log(`planner   ${planner.id} ${plannerLive?.run ? "working" : "idle"}`);
				console.log(`foreman   ${foreman.id} ${foremanLive?.run ? "working" : "idle"}`);
				console.log(`wake      ${JSON.stringify(harness.wake())}`);
				console.log(`usage     ${JSON.stringify(usage.models)}`);
				return;
			}
			case "resume": {
				console.log(`wake ${JSON.stringify(harness.wake())}`);
				return;
			}
			default:
				console.log(`usage: main.ts ui|chat|ask|foreman|auto|status|resume [message...]
  ui       web console on 127.0.0.1:8787 (--port N to change)
  chat     interactive console: chat|foreman [planner|foreman], /help for commands
  ask      talk to the planner (files tickets, cannot touch the world)
  foreman  talk to the foreman (claims tickets, runs workers, validates)
  auto     <planner|foreman> <message>, then stay up until work drains
  status   conversation ids, busy state, spend
  resume   continue interrupted work, print what the scheduler wants`);
				return;
		}
	} finally {
		await harness.close(context);
	}
}

await main(process.argv.slice(2)).catch((error: unknown) => {
	console.error(error instanceof Error ? (error.stack ?? error.message) : String(error));
	process.exitCode = 1;
});
