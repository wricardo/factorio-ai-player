import { type AssistantMessage, Type } from "@earendil-works/pi-ai";
import {
	AssistantEntry,
	configure,
	type ConversationId,
	type EntryId,
	type Extension,
	defineDoc,
	defineExtension,
	defineTask,
	defineTool,
	LiveDoc,
	type TaskId,
} from "@earendil-works/pi-durable";
import { MODEL } from "./config.ts";

/**
 * One worker per ticket. The conversation is owned by a background anchor task, so it
 * outlives the foreman's turn and its Esc; a reporter task delivers a message and posts
 * the answer back, both restart-safe through request ids.
 */
type Worker = {
	conversationId: ConversationId;
	/** Answers already reported: several messages can end in one answer, reported once. */
	reported: EntryId[];
};
export const Workers = defineDoc<{ workers: Record<string, Worker>; reporters: Record<string, TaskId> }>({
	kind: "app.workers",
	version: 1,
	scope: "conversation",
	history: "latest",
	fork: "initial",
	initial: () => ({ workers: {}, reporters: {} }),
	checkpointWhen: () => false,
});

/** A background task that finishes at once: it only gives the worker's conversation an owner. */
const Anchor = defineTask<null, { phase: "done" }, null>({
	name: "app.worker-anchor",
	version: 1,
	initial: () => ({ phase: "done" }),
	phases: {
		done: (_task, runtime, context) =>
			runtime.commit(() => ({ status: "terminal", outcome: { status: "completed", result: null } }), context),
	},
	abort: (_task, runtime, context) =>
		runtime.commit(() => ({ status: "terminal", outcome: { status: "aborted" } }), context),
});

type ReporterInput = { ticket: string; conversationId: ConversationId; message: string; followUp: boolean };
type ReporterState = { phase: "deliver" } | { phase: "report"; report?: string };

/** Delivers one message to a worker and reports its answer to the foreman once. */
const Reporter = defineTask<ReporterInput, ReporterState, null>({
	name: "app.worker-reporter",
	version: 1,
	initial: () => ({ phase: "deliver" }),
	phases: {
		deliver: async (reporter, runtime, context) => {
			const { ticket, conversationId, message, followUp } = reporter.input;
			const worker = await runtime.conversation(conversationId, context);
			if (!worker) throw new Error(`worker conversation ${conversationId} is gone`);
			const submission = await worker.submit(
				{ type: "input", content: message, whenBusy: followUp ? "followUp" : "steer", requestId: `worker:${reporter.id}` },
				context,
			);
			const settled = await submission.wait(context);
			await runtime.commit(async (tx) => {
				const next = (report?: string) => ({ status: "running", checkpoint: { phase: "report", report } }) as const;
				if (settled.status === "unanswered") {
					return next(settled.reason === "aborted" ? undefined : `[worker ${ticket} failed: ${settled.reason}]`);
				}
				if (settled.type !== "input" || settled.answer === undefined) return next();
				const state = await tx.doc(Workers, runtime.conversationId);
				const recorded = state.workers[ticket];
				if (!recorded || recorded.reported.includes(settled.answer)) return next();
				recorded.reported.push(settled.answer);
				const message = (await tx.entry(AssistantEntry, settled.answer))?.model?.[0] as AssistantMessage | undefined;
				const text = (message?.content ?? []).flatMap((c) => (c.type === "text" ? [c.text] : [])).join("") || "(no text)";
				return next(`[worker ${ticket} answered] ${text}`);
			}, context);
		},
		report: async (reporter, runtime, context) => {
			const report = reporter.state.checkpoint.report;
			if (report !== undefined) {
				const foreman = await runtime.conversation(runtime.conversationId, context);
				await foreman?.submit(
					{ type: "input", content: report, whenBusy: "followUp", requestId: `worker-report:${reporter.id}` },
					context,
				);
			}
			await runtime.commit(
				() => ({ status: "terminal", outcome: { status: "completed", result: null } }),
				context,
			);
		},
	},
	abort: (_reporter, runtime, context) =>
		runtime.commit(() => ({ status: "terminal", outcome: { status: "aborted" } }), context),
});

type WatchdogInput = { ticket: string; conversationId: ConversationId };
type WatchdogState = { phase: "check"; checks: number; entries: number };

/**
 * Watches a worker for a wedge. It reads the worker's active transcript and its live run: while the
 * transcript grows the worker is making progress. Three checks (3 minutes) with a busy run and no new
 * entry means the worker is stuck, so it is aborted and the foreman is told.
 */
const Watchdog = defineTask<WatchdogInput, WatchdogState, null>({
	name: "app.worker-watchdog",
	version: 1,
	initial: () => ({ phase: "check", checks: 0, entries: 0 }),
	phases: {
		check: async (task, runtime, context) => {
			const finished = () => ({ status: "terminal", outcome: { status: "completed", result: null } }) as const;
			const view = await runtime.context(task.input.conversationId, context);
			const busy = (await runtime.snapshot(LiveDoc, task.input.conversationId, context))?.run !== undefined;
			// Idle: the reporter has delivered the answer, or the worker was aborted. Either way, nothing to watch.
			if (!busy) return runtime.commit(finished, context);
			const entries = view.entries.length;
			if (entries > task.state.checkpoint.entries) {
				await runtime.commit(() => ({ status: "sleeping", until: runtime.now() + 60_000, checkpoint: { phase: "check", checks: 0, entries } }), context);
				return;
			}
			const checks = task.state.checkpoint.checks + 1;
			if (checks >= 3) {
				await (await runtime.conversation(task.input.conversationId, context))?.abort(context);
				const foreman = await runtime.conversation(runtime.conversationId, context);
				await foreman?.submit(
					{
						type: "input",
						content: `[watchdog] worker on ${task.input.ticket} made no progress for ${checks} minutes with a live run (${entries} entries) and was stopped. Check the ticket, then either respawn with a narrower brief or finish it yourself.`,
						whenBusy: "followUp",
						requestId: `watchdog:${task.id}`,
					},
					context,
				);
				return runtime.commit(finished, context);
			}
			return runtime.commit(
				() => ({ status: "sleeping", until: runtime.now() + 60_000, checkpoint: { phase: "check", checks, entries } }),
				context,
			);
		},
	},
	abort: (_task, runtime, context) =>
		runtime.commit(() => ({ status: "terminal", outcome: { status: "aborted" } }), context),
});

/**
 * The foreman's worker manager: spawn, send (steer or follow up), stop, status.
 * The foreman owns exclusivity through `bd_claim`; this extension assumes one worker per ticket.
 */
export function workerExtension(options: { extensions: readonly Extension[] }) {
	const worker = defineTool({
		name: "worker",
		description:
			"Manage the workers that execute tickets in the background. Actions: spawn(ticket, brief), send(ticket, message, followUp), stop(ticket), status(ticket?). Answers and questions are reported back to you as messages. Claim the ticket first: the claim is the mutex, and a world-changing ticket runs alone.",
		parameters: Type.Object({
			action: Type.Union([Type.Literal("spawn"), Type.Literal("send"), Type.Literal("stop"), Type.Literal("status")]),
			ticket: Type.Optional(Type.String({ description: "Ticket id" })),
			brief: Type.Optional(Type.String({ description: "Everything the worker needs: title, observed state, plan, acceptance" })),
			message: Type.Optional(Type.String()),
			followUp: Type.Optional(Type.Boolean({ description: "Queue after the current answer instead of steering" })),
		}),
		// Repeating `stop` could stop newer work, so an interrupted call is reported, not replayed.
		replay: "unsafe",
		execute: async (args, api, context) => {
			const { action, ticket, brief, message, followUp } = args;
			const state = (await api.snapshot(Workers, api.conversationId, context)) ?? { workers: {}, reporters: {} };
			const reply = (text: string, conversationId?: ConversationId) => ({
				output: [{ type: "text" as const, text }],
				...(conversationId === undefined || ticket === undefined ? {} : { details: { ticket, conversationId } }),
			});

			if (action === "status") {
				const tickets = ticket === undefined ? Object.keys(state.workers) : [ticket];
				const lines: string[] = [];
				for (const each of tickets) {
					const found = state.workers[each];
					if (!found) continue;
					const busy = (await api.snapshot(LiveDoc, found.conversationId, context))?.run !== undefined;
					lines.push(`${each}: ${busy ? "working" : "idle"} (conversation ${found.conversationId})`);
				}
				return reply(lines.length === 0 ? "No workers. Claim a ticket and spawn one." : lines.join("\n"));
			}
			if (ticket === undefined) return reply(`${action} needs a ticket id.`);
			const existing = state.workers[ticket];

			if (action === "stop") {
				if (!existing) return reply(`No worker on ${ticket}.`);
				await (await api.conversation(existing.conversationId, context))?.abort(context);
				return reply(`Stopped the worker on ${ticket}.`, existing.conversationId);
			}
			if (action === "send") {
				if (!existing) return reply(`No worker on ${ticket}; spawn one first.`);
				if (message === undefined) return reply("send needs a message.");
				await api.commit(async (tx) => {
					const doc = await tx.doc(Workers, api.conversationId);
					doc.reporters[api.taskId] = await tx.createTask(
						Reporter,
						{ ticket, conversationId: existing.conversationId, message, followUp: followUp === true },
						{ ownership: { kind: "conversation" }, background: true },
					);
				}, context);
				return reply(`Sent to the worker on ${ticket}.`, existing.conversationId);
			}

			if (existing) return reply(`${ticket} already has a worker; use send.`, existing.conversationId);
			if (brief === undefined) return reply("spawn needs a brief.");
			const conversationId = await api.commit(async (tx) => {
				const anchor = await tx.createTask(Anchor, null, { ownership: { kind: "conversation" }, background: true });
				const child = await tx.createConversation({ ownership: { kind: "task", taskId: anchor } });
				// Exactly these extensions: world-clearing gqlcli, worker-scoped bd, the worker laws.
				await configure(tx, child.id, { model: MODEL, extensions: options.extensions, instructions: brief });
				const doc = await tx.doc(Workers, api.conversationId);
				doc.workers[ticket] = { conversationId: child.id, reported: [] };
				doc.reporters[api.taskId] = await tx.createTask(
					Reporter,
					{ ticket, conversationId: child.id, message: brief, followUp: false },
					{ ownership: { kind: "conversation" }, background: true },
				);
				await tx.createTask(
					Watchdog,
					{ ticket, conversationId: child.id } as WatchdogInput,
					{ ownership: { kind: "conversation" }, background: true },
				);
				return child.id;
			}, context);
			return reply(`Spawned a worker on ${ticket}.`, conversationId);
		},
	});
	return defineExtension({ name: "workers", tools: [worker], tasks: [Anchor, Reporter, Watchdog] });
}
