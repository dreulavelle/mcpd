import { useState } from "react";
import {
  api, ApiError, type SettingField, type SettingGroup, type SettingsPayload,
} from "@/lib/api";
import { Notice, Out } from "@/components/chrome";
import { Chip } from "@/components/status";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { Switch } from "@/components/ui/switch";

export interface FieldLink {
  href: string;
  label: string;
}

/**
 * A form over a set of setting groups.
 *
 * Shared rather than duplicated because settings live where they are used --
 * tunnel credentials belong on the Tunnels page, a plugin's credentials on its
 * own page, approval rules on Settings -- and a second copy of this would
 * drift from the first in exactly the ways that matter: validation, secret
 * handling, and what "saved" means.
 */
export function SettingsForm({ groups, settings, links, onSaved, readOnly = false }: {
  groups: SettingGroup[];
  settings: SettingsPayload;
  links?: Record<string, FieldLink>;
  onSaved: () => void;
  /**
   * Renders the values with no way to change them.
   *
   * Somebody who may read the host's configuration understands most of what it
   * will do; changing it is an administrator's. The API refuses the write
   * regardless -- this is so nobody fills in a field and then learns that.
   */
  readOnly?: boolean;
}) {
  const notify = useNotify();
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [clearing, setClearing] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [problems, setProblems] = useState<string[]>([]);

  const dirty = Object.keys(draft).length > 0 || clearing.length > 0;

  const valueOf = (key: string): string => {
    if (key in draft) return draft[key]!;
    const stored = settings.values[key];
    if (stored === undefined || stored === null) return "";
    if (Array.isArray(stored)) return stored.join(", ");
    return String(stored);
  };

  async function save() {
    setBusy(true);
    setProblems([]);
    try {
      const result = await api.saveSettings(draft, clearing);
      notify("good", result.restart_required?.length
        ? "Saved — some of it needs a restart."
        : "Saved.");
      setDraft({});
      setClearing([]);
      onSaved();
    } catch (e) {
      if (e instanceof ApiError) {
        const list = (e as unknown as { problems?: string[] }).problems;
        setProblems(list?.length ? list : [e.detail]);
      } else {
        setProblems(["Couldn't save. Is mcpd still running?"]);
      }
      notify("problem", "Nothing was saved.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="space-y-4">
      {problems.map((p, i) => (
        <Notice tone="problem" key={i}>{tidy(p)}</Notice>
      ))}

      {!settings.encryption_available && (
        <Notice tone="attention">
          <strong>Passwords and keys can't be saved yet.</strong> mcpd has
          nowhere safe to keep them. Add <code className="font-mono">secret_key_ref</code>{" "}
          to your startup file pointing at a key —{" "}
          <code className="font-mono">openssl rand -base64 32</code> makes a good
          one — then restart.
        </Notice>
      )}

      {groups.map((group) => {
        const on = !group.enabled_by || valueOf(group.enabled_by) === "true";
        return (
          <Card key={group.name}>
            {groups.length > 1 && (
              <CardHeader>
                <CardTitle className="text-base">{group.title}</CardTitle>
                {group.help && (
                  <p className="text-sm text-muted-foreground">{group.help}</p>
                )}
              </CardHeader>
            )}
            <CardContent className="space-y-5">
              {groups.length === 1 && group.help && (
                <p className="text-sm text-muted-foreground">{group.help}</p>
              )}
              {group.fields.map((f) => {
                if (group.enabled_by && f.key !== group.enabled_by && !on) return null;
                return (
                  <Field
                    key={f.key} field={f} value={valueOf(f.key)}
                    link={links?.[f.key]}
                    isSet={settings.secrets_set[f.key] ?? false}
                    clearing={clearing.includes(f.key)}
                    readOnly={readOnly}
                    onChange={(v) => setDraft((d) => ({ ...d, [f.key]: v }))}
                    onToggleClear={() =>
                      setClearing((c) => c.includes(f.key)
                        ? c.filter((k) => k !== f.key)
                        : [...c, f.key])}
                  />
                );
              })}
            </CardContent>
          </Card>
        );
      })}

      {!readOnly && (
        <div className="sticky bottom-0 flex items-center gap-3 border-t bg-background/90 py-3 backdrop-blur">
          <Button disabled={!dirty || busy} onClick={save}>
            {busy ? "Saving…" : "Save changes"}
          </Button>
          {dirty ? (
            <Button
              variant="ghost" disabled={busy}
              onClick={() => { setDraft({}); setClearing([]); }}
            >
              Discard
            </Button>
          ) : (
            <span className="text-xs text-muted-foreground">Nothing to save.</span>
          )}
        </div>
      )}
    </div>
  );
}

function Field({ field, value, link, isSet, clearing, readOnly, onChange, onToggleClear }: {
  field: SettingField;
  value: string;
  link?: FieldLink;
  isSet: boolean;
  clearing: boolean;
  readOnly: boolean;
  onChange: (v: string) => void;
  onToggleClear: () => void;
}) {
  const id = `s-${field.key}`;

  if (field.kind === "bool") {
    return (
      <div className="flex items-start gap-3">
        <Switch
          id={id} checked={value === "true"} disabled={readOnly}
          onCheckedChange={(checked) => onChange(String(checked))}
        />
        <div className="space-y-0.5">
          <Label htmlFor={id}>{field.label}</Label>
          {field.help && (
            <p className="text-xs text-muted-foreground">{field.help}</p>
          )}
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-1.5">
      <Label htmlFor={id} className="flex flex-wrap items-center gap-2">
        {field.label}
        {field.apply === "restart" && <Chip tone="attention">needs a restart</Chip>}
      </Label>

      {field.kind === "enum" ? (
        <NativeSelect
          id={id} value={value} disabled={readOnly}
          onChange={(e) => onChange(e.target.value)}
        >
          {field.options?.map((o) => (
            <option key={o} value={o}>{optionLabel(field.key, o)}</option>
          ))}
        </NativeSelect>
      ) : field.kind === "secret" ? (
        <div className="flex items-center gap-2">
          <Input
            id={id} type="password" autoComplete="new-password"
            disabled={clearing || readOnly}
            placeholder={isSet ? "Saved — type to replace" : field.placeholder ?? ""}
            value={value} onChange={(e) => onChange(e.target.value)}
          />
          {isSet && !readOnly && (
            <Button variant="outline" size="sm" type="button" onClick={onToggleClear}>
              {clearing ? "Keep" : "Remove"}
            </Button>
          )}
        </div>
      ) : (
        <Input
          id={id} disabled={readOnly}
          type={field.kind === "int" || field.kind === "duration" ? "number" : "text"}
          value={value} placeholder={field.placeholder ?? ""}
          min={field.min} max={field.max}
          onChange={(e) => onChange(e.target.value)}
        />
      )}

      {field.help && <p className="text-xs text-muted-foreground">{field.help}</p>}
      {field.kind === "duration" && (
        <p className="text-xs text-muted-foreground">In minutes.</p>
      )}
      {link && (
        <p className="text-xs"><Out href={link.href}>{link.label}</Out></p>
      )}
    </div>
  );
}

/**
 * Presents a stored enum value.
 *
 * Only roles need it, and only to capitalise them: the stored values are
 * lowercase and a dropdown reading "user" looks like a bug.
 */
function optionLabel(key: string, option: string): string {
  if (!key.endsWith("role")) return option;
  return { user: "User", admin: "Admin" }[option] ?? option;
}

/** Strips the internal prefix off a validation message. */
function tidy(problem: string): string {
  return problem.replace(/^settings:\s*/, "");
}
