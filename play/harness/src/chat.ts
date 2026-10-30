import { createInterface } from "node:readline";
import type { Context } from "@earendil-works/chord";
import type { AssistantMessage } from "@earendil-works/pi-ai";
import { type Conversation, type EntryRecord, type Harness, LiveDoc, type LiveState } from "@earendil-works/pi-durable";
import { Workers } from "./workers.ts";

type Role = "planner" | "foreman";

const COMMANDS: Record<string, string> = {
	"/planner": "talk to the planner (files tickets, cannot touch the world)",
	"/foreman": "talk to the foreman (claims tickets, runs workers, validates)",
	"/history": "replay the last 10 turns of the current conversation (/history 30)",
	"/status": "conversation ids, live task count, spend",
	"/tasks": "live tasks, their status and owner",
	"/workers": "one line per worker: ticket, state, conversation",
	"/abort": "stop the current run; the conversation stays usable",
	"/help": "this list",
	"/clear": "start a new context: older entries stay in storage",
	"/quit": "close the harness and exit",
};

/** Text parts of one model message. A streamed partial is a JSON representation, so nothing may assume an array. */
function textOf(message: unknown): string {
	const content = (message as { content?: unknown } | undefined)?.content;
	if (!Array.isArray(content)) return "";
	return content
		.map((part) => (part !== null && typeof part === "object" && "type" in part && part.type === "text" && "text" in part ? String(part.text) : ""))
		.join("");
}

function callsOf(message: unknown): string[] {
	const content = (message as { content?: unknown } | undefined)?.content;
	if (!Array.isArray(content)) return [];
	return content.flatMap((part) =>
		part !== null && typeof part === "object" && "type" in part && part.type === "toolCall" && "name" in part
			? [`${String(part.name)} ${JSON.stringify("arguments" in part ? part.arguments : {}).slice(0, 160)}`]
			: [],
	);
}

/**
 * Interactive console over the durable conversations: streamed answers, live tool calls, steer
 * into a running turn, `/` commands, worker inspection. Everything it shows is committed state,
 * so a reconnect replays from the transcript.
 */
export async function chat(harness: Harness, roles: Record<Role, Conversation>, initial: Role, context: Context) {
	let current: Role = initial;
	let attached = await roles[current].viewState(context);
	let lastEntry = 0;
	let streamed = "";
	let busy = false;

	const printEntry = (entry: EntryRecord) => {
		const message = entry.model?.[0];
		if (!message || !("role" in message)) return;
		if (message.role === "assistant") {
			const text = textOf(message);
			// The last streamed partial usually lags the committed entry by a few words: print only the tail.
			const shown = streamed.trim();
			if (shown.length === 0) {
				if (text.length > 0) console.log(text);
				else process.stdout.write("\n");
			} else if (text.startsWith(shown)) {
				process.stdout.write(`${text.slice(shown.length)}\n`);
			} else {
				process.stdout.write("\n");
				if (text.length > 0) console.log(text);
			}
			streamed = "";
			for (const call of callsOf(message)) console.log(`  → ${call}`);
			return;
		}
		if (message.role === "toolResult") {
			console.log(`  ← ${message.toolName} ${JSON.stringify(message.content ?? "").slice(0, 300)}`);
		}
	};

	const render = (value: typeof attached.value) => {
		for (const entry of value.entries) {
			if (entry.id <= lastEntry) continue;
			lastEntry = entry.id;
			printEntry(entry);
		}
		const live = value.docs["pi.live"] as LiveState | undefined;
		busy = live?.run !== undefined;
		const partial = textOf(live?.generation?.message);
		if (partial.length > streamed.length) process.stdout.write(partial.slice(streamed.length));
		else if (partial.length < streamed.length && streamed.length > 0) process.stdout.write("\n");
		streamed = partial;
	};

	const status = async () => {
		const inspection = await harness.inspect(context);
		const workers = (await harness.snapshot(Workers, roles.foreman.id, context))?.workers ?? {};
		const usage = await harness.usage(context);
		console.log(`planner ${roles.planner.id}   foreman ${roles.foreman.id}   talking to ${current}`);
		console.log(`tasks   ${inspection.tasks.length} live, ${inspection.submissions.length} submissions`);
		console.log(`workers ${Object.keys(workers).join(", ") || "(none)"}`);
		console.log(`wake    ${JSON.stringify(harness.wake())}`);
		console.log(`spend   ${JSON.stringify(usage.models)}`);
	};

	const listTasks = async () => {
		const inspection = await harness.inspect(context);
		if (inspection.tasks.length === 0) console.log("(no live tasks)");
		for (const task of inspection.tasks) {
			const record = task.record;
			console.log(`${record.kind} ${record.state.status} owner=${record.owner ?? `conversation ${record.conversationId}`}`);
		}
	};

	const listWorkers = async () => {
		const workers = (await harness.snapshot(Workers, roles.foreman.id, context))?.workers ?? {};
		const entries = Object.entries(workers);
		if (entries.length === 0) console.log("(no workers)");
		for (const [ticket, worker] of entries) {
			const live = await harness.snapshot(LiveDoc, worker.conversationId, context);
			console.log(`${ticket} ${live?.run ? "working" : "idle"} conversation=${worker.conversationId}`);
		}
	};

	const history = async (limit: number) => {
		const page = await roles[current].entries({ order: "descending" }, limit, undefined, context);
		for (const entry of [...page.items].reverse()) printEntry(entry);
	};

	const switchTo = async (role: Role) => {
		if (role === current) return;
		current = role;
		attached.dispose();
		attached = await roles[role].viewState(context);
		lastEntry = 0;
		streamed = "";
		attached.subscribe(render);
		render(attached.value);
		console.log(`[now talking to ${role}]`);
	};

	async function handle(line: string): Promise<boolean> {
		if (!line.startsWith("/")) {
			const draft = busy
				? ({ type: "input", content: line, whenBusy: "steer" } as const)
				: ({ type: "input", content: line } as const);
			process.stdout.write(`\n${current}> `);
			try {
				const submission = await roles[current].submit(draft, context);
				await submission.wait(context);
			} catch (error) {
				console.error(`\nerror: ${error instanceof Error ? error.message : String(error)}`);
			}
			return true;
		}
		switch (line.split(/\s+/)[0]) {
			case "/quit":
				return false;
			case "/planner":
				await switchTo("planner");
				return true;
			case "/foreman":
				await switchTo("foreman");
				return true;
			case "/clear":
				await roles[current].reset(undefined, context);
				lastEntry = 0;
				streamed = "";
				console.log("[new context: the earlier transcript is still in storage]");
				return true;
			case "/abort":
				await roles[current].abort(context);
				console.log("[interrupted]");
				return true;
			case "/history":
				await history(Number(line.split(/\s+/)[1] ?? 10) || 10);
				return true;
			case "/status":
				await status();
				return true;
			case "/tasks":
				await listTasks();
				return true;
			case "/workers":
				await listWorkers();
				return true;
			case "/help":
				for (const [command, description] of Object.entries(COMMANDS)) console.log(`${command.padEnd(10)} ${description}`);
				return true;
			default:
				console.log(`unknown command ${line.split(/\s+/)[0]}; /help`);
				return true;
		}
	}

	const newest = attached.value.entries.at(-1)?.id ?? 0;
	lastEntry = newest;
	console.log(
		`talking to ${current} (conversation ${roles[current].id}, ${attached.value.entries.length} active entries).` +
			" /help for commands, /history to replay, Ctrl-C interrupts a run.",
	);
	attached.subscribe(render);

	const rl = createInterface({ input: process.stdin, output: process.stdout, terminal: true, prompt: "you> " });
	rl.prompt();
	let running: Promise<void> = Promise.resolve();
	rl.on("line", (raw) => {
		const line = raw.trim();
		if (line.length === 0) {
			rl.prompt();
			return;
		}
		running = running.then(async () => {
			const keepGoing = await handle(line);
			if (!keepGoing) rl.close();
			else rl.prompt();
		});
	});
	rl.on("SIGINT", () => {
		if (busy) {
			void roles[current].abort(context).then(() => console.log("\n[interrupted]"));
			return;
		}
		rl.close();
	});

	await new Promise<void>((resolve) => rl.on("close", () => resolve()));
	await running;
	attached.dispose();
}
