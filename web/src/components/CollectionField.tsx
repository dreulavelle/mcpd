import { useCallback, useEffect, useMemo, useState } from "react";
import { api, ApiError, type SettingField, type SettingRow, problemText } from "@/lib/api";
import { useQueryParam } from "@/lib/router";
import { weakestWord } from "@/lib/search";
import { Notice } from "@/components/chrome";
import { useConfirm } from "@/components/confirm";
import { Chip } from "@/components/status";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Switch } from "@/components/ui/switch";

/**
 * A table setting: rows shaped by the field's columns, each added, edited and
 * removed on its own through the row endpoints.
 *
 * Drawn as a list rather than a table, and that is not a style choice. A
 * collection's width is whatever the plugin declared: six columns holding a
 * name, a business, an alias list, an FQDN, an extension and a secret do not
 * fit the settings panel, and a table that does not fit gets a scroll bar
 * inside the page -- which hides columns behind a gesture nobody makes, and
 * hides them worst on the narrow screens where the page is already hard. A
 * list wraps instead. Each row leads with the column that names it and carries
 * the rest as labelled pairs, so a seventh column costs a line break rather
 * than another thing off the right-hand edge.
 *
 * Not part of the surrounding form's draft. A row's secret column cannot live
 * in a draft of strings without being sent back whole on every save, and a
 * customer's credential is exactly the value that must be replaceable one at
 * a time. So a row is saved when its dialog is, and the list reloads.
 */
export function CollectionField({ field, readOnly }: { field: SettingField; readOnly: boolean }) {
  const notify = useNotify();
  const confirm = useConfirm();
  const [rows, setRows] = useState<SettingRow[] | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [editing, setEditing] = useState<SettingRow | "new" | null>(null);
  // The row last saved from this page, shown whether or not it matches the
  // search. Adding "Initech" while the list is narrowed to "acme" otherwise
  // saves it straight out of sight, and the only sign it worked is a count
  // going up by one -- which reads as the save having gone somewhere else.
  const [saved, setSaved] = useState<string | null>(null);
  const columns = field.columns ?? noColumns;
  const identity = columns[0];
  // Named after the field's own last segment -- "customers", "sites" -- so the
  // address says what is being searched, two collections on one page do not
  // fight over one parameter, and a link can arrive with the search already in
  // it. That last one is the point: "the row for X" is a thing people send
  // each other.
  const [query, setQuery] = useQueryParam(field.key.split(".").pop() || "find");
  const q = query.trim();
  // A search arriving in a link is honoured on a short list too, so the box
  // has to be there whenever one is in force: rows hidden by a filter nobody
  // can see or clear read as rows that were deleted.
  const searchable = rows !== null && (rows.length >= searchFrom || q !== "");
  useEffect(() => setSaved(null), [q]);

  const matching = useMemo(() => {
    if (rows === null) return null;
    if (!q) return rows;
    // Matched with the command palette's scorer, not a second one written
    // here: every word has to appear somewhere in the row, in any order, which
    // is how people search for a thing they half remember. Filtered but not
    // reordered -- a settings list is an inventory, and rows that rearrange
    // themselves under the cursor are hard to work in.
    return rows.filter((r) => r.id === saved || weakestWord(haystack(columns, r), q) >= asSubstring);
  }, [rows, q, columns, saved]);

  const load = useCallback(() => {
    api.settingRows(field.key)
      .then((r) => { setRows(r.rows); setProblem(null); })
      .catch((e) => setProblem(problemText(e, "Couldn't load the rows.")));
  }, [field.key]);
  useEffect(load, [load]);

  async function remove(row: SettingRow) {
    const name = String(row.values[identity?.key ?? ""] ?? row.id);
    const ok = await confirm({
      title: `Remove ${name}?`,
      description: `Its entry in ${field.label.toLowerCase()} and any credential it holds are forgotten. This cannot be undone.`,
      action: "Remove",
    });
    if (!ok) return;
    try {
      await api.removeSettingRow(field.key, row.id);
      notify("good", `Removed ${name}.`);
      load();
    } catch (e) {
      notify("problem", problemText(e, "Couldn't remove it."));
    }
  }

  const shown = columns.filter((c) => c.kind !== "secret");
  const secrets = columns.filter((c) => c.kind === "secret");

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Label className="flex flex-wrap items-center gap-2">
          {field.label}
          {field.required && rows !== null && rows.length === 0 && (
            <Chip tone="attention">needs at least one</Chip>
          )}
          {rows !== null && matching !== null && searchable && (
            <span className="text-xs font-normal text-muted-foreground">
              {matching.length === rows.length
                ? `${rows.length}`
                : `${matching.length} of ${rows.length}`}
            </span>
          )}
        </Label>
        <div className="flex flex-wrap items-center gap-2">
          {searchable && (
            <Input
              aria-label={`Find in ${field.label.toLowerCase()}`}
              className="h-8 w-48"
              placeholder="Find…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
          )}
          {!readOnly && (
            <Button size="sm" type="button" onClick={() => setEditing("new")}>Add</Button>
          )}
        </div>
      </div>
      {field.help && <p className="text-xs text-muted-foreground">{field.help}</p>}

      {problem && <Notice tone="problem">{problem}</Notice>}

      {rows !== null && rows.length === 0 && !problem && (
        <p className="text-sm text-muted-foreground">Nothing here yet.</p>
      )}

      {matching !== null && rows !== null && rows.length > 0 && matching.length === 0 && (
        <p className="text-sm text-muted-foreground">
          Nothing here matches “{q}”.{" "}
          <button
            type="button"
            className="text-primary hover:underline"
            onClick={() => setQuery("")}
          >
            Show all {rows.length}
          </button>
        </p>
      )}

      {matching !== null && matching.length > 0 && (
        <ul className="divide-y rounded-md border">
          {matching.map((row) => (
            <li
              key={row.id}
              className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2 p-3"
            >
              <div className="min-w-0 space-y-1">
                <p className="text-sm font-medium break-words">
                  {cellText(row.values[identity?.key ?? ""])}
                </p>
                <dl className="flex flex-wrap gap-x-4 gap-y-1 text-xs">
                  {shown.filter((c) => c.key !== identity?.key).map((c) => {
                    const text = cellText(row.values[c.key]);
                    // A column nothing has filled in is left out rather than
                    // shown as a dash. One optional column empty on sixteen of
                    // eighteen rows is sixteen dashes, and they are read before
                    // they can be dismissed.
                    if (text === EMPTY) return null;
                    return (
                      <div key={c.key} className="flex min-w-0 items-baseline gap-1.5">
                        <dt className="shrink-0 text-muted-foreground">{c.label}</dt>
                        <dd className="min-w-0 break-words">{text}</dd>
                      </div>
                    );
                  })}
                  {/* A secret is always named, because "missing" is the whole
                      reason somebody opened this page. */}
                  {secrets.map((c) => (
                    <div key={c.key} className="flex min-w-0 items-baseline gap-1.5">
                      <dt className="shrink-0 text-muted-foreground">{c.label}</dt>
                      <dd>
                        {row.secrets_set.includes(c.key)
                          ? <Chip tone="good">set</Chip>
                          : <Chip tone="attention">missing</Chip>}
                      </dd>
                    </div>
                  ))}
                </dl>
              </div>
              {!readOnly && (
                <div className="flex shrink-0 gap-1">
                  <Button variant="ghost" size="sm" type="button" onClick={() => setEditing(row)}>Edit</Button>
                  <Button variant="ghost" size="sm" type="button" onClick={() => remove(row)}>Remove</Button>
                </div>
              )}
            </li>
          ))}
        </ul>
      )}

      {editing !== null && (
        <RowDialog
          field={field}
          row={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={(r) => { setEditing(null); setSaved(r.id); load(); }}
        />
      )}
    </div>
  );
}

/**
 * How many rows there have to be before a search box is worth its space.
 *
 * Under this a person finds a row by looking. Well over it -- an MSP adding
 * phone systems by the hundred -- looking is not an option, and the box is the
 * only way to reach a row at all.
 */
const searchFrom = 10;

/**
 * The weakest score this filter accepts: the palette's lowest substring
 * bucket, which leaves out its subsequence one.
 *
 * The palette wants that bucket -- guessing at a half-typed command is its
 * job. A list of configured rows does not: searching pbx2.example there turned
 * up pbx2.globex.example, because every character of the first appears
 * somewhere in the second in order. A row that is not the one you named is
 * worse than no row, especially in a list long enough to need searching, where
 * nobody is going to check whether the one match is really a match.
 */
const asSubstring = 60;

/** Stable, so a field without columns does not rebuild every row's haystack each render. */
const noColumns: SettingField[] = [];

/** What an empty value reads as where one is shown at all. */
const EMPTY = "—";

/**
 * The text of one row that a search runs against: every column a reader can
 * see, in one string.
 *
 * Every column, not just the one that names the row, because the thing people
 * have to hand is rarely the name -- it is the address they are looking at, an
 * alias somebody used, or the business a site belongs to. A secret is not in
 * here: it is not on the page, and a value nobody can see is not one anybody
 * can search for.
 *
 * The stored value goes in beside the shown one where the two differ, because
 * an address is most often pasted from a browser, scheme and all, and the
 * list shows it without one.
 */
function haystack(columns: SettingField[], row: SettingRow): string {
  return columns
    .filter((c) => c.kind !== "secret")
    .flatMap((c) => {
      const v = row.values[c.key];
      const shown = cellText(v);
      if (shown === EMPTY) return [];
      return typeof v === "string" && v !== shown ? [shown, v] : [shown];
    })
    .join(" ");
}

/** A cell, as text. A list joins; a switch says yes or no; nothing is a dash. */
function cellText(v: unknown): string {
  if (v === undefined || v === null || v === "") return EMPTY;
  if (Array.isArray(v)) return v.length ? v.join(", ") : EMPTY;
  if (typeof v === "boolean") return v ? "yes" : "no";
  return tidyAddress(String(v));
}

/**
 * Drops the scheme from an https address, and any trailing slash.
 *
 * In a table the scheme is noise: https is what an address is unless somebody
 * says otherwise, and a column of them repeats eight characters on every row.
 * `http://` is left exactly as it is, because that one is the exception and
 * seeing it is the point. Display only -- what was typed is what is stored,
 * and the edit dialog shows that.
 */
function tidyAddress(v: string): string {
  if (!v.startsWith("https://")) return v;
  const trimmed = v.slice("https://".length).replace(/\/+$/, "");
  return trimmed || v;
}

/** The form for one row: every column, with a secret shown as saved or not. */
function RowDialog({ field, row, onClose, onSaved }: {
  field: SettingField;
  row: SettingRow | null;
  onClose: () => void;
  onSaved: (row: SettingRow) => void;
}) {
  const notify = useNotify();
  const columns = field.columns ?? [];
  const [values, setValues] = useState<Record<string, string>>(() => {
    const out: Record<string, string> = {};
    for (const c of columns) {
      if (c.kind === "secret") { out[c.key] = ""; continue; }
      const v = row?.values[c.key];
      out[c.key] = v === undefined || v === null ? (c.default === undefined ? "" : String(c.default))
        : Array.isArray(v) ? v.join(", ") : String(v);
    }
    return out;
  });
  const [clearing, setClearing] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [problems, setProblems] = useState<string[]>([]);
  const title = row ? `Edit ${cellText(row.values[columns[0]?.key ?? ""])}` : `Add to ${field.label.toLowerCase()}`;

  async function save() {
    setBusy(true);
    setProblems([]);
    try {
      const cleaned = tidyValues(columns, values);
      setValues(cleaned);
      const saved = row
        ? await api.updateSettingRow(field.key, row.id, cleaned, clearing)
        : await api.addSettingRow(field.key, cleaned);
      notify("good", "Saved.");
      onSaved(saved);
    } catch (e) {
      if (e instanceof ApiError && e.problems?.length) setProblems(e.problems);
      else setProblems([problemText(e, "Couldn't save. Try again in a moment.")]);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {field.help && <DialogDescription>{field.help}</DialogDescription>}
        </DialogHeader>

        {problems.map((p, i) => (
          <Notice tone="problem" key={i}>{p.replace(/^settings:\s*/, "")}</Notice>
        ))}

        <div className="space-y-4">
          {columns.map((c) => {
            const id = `row-${field.key}-${c.key}`;
            const isSet = row?.secrets_set.includes(c.key) ?? false;
            const clearingThis = clearing.includes(c.key);
            if (c.kind === "bool") {
              return (
                <div key={c.key} className="flex items-start gap-3">
                  <Switch
                    id={id} checked={values[c.key] === "true"}
                    onCheckedChange={(checked) => setValues((v) => ({ ...v, [c.key]: String(checked) }))}
                  />
                  <div className="space-y-0.5">
                    <Label htmlFor={id}>{c.label}</Label>
                    {c.help && <p className="text-xs text-muted-foreground">{c.help}</p>}
                  </div>
                </div>
              );
            }
            return (
              <div key={c.key} className="space-y-1.5">
                <Label htmlFor={id}>
                  {c.label}{c.required && <span className="text-muted-foreground"> · required</span>}
                </Label>
                {c.kind === "enum" ? (
                  <NativeSelect
                    id={id} value={values[c.key] ?? ""}
                    onChange={(e) => setValues((v) => ({ ...v, [c.key]: e.target.value }))}
                  >
                    {c.options?.map((o) => (
                      <option key={o} value={o}>{c.option_labels?.[o] ?? o}</option>
                    ))}
                  </NativeSelect>
                ) : c.kind === "secret" ? (
                  <div className="flex items-center gap-2">
                    <Input
                      id={id} type="password" autoComplete="new-password"
                      disabled={clearingThis}
                      placeholder={isSet ? "Saved — type to replace" : c.placeholder ?? ""}
                      value={values[c.key] ?? ""}
                      onChange={(e) => setValues((v) => ({ ...v, [c.key]: e.target.value }))}
                    />
                    {isSet && (
                      <Button
                        variant="outline" size="sm" type="button"
                        onClick={() => setClearing((cl) => cl.includes(c.key)
                          ? cl.filter((k) => k !== c.key) : [...cl, c.key])}
                      >
                        {clearingThis ? "Keep" : "Remove"}
                      </Button>
                    )}
                  </div>
                ) : (
                  <Input
                    id={id}
                    type={c.kind === "int" || c.kind === "duration" ? "number" : "text"}
                    value={values[c.key] ?? ""} placeholder={c.placeholder ?? ""}
                    min={c.min} max={c.max}
                    onChange={(e) => setValues((v) => ({ ...v, [c.key]: e.target.value }))}
                  />
                )}
                {c.help && <p className="text-xs text-muted-foreground">{c.help}</p>}
                {c.kind === "list" && (
                  <p className="text-xs text-muted-foreground">Separate entries with commas.</p>
                )}
              </div>
            );
          })}
        </div>

        <DialogFooter>
          <Button variant="ghost" type="button" disabled={busy} onClick={onClose}>Cancel</Button>
          <Button type="button" disabled={busy} onClick={save}>{busy ? "Saving…" : "Save"}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/**
 * Tidies what a row form submits: a trailing slash off every plain string.
 *
 * An address is pasted out of a browser bar as often as it is typed, and it
 * arrives with the slash the bar puts there. It means nothing in a field like
 * this and it survives into every place the value is shown, so it is dropped
 * once, here, rather than tolerated everywhere downstream.
 *
 * Plain strings only. A secret is bytes and must reach the store exactly as
 * typed; a list has its own separator; a number and a switch have no slash to
 * lose.
 */
function tidyValues(columns: SettingField[], values: Record<string, string>): Record<string, string> {
  const out = { ...values };
  for (const c of columns) {
    if (c.kind !== "string") continue;
    const v = out[c.key];
    if (typeof v !== "string") continue;
    const trimmed = v.trim().replace(/\/+$/, "");
    // A value that is only slashes keeps what was typed, so the server can
    // refuse it and say why rather than the form silently emptying the field.
    out[c.key] = trimmed || v.trim();
  }
  return out;
}