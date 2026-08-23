import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { ShieldBan, ShieldCheck, TriangleAlert } from "lucide-react";
import {
  api, ApiError,
  type ApprovalPolicy as Policy, type ApprovalRule, type PolicyEvaluation,
} from "@/lib/api";
import { riskLabel } from "@/lib/format";
import { useCan } from "@/lib/session";
import { EmptyState, Loading, Notice, PageHeader, Section } from "@/components/chrome";
import { Chip } from "@/components/status";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Switch } from "@/components/ui/switch";

/**
 * The standing rules that decide when nobody is asked.
 *
 * This is the page the design rests on. Rules live in the settings store
 * rather than a configuration file so that a change is recorded against
 * whoever made it *and* so an operator can see the whole set at once -- and
 * until this existed, the second half of that was not true and the endpoints
 * were reachable only by curl.
 *
 * Two things here are dangerous to get wrong rather than merely ugly, and both
 * shape the layout:
 *
 * An **exclusion is not a level**. A rule with no ceiling authorises nothing
 * and beats every grant that matches beside it, whatever their scopes are. It
 * is a different kind of statement from "up to medium", so it is a separate
 * list with its own heading rather than the bottom of one ordered list. The
 * ceilings the server offers are what a *grant* may be set to; they are not a
 * ranking of rules, and nothing here draws them as one.
 *
 * The **set is the unit**. Whether a rule is legal depends on the others
 * beside it -- two rules on one scope are refused -- so there is one save for
 * the whole page and no per-row write pretending otherwise.
 */

/** A rule being edited. `key` is React identity and is never sent. */
interface Draft {
  key: string;
  id: string;
  plugin: string;
  action: string;
  principal: string;
  /** "" is an exclusion, not an unset field. */
  max_risk: string;
  note: string;
}

let nextKey = 0;

function toDraft(rule: ApprovalRule): Draft {
  return {
    key: `rule-${nextKey++}`,
    id: rule.id,
    plugin: rule.plugin,
    action: rule.action,
    principal: rule.principal,
    max_risk: rule.max_risk,
    note: rule.note ?? "",
  };
}

/**
 * A draft as the API will read it.
 *
 * Every field is named, which is what keeps `key` out of the body: the
 * endpoint refuses an unknown field, and it is right to -- a rule that decides
 * who may write unattended should not be accepted with a typo in it.
 *
 * A blank selector becomes the wildcard here rather than being sent as `""`,
 * which the server refuses so that "anything" has exactly one spelling.
 */
function toRule(draft: Draft, wildcard: string): ApprovalRule {
  const selector = (value: string) => value.trim() || wildcard;
  return {
    id: draft.id.trim(),
    plugin: selector(draft.plugin),
    action: selector(draft.action),
    principal: selector(draft.principal),
    max_risk: draft.max_risk,
    note: draft.note.trim(),
  };
}

/**
 * A rule set reduced to what a save would change.
 *
 * Order is not part of it. The server sorts the set canonically on the way
 * out, so a rule that moved between the two lists on screen is not an edit.
 */
function signature(rules: ApprovalRule[]): string {
  return JSON.stringify(
    rules
      .map((r) => [r.id, r.plugin, r.action, r.principal, r.max_risk, r.note ?? ""])
      .sort((a, b) => (a[0]! < b[0]! ? -1 : a[0]! > b[0]! ? 1 : 0)),
  );
}

/**
 * Puts each warning beside the rule it is about.
 *
 * The server phrases them as `rule "id" names ...`, which is the only handle
 * on offer -- the payload is a flat list of sentences. Anything that does not
 * name a rule this page is showing is kept rather than dropped: a warning
 * nobody sees is worse than one in the wrong place, and a rule the operator
 * has just deleted from the draft is exactly when the sentence still matters.
 *
 * Exported for its own test, because the parse is the fragile part.
 */
export function warningsByRule(
  warnings: string[] | undefined,
  ids: readonly string[],
): { byRule: Map<string, string[]>; loose: string[] } {
  const known = new Set(ids);
  const byRule = new Map<string, string[]>();
  const loose: string[] = [];

  for (const warning of warnings ?? []) {
    const named = /^rule "([^"]+)"/.exec(warning);
    if (named && known.has(named[1]!)) {
      const id = named[1]!;
      byRule.set(id, [...(byRule.get(id) ?? []), warning]);
    } else {
      loose.push(warning);
    }
  }
  return { byRule, loose };
}

export function ApprovalPolicy() {
  const mayWrite = useCan("admin");
  const notify = useNotify();

  const [policy, setPolicy] = useState<Policy | null>(null);
  const [drafts, setDrafts] = useState<Draft[]>([]);
  const [loadProblem, setLoadProblem] = useState<ApiError | Error | null>(null);
  const [saveProblem, setSaveProblem] = useState("");
  const [busy, setBusy] = useState(false);
  const [suggestions, setSuggestions] = useState<Record<string, string[]>>({});

  const adopt = useCallback((next: Policy) => {
    setPolicy(next);
    setDrafts((next.rules ?? []).map(toDraft));
    setSaveProblem("");
  }, []);

  const load = useCallback(() => {
    api.approvalPolicy().then(
      (next) => { adopt(next); setLoadProblem(null); },
      (err: unknown) => setLoadProblem(err instanceof Error ? err : new Error(String(err))),
    );
  }, [adopt]);

  // Loaded once and never polled. This page is a form over the whole set, and
  // a tick that replaced the draft would discard an edit somebody was halfway
  // through writing.
  useEffect(load, [load]);

  // What this host actually serves, offered as suggestions. The failure these
  // guard against is a typo in a rule: it is not refused, because a rule may
  // legitimately name a plugin about to be added, so nothing but the operator
  // stops it. A catalogue of real names is cheap and stops most of them.
  useEffect(() => {
    let live = true;
    api.plugins().then(
      (r) => {
        if (!live) return;
        const map: Record<string, string[]> = {};
        for (const plugin of r.plugins ?? []) map[plugin.name] = plugin.mutations ?? [];
        setSuggestions(map);
      },
      () => undefined,
    );
    return () => { live = false; };
  }, []);

  const wildcard = policy?.wildcard ?? "*";
  const ceilings = policy?.ceilings ?? [];

  const rules = useMemo(
    () => drafts.map((d) => toRule(d, wildcard)),
    [drafts, wildcard],
  );
  const dirty = policy !== null && signature(rules) !== signature(policy.rules ?? []);
  const incomplete = drafts.some((d) => d.id.trim() === "");

  // Attributed against the drafts rather than the stored set. A rule the
  // operator has just renamed or deleted no longer has a row to sit beside, and
  // matching on the stored ids would drop the sentence entirely -- attributed
  // to a row that is gone and therefore rendered nowhere.
  const { byRule, loose } = useMemo(
    () => warningsByRule(policy?.warnings, drafts.map((d) => d.id.trim())),
    [policy, drafts],
  );

  const update = (key: string, patch: Partial<Draft>) =>
    setDrafts((list) => list.map((d) => (d.key === key ? { ...d, ...patch } : d)));

  const remove = (key: string) =>
    setDrafts((list) => list.filter((d) => d.key !== key));

  const add = (maxRisk: string) =>
    setDrafts((list) => [...list, {
      key: `rule-${nextKey++}`,
      id: "", plugin: wildcard, action: wildcard, principal: wildcard,
      max_risk: maxRisk, note: "",
    }]);

  async function save() {
    setBusy(true);
    setSaveProblem("");
    try {
      adopt(await api.saveApprovalPolicy(rules));
      notify("good", "Rules saved.");
    } catch (e) {
      // The endpoint validates everything before storing anything, so a
      // refusal means nothing changed -- worth saying, because the operator's
      // next question is whether half of it landed.
      setSaveProblem(e instanceof ApiError ? e.detail : "Couldn't save the rules.");
      notify("problem", "Nothing was saved.");
    } finally {
      setBusy(false);
    }
  }

  // Narrowed to the error itself rather than a boolean: "the stored rules do
  // not read" and "there are no rules" produce the same behaviour from the
  // host and are different facts, and the page has to say which.
  const unreadable = loadProblem instanceof ApiError
    && loadProblem.code === "unreadable_rules" ? loadProblem : null;

  /**
   * The way out of a stored value this build cannot parse.
   *
   * Without it the page is a dead end: the editor cannot be rendered over a
   * set it could not read, `PUT /api/settings` refuses this key on purpose,
   * and so nothing in the console can replace it. Destroying the value is
   * safe in the sense that matters -- the host is already ignoring it and
   * asking about everything -- but it is destructive, so it is one deliberate
   * click behind a confirmation rather than a side effect of loading the page.
   */
  async function clearUnreadable() {
    if (!confirm(
      "Replace the stored approval rules with none? What is there now cannot be "
      + "read and is not in effect, and this cannot be undone.",
    )) return;
    setBusy(true);
    try {
      adopt(await api.saveApprovalPolicy([]));
      setLoadProblem(null);
      notify("good", "The rules were replaced with none.");
    } catch (e) {
      notify("problem", e instanceof ApiError ? e.detail : "That didn't work.");
    } finally {
      setBusy(false);
    }
  }

  const exclusions = drafts.filter((d) => d.max_risk === "");
  const grants = drafts.filter((d) => d.max_risk !== "");

  return (
    <>
      <PageHeader
        title="Approval policy"
        lede={mayWrite
          ? "A rule here lets a change run with nobody asked. Everything not covered by one still goes to a person."
          : "What this host will let through without asking anybody. Changing it takes an administrator."}
      />

      {unreadable ? (
        <Notice tone="problem" icon={<TriangleAlert />}>
          <strong>The stored rules do not read.</strong> Every change is being
          put to a person, which is what happens with no rules at all — but it
          is a different fact, and what is stored is not what anybody
          configured. Nothing is shown below because there is nothing this build
          can show; these endpoints are the only way to write the value, so
          replacing the set is the way out.
          <span className="mt-1 block font-mono text-xs opacity-80">
            {unreadable.detail}
          </span>
          {mayWrite && (
            <Button
              variant="outline" size="sm" className="mt-3" disabled={busy}
              onClick={clearUnreadable}
            >
              {busy ? "Replacing…" : "Replace them with no rules"}
            </Button>
          )}
        </Notice>
      ) : loadProblem ? (
        <Notice tone="problem">
          {loadProblem instanceof ApiError
            ? loadProblem.detail
            : "Couldn't load the approval policy."}
        </Notice>
      ) : null}

      {policy === null && !loadProblem ? <Loading rows={5} /> : policy === null ? null : (
        <div className="space-y-6">
          <HowItResolves policy={policy} />

          {loose.length > 0 && (
            <Notice tone="attention" icon={<TriangleAlert />}>
              <strong>Some rules match nothing this host serves.</strong>
              {loose.map((w) => (
                <span key={w} className="mt-1 block">{w}</span>
              ))}
            </Notice>
          )}

          {/* The rules and their one save share a containing block, so the
              sticky bar stops pinning where they stop rather than hovering
              over the question below them. */}
          <div className="space-y-6">
            <Section
              title="Exclusions — always ask"
              description="A rule that authorises nothing. If one matches, a person is asked, whatever a grant beside it says."
              actions={mayWrite && (
                <Button variant="outline" size="sm" onClick={() => add("")}>
                  Add an exclusion
                </Button>
              )}
            >
              {exclusions.length === 0 ? (
                <EmptyState mark={<ShieldBan />} title="No exclusions">
                  Nothing is carved out. A change is still only automatic if a
                  grant below covers it.
                </EmptyState>
              ) : (
                <ul className="space-y-3">
                  {exclusions.map((draft) => (
                    <RuleRow
                      key={draft.key} draft={draft} ceilings={ceilings}
                      wildcard={wildcard} suggestions={suggestions}
                      warnings={byRule.get(draft.id) ?? []}
                      readOnly={!mayWrite}
                      onChange={(patch) => update(draft.key, patch)}
                      onRemove={() => remove(draft.key)}
                    />
                  ))}
                </ul>
              )}
            </Section>

            <Section
              title="Grants — authorised in advance"
              description="The most specific matching grant decides, and only one ever does. They are never merged, and an exclusion above beats all of them."
              actions={mayWrite && ceilings.length > 0 && (
                <Button variant="outline" size="sm" onClick={() => add(ceilings[0]!)}>
                  Add a grant
                </Button>
              )}
            >
              {grants.length === 0 ? (
                <EmptyState mark={<ShieldCheck />} title="No grants">
                  Nothing is authorised in advance, so every change goes to a
                  person — which is where this starts and the direction to be
                  wrong in.
                </EmptyState>
              ) : (
                <ul className="space-y-3">
                  {grants.map((draft) => (
                    <RuleRow
                      key={draft.key} draft={draft} ceilings={ceilings}
                      wildcard={wildcard} suggestions={suggestions}
                      warnings={byRule.get(draft.id) ?? []}
                      readOnly={!mayWrite}
                      onChange={(patch) => update(draft.key, patch)}
                      onRemove={() => remove(draft.key)}
                    />
                  ))}
                </ul>
              )}
            </Section>

            {saveProblem && (
              <Notice tone="problem">
                <strong>Nothing was saved.</strong> Every rule is checked before
                any of them is stored, so the set is as it was.
                <span className="mt-1 block font-mono text-xs opacity-80">
                  {saveProblem}
                </span>
              </Notice>
            )}

            {mayWrite && (
              <div className="sticky bottom-0 flex flex-wrap items-center gap-3 border-t bg-background/90 py-3 backdrop-blur">
                <Button disabled={!dirty || busy || incomplete} onClick={save}>
                  {busy ? "Saving…" : "Save all rules"}
                </Button>
                {dirty ? (
                  <>
                    <Button variant="ghost" disabled={busy} onClick={() => adopt(policy)}>
                      Discard
                    </Button>
                    <span className="text-xs text-muted-foreground">
                      {incomplete
                        ? "Every rule needs an id before the set can be saved."
                        : "Saving replaces the whole set — this is the only unit at which two rules covering the same thing can be caught."}
                    </span>
                  </>
                ) : (
                  <span className="text-xs text-muted-foreground">Nothing to save.</span>
                )}
              </div>
            )}
          </div>

          <WhatWouldHappen
            wildcard={wildcard}
            suggestions={suggestions}
            stale={dirty}
          />
        </div>
      )}
    </>
  );
}

/**
 * How the two kinds of rule argue, said once at the top.
 *
 * The cost of exclusion-wins is the part that surprises people, so it is
 * stated rather than left to be discovered: a grant cannot carve an exception
 * out of an exclusion, and the way to write "nobody but Alice" is the narrow
 * grant on its own.
 */
function HowItResolves({ policy }: { policy: Policy }) {
  return (
    <Notice tone="info">
      <strong>{policy.default}</strong> An exclusion that matches wins outright
      — specificity is not consulted — so a grant cannot carve an exception out
      of one. Write “nobody but Alice may do this automatically” as the narrow
      grant alone: the absence of a grant already means ask. Between grants, the
      most specific matching one decides and nothing is merged. A change that
      declares no way back, or whose risk this host does not recognise, is never
      automatic. There is no rate limit on mutations, so a rule is the only
      thing bounding how fast an assistant can write what it covers — scope one
      as narrowly as the job allows.
    </Notice>
  );
}

function RuleRow({
  draft, ceilings, wildcard, suggestions, warnings, readOnly, onChange, onRemove,
}: {
  draft: Draft;
  ceilings: string[];
  wildcard: string;
  suggestions: Record<string, string[]>;
  warnings: string[];
  readOnly: boolean;
  onChange: (patch: Partial<Draft>) => void;
  onRemove: () => void;
}) {
  const exclusion = draft.max_risk === "";
  const field = (name: string) => `${draft.key}-${name}`;
  const actions = suggestions[draft.plugin] ?? [
    ...new Set(Object.values(suggestions).flat()),
  ];

  if (readOnly) {
    return (
      <li className="space-y-2 rounded-lg border p-3">
        <div className="flex flex-wrap items-center gap-2">
          <code className="font-mono text-sm font-medium">{draft.id}</code>
          <Kind exclusion={exclusion} maxRisk={draft.max_risk} />
        </div>
        <p className="font-mono text-xs text-muted-foreground">
          {draft.plugin}/{draft.action} for {draft.principal}
        </p>
        {draft.note && <p className="text-sm text-muted-foreground">{draft.note}</p>}
        <RuleWarnings warnings={warnings} exclusion={exclusion} />
      </li>
    );
  }

  return (
    <li className="space-y-3 rounded-lg border p-3 sm:p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Kind exclusion={exclusion} maxRisk={draft.max_risk} />
        <div className="flex items-center gap-1">
          {/* Changing what a rule *is* moves it to the other list, which is
              the point: it is a different kind of statement, not a step up or
              down a scale. */}
          <Button
            variant="ghost" size="sm"
            onClick={() => onChange({ max_risk: exclusion ? ceilings[0] ?? "low" : "" })}
          >
            {exclusion ? "Make it a grant" : "Make it an exclusion"}
          </Button>
          <Button
            variant="ghost" size="sm"
            className="text-destructive hover:text-destructive"
            onClick={onRemove}
          >
            Remove
          </Button>
        </div>
      </div>

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <div className="space-y-1.5">
          <Label htmlFor={field("id")}>Rule id</Label>
          <Input
            id={field("id")} value={draft.id} placeholder="routine-radio"
            onChange={(e) => onChange({ id: e.target.value })}
          />
          <p className="text-xs text-muted-foreground">
            What the audit trail names, so it should read as a reason.
          </p>
        </div>

        <div className="space-y-1.5">
          <Label htmlFor={field("plugin")}>Plugin</Label>
          <Input
            id={field("plugin")} value={draft.plugin} placeholder={wildcard}
            list={`${draft.key}-plugins`}
            onChange={(e) => onChange({ plugin: e.target.value })}
          />
          <datalist id={`${draft.key}-plugins`}>
            {Object.keys(suggestions).map((name) => <option key={name} value={name} />)}
          </datalist>
        </div>

        <div className="space-y-1.5">
          <Label htmlFor={field("action")}>Action</Label>
          <Input
            id={field("action")} value={draft.action} placeholder={wildcard}
            list={`${draft.key}-actions`}
            onChange={(e) => onChange({ action: e.target.value })}
          />
          <datalist id={`${draft.key}-actions`}>
            {actions.map((name) => <option key={name} value={name} />)}
          </datalist>
        </div>

        <div className="space-y-1.5">
          <Label htmlFor={field("principal")}>Principal</Label>
          <Input
            id={field("principal")} value={draft.principal} placeholder={wildcard}
            onChange={(e) => onChange({ principal: e.target.value })}
          />
          <p className="text-xs text-muted-foreground">
            Whose proposals — <code className="font-mono">user:alice@example.com</code>,{" "}
            <code className="font-mono">svc:chatgpt</code>, or {wildcard} for anyone.
          </p>
        </div>

        {/* No ceiling control on an exclusion, because there is no ceiling to
            set: it authorises nothing, and offering an empty entry in the list
            would make "never" look like the bottom of the scale. */}
        {!exclusion && (
          <div className="space-y-1.5">
            <Label htmlFor={field("ceiling")}>Authorises up to</Label>
            <NativeSelect
              id={field("ceiling")} value={draft.max_risk}
              onChange={(e) => onChange({ max_risk: e.target.value })}
            >
              {ceilings.map((c) => (
                <option key={c} value={c}>{riskLabel(c)}</option>
              ))}
            </NativeSelect>
            <p className="text-xs text-muted-foreground">
              Compared against the risk as it finally stands, after any raise.
              Critical is never offered.
            </p>
          </div>
        )}

        <div className="space-y-1.5 sm:col-span-2 lg:col-span-3">
          <Label htmlFor={field("note")}>Note</Label>
          <Input
            id={field("note")} value={draft.note}
            placeholder="Why this is safe to do without asking"
            onChange={(e) => onChange({ note: e.target.value })}
          />
          <p className="text-xs text-muted-foreground">
            Carried into the audit entry of every change this rule authorises.
          </p>
        </div>
      </div>

      <RuleWarnings warnings={warnings} exclusion={exclusion} />
    </li>
  );
}

/** Grant or exclusion, said in words rather than by the absence of a value. */
function Kind({ exclusion, maxRisk }: { exclusion: boolean; maxRisk: string }) {
  if (exclusion) {
    return (
      <Chip tone="neutral">
        <ShieldBan className="size-3" aria-hidden="true" />
        Exclusion — always ask
      </Chip>
    );
  }
  return (
    <Chip tone="info">
      <ShieldCheck className="size-3" aria-hidden="true" />
      Grant — up to {riskLabel(maxRisk)}
    </Chip>
  );
}

/**
 * What the host has noticed about this rule.
 *
 * Beside the rule rather than in a list at the top, because the sentence is
 * about one rule and a heap of them at the top is read once and then not
 * again. On an exclusion it is worth more than it looks: a misspelled
 * exclusion refuses nothing, so it cannot do damage of its own — it simply
 * stops protecting what it was written for, and the broader grant decides.
 */
function RuleWarnings({ warnings, exclusion }: {
  warnings: string[];
  exclusion: boolean;
}) {
  if (warnings.length === 0) return null;
  return (
    <div className="space-y-2">
      {warnings.map((w) => (
        <Notice key={w} tone="attention" icon={<TriangleAlert />}>
          {w}
          {exclusion && (
            <span className="mt-1 block">
              An exclusion that matches nothing is not protecting what it was
              written for. Whatever grant would have been beaten by it decides
              instead.
            </span>
          )}
        </Notice>
      ))}
    </div>
  );
}

const RISKS = ["low", "medium", "high", "critical"];

/**
 * Asking the host what it would do, before writing a rule that does it.
 *
 * Worth more than any amount of explanatory copy: resolution is deterministic
 * and the answer is a fact, so an operator can check the case they are worried
 * about rather than reasoning about specificity in their head.
 *
 * It reads the *stored* rules, which is what makes it trustworthy and also the
 * one thing it must say out loud when the form above has unsaved edits.
 */
function WhatWouldHappen({ wildcard, suggestions, stale }: {
  wildcard: string;
  suggestions: Record<string, string[]>;
  stale: boolean;
}) {
  const [plugin, setPlugin] = useState("");
  const [action, setAction] = useState("");
  const [principal, setPrincipal] = useState("");
  const [risk, setRisk] = useState("low");
  const [reversible, setReversible] = useState(true);
  const [result, setResult] = useState<PolicyEvaluation | null>(null);
  const [problem, setProblem] = useState("");
  const [busy, setBusy] = useState(false);

  const ready = plugin.trim() !== "" && action.trim() !== "" && principal.trim() !== "";
  const actions = suggestions[plugin] ?? [...new Set(Object.values(suggestions).flat())];

  async function check(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setProblem("");
    setResult(null);
    try {
      setResult(await api.evaluateApprovalPolicy({
        plugin: plugin.trim(),
        action: action.trim(),
        principal: principal.trim(),
        risk,
        reversible,
      }));
    } catch (e) {
      setProblem(e instanceof ApiError ? e.detail : "Couldn't ask about that change.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Section
      title="Would this be authorised?"
      description="Ask about one change. Nothing is proposed and nothing is written."
    >
      <Card>
        <CardContent>
          {/* A form, and named. Three of these fields share a label with the
              rule rows above, and the form is what tells them apart -- for a
              screen reader walking the page, and for a test. Enter submits it,
              which is what somebody who has just typed a plugin name expects. */}
          <form
            className="space-y-4"
            aria-label="Would this be authorised?"
            onSubmit={check}
          >
            {stale && (
              <Notice tone="attention">
                This asks the rules as stored. The edits above have not been
                saved, so they are not part of the answer.
              </Notice>
            )}

            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
              <div className="space-y-1.5">
                <Label htmlFor="ask-plugin">Plugin</Label>
                <Input
                  id="ask-plugin" value={plugin} list="ask-plugins"
                  placeholder="cnmaestro"
                  onChange={(e) => setPlugin(e.target.value)}
                />
                <datalist id="ask-plugins">
                  {Object.keys(suggestions).map((n) => <option key={n} value={n} />)}
                </datalist>
              </div>

              <div className="space-y-1.5">
                <Label htmlFor="ask-action">Action</Label>
                <Input
                  id="ask-action" value={action} list="ask-actions"
                  placeholder="device.reboot"
                  onChange={(e) => setAction(e.target.value)}
                />
                <datalist id="ask-actions">
                  {actions.map((n) => <option key={n} value={n} />)}
                </datalist>
              </div>

              <div className="space-y-1.5">
                <Label htmlFor="ask-principal">Principal</Label>
                <Input
                  id="ask-principal" value={principal}
                  placeholder="user:alice@example.com"
                  onChange={(e) => setPrincipal(e.target.value)}
                />
              </div>

              <div className="space-y-1.5">
                <Label htmlFor="ask-risk">Risk</Label>
                <NativeSelect
                  id="ask-risk" value={risk}
                  onChange={(e) => setRisk(e.target.value)}
                >
                  {RISKS.map((r) => (
                    <option key={r} value={r}>{riskLabel(r)}</option>
                  ))}
                </NativeSelect>
                <p className="text-xs text-muted-foreground">
                  As it finally stands, after the plan and any override have
                  raised it.
                </p>
              </div>

              <div className="flex items-start gap-3 sm:col-span-2 lg:col-span-3">
                <Switch
                  id="ask-reversible" checked={reversible}
                  onCheckedChange={setReversible}
                />
                <div className="space-y-0.5">
                  <Label htmlFor="ask-reversible">There is a way back</Label>
                  <p className="text-xs text-muted-foreground">
                    A change that declares no way back is never authorised in
                    advance, whatever the rules say.
                  </p>
                </div>
              </div>
            </div>

            <div className="flex flex-wrap items-center gap-3">
              <Button type="submit" variant="outline" disabled={!ready || busy}>
                {busy ? "Asking…" : "Ask"}
              </Button>
              {!ready && (
                <span className="text-xs text-muted-foreground">
                  A plugin, an action and a principal — the answer depends on
                  all three. This asks about one real change, so{" "}
                  <code className="font-mono">{wildcard}</code> is not an answer
                  here.
                </span>
              )}
            </div>

            {problem && <Notice tone="problem">{problem}</Notice>}
            {result && <Answer result={result} />}
          </form>
        </CardContent>
      </Card>
    </Section>
  );
}

function Answer({ result }: { result: PolicyEvaluation }) {
  const rule = result.rule;
  return (
    <div className="space-y-2 rounded-lg border p-3">
      {/* Neither answer is coloured as a fault. An authorisation given in
          advance is a legitimate thing an administrator arranged, and painting
          it amber would be this page disagreeing with the operator. */}
      <Chip tone={result.auto_approve ? "info" : "neutral"}>
        {result.auto_approve
          ? "Authorised in advance — nobody would be asked"
          : "A person would be asked"}
      </Chip>
      {/* The server's prose, shown as written. It names the rule and says why,
          and rebuilding that sentence here would be a second opinion about a
          decision this page does not make. */}
      <p className="text-sm">{result.reason}</p>
      {rule && (
        <p className="text-xs text-muted-foreground">
          Decided by <code className="font-mono">{rule.id}</code> —{" "}
          <span className="font-mono">
            {rule.plugin}/{rule.action} for {rule.principal}
          </span>
          {rule.max_risk === ""
            ? ", an exclusion, which authorises nothing"
            : `, which authorises up to ${riskLabel(rule.max_risk)}`}
          .
        </p>
      )}
    </div>
  );
}
