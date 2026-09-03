import { api } from "./api";
import type { Permission } from "./permissions";

/**
 * The first things to do on a new host, and how each one is answered.
 *
 * A checklist that only lists tasks is a brochure: it tells a host with three
 * plugins and a live tunnel to add its first plugin, which is how onboarding
 * gets dismissed on day one and never read again. Every step here is answered
 * from state this host already holds, so a step that is done says so.
 *
 * None of this is access control. Each step names the capability its *action*
 * takes, so somebody is never shown a task the server would refuse them; the
 * server authorises every call again regardless.
 */

export type StepID = "plugin" | "tools" | "tunnel" | "policy" | "people";

/**
 * What a probe found. Four values, not two, and the last two are not the same
 * fact: `absent` is "this host will never need this step", `unknown` is "the
 * call failed and nobody knows". Both hide the row -- a spinner or an error in
 * the corner of every page is worse than a shorter list -- but only `absent`
 * lets the panel conclude the host is finished and take itself away.
 */
export type Outcome =
  | { kind: "done" }
  | { kind: "todo"; to?: string }
  | { kind: "absent" }
  | { kind: "unknown" };

export interface Step {
  id: StepID;
  /** The task, in as few words as it takes. */
  label: string;
  /** One line saying what doing it gets you. */
  detail: string;
  /** The page that completes it. A probe may name a more specific one. */
  to: string;
  /** What the action takes, not what reading its state takes. */
  permission: Permission;
  probe: () => Promise<Outcome>;
}

/**
 * Ordered the way somebody would actually work: something to serve, then the
 * tools it may serve, then the client that reaches them, then the interruptions
 * worth keeping, then the second pair of hands.
 */
export const STEPS: readonly Step[] = [
  {
    id: "plugin",
    label: "Add an MCP server",
    detail: "Browse the public catalogues, or paste a server.json of your own.",
    to: "/marketplace",
    permission: "plugins:write",
    probe: async () => {
      const { instances } = await api.instances();
      // A removed instance is an override of the configuration file rather than
      // something this host serves, so it does not answer the step.
      const live = (instances ?? []).filter((i) => !i.removed);
      return live.length > 0 ? { kind: "done" } : { kind: "todo" };
    },
  },
  {
    id: "tools",
    label: "Approve its tools",
    detail: "Nothing a remote server offers is served until you have read it and said yes.",
    to: "/plugins",
    permission: "plugins:write",
    probe: async () => {
      const { servers } = await api.mcpServers();
      const imported = servers ?? [];
      // A host serving only built-in plugins has no third party's catalogue to
      // classify, so this is absent rather than permanently unfinished -- which
      // is what would keep the panel on that host's screen for ever.
      if (imported.length === 0) return { kind: "absent" };
      if (imported.some((s) => s.enabled_tools > 0)) return { kind: "done" };
      const waiting = imported.find((s) => s.pending > 0);
      return {
        kind: "todo",
        to: waiting ? `/plugins/${encodeURIComponent(waiting.name)}` : undefined,
      };
    },
  },
  {
    id: "tunnel",
    label: "Connect ChatGPT",
    detail: "A tunnel dials out from here, so this host needs no inbound port.",
    to: "/tunnels",
    permission: "plugins:write",
    probe: async () => {
      const info = await api.tunnel();
      const up = (info.tunnels ?? []).some((t) => t.state === "connected");
      return up ? { kind: "done" } : { kind: "todo" };
    },
  },
  {
    id: "policy",
    label: "Let routine changes run",
    detail: "A standing rule authorises a class of change, so you are asked about the rest.",
    to: "/settings/policy",
    permission: "plugins:write",
    probe: async () => {
      const policy = await api.approvalPolicy();
      return (policy.rules ?? []).length > 0 ? { kind: "done" } : { kind: "todo" };
    },
  },
  {
    id: "people",
    label: "Invite someone",
    detail: "A second account, with a role of its own and the plugins it may reach.",
    to: "/settings/users",
    permission: "plugins:write",
    probe: async () => {
      const { users, count } = await api.users();
      return (count ?? (users ?? []).length) > 1
        ? { kind: "done" }
        : { kind: "todo" };
    },
  },
];

/** What has been found so far. A step with no entry has not been asked about. */
export type Found = ReadonlyMap<StepID, Outcome>;

/** Runs one probe. A refused or failed call hides the step rather than the page. */
async function run(step: Step): Promise<Outcome> {
  try {
    return await step.probe();
  } catch {
    return { kind: "unknown" };
  }
}

/**
 * Asks in order and stops as soon as an answer settles the question.
 *
 * This is what keeps a secondary surface from costing five requests on every
 * load: a host with nothing configured answers on the first, and only one that
 * has finished everything pays for the whole list -- once, because finishing
 * is remembered. A step that could not be read stops it too. The panel is
 * going to render either way, and asking four more questions of a host that
 * has just refused one is four more refusals.
 */
export async function probeInOrder(steps: readonly Step[]): Promise<Found> {
  const found = new Map<StepID, Outcome>();
  for (const step of steps) {
    const outcome = await run(step);
    found.set(step.id, outcome);
    if (outcome.kind !== "done" && outcome.kind !== "absent") break;
  }
  return found;
}

/** All of them at once, for when somebody has opened the list and is reading it. */
export async function probeAll(steps: readonly Step[]): Promise<Found> {
  const outcomes = await Promise.all(steps.map(run));
  return new Map(steps.map((step, i) => [step.id, outcomes[i]!]));
}

/**
 * Whether there is nothing left to do, which is when the panel stops rendering.
 *
 * A step nobody could read leaves this false. Hiding the row is a reasonable
 * answer to one failed call; concluding from it that the host is set up, and
 * remembering that for good, is not.
 */
export function nothingLeft(steps: readonly Step[], found: Found): boolean {
  return steps.every((step) => {
    const outcome = found.get(step.id);
    return outcome?.kind === "done" || outcome?.kind === "absent";
  });
}

/** How many of the steps worth showing are done, and how many there are. */
export function progress(steps: readonly Step[], found: Found): {
  done: number;
  total: number;
} {
  const shown = steps.filter((s) => {
    const kind = found.get(s.id)?.kind;
    return kind === "done" || kind === "todo";
  });
  return {
    done: shown.filter((s) => found.get(s.id)?.kind === "done").length,
    total: shown.length,
  };
}

/**
 * Where the dismissal lives.
 *
 * Per browser and not per account: the alternative is keying on the signed-in
 * address, which would put an identity in local storage to save a second
 * administrator on the same machine one click.
 */
const KEY = "mcpd.getting-started";

export type Reason = "dismissed" | "complete";

/** Whether this browser has been told not to show it again. */
export function isHidden(): boolean {
  try {
    return localStorage.getItem(KEY) !== null;
  } catch {
    // Storage can be refused outright rather than merely be empty. Showing the
    // panel every load is a better failure than not rendering it at all.
    return false;
  }
}

/** Remembers that it should not come back, and why. */
export function remember(reason: Reason): void {
  try {
    localStorage.setItem(KEY, reason);
  } catch {
    // Nothing to do: it will be offered again next load, which is harmless.
  }
}

/**
 * Runs something once the browser has nothing better to do, and can be called
 * off. The panel is secondary to every page it sits on, so its requests wait
 * for the page's own.
 */
export function whenIdle(fn: () => void): () => void {
  if (typeof window.requestIdleCallback === "function") {
    // The timeout is a ceiling rather than a delay: a tab that never goes idle
    // still gets there.
    const id = window.requestIdleCallback(fn, { timeout: 4000 });
    return () => window.cancelIdleCallback?.(id);
  }
  // Safari has no idle callback, so the wait is spelled out. It is long enough
  // for the page's own requests to have been made, and nothing here is urgent.
  const timer = setTimeout(fn, 1200);
  return () => clearTimeout(timer);
}
