import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { ShieldBan, ShieldCheck, TriangleAlert } from "lucide-react";
import {
  api,
  ApiError,
  type ApprovalPolicy as Policy,
  type ApprovalRule,
  type PolicyEvaluation,
  problemText,
} from "@/lib/api";
import { riskLabel } from "@/lib/format";
import { useLoader } from "@/lib/hooks";
import { useCan } from "@/lib/session";
import { EmptyState, Loading, Notice, PageHeader, Section } from "@/components/chrome";
import { BypassControl } from "./BypassControl";
import { SettingsForm } from "@/components/SettingsForm";
import { Chip } from "@/components/status";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Switch } from "@/components/ui/switch";
import { useConfirm } from "@/components/confirm";

/** A rule being edited. `key` is React identity and is never sent. */
interface Draft {
  key: string;
  id: string;
  plugin: string;
  action: string;
  principal: string;
  /** "" means always ask. It is a kind of rule, not an unset field. */
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

// Every field is named so `key` cannot reach the body: the endpoint refuses an
// unknown field. A blank selector is sent as the wildcard, never as "", which
// the endpoint also refuses.
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

// Order-insensitive: the server sorts the set canonically, so a rule that moved
// between the two lists on screen is not an edit.
function signature(rules: ApprovalRule[]): string {
  return JSON.stringify(
    rules
      .map((r) => [r.id, r.plugin, r.action, r.principal, r.max_risk, r.note ?? ""])
      .sort((a, b) => (a[0]! < b[0]! ? -1 : a[0]! > b[0]! ? 1 : 0)),
  );
}

/**
 * Puts each warning beside the rule it names.
 *
 * The server phrases them as `rule "id" names ...`, which is the only handle on
 * offer. Anything unattributable is kept in `loose` rather than dropped.
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
  const confirm = useConfirm();
  const mayWrite = useCan("policies:write");
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

  // Never polled: a tick would replace a draft somebody is halfway through.
  useEffect(load, [load]);

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

  // Attributed against the drafts, not the stored set: a rule just renamed has
  // no row under its old id, and its warning would be rendered nowhere.
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
      setSaveProblem(problemText(e, "Couldn't save the rules."));
      notify("problem", "Nothing was saved.");
    } finally {
      setBusy(false);
    }
  }

  // "The rules can't be read" and "there are no rules" behave the same way and
  // are different facts. The page has to say which.
  const unreadable = loadProblem instanceof ApiError
    && loadProblem.code === "unreadable_rules" ? loadProblem : null;

  // The only way out of a stored value this build cannot parse: the editor
  // cannot be drawn over it, and PUT /api/settings refuses this key.
  async function clearUnreadable() {
    if (!(await confirm({
      title: "Delete the saved rules and start with none?",
      description: "They can't be read and aren't in effect. This cannot be undone.",
      action: "Start over",
    }))) return;
    setBusy(true);
    try {
      adopt(await api.saveApprovalPolicy([]));
      setLoadProblem(null);
      notify("good", "Started over with no rules.");
    } catch (e) {
      notify("problem", problemText(e, "That didn't work."));
    } finally {
      setBusy(false);
    }
  }

  const alwaysAsk = drafts.filter((d) => d.max_risk === "");
  const allow = drafts.filter((d) => d.max_risk !== "");

  return (
    <>
      <PageHeader
        title="Policies"
        lede={mayWrite
          ? "Which changes can run without asking anyone."
          : "Which changes run here without asking anyone. Only an administrator can change these."}
      />

      {/* How long a suggestion lives, how long an approval stands, and how
          much may be settled in the conversation. They were on the general
          settings page, a section away from the rules they time. */}
      <ApprovalTimings />

      {/* Beside the rules it temporarily outranks, so the trade is visible in
          one place: this is the alternative to widening one of them. */}
      <BypassControl />

      {unreadable ? (
        <Notice tone="problem" icon={<TriangleAlert />}>
          <strong>The saved rules can't be read.</strong> Every change is going
          to a person, which is not what anybody set up. Nothing can be shown or
          edited until the rules are replaced.
          <span className="mt-1 block font-mono text-xs opacity-80">
            {unreadable.detail}
          </span>
          {mayWrite && (
            <Button
              variant="outline" size="sm" className="mt-3" disabled={busy}
              onClick={clearUnreadable}
            >
              {busy ? "Starting over…" : "Start over with no rules"}
            </Button>
          )}
        </Notice>
      ) : loadProblem ? (
        <Notice tone="problem">
          {problemText(loadProblem, "Couldn't load the approval policy.")}
        </Notice>
      ) : null}

      {policy === null && !loadProblem ? <Loading rows={5} /> : policy === null ? null : (
        <div className="space-y-6">
          <DefaultDecision
            policy={policy} mayWrite={mayWrite} onSaved={load}
          />

          {loose.length > 0 && (
            <Notice tone="attention" icon={<TriangleAlert />}>
              <strong>Some rules match nothing here.</strong>
              {loose.map((w) => (
                <span key={w} className="mt-1 block">{w}</span>
              ))}
            </Notice>
          )}

          {/* The rules and their one save share a containing block, so the
              sticky bar stops pinning where they stop. */}
          <div className="space-y-6">
            <Section
              title="Always ask"
              description="A change matching one of these always goes to a person, with no exceptions. To let only Alice run something automatically, write her allow rule and nothing here."
              actions={mayWrite && (
                <Button variant="outline" size="sm" onClick={() => add("")}>
                  Add an always-ask rule
                </Button>
              )}
            >
              {alwaysAsk.length === 0 ? (
                <EmptyState mark={<ShieldBan />} title="No always-ask rules">
                  Nothing is singled out to always go to a person.
                </EmptyState>
              ) : (
                <ul className="space-y-3">
                  {alwaysAsk.map((draft) => (
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
              title="Allow automatically"
              actions={mayWrite && ceilings.length > 0 && (
                <Button variant="outline" size="sm" onClick={() => add(ceilings[0]!)}>
                  Add an allow rule
                </Button>
              )}
            >
              {allow.length === 0 ? (
                <EmptyState mark={<ShieldCheck />} title="No allow rules">
                  Nothing runs without asking. Every change goes to a person.
                </EmptyState>
              ) : (
                <ul className="space-y-3">
                  {allow.map((draft) => (
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
                <strong>Nothing was saved — the rules are as they were.</strong>
                <span className="mt-1 block font-mono text-xs opacity-80">
                  {saveProblem}
                </span>
              </Notice>
            )}

            {mayWrite && (
              <div className="sticky bottom-0 flex flex-wrap items-center gap-3 border-t bg-background/90 py-3 backdrop-blur">
                <Button disabled={!dirty || busy || incomplete} onClick={save}>
                  {busy ? "Saving…" : "Save rules"}
                </Button>
                {dirty ? (
                  <>
                    <Button variant="ghost" disabled={busy} onClick={() => adopt(policy)}>
                      Discard
                    </Button>
                    {incomplete && (
                      <span className="text-xs text-muted-foreground">
                        Every rule needs a name.
                      </span>
                    )}
                  </>
                ) : (
                  <span className="text-xs text-muted-foreground">Nothing to save.</span>
                )}
              </div>
            )}
          </div>

          <WouldThisRun suggestions={suggestions} stale={dirty} />
        </div>
      )}
    </>
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
  const ask = draft.max_risk === "";
  const field = (name: string) => `${draft.key}-${name}`;
  const actions = suggestions[draft.plugin] ?? [
    ...new Set(Object.values(suggestions).flat()),
  ];
  const any = (value: string) => (value === wildcard ? "any" : value);

  if (readOnly) {
    return (
      <li className="space-y-2 rounded-lg border p-3">
        <div className="flex flex-wrap items-center gap-2">
          <code className="font-mono text-sm font-medium">{draft.id}</code>
          {!ask && (
            <Chip tone="info">
              <ShieldCheck className="size-3" aria-hidden="true" />
              Up to {riskLabel(draft.max_risk)}
            </Chip>
          )}
        </div>
        <p className="text-xs text-muted-foreground">
          {any(draft.plugin)} / {any(draft.action)}, proposed by {any(draft.principal)}
        </p>
        {draft.note && <p className="text-sm text-muted-foreground">{draft.note}</p>}
        <RuleWarnings warnings={warnings} ask={ask} />
      </li>
    );
  }

  return (
    <li className="space-y-3 rounded-lg border p-3 sm:p-4">
      <div className="flex flex-wrap items-center justify-end gap-2">
        <div className="flex items-center gap-1">
          <Button
            variant="ghost" size="sm"
            onClick={() => onChange({ max_risk: ask ? ceilings[0] ?? "low" : "" })}
          >
            {ask ? "Change to allow" : "Change to always ask"}
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
          <Label htmlFor={field("id")}>Name</Label>
          <Input
            id={field("id")} value={draft.id} placeholder="routine-radio"
            onChange={(e) => onChange({ id: e.target.value })}
          />
          <p className="text-xs text-muted-foreground">
            Lowercase, no spaces. It appears in the history whenever the rule
            applies.
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
          <Label htmlFor={field("principal")}>Proposed by</Label>
          <Input
            id={field("principal")} value={draft.principal} placeholder={wildcard}
            onChange={(e) => onChange({ principal: e.target.value })}
          />
          <p className="text-xs text-muted-foreground">
            <code className="font-mono">user:alice@example.com</code>,{" "}
            <code className="font-mono">svc:chatgpt</code>, or {wildcard} for anyone.
          </p>
        </div>

        {!ask && (
          <div className="space-y-1.5">
            <Label htmlFor={field("ceiling")}>Up to</Label>
            <NativeSelect
              id={field("ceiling")} value={draft.max_risk}
              onChange={(e) => onChange({ max_risk: e.target.value })}
            >
              {ceilings.map((c) => (
                <option key={c} value={c}>{riskLabel(c)}</option>
              ))}
            </NativeSelect>
            <p className="text-xs text-muted-foreground">
              A change that turns out riskier than this goes to a person.
              Critical always does.
            </p>
          </div>
        )}

        <div className="space-y-1.5 sm:col-span-2 lg:col-span-3">
          <Label htmlFor={field("note")}>Note</Label>
          <Input
            id={field("note")} value={draft.note}
            placeholder={ask ? "Why this needs a person" : "Why this can run without asking"}
            onChange={(e) => onChange({ note: e.target.value })}
          />
          <p className="text-xs text-muted-foreground">
            Recorded with every change this rule decides.
          </p>
        </div>
      </div>

      <RuleWarnings warnings={warnings} ask={ask} />
    </li>
  );
}

function RuleWarnings({ warnings, ask }: { warnings: string[]; ask: boolean }) {
  if (warnings.length === 0) return null;
  return (
    <div className="space-y-2">
      {warnings.map((w) => (
        <Notice key={w} tone="attention" icon={<TriangleAlert />}>
          {w}
          {ask && (
            <span className="mt-1 block">
              So this rule is not sending anything to a person, and an allow rule
              may be deciding instead.
            </span>
          )}
        </Notice>
      ))}
    </div>
  );
}

const RISKS = ["low", "medium", "high", "critical"];

function WouldThisRun({ suggestions, stale }: {
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
      setProblem(problemText(e, "Couldn't ask about that change."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Section
      title="Would this run without asking?"
      description="Nothing is proposed and nothing changes."
    >
      <Card>
        <CardContent>
          {/* Named, because three of these fields share a label with the rule
              rows above and the form is what tells them apart. */}
          <form
            className="space-y-4"
            aria-label="Would this run without asking?"
            onSubmit={check}
          >
            {stale && (
              <Notice tone="attention">
                Your unsaved edits above aren't included — this uses the saved
                rules.
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
                <Label htmlFor="ask-principal">Proposed by</Label>
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
                  How risky the change turns out to be.
                </p>
              </div>

              <div className="flex items-start gap-3 sm:col-span-2 lg:col-span-3">
                <Switch
                  id="ask-reversible" checked={reversible}
                  onCheckedChange={setReversible}
                />
                <div className="space-y-0.5">
                  <Label htmlFor="ask-reversible">Can be undone</Label>
                  <p className="text-xs text-muted-foreground">
                    A change that can't be undone always goes to a person.
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
                  Fill in all three — the answer depends on them.
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
      <Chip tone={result.auto_approve ? "info" : "neutral"}>
        {result.auto_approve ? "Runs without asking" : "Goes to a person"}
      </Chip>
      {/* The server's own sentence, shown as written. */}
      <p className="text-sm">{result.reason}</p>
      {rule && (
        <p className="text-xs text-muted-foreground">
          Rule <code className="font-mono">{rule.id}</code> —{" "}
          {rule.plugin} / {rule.action}, proposed by {rule.principal}
          {rule.max_risk === ""
            ? " — always ask"
            : ` — allow up to ${riskLabel(rule.max_risk)}`}
        </p>
      )}
    </div>
  );
}


/**
 * What a change meets when no rule covers it.
 *
 * At the top of this page rather than buried in Settings, because it is the
 * rule everything else is an exception to. Reading a list of exceptions
 * without knowing what they are exceptions to tells nobody anything.
 */
function DefaultDecision({ policy, mayWrite, onSaved }: {
  policy: Policy;
  mayWrite: boolean;
  onSaved: () => void;
}) {
  const notify = useNotify();
  const [busy, setBusy] = useState(false);

  async function choose(value: string) {
    setBusy(true);
    try {
      await api.saveSettings({ "approval.unmatched": value });
      notify("good", "Saved.");
      onSaved();
    } catch (e) {
      notify("problem", problemText(e, "Couldn't save that."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardContent className="space-y-3">
        <div className="space-y-1.5">
          <Label htmlFor="unmatched">When no rule covers a change</Label>
          <NativeSelect
            id="unmatched" value={policy.unmatched || "none"} disabled={!mayWrite || busy}
            onChange={(e) => choose(e.target.value)}
          >
            <option value="high">Let it run — the assistant already asked</option>
            <option value="medium">Let it run up to medium risk</option>
            <option value="low">Let it run up to low risk</option>
            <option value="none">Hold it for approval here</option>
          </NativeSelect>
        </div>
        <p className="text-sm text-muted-foreground">{policy.default}</p>
        <p className="text-xs text-muted-foreground">
          An always-ask rule below beats this, and beats an allow rule for the
          same change. An allow rule runs as often as an assistant asks, with no
          limit, so keep them narrow.
        </p>
      </CardContent>
    </Card>
  );
}

/**
 * The approval timings, beside the rules they apply to.
 *
 * Split across two pages they were two subjects; together they are one. An
 * operator widening a rule and an operator lengthening the window a suggestion
 * survives are answering the same question about how much this host decides on
 * its own.
 */
function ApprovalTimings() {
  const mayWrite = useCan("policies:write");
  const load = useCallback(() => api.settings(), []);
  const { data, reload } = useLoader(load, "Couldn't load the approval settings.");

  if (!data) return null;
  const groups = data.groups.filter((g) => g.section === "approvals");
  if (groups.length === 0) return null;

  return (
    <div className="mb-4">
      <SettingsForm
        groups={groups} settings={data} onSaved={reload} readOnly={!mayWrite}
      />
    </div>
  );
}
