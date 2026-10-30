import { execFile } from "node:child_process";
import { readFileSync } from "node:fs";
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { join } from "node:path";
import type { Context } from "@earendil-works/chord";
import {
	type Conversation,
	type ConversationId,
	type EntryRecord,
	type Harness,
	InboxDoc,
	LiveDoc,
	type LiveState,
} from "@earendil-works/pi-durable";
import { BD_CWD, HARNESS_DIR } from "./config.ts";
import { Workers } from "./workers.ts";

type Roles = { planner: Conversation; foreman: Conversation };
type Role = "planner" | "foreman";

const ENTRY_LIMIT = 200;

type WireEntry = {
	id: number;
	role: string;
	text: string;
	toolName?: string;
	toolArgs?: string;
	toolResult?: string;
};

type WireConversation = {
	role: string;
	label: string;
	conversationId: number;
	busy: boolean;
	streamingText?: string;
	streamingThinking?: string;
	entries: WireEntry[];
	/** Id of the newest `pi.reset` entry: the client clears its transcript when this changes. */
	epoch: number;
	inbox: number;
};

const HTML = readFileSync(join(HARNESS_DIR, "src", "ui.html"), "utf8");

/** One chat-part kind of a model message: a streamed partial is JSON, so nothing may assume an array. */
function partsOf(message: unknown, kind: "text" | "thinking"): string {
	const content = (message as { content?: unknown } | undefined)?.content;
	if (!Array.isArray(content)) return "";
	return content
		.map((part) =>
			part !== null && typeof part === "object" && "type" in part && part.type === kind && "text" in part
				? String(part.text)
				: part !== null && typeof part === "object" && "type" in part && part.type === kind && "thinking" in part
					? String((part as { thinking: unknown }).thinking)
					: "",
		)
		.join("");
}

/** Text parts of a model message. */
function textOf(message: unknown): string {
	const content = (message as { content?: unknown } | undefined)?.content;
	if (!Array.isArray(content)) return "";
	return content
		.map((part) => (part !== null && typeof part === "object" && "type" in part && part.type === "text" && "text" in part ? String(part.text) : ""))
		.join("");
}

function wireEntry(entry: EntryRecord): WireEntry | undefined {
	const message = entry.model?.[0];
	if (!message || !("role" in message)) return undefined;
	if (message.role === "assistant") {
		const calls = (Array.isArray(message.content) ? message.content : []).flatMap((part) =>
			part.type === "toolCall" ? [{ name: part.name, args: JSON.stringify(part.arguments).slice(0, 400) }] : [],
		);
		const first = calls[0];
		return {
			id: entry.id,
			role: "assistant",
			text: textOf(message),
			...(first === undefined ? {} : { toolName: calls.map((call) => call.name).join(", "), toolArgs: first.args }),
		};
	}
	if (message.role === "toolResult") {
		return {
			id: entry.id,
			role: "toolResult",
			text: "",
			toolName: message.toolName,
			toolResult: JSON.stringify(message.content ?? "").slice(0, 4_000),
		};
	}
	return { id: entry.id, role: message.role, text: textOf(message) };
}

/** One `bd --json` call, resolving `[]` on any failure: the panel is a convenience, not a contract. */
function runBd(args: string[], timeoutMs = 20_000): Promise<unknown> {
	return new Promise<unknown>((resolve) => {
		execFile("bd", ["--json", ...args], { cwd: BD_CWD, timeout: timeoutMs, maxBuffer: 8 * 1024 * 1024 }, (error, stdout) => {
			if (error) {
				resolve([]);
				return;
			}
			try {
				resolve(JSON.parse(stdout));
			} catch {
				resolve([]);
			}
		});
	});
}

/** Reading `bd` for the ticket panel. Cached: the panel does not need per-request freshness. */
function tickets(): Promise<unknown> {
	return Promise.all([runBd(["ready", "--limit", "20"]), runBd(["list", "--all", "--limit", "20"])]).then(([ready, list]) => ({
		ready,
		list,
	}));
}

/** One ticket. Comments are a separate request: `bd comments` can take many seconds on this workspace. */
async function ticketDetail(id: string) {
	const show = await runBd(["show", id]);
	return { issue: (Array.isArray(show) ? show[0] : show) ?? {} };
}

async function ticketComments(id: string) {
	const comments = await runBd(["comments", id], 90_000);
	return { comments: Array.isArray(comments) ? comments : [] };
}

/**
 * A web control surface over the same harness process. It owns nothing: every action is a
 * `submit` or `abort` on a durable conversation, so the page is a view over committed state.
 */
export async function ui(harness: Harness, roles: Roles, context: Context, port: number) {
	// `bd` on this workspace takes seconds to tens of seconds per call, so nothing in the request path
	// waits for it: a background refresher owns the cache and the snapshot serves whatever it holds.
	let ticketValue: unknown = { ready: [], list: [] };
	let ticketAt = 0;
	let refreshing = false;
	const TICKETS_TTL_MS = 8_000;
	const detailCache = new Map<string, { at: number; value: unknown }>();
	const commentCache = new Map<string, { at: number; value: unknown }>();
	const TICKET_DETAIL_TTL_MS = 60_000;

	const refreshTickets = async () => {
		if (refreshing) return;
		refreshing = true;
		try {
			ticketValue = await tickets();
			ticketAt = Date.now();
			// Prefetch details for the newest tickets so a click paints at once.
			const recent = (ticketValue as { list?: { id?: string }[] }).list?.slice(0, 8) ?? [];
			for (const ticket of recent) {
				if (ticket.id === undefined) continue;
				const cached = detailCache.get(ticket.id);
				if (cached !== undefined && Date.now() - cached.at < TICKET_DETAIL_TTL_MS) continue;
				detailCache.set(ticket.id, { at: Date.now(), value: await ticketDetail(ticket.id) });
			}
		} catch {
			ticketAt = Date.now();
		} finally {
			refreshing = false;
		}
	};
	void refreshTickets();
	const refreshTimer = setInterval(() => {
		if (Date.now() - ticketAt > TICKETS_TTL_MS) void refreshTickets();
	}, 2_000);
	refreshTimer.unref();


	const workerConversations = async (): Promise<{ ticket: string; conversation: Conversation | undefined }[]> => {
		const workers = (await harness.snapshot(Workers, roles.foreman.id, context))?.workers ?? {};
		const entries = await Promise.all(
			Object.entries(workers).map(async ([ticket, worker]) => ({
				ticket,
				conversation: await harness.conversation(worker.conversationId, context),
			})),
		);
		return entries;
	};

	const conversationFor = async (role: string): Promise<{ conversation: Conversation; role: string; label: string } | undefined> => {
		if (role === "planner" || role === "foreman") {
			return { conversation: roles[role], role, label: role };
		}
		if (role.startsWith("worker:")) {
			const ticket = role.slice("worker:".length);
			const workers = await harness.snapshot(Workers, roles.foreman.id, context);
			const worker = workers?.workers[ticket];
			if (!worker) return undefined;
			const conversation = await harness.conversation(worker.conversationId, context);
			return conversation === undefined ? undefined : { conversation, role, label: `worker ${ticket}` };
		}
		return undefined;
	};

	const snapshot = async () => {
		if (Date.now() - ticketAt > TICKETS_TTL_MS) void refreshTickets();
		const conversations: WireConversation[] = [];
		// Stateless reads: `viewState()` attaches a replicated source that would have to be disposed, and
		// this loop runs several times a second for every open browser tab.
		const collect = async (conversation: Conversation, role: string, label: string) => {
			const page = await conversation.entries({ order: "descending" }, ENTRY_LIMIT, undefined, context);
			const live = await harness.snapshot(LiveDoc, conversation.id, context);
			const inbox = await harness.snapshot(InboxDoc, conversation.id, context);
			conversations.push({
				role,
				label,
				conversationId: conversation.id,
				busy: live?.run !== undefined,
				...(live?.generation?.message === undefined
					? {}
					: {
							streamingText: textOf(live.generation.message),
							streamingThinking: partsOf(live.generation.message, "thinking"),
						}),
				// `entries()` is history: a reset starts a new context, and what came before it is no longer
				// what the model sees, so the panel shows only the part after the newest reset.
				entries: (() => {
					const epoch = page.items.find((entry) => entry.kind === "pi.reset")?.id ?? 0;
					return [...page.items]
						.filter((entry) => entry.id > epoch)
						.reverse()
						.map(wireEntry)
						.filter((entry): entry is WireEntry => entry !== undefined);
				})(),
				epoch: page.items.find((entry) => entry.kind === "pi.reset")?.id ?? 0,
				inbox: inbox?.items?.length ?? 0,
			});
		};
		await collect(roles.planner, "planner", "planner");
		await collect(roles.foreman, "foreman", "foreman");
		for (const { ticket, conversation } of await workerConversations()) {
			// The sidebar is narrow: the ticket's short suffix is the readable part.
			if (conversation) await collect(conversation, `worker:${ticket}`, `worker ${ticket.split("-").at(-1) ?? ticket}`);
		}
		const inspection = await harness.inspect(context);
		return {
			conversations,
			tasks: inspection.tasks.map((task) => ({
				id: String(task.record.id),
				kind: task.record.kind,
				status: task.record.state.status,
				owner: task.record.owner === undefined ? `conversation ${task.record.conversationId}` : String(task.record.owner),
			})),
			wake: harness.wake(),
			usage: await harness.usage(context),
			tickets: ticketValue,
		};
	};

	const readBody = (request: IncomingMessage) =>
		new Promise<string>((resolve, reject) => {
			let body = "";
			request.on("data", (chunk) => {
				body += chunk;
				if (body.length > 200_000) reject(new Error("body too large"));
			});
			request.on("end", () => resolve(body));
			request.on("error", reject);
		});

	const json = (response: ServerResponse, status: number, value: unknown) => {
		const body = JSON.stringify(value);
		response.writeHead(status, { "content-type": "application/json", "cache-control": "no-store" });
		response.end(body);
	};

	const server = createServer(async (request, response) => {
		const url = new URL(request.url ?? "/", `http://${request.headers.host ?? "127.0.0.1"}`);
		try {
			if (request.method === "GET" && (url.pathname === "/" || url.pathname === "/index.html")) {
				response.writeHead(200, { "content-type": "text/html; charset=utf-8", "cache-control": "no-store" });
				response.end(HTML);
				return;
			}
			if (request.method === "GET" && url.pathname === "/api/state") {
				json(response, 200, await snapshot());
				return;
			}
			if (request.method === "GET" && url.pathname === "/api/ticket") {
				const id = url.searchParams.get("id") ?? "";
				if (!/^[A-Za-z0-9._-]+$/.test(id)) return json(response, 400, { error: "bad ticket id" });
				const cached = detailCache.get(id);
				if (cached !== undefined && Date.now() - cached.at < TICKET_DETAIL_TTL_MS) return json(response, 200, cached.value);
				const value = await ticketDetail(id);
				detailCache.set(id, { at: Date.now(), value });
				return json(response, 200, value);
			}
			if (request.method === "GET" && url.pathname === "/api/ticket-comments") {
				const id = url.searchParams.get("id") ?? "";
				if (!/^[A-Za-z0-9._-]+$/.test(id)) return json(response, 400, { error: "bad ticket id" });
				const cached = commentCache.get(id);
				// Comments are append-only: a longer cache is safe and keeps the drawer responsive.
				if (cached !== undefined && Date.now() - cached.at < 60_000) return json(response, 200, cached.value);
				const value = await ticketComments(id);
				commentCache.set(id, { at: Date.now(), value });
				return json(response, 200, value);
			}
			if (request.method === "GET" && url.pathname === "/api/stream") {
				response.writeHead(200, {
					"content-type": "text/event-stream",
					"cache-control": "no-store",
					connection: "keep-alive",
				});
				let last = "";
				let closed = false;
				request.on("close", () => {
					closed = true;
				});
				const push = async () => {
					if (closed) return;
					const value = JSON.stringify(await snapshot().catch(() => undefined));
					if (value !== last) {
						last = value;
						response.write(`data: ${value}\n\n`);
					}
				};
				await push();
				const timer = setInterval(() => void push(), 400);
				request.on("close", () => clearInterval(timer));
				return;
			}
			if (request.method === "POST" && url.pathname === "/api/submit") {
				const body = JSON.parse(await readBody(request)) as { role?: string; text?: string };
				const target = await conversationFor(body.role ?? "");
				const text = (body.text ?? "").trim();
				if (!target) return json(response, 404, { error: `unknown role ${body.role}` });
				if (text.length === 0) return json(response, 400, { error: "empty message" });
				const live = await harness.snapshot(LiveDoc, target.conversation.id, context);
				const busy = live?.run !== undefined;
				const submission = await target.conversation.submit(
					busy ? { type: "input", content: text, whenBusy: "steer" } : { type: "input", content: text },
					context,
				);
				json(response, 200, { id: String(submission.id), steered: busy });
				return;
			}
			if (request.method === "POST" && url.pathname === "/api/reset") {
				const body = JSON.parse(await readBody(request)) as { role?: string; handoff?: string };
				const target = await conversationFor(body.role ?? "");
				if (!target) return json(response, 404, { error: `unknown role ${body.role}` });
				// Starts a new context. Older entries stay in storage and stay searchable; the model stops seeing them.
				await target.conversation.reset(body.handoff?.trim() || undefined, context);
				return json(response, 200, { reset: true });
			}
			if (request.method === "POST" && url.pathname === "/api/abort") {
				const body = JSON.parse(await readBody(request)) as { role?: string };
				const target = await conversationFor(body.role ?? "");
				if (!target) return json(response, 404, { error: `unknown role ${body.role}` });
				await target.conversation.abort(context);
				json(response, 200, { aborted: true });
				return;
			}
			json(response, 404, { error: "not found" });
		} catch (error) {
			json(response, 500, { error: error instanceof Error ? error.message : String(error) });
		}
	});

	await new Promise<void>((resolve, reject) => {
		server.once("error", reject);
		server.listen(port, "127.0.0.1", () => resolve());
	});
	console.log(`ui http://127.0.0.1:${port}/  (loopback only, one writer process)`);
	return server;
}

export type { ConversationId };
