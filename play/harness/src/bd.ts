import type { Context } from "@earendil-works/chord";
import { Type } from "@earendil-works/pi-ai";
import { defineExtension, defineTool } from "@earendil-works/pi-durable";
import type { ExecutionEnv } from "@earendil-works/pi-durable/env";
import { BD_CWD } from "./config.ts";

export type BdRole = "planner" | "foreman" | "worker";

type BdRun = { code: number; stdout: string; stderr: string; data: unknown };

/** `bd --json` for code that is not a tool (tasks, dispatchers). Same argv discipline as the tools. */
export async function bdJson(env: ExecutionEnv | undefined, actor: string, argv: string[], ctx: Context): Promise<unknown> {
	return (await runBd(env, actor, argv, ctx)).data;
}

/** One `bd` invocation. The model never types a CLI flag: argv is built here. */
async function runBd(env: ExecutionEnv | undefined, actor: string, argv: string[], ctx: Context): Promise<BdRun> {
	if (!env) throw new Error("no execution environment: HarnessOptions.env is missing");
	let stdout = "";
	let stderr = "";
	const result = await env.exec(["bd", "--json", "--actor", actor, ...argv], {
		cwd: BD_CWD,
		onOutput: (chunk, _context, { stream }) => {
			if (stream === "stdout") stdout += chunk;
			else stderr += chunk;
		},
	}, ctx);
	if (!result.ok) throw new Error(`bd ${argv[0]}: ${result.error.message}`);
	let data: unknown;
	if (stdout.trim()) {
		try {
			data = JSON.parse(stdout);
		} catch {
			data = undefined;
		}
	}
	return { code: result.value.exitCode, stdout, stderr, data };
}

/** Exit 13 is bd's lost compare-and-set: another actor changed the ticket first. */
function present(run: BdRun) {
	if (run.code === 13) {
		return {
			isError: true as const,
			output: [{ type: "text" as const, text: "precondition lost (bd exit 13): another actor changed this ticket first. Re-read it with bd_show before deciding anything." }],
		};
	}
	if (run.code !== 0) {
		return {
			isError: true as const,
			output: [{ type: "text" as const, text: `bd exited ${run.code}: ${(run.stderr || run.stdout).trim() || "(no output)"}` }],
		};
	}
	const text = run.data === undefined ? run.stdout.trim() || "(no output)" : JSON.stringify(run.data);
	return { output: [{ type: "text" as const, text }] };
}

const Id = Type.String({ description: "Ticket id, e.g. factorio-ai-player-12" });

/** Read tools every role gets. All replay-safe: a crash rerun only re-reads. */
function readTools(actor: string) {
	const ready = defineTool({
		name: "bd_ready",
		description: "List actionable tickets: open, unblocked, and not deferred. Use label to filter a lane (e.g. world, readonly).",
		parameters: Type.Object({
			label: Type.Optional(Type.String({ description: "Require this label" })),
			excludeLabel: Type.Optional(Type.String({ description: "Exclude tickets carrying this label" })),
			unassigned: Type.Optional(Type.Boolean({ description: "Only unclaimed tickets" })),
			limit: Type.Optional(Type.Integer({ minimum: 1, maximum: 100, description: "Default 20" })),
		}),
		replay: "safe",
		execute: async (a, api, ctx) => {
			const argv = ["ready", "--limit", String(a.limit ?? 20)];
			if (a.label) argv.push("--label", a.label);
			if (a.excludeLabel) argv.push("--exclude-label", a.excludeLabel);
			if (a.unassigned) argv.push("--unassigned");
			return present(await runBd(api.env, actor, argv, ctx));
		},
	});

	const show = defineTool({
		name: "bd_show",
		description: "Read one ticket in full: description, design, acceptance criteria, labels, status, assignee, comments.",
		parameters: Type.Object({ id: Id }),
		replay: "safe",
		execute: async (a, api, ctx) => present(await runBd(api.env, actor, ["show", a.id], ctx)),
	});

	const list = defineTool({
		name: "bd_list",
		description: "List tickets, optionally a lane's or one assignee's. Closed tickets are hidden unless all is set.",
		parameters: Type.Object({
			all: Type.Optional(Type.Boolean({ description: "Include closed tickets" })),
			label: Type.Optional(Type.String()),
			assignee: Type.Optional(Type.String()),
			limit: Type.Optional(Type.Integer({ minimum: 1, maximum: 200, description: "Default 50" })),
		}),
		replay: "safe",
		execute: async (a, api, ctx) => {
			const argv = ["list", "--limit", String(a.limit ?? 50)];
			if (a.all) argv.push("--all");
			if (a.label) argv.push("--label", a.label);
			if (a.assignee) argv.push("--assignee", a.assignee);
			return present(await runBd(api.env, actor, argv, ctx));
		},
	});

	const comments = defineTool({
		name: "bd_comments",
		description: "Read a ticket's comment log: the evidence trail every worker appends to.",
		parameters: Type.Object({ id: Id }),
		replay: "safe",
		execute: async (a, api, ctx) => present(await runBd(api.env, actor, ["comments", a.id], ctx)),
	});

	return [ready, show, list, comments];
}

/** Worker tools: read plus a comment log. A worker never changes ticket status. */
function workerTools(actor: string) {
	const comment = defineTool({
		name: "bd_comment",
		description:
			"Append to the ticket's evidence log. Write the exact query or mutation you ran and its observed output. A crash mid-call may repeat the comment: check bd_comments first.",
		parameters: Type.Object({ id: Id, text: Type.String({ description: "Evidence, with the command and what it returned" }) }),
		execute: async (a, api, ctx) => present(await runBd(api.env, actor, ["comment", a.id, a.text], ctx)),
	});
	return [comment];
}

/** Planner tools: file tickets. No claim, no close, no world access. */
function plannerTools(actor: string) {
	const create = defineTool({
		name: "bd_create",
		description:
			"File a ticket. acceptance must be checkable by a query, not by prose: name the query and the expected result. labels: use `world` for anything that changes the save (runs one at a time) and `readonly` for surveys and planning. `bd create` is not idempotent: on an interrupted call, search before creating twice.",
		parameters: Type.Object({
			title: Type.String({ description: "Imperative outcome, one line" }),
			description: Type.String({ description: "Current observed state, with the query output that motivates the work" }),
			acceptance: Type.String({ description: "The check: query + expected result" }),
			design: Type.Optional(Type.String({ description: "Planned steps: coordinates, entities, order" })),
			labels: Type.Optional(Type.Array(Type.String(), { description: "e.g. [\"world\", \"area:smelting\"]" })),
			type: Type.Optional(Type.String({ description: "task (default), chore, bug, feature, spike" })),
		}),
		execute: async (a, api, ctx) => {
			const argv = ["create", a.title, "-t", a.type ?? "task", "-d", a.description, "--acceptance", a.acceptance];
			if (a.design) argv.push("--design", a.design);
			if (a.labels?.length) argv.push("--labels", a.labels.join(","));
			return present(await runBd(api.env, actor, argv, ctx));
		},
	});
	return [create];
}

/** Foreman tools: claim (mutex), heartbeat (lease), update, validate-and-close. */
function foremanTools(actor: string) {
	const claim = defineTool({
		name: "bd_claim",
		description:
			"Atomically claim a ticket: sets assignee to you and status in_progress. This is the mutex that stops two workers running one ticket. Safe to rerun: it is idempotent for the same actor.",
		parameters: Type.Object({ id: Id }),
		replay: "safe",
		execute: async (a, api, ctx) => present(await runBd(api.env, actor, ["update", a.id, "--claim"], ctx)),
	});

	const heartbeat = defineTool({
		name: "bd_heartbeat",
		description: "Push the claim lease forward while a worker runs. A dead process stops heartbeating and `bd reclaim` frees the ticket.",
		parameters: Type.Object({ id: Id }),
		replay: "safe",
		execute: async (a, api, ctx) => present(await runBd(api.env, actor, ["heartbeat", a.id], ctx)),
	});

	const comment = defineTool({
		name: "bd_comment",
		description: "Append to the ticket's log: a steer you sent, a validation result, why you reopened it.",
		parameters: Type.Object({ id: Id, text: Type.String() }),
		execute: async (a, api, ctx) => present(await runBd(api.env, actor, ["comment", a.id, a.text], ctx)),
	});

	const update = defineTool({
		name: "bd_update",
		description:
			"Change a ticket's fields. Pass ifStatus to make it a compare-and-set: the write is refused (exit 13) unless the ticket is still in that status.",
		parameters: Type.Object({
			id: Id,
			status: Type.Optional(Type.String({ description: "open, in_progress, blocked, deferred, closed" })),
			ifStatus: Type.Optional(Type.String({ description: "Only write when the ticket has this status" })),
			addLabels: Type.Optional(Type.Array(Type.String())),
			removeLabels: Type.Optional(Type.Array(Type.String())),
			appendNotes: Type.Optional(Type.String()),
			title: Type.Optional(Type.String()),
			description: Type.Optional(Type.String()),
		}),
		execute: async (a, api, ctx) => {
			const argv = ["update", a.id];
			if (a.ifStatus) argv.push("--if-status", a.ifStatus);
			if (a.status) argv.push("--status", a.status);
			if (a.addLabels?.length) argv.push("--add-label", a.addLabels.join(","));
			if (a.removeLabels?.length) argv.push("--remove-label", a.removeLabels.join(","));
			if (a.appendNotes) argv.push("--append-notes", a.appendNotes);
			if (a.title) argv.push("--title", a.title);
			if (a.description) argv.push("--description", a.description);
			return present(await runBd(api.env, actor, argv, ctx));
		},
	});

	const close = defineTool({
		name: "bd_close",
		description:
			"Close a validated ticket with one compare-and-set: refused (exit 13) unless the ticket is still in_progress, so a claim you lost cannot be closed. Close only after you re-ran the ticket's acceptance check yourself; the reason is your evidence.",
		parameters: Type.Object({ id: Id, reason: Type.String({ description: "The evidence: what you queried and what it showed" }) }),
		execute: async (a, api, ctx) =>
			present(
				await runBd(api.env, actor, ["update", a.id, "--if-status", "in_progress", "--status", "closed", "--append-notes", `close: ${a.reason}`], ctx),
			),
	});

	return [claim, heartbeat, comment, update, close];
}

/**
 * bd tools split by role. Capabilities are part of the prompt: a worker cannot close its own
 * ticket, and the planner cannot touch the world or the claim table.
 */
export function bdExtension(role: BdRole) {
	const actor = role === "worker" ? "worker" : role;
	const tools =
		role === "planner" ? [...readTools(actor), ...plannerTools(actor)]
		: role === "worker" ? [...readTools(actor), ...workerTools(actor)]
		: [...readTools(actor), ...foremanTools(actor)];
	return defineExtension({ name: `bd-${role}`, tools });
}
