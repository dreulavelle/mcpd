import { Fragment, useCallback, useEffect, useMemo, useState } from "react";
import {
  Bot, Cog, ScrollText, ShieldAlert, ShieldCheck, Trash2, UserPlus,
} from "lucide-react";
import { api, type AuditRecord, problemText } from "@/lib/api";
import {
  auditCategory, auditTone, auditWords, changeRows, describeActor, EVENT_CATEGORIES,
  phraseText, pretty, relative, stepWords, when,
  type NameBook, type Phrase,
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
  const [actorWord, setActorWord] = useQueryParam("who");
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
      .catch(() => { if (live) setChain(null); })
      .finally(() => undefined);
    return () => { live = false; };
  }, [mayVerify, checks]);

  const items = useMemo(() => thread(records), [records]);

  // Over what is loaded, not a query the server answers: the endpoint takes a
  // count and nothing else, and a filter that quietly narrowed the window it
  // asked for would hide the entries somebody was looking for.
  const actors = useMemo(
    () => [...new Set(records.map((r) => describeActor(r.actor, book).word))].sort(),
    [records, book],
  );
  const categories = useMemo(
    () => [...new Set(records.map(auditCategory))].sort(),
    [records],
  );
  const systems = useMemo(
    () => [...new Set(records.map((r) => r.plugin).filter((p): p is string => !!p))]
      .filter((p) => !p.startsWith("key_") && !p.startsWith("byp_"))
      .sort(),
    [records],
  );

  const filtering = actorWord !== "" || category !== "" || system !== "" || needle.trim() !== "";
  const shown = useMemo(() => {
    const q = needle.trim().toLowerCase();
    // A step of a change matching is the change matching: filtering by the
    // person who proposed something must not drop the step where mcpd applied
    // it, or the thread would claim nothing came of it.
    const matches = (r: AuditRecord) =>
      (!actorWord || describeActor(r.actor, book).word === actorWord) &&
      (!category || auditCategory(r) === category) &&
      (!system || r.plugin === system) &&
      (!q || haystack(r, book).includes(q));
    return items.filter((item) => item.records.some(matches));
  }, [items, actorWord, category, system, needle, book]);

  const days = useMemo(() => byDay(shown), [shown]);

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
          {" "}{chain.brokenAt} does not follow the one before it. Nothing older
          than that can be trusted.
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
            <NativeSelect aria-label="Who" className="w-44" value={actorWord}
                          onChange={(e) => setActorWord(e.target.value)}>
              <option value="">Anyone</option>
              {actors.map((a) => <option key={a} value={a}>{a}</option>)}
            </NativeSelect>
            <NativeSelect aria-label="What happened" className="w-52" value={category}
                          onChange={(e) => setCategory(e.target.value)}>
              <option value="">Everything</option>
              {categories.map((c) => (
                <option key={c} value={c}>{EVENT_CATEGORIES[c]}</option>
              ))}
            </NativeSelect>
            {systems.length > 0 && (
              <NativeSelect aria-label="System" className="w-44" value={system}
                            onChange={(e) => setSystem(e.target.value)}>
                <option value="">Every system</option>
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
            shown={shown.length}
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
                      // The page reads newest first, so the severed link is
                      // below the entry the check named.
                      severed={item.records.some((r) => r.seq === chain?.brokenAt)}
                      // A thread's entries are interleaved with everything
                      // else, so a difference of seq across it says nothing;
                      // this under-reports a hole rather than inventing one.
                      // Pruning announces itself with an entry of its own, and
                      // the chain check is what proves nothing was altered.
                      missing={filtering ? 0 : gapBelow(day.items, i)}
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

function Entry({ item, book, day, severed, missing, last }: {
  item: Item;
  book: NameBook;
  day: string;
  severed: boolean;
  missing: number;
  last: boolean;
}) {
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
        <span key={f} className="flex items-center gap-2">
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
      last={last && missing === 0 && !severed}
    >
      {item.change ? (
        <ThreadCard tone={tone}>
          {sentence}
          {facts}
          {changes.length > 0 && <ChangeFields changes={changes} />}
          {steps.length > 0 && (
            <Thread>
              {steps.map((s, i) => (
                <Step key={s.seq} record={s} book={book} last={i === steps.length - 1} />
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
function Step({ record, book, last }: {
  record: AuditRecord;
  book: NameBook;
  last: boolean;
}) {
  const words = stepWords(record, book);
  return (
    <ThreadStep tone={words.tone} at={record.at} last={last}>
      {words.line}
      {words.facts.map((f) => (
        <span key={f} className="text-muted-foreground"> · {f}</span>
      ))}
    </ThreadStep>
  );
}

/**
 * Said only when it needs saying: a change proposed before the day it finished
 * on would otherwise show a bare time from another day under today's heading.
 */
function DayCrossing({ head, day }: { head: AuditRecord; day: string }) {
  if (dayKey(head.at) === day) return null;
  return (
    <p className="mt-1 text-xs text-muted-foreground">
      Proposed on {when(head.at)}.
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
  return (
    <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 border-b pb-3 text-xs text-muted-foreground">
      {checkedAt && !broken && (
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
 * How many entries are absent between this item and the older one below it.
 *
 * Computed across a whole item rather than a row, so a change whose entries
 * are interleaved with everything else reports nothing rather than reporting a
 * hole that is not there.
 */
function gapBelow(items: Item[], i: number): number {
  const older = items[i + 1];
  if (!older) return 0;
  const mine = Math.min(...items[i]!.records.map((r) => r.seq));
  const theirs = Math.max(...older.records.map((r) => r.seq));
  return Math.max(0, mine - theirs - 1);
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
