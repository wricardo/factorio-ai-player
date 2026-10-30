import type { Context } from "@earendil-works/chord";
import { type Conversation, defineDoc, defineExtension, defineTask, type Harness, type TaskId } from "@earendil-works/pi-durable";
import { bdJson } from "./bd.ts";

/**
 * The dispatcher is what makes the harness run without a human nudging the foreman: every minute it
 * reads `bd ready` and tells the foreman about tickets nobody has announced yet. It never claims and
 * never spawns: the foreman owns that decision, and the checks below only keep the lanes honest.
 */
type Ticket = { id?: string; title?: string; labels?: string[]; priority?: number; description?: string; acceptance_criteria?: string };

/** Announced tickets and the dispatcher task itself, so a restart neither re-announces nor duplicates it. */
export const DispatchDoc = defineDoc<{ announced: Record<string, number>; taskId?: TaskId }>({
	kind: "app.dispatch",
	version: 1,
	scope: "session",
	initial: () => ({ announced: {} }),
	checkpointWhen: () => false,
});

const LANE_POLL_MS = 60_000;
/** A ticket stays announced this long; `bd reclaim` or a reopen brings it back after that. */
const ANNOUNCE_TTL_MS = 6 * 60 * 60 * 1000;

const isWorld = (ticket: Ticket) => (ticket.labels ?? []).includes("world");

const Dispatcher = defineTask<null, { phase: "poll" }, null>({
	name: "app.dispatch",
	version: 1,
	initial: () => ({ phase: "poll" }),
	phases: {
		poll: async (_task, runtime, context) => {
			const foreman = await runtime.conversation(runtime.conversationId, context);
			const env = await runtime.env(context);
			const ready = ((await bdJson(env, "dispatch", ["ready", "--limit", "20"], context)) ?? []) as Ticket[];
			const state = (await runtime.snapshot(DispatchDoc, context)) ?? { announced: {} };
			const now = runtime.now();

			// One world-changing worker at a time: the lane is decided here, not by the model.
			const inProgress = ((await bdJson(env, "dispatch", ["list", "--limit", "50"], context)) ?? []) as Ticket[];
			const worldBusy = inProgress.some((ticket) => ticket.labels?.includes("world"));

			const fresh = ready.filter((ticket) => {
				if (ticket.id === undefined) return false;
				const announcedAt = state.announced[ticket.id];
				if (announcedAt !== undefined && now - announcedAt < ANNOUNCE_TTL_MS) return false;
				if (worldBusy && isWorld(ticket)) return false;
				return true;
			});

			if (fresh.length > 0 && foreman) {
				const lines = fresh.map((ticket) => {
					const lane = isWorld(ticket) ? "world" : "readonly";
					return `- ${ticket.id} [${lane}] ${ticket.title ?? ""}\n  acceptance: ${(ticket.acceptance_criteria ?? "").slice(0, 300)}`;
				});
				// At-least-once: submit first, mark second. A crash in between repeats one announcement,
				// which the foreman tolerates; the reverse order would drop the ticket silently.
				await foreman.submit(
					{
						type: "input",
						content: `[dispatch] ${fresh.length} ticket(s) ready and unclaimed. Lane rules: claim before spawning, one world ticket at a time, readonly tickets may run together. Spawn a worker for each one you take.\n${lines.join("\n")}`,
						whenBusy: "followUp",
						requestId: `dispatch:${fresh.map((ticket) => ticket.id).join(",")}:${Math.floor(now / 1000)}`,
					},
					context,
				);
				await runtime.commit(async (tx) => {
					const doc = await tx.doc(DispatchDoc);
					for (const ticket of fresh) doc.announced[ticket.id as string] = now;
				}, context);
			}

			// Forget old announcements so a reclaimed ticket can be dispatched again.
			const stale = Object.entries(state.announced).filter(([, at]) => now - at > ANNOUNCE_TTL_MS);
			if (stale.length > 0) {
				await runtime.commit(async (tx) => {
					const doc = await tx.doc(DispatchDoc);
					for (const [id] of stale) delete doc.announced[id];
				}, context);
			}

			await runtime.sleepUntil(now + LANE_POLL_MS, { phase: "poll" }, context);
		},
	},
	abort: (_task, runtime, context) =>
		runtime.commit(() => ({ status: "terminal", outcome: { status: "aborted" } }), context),
});

export const dispatcher = defineExtension({ name: "dispatcher", tasks: [Dispatcher] });

/** Start the dispatcher once, on the foreman's conversation, and keep its id for later boots. */
export async function ensureDispatcher(harness: Harness, foreman: Conversation, context: Context): Promise<void> {
	const state = await harness.snapshot(DispatchDoc, context);
	const live = state?.taskId === undefined ? undefined : await harness.getTask(state.taskId, context);
	if (live !== undefined && live.state.status !== "terminal") return;
	const taskId = await foreman.commit(
		(tx) => tx.createTask(Dispatcher, null, { ownership: { kind: "conversation" }, background: true }),
		context,
	);
	await harness.commit(async (tx) => {
		(await tx.doc(DispatchDoc)).taskId = taskId;
	}, context);
	void foreman;
}
