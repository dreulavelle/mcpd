import { Fragment, useCallback, useEffect, useMemo, useState } from "react";
import {
  Bot, Cog, ScrollText, ShieldAlert, ShieldCheck, Trash2, UserPlus,
} from "lucide-react";
import { api, type AuditRecord, problemText } from "@/lib/api";
import {
  auditCategory, auditTone, auditWords, changeRows, describeActor, EVENT_CATEGORIES,
  phraseText, pretty, relative, stepWords, when,
  type EventCategory, type NameBook, type Phrase,
} from "@/lib/format";
import { useLoader } from "@/lib/hooks";
import { Link, useQueryParam } from "@/lib/router";
import { useCan } from "@/lib/session";
import { useNotify } from "@/components/toast";
import { Avatar } from "@/components/Avatar";
import {
  CodeBlock, EmptyState, Loading, Notice, PageHeader,
} from "@/components/chrome";
import { RiskBadge, StatusDot } from "@/components/status";
import {
  ChangeFields, GapNote, RawEntry, SeveredNote, Thread, ThreadCard, ThreadStep,
  Timeline, TimelineDay, TimelineFacts, TimelineItem, TimelineSentence,
  FactDot,
} from "@/components/Timeline";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { useConfirm } from "@/components/confirm";

const LIMITS = [100, 250, 500];

/**
 * The audit trail as a timeline: who did what on this host, and when.
 *
 * Two things make it readable rather than merely complete. Each entry is a
 * sentence naming the person, key or rule that acted, so a day can be followed
 * by eye; and a change -- five rows in the table for one thing that happened --
 * is drawn as one thing that happened, with its decision and its outcome as
 * steps under the proposal.
 *
 * The chain is still the property worth seeing, so it still has somewhere to
 * visibly break, and the break is rendered where the check found it.
 */
export function Audit() {
  const mayVerify = useCan("history:read");
  const mayName = useCan("access:read");
  const [limit, setLimit] = useState(LIMITS[0]!);
  const [chain, setChain] = useState<{ brokenAt: number | null; at: string } | null>(null);
  const [checks, setChecks] = useState(0);

  // In the address, so a link can arrive filtered: "everything Sam did on
  // echo" is a thing one person sends another.
  const [actor, setActor] = useQueryParam("who");
  const [category, setCategory] = useQueryParam("what");
  const [system, setSystem] = useQueryParam("system");
  const [needle, setNeedle] = useQueryParam("q");

  const load = useCallback(() => api.audit(limit), [limit]);
  const { data, error, reload } = useLoader(load, "Couldn't load the history.");
  const records = useMemo(() => data?.records ?? [], [data]);
  const book = useNameBook(mayName);

  /** Only somebody who may read history may run the check, so only they ask. */
  useEffect(() => {
    if (!mayVerify) return;
    let live = true;
    api.verifyAudit()
      .then((c) => {
        if (live) {
          setChain({ brokenAt: c.intact ? null : c.broken_at, at: new Date().toISOString() });
        }
      })
      // A check that could not run is not a check that failed.
      .catch(() => { if (live) setChain(null); });
    return () => { live = false; };
  }, [mayVerify, checks]);

  const items = useMemo(() => thread(records), [records]);

  // Over what is loaded, not a query the server answers: the endpoint takes a
  // count and nothing else, and a filter that quietly narrowed the window it
  // asked for would hide the entries somebody was looking for.
  // The principal, not the words: two accounts can share a display name, and
  // a filtered link has to mean the same thing to whoever opens it as it did
  // to whoever sent it.
  const actors = useMemo(
    () => [...new Set(records.map((r) => r.actor))]
      .sort((a, b) => actorLabel(a, book).localeCompare(actorLabel(b, book))),
    [records, book],
  );
  const categories = useMemo(
    () => [...new Set(records.map(auditCategory))].sort(),
    [records],
  );
  // Only from entries whose subject really is a system. Everywhere else that
  // column carries whatever the writer had to hand -- a role's name, a
  // certificate's, an account id -- and offering those as systems to filter by
  // is offering a filter that matches one entry and means nothing.
  const systems = useMemo(
    () => [...new Set(records.filter(namesASystem).map((r) => r.plugin!))].sort(),
    [records],
  );

  // Built once per load rather than per keystroke: the search runs over every
  // loaded record on every character typed, and each haystack is a sentence
  // built from scratch.
  const haystacks = useMemo(() => {
    const bySeq = new Map<number, string>();
    for (const r of records) bySeq.set(r.seq, haystack(r, book));
    return bySeq;
  }, [records, book]);

  const filtering = actor !== "" || category !== "" || system !== "" || needle.trim() !== "";
  const shown = useMemo(() => {
    const q = needle.trim().toLowerCase();
    // A step of a change matching is the change matching: filtering by the
    // person who proposed something must not drop the step where mcpd applied
    // it, or the thread would claim nothing came of it.
    const matches = (r: AuditRecord) =>
      (!actor || r.actor === actor) &&
      (!category || auditCategory(r) === category) &&
      (!system || (namesASystem(r) && r.plugin === system)) &&
      (!q || (haystacks.get(r.seq) ?? "").includes(q));
    return items.filter((item) => item.records.some(matches));
  }, [items, actor, category, system, needle, haystacks]);

  const days = useMemo(() => byDay(shown), [shown]);
  const gaps = useMemo(() => gapsBelow(shown, records), [shown, records]);
  const matched = useMemo(
    () => shown.reduce((n, item) => n + item.records.length, 0),
    [shown],
  );

  return (
    <>
      <PageHeader
        title="Audit"
        lede="Who did what on this host, and when."
        actions={
          <div className="flex items-center gap-2">
            <NativeSelect
              aria-label="How many entries"
              className="w-32"
              value={limit}
              onChange={(e) => setLimit(Number(e.target.value))}
            >
              {LIMITS.map((n) => <option key={n} value={n}>Last {n}</option>)}
            </NativeSelect>
            <ClearHistory
              disabled={records.length === 0}
              onCleared={() => { reload(); setChecks((n) => n + 1); }}
            />
          </div>
        }
      />

      {error && <Notice tone="problem">{error}</Notice>}

      {chain?.brokenAt != null && (
        <Notice tone="problem" icon={<ShieldAlert />}>
          <strong>Something changed the record directly.</strong> Entry
          {" "}{chain.brokenAt} does not follow the one before it. Nothing from
          that entry on can be trusted.
        </Notice>
      )}

      {data === null && !error ? (
        <Loading rows={6} />
      ) : records.length === 0 ? (
        <EmptyState mark={<ScrollText />} title="Nothing recorded yet">
          Entries appear here as soon as somebody signs in, an assistant
          proposes a change, or anything on this host is altered.
        </EmptyState>
      ) : (
        <>
          <div className="mt-4 flex flex-wrap items-end gap-2">
            {/*
              Each of these carries whatever it is set to, whether or not this
              window holds an entry matching it. A shared link is the point of
              keeping the filters in the address, and one that arrives filtered
              to something outside the loaded window has to show a control that
              says so and can be cleared -- not an empty page and a select
              displaying "Anyone" while it filters on somebody.
            */}
            <NativeSelect aria-label="Who" className="w-52" value={actor}
                          onChange={(e) => setActor(e.target.value)}>
              <option value="">Anyone</option>
              {actor !== "" && !actors.includes(actor) && (
                <option value={actor}>{actorLabel(actor, book)}</option>
              )}
              {actors.map((a) => (
                <option key={a} value={a}>{actorLabel(a, book)}</option>
              ))}
            </NativeSelect>
            <NativeSelect aria-label="What happened" className="w-52" value={category}
                          onChange={(e) => setCategory(e.target.value)}>
              <option value="">Everything</option>
              {category !== "" && !categories.includes(category as EventCategory) && (
                <option value={category}>
                  {EVENT_CATEGORIES[category as EventCategory] ?? category}
                </option>
              )}
              {categories.map((c) => (
                <option key={c} value={c}>{EVENT_CATEGORIES[c]}</option>
              ))}
            </NativeSelect>
            {/*
              Drawn whenever it is set, even when this window holds no entry
              about that system. A link arriving with ?system=echo against a
              window that reaches back no further than this morning would
              otherwise filter everything away with no control to undo it.
            */}
            {(systems.length > 0 || system !== "") && (
              <NativeSelect aria-label="System" className="w-44" value={system}
                            onChange={(e) => setSystem(e.target.value)}>
                <option value="">Every system</option>
                {!systems.includes(system) && system !== "" && (
                  <option value={system}>{system}</option>
                )}
                {systems.map((p) => <option key={p} value={p}>{p}</option>)}
              </NativeSelect>
            )}
            <Input
              aria-label="Find in these entries"
              className="min-w-48 flex-1"
              placeholder="Find a name, a system, a word in an entry…"
              value={needle}
              onChange={(e) => setNeedle(e.target.value)}
            />
          </div>

          <ChainSeal
            broken={chain?.brokenAt != null}
            checkedAt={chain?.at ?? null}
            filtering={filtering}
            shown={matched}
            loaded={records.length}
          />

          {shown.length === 0 ? (
            <EmptyState title="Nothing matches">
              None of the last {records.length} entries match that. Ask for more
              entries above, or widen the filter.
            </EmptyState>
          ) : (
            <Timeline>
              {days.map((day) => (
                <TimelineDay
                  key={day.key}
                  label={day.label}
                  // Entries, not items: a change is five of the first and one
                  // of the second, and the heading counts the record.
                  count={day.items.reduce((n, item) => n + item.records.length, 0)}
                >
                  {day.items.map((item, i) => (
                    <Entry
                      key={item.key}
                      item={item}
                      book={book}
                      day={day.key}
                      brokenAt={chain?.brokenAt ?? null}
                      missing={filtering ? 0 : gaps.get(item.key) ?? 0}
                      last={i === day.items.length - 1}
                    />
                  ))}
                </TimelineDay>
              ))}
            </Timeline>
          )}
        </>
      )}
    </>
  );
}

/* -- one entry, or one change --------------------------------------------- */

function Entry({ item, book, day, brokenAt, missing, last }: {
  item: Item;
  book: NameBook;
  day: string;
  /** The seq the chain check named, or null. */
  brokenAt: number | null;
  missing: number;
  last: boolean;
}) {
  // A break at one of a change's own later entries belongs on that step. Only
  // a break at the entry this item is headed by is the item's own.
  const severed = item.head.seq === brokenAt;
  const { head, steps } = item;
  const actor = describeActor(head.actor, book);
  const words = auditWords(head, book);
  const tone = auditTone(steps.length > 0 ? steps[steps.length - 1]! : head);
  const changes = changeRows(head);

  const sentence = (
    <TimelineSentence
      actor={actor.word}
      at={head.at}
      badges={head.risk && head.risk !== "low" ? <RiskBadge risk={head.risk} /> : undefined}
    >
      <Sentence phrase={words.phrase} />
    </TimelineSentence>
  );

  const facts = words.facts.length > 0 && (
    <TimelineFacts mark={tone !== "neutral" ? <StatusDot tone={tone} /> : undefined}>
      {words.facts.map((f, i) => (
        <span key={i} className="flex items-center gap-2">
          {i > 0 && <FactDot />}
          {f}
        </span>
      ))}
    </TimelineFacts>
  );

  const raw = (
    <RawEntry>
      <CodeBlock>{pretty(rawOf(item))}</CodeBlock>
    </RawEntry>
  );

  return (
    <TimelineItem
      mark={<ActorMark actor={head.actor} book={book} />}
      severed={severed}
      muted={actor.housekeeping}
      last={last && missing === 0}
    >
      {item.change ? (
        <ThreadCard tone={tone}>
          {sentence}
          {facts}
          {changes.length > 0 && <ChangeFields changes={changes} />}
          {steps.length > 0 && (
            <Thread>
              {steps.map((s, i) => (
                <Step
                  key={s.seq}
                  record={s}
                  book={book}
                  severed={s.seq === brokenAt}
                  last={i === steps.length - 1}
                />
              ))}
            </Thread>
          )}
          {raw}
        </ThreadCard>
      ) : (
        <>
          {sentence}
          {facts}
          {raw}
        </>
      )}

      {severed && <SeveredNote />}
      {missing > 0 && !severed && <GapNote missing={missing} />}
      <DayCrossing head={head} day={day} />
    </TimelineItem>
  );
}

/** One later entry of a change, under the proposal it belongs to. */
function Step({ record, book, severed, last }: {
  record: AuditRecord;
  book: NameBook;
  severed: boolean;
  last: boolean;
}) {
  const words = stepWords(record, book);
  return (
    <ThreadStep tone={words.tone} at={record.at} severed={severed} last={last}>
      {words.line}
      {words.facts.map((f, i) => (
        <span key={i} className="text-muted-foreground"> · {f}</span>
      ))}
    </ThreadStep>
  );
}

/**
 * Said only when it needs saying: a change that started before the day it
 * finished on would otherwise show a bare time from another day under today's
 * heading.
 *
 * "Proposed on" only when the entry heading the card really is the proposal.
 * A window that reaches back far enough to hold a change's later entries but
 * not its proposal heads the card with whatever its oldest loaded entry is,
 * and calling that the proposal would date the request to when somebody
 * approved or applied it.
 */
function DayCrossing({ head, day }: { head: AuditRecord; day: string }) {
  if (dayKey(head.at) === day) return null;
  return (
    <p className="mt-1 text-xs text-muted-foreground">
      {head.kind === "operation.proposed"
        ? `Proposed on ${when(head.at)}.`
        : `This is the oldest entry loaded for it, from ${when(head.at)}.`}
    </p>
  );
}

/**
 * An audit sentence, with the things in it that have a page of their own.
 *
 * The words between the links are bare text rather than wrapped in spans, so
 * the sentence is one run of text to anything reading the page -- a find, a
 * selection, a screen reader -- rather than nine fragments that happen to sit
 * beside each other.
 */
function Sentence({ phrase }: { phrase: Phrase[] }) {
  return (
    <>
      {phrase.map((p, i) =>
        typeof p === "string" ? (
          <Fragment key={i}>{p}</Fragment>
        ) : (
          <Link key={i} to={p.to} className="text-primary hover:underline">{p.text}</Link>
        ))}
    </>
  );
}

const SYSTEM_MARKS: Record<string, typeof Cog> = {
  "system:policy": ShieldCheck,
  "system:registration": UserPlus,
  "system:retention": Trash2,
};

/**
 * Who acted, as a mark on the rail.
 *
 * Initials for a person, because following one person down a busy day is what
 * the rail is for. A rule, a sign-up default and the host itself each get
 * their own glyph, so an entry nobody performed is not wearing somebody's
 * initials.
 */
function ActorMark({ actor, book }: { actor: string; book: NameBook }) {
  const words = describeActor(actor, book);
  if (words.kind === "person" || words.kind === "key") {
    return <Avatar name={words.mark} kind={words.kind} className="size-7" />;
  }
  const Icon = words.kind === "service" ? Bot : SYSTEM_MARKS[actor] ?? Cog;
  return (
    <span
      aria-hidden="true"
      className="flex size-7 items-center justify-center rounded-md border bg-muted text-muted-foreground"
    >
      <Icon className="size-3.5" />
    </span>
  );
}

/**
 * What the chain proves, as one line rather than a banner.
 *
 * A check that announced its success loudly on every visit would train
 * somebody to skip the one time it did not, so this is the quietest thing on
 * the page until it is the loudest.
 */
function ChainSeal({ broken, checkedAt, filtering, shown, loaded }: {
  broken: boolean;
  checkedAt: string | null;
  filtering: boolean;
  shown: number;
  loaded: number;
}) {
  const sealed = checkedAt !== null && !broken;
  // With nothing to seal and nothing filtered there is nothing to say, and a
  // bordered strip with no words in it is a rule across the page that means
  // something is missing.
  if (!sealed && !filtering) return null;

  return (
    <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 border-b pb-3 text-xs text-muted-foreground">
      {sealed && (
        <span className="flex items-center gap-1.5">
          <ShieldCheck className="size-3.5 text-good" aria-hidden="true" />
          Every entry follows the one before it. Checked {relative(checkedAt)}.
        </span>
      )}
      {filtering && (
        <span>
          {shown} of the last {loaded} entries match. Gaps below are entries the
          filter hides, not missing ones.
        </span>
      )}
    </div>
  );
}

/* -- grouping -------------------------------------------------------------- */

interface Item {
  key: string;
  /** Every record this item draws, for filtering and for the raw view. */
  records: AuditRecord[];
  head: AuditRecord;
  steps: AuditRecord[];
  /** Whether this is a change, which is the one thing drawn as a card. */
  change: boolean;
  /** Where it sits on the timeline: when it last moved. */
  at: string;
}

/**
 * Entries sharing an operation become one item.
 *
 * A change is proposed, decided, applied and confirmed, and reading that as
 * four unrelated lines is what made the page hard to follow. The item sits at
 * the newest of its entries, because that is when it last moved and that is
 * where somebody scanning a day expects to find it; the proposal still heads
 * it, and every step keeps its own time.
 */
function thread(records: AuditRecord[]): Item[] {
  const byOperation = new Map<string, AuditRecord[]>();
  const items: Item[] = [];

  for (const r of records) {
    if (!r.operation_id) {
      items.push({
        key: `seq-${r.seq}`, records: [r], head: r, steps: [], change: false, at: r.at,
      });
      continue;
    }
    const group = byOperation.get(r.operation_id);
    if (group) {
      group.push(r);
      continue;
    }
    const started = [r];
    byOperation.set(r.operation_id, started);
    // A placeholder in the newest entry's position; filled in below, once
    // every entry of the change has been seen.
    items.push({
      key: `op-${r.operation_id}`, records: started, head: r, steps: [], change: true, at: r.at,
    });
  }

  return items.map((item) => {
    if (!item.change) return item;
    // Oldest first within a change, so the proposal heads it. The endpoint
    // returns newest first and a sequence is monotonic, so this is the whole
    // of the ordering.
    const ordered = [...item.records].sort((a, b) => a.seq - b.seq);
    const head = ordered.find((r) => r.kind === "operation.proposed") ?? ordered[0]!;
    return {
      ...item,
      records: ordered,
      head,
      steps: ordered.filter((r) => r !== head),
    };
  });
}

interface Day { key: string; label: string; items: Item[] }

function byDay(items: Item[]): Day[] {
  const days: Day[] = [];
  for (const item of items) {
    const key = dayKey(item.at);
    const last = days[days.length - 1];
    if (last?.key === key) last.items.push(item);
    else days.push({ key, label: dayLabel(item.at), items: [item] });
  }
  return days;
}

/** The reader's own day, which is the one they mean by "yesterday". */
function dayKey(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
}

function dayLabel(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const today = new Date();
  const yesterday = new Date(today);
  yesterday.setDate(today.getDate() - 1);
  if (dayKey(iso) === dayKey(today.toISOString())) return "Today";
  if (dayKey(iso) === dayKey(yesterday.toISOString())) return "Yesterday";
  return d.toLocaleDateString(undefined, {
    weekday: "long", day: "numeric", month: "long",
  });
}

/**
 * How many entries are missing below each item, by item key.
 *
 * Counted against every sequence number that was loaded, and worked out before
 * the items are split into days. Both halves matter. A change's later entries
 * are drawn inside its card but still occupy numbers between it and its
 * neighbours, so comparing one item's lowest number against the next item's
 * highest invents holes that are not there. And a hole that happens to fall on
 * a day boundary is still a hole: the split into days is presentation, and a
 * gap counted per day would never see across one.
 */
function gapsBelow(items: Item[], records: AuditRecord[]): Map<string, number> {
  const loaded = [...new Set(records.map((r) => r.seq))].sort((a, b) => a - b);
  const missingBelow = new Map<number, number>();
  for (let i = 1; i < loaded.length; i++) {
    missingBelow.set(loaded[i]!, loaded[i]! - loaded[i - 1]! - 1);
  }

  const gaps = new Map<string, number>();
  for (const item of items) {
    const lowest = Math.min(...item.records.map((r) => r.seq));
    const missing = missingBelow.get(lowest) ?? 0;
    if (missing > 0) gaps.set(item.key, missing);
  }
  return gaps;
}

/**
 * Whether an entry's subject is a system this host serves.
 *
 * The column is the subject of whatever the entry is about, so for a role it
 * holds a role's name, for a key an identifier, for a closed approval window
 * the word "all". Only these three families put a plugin or a remote server
 * there, and only they belong in a filter that says "every system".
 */
function namesASystem(r: AuditRecord): boolean {
  return !!r.plugin && /^(operation|mcpserver|plugin)\./.test(r.kind);
}

/**
 * What to call an actor in the filter, as opposed to in a sentence.
 *
 * A sentence calls the executor, the reaper and the rediscovery pass all
 * "mcpd", which is true and is what a reader wants to read. A list of choices
 * cannot: three options reading "mcpd" is a control that cannot be used. So
 * the parts of mcpd say which part they are, in the words somebody would use
 * for what that part does.
 */
const SYSTEM_LABELS: Record<string, string> = {
  "system:executor": "mcpd, applying changes",
  "system:reaper": "mcpd, closing changes nobody decided",
  "system:retention": "mcpd, tidying the record",
  "system:rediscovery": "mcpd, re-reading systems",
  "system:account-seed": "mcpd, setting up an account",
  "system:tunnel-reconcile": "mcpd, keeping tunnels in step",
};

function actorLabel(actor: string, book: NameBook): string {
  return SYSTEM_LABELS[actor] ?? describeActor(actor, book).word;
}

/** Everything about an entry a search should reach, lowercased once. */
function haystack(r: AuditRecord, book: NameBook): string {
  const words = auditWords(r, book);
  return [
    describeActor(r.actor, book).word,
    phraseText(words.phrase),
    words.facts.join(" "),
    r.plugin ?? "",
    r.operation_id ?? "",
    String(r.seq),
    pretty(r.detail),
  ].join(" ").toLowerCase();
}

/** The record as stored, which is where every identifier lives. */
function rawOf(item: Item): unknown {
  const rows = item.records.map((r) => ({
    seq: r.seq, at: r.at, kind: r.kind, actor: r.actor,
    ...(r.plugin ? { subject: r.plugin } : {}),
    ...(r.operation_id ? { operation_id: r.operation_id } : {}),
    ...(r.action ? { action: r.action } : {}),
    ...(r.from_state ? { from_state: r.from_state } : {}),
    ...(r.to_state ? { to_state: r.to_state } : {}),
    ...(r.risk ? { risk: r.risk } : {}),
    ...(r.detail !== undefined && r.detail !== null ? { detail: r.detail } : {}),
  }));
  return rows.length === 1 ? rows[0] : rows;
}

/**
 * Names for the identifiers the trail records.
 *
 * Both lists take a permission the reader may not hold, so both are asked for
 * once and neither is required: without them a sentence says "a key" instead
 * of naming it, which is a worse sentence and still a sentence.
 */
function useNameBook(mayName: boolean): NameBook {
  const [book, setBook] = useState<NameBook>({});

  useEffect(() => {
    if (!mayName) return;
    let live = true;
    Promise.all([
      api.users().catch(() => null),
      api.keys().catch(() => null),
      api.roles().catch(() => null),
    ]).then(([users, keys, roles]) => {
      if (!live) return;
      const next: NameBook = { users: {}, keys: {}, roles: {} };
      // Keyed by both, because a membership entry names an account by id and
      // an approval names one by address.
      for (const u of users?.users ?? []) {
        next.users![u.id] = u.name;
        next.users![u.email] = u.name;
      }
      for (const k of keys?.keys ?? []) next.keys![k.id] = k.name;
      for (const r of roles?.roles ?? []) next.roles![r.id] = r.name;
      setBook(next);
    });
    return () => { live = false; };
  }, [mayName]);

  return book;
}

function ClearHistory({ disabled, onCleared }: {
  disabled: boolean;
  onCleared: () => void;
}) {
  const confirm = useConfirm();
  const mayClear = useCan("history:write");
  const notify = useNotify();
  const [busy, setBusy] = useState(false);

  if (!mayClear) return null;

  async function clear() {
    if (!(await confirm({
      title: "Clear the history?",
      description: "Everything recorded so far is removed. Clearing it is itself recorded.",
      action: "Clear it",
    }))) return;
    setBusy(true);
    try {
      const r = await api.clearAudit();
      notify("good", `Cleared ${r.removed} ${r.removed === 1 ? "entry" : "entries"}.`);
    } catch (e) {
      notify("problem", problemText(e, "Couldn't clear it."));
    } finally {
      setBusy(false);
      onCleared();
    }
  }

  return (
    <Button variant="outline" size="sm" disabled={busy || disabled} onClick={clear}>
      {busy ? "Clearing…" : "Clear history"}
    </Button>
  );
}
