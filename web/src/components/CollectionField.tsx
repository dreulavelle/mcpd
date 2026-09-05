import { useCallback, useEffect, useState } from "react";
import { api, ApiError, type SettingField, type SettingRow, problemText } from "@/lib/api";
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
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";

/**
 * A table setting: rows shaped by the field's columns, each added, edited and
 * removed on its own through the row endpoints.
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
  const columns = field.columns ?? [];
  const identity = columns[0];

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
        </Label>
        {!readOnly && (
          <Button size="sm" type="button" onClick={() => setEditing("new")}>Add</Button>
        )}
      </div>
      {field.help && <p className="text-xs text-muted-foreground">{field.help}</p>}

      {problem && <Notice tone="problem">{problem}</Notice>}

      {rows !== null && rows.length === 0 && !problem && (
        <p className="text-sm text-muted-foreground">Nothing here yet.</p>
      )}

      {rows !== null && rows.length > 0 && (
        <div className="overflow-x-auto rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                {shown.map((c) => <TableHead key={c.key}>{c.label}</TableHead>)}
                {secrets.map((c) => <TableHead key={c.key}>{c.label}</TableHead>)}
                {!readOnly && <TableHead className="w-0" />}
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((row) => (
                <TableRow key={row.id}>
                  {shown.map((c) => (
                    <TableCell key={c.key} className={c.key === identity?.key ? "font-medium" : ""}>
                      {cellText(row.values[c.key])}
                    </TableCell>
                  ))}
                  {secrets.map((c) => (
                    <TableCell key={c.key}>
                      {row.secrets_set.includes(c.key)
                        ? <Chip tone="good">set</Chip>
                        : <Chip tone="attention">missing</Chip>}
                    </TableCell>
                  ))}
                  {!readOnly && (
                    <TableCell className="whitespace-nowrap text-right">
                      <Button variant="ghost" size="sm" type="button" onClick={() => setEditing(row)}>Edit</Button>
                      <Button variant="ghost" size="sm" type="button" onClick={() => remove(row)}>Remove</Button>
                    </TableCell>
                  )}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      {editing !== null && (
        <RowDialog
          field={field}
          row={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); load(); }}
        />
      )}
    </div>
  );
}

/** A cell, as text. A list joins; a switch says yes or no; nothing is a dash. */
function cellText(v: unknown): string {
  if (v === undefined || v === null || v === "") return "—";
  if (Array.isArray(v)) return v.length ? v.join(", ") : "—";
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
  onSaved: () => void;
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
      if (row) await api.updateSettingRow(field.key, row.id, cleaned, clearing);
      else await api.addSettingRow(field.key, cleaned);
      notify("good", "Saved.");
      onSaved();
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