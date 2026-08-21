import { useState } from "react";
import {
  api, ApiError, type SettingField, type SettingGroup, type SettingsPayload,
} from "./api";
import { Message, Out, Pill, type Notify } from "./components";

export interface FieldLink {
  href: string;
  label: string;
}

/**
 * A form over a set of setting groups.
 *
 * It is shared rather than duplicated because settings live where they are
 * used -- tunnel credentials belong on the Tunnels page, approval rules on
 * Settings -- and a second copy of this would drift from the first in exactly
 * the ways that matter: validation, secret handling, and what "saved" means.
 */
export function SettingsForm({ groups, settings, links, onSaved, show, readOnly = false }: {
  groups: SettingGroup[];
  settings: SettingsPayload;
  links?: Record<string, FieldLink>;
  onSaved: () => void;
  show: Notify;
  /**
   * Renders the values without any way to change them.
   *
   * A user may see how the host is set up -- that is most of understanding
   * what it will do -- but changing it is an administrator's. The API refuses
   * the write regardless; this is so nobody fills in a field and then learns
   * that.
   */
  readOnly?: boolean;
}) {
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
      show("good", result.restart_required?.length
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
      show("problem", "Nothing was saved.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      {problems.map((p, i) => <Message tone="problem" key={i}>{tidy(p)}</Message>)}

      {!settings.encryption_available && (
        <Message tone="attention">
          <span>
            <strong>Passwords and keys can't be saved yet.</strong> mcpd has
            nowhere safe to keep them. Add <code>secret_key_ref</code> to your
            startup file pointing at a key — <code>openssl rand -base64 32</code>{" "}
            makes a good one — then restart.
          </span>
        </Message>
      )}

      {groups.map((group) => {
        const on = !group.enabled_by || valueOf(group.enabled_by) === "true";
        return (
          <div className="card" key={group.name}>
            <div className="card-body">
              {groups.length > 1 && <h3>{group.title}</h3>}
              {group.help && (
                <p className="note" style={{ marginBottom: "var(--s5)" }}>{group.help}</p>
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
            </div>
          </div>
        );
      })}

      {!readOnly && (
        <div className="savebar">
          <button className="btn primary" disabled={!dirty || busy} onClick={save}>
            {busy ? "Saving…" : "Save changes"}
          </button>
          {dirty
            ? <button className="btn quiet" disabled={busy}
                      onClick={() => { setDraft({}); setClearing([]); }}>Discard</button>
            : <span className="note tight">Nothing to save.</span>}
        </div>
      )}
    </>
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
      <div className="field">
        <label className="switch" htmlFor={id}>
          <input id={id} type="checkbox" checked={value === "true"} disabled={readOnly}
                 onChange={(e) => onChange(String(e.target.checked))} />
          <span className="switch-track" aria-hidden="true" />
          <span>
            <span className="switch-label">{field.label}</span>
            {field.help && <p className="note tight">{field.help}</p>}
          </span>
        </label>
      </div>
    );
  }

  return (
    <div className="field">
      <label htmlFor={id}>
        {field.label}
        {field.apply === "restart" && <> <Pill tone="attention">needs a restart</Pill></>}
      </label>

      {field.kind === "enum" ? (
        <select id={id} value={value} disabled={readOnly}
                onChange={(e) => onChange(e.target.value)}>
          {field.options?.map((o) => (
            <option key={o} value={o}>{optionLabel(field.key, o)}</option>
          ))}
        </select>
      ) : field.kind === "secret" ? (
        <div className="row">
          <input id={id} type="password" autoComplete="new-password"
                 disabled={clearing || readOnly}
                 placeholder={isSet ? "Saved — type to replace" : field.placeholder ?? ""}
                 value={value} onChange={(e) => onChange(e.target.value)} />
          {isSet && !readOnly && (
            <button className="btn sm" type="button" onClick={onToggleClear}>
              {clearing ? "Keep" : "Remove"}
            </button>
          )}
        </div>
      ) : (
        <input id={id} disabled={readOnly}
               type={field.kind === "int" || field.kind === "duration" ? "number" : "text"}
               value={value} placeholder={field.placeholder ?? ""}
               min={field.min} max={field.max}
               onChange={(e) => onChange(e.target.value)} />
      )}

      {field.help && <p className="note">{field.help}</p>}
      {field.kind === "duration" && <p className="note">In minutes.</p>}
      {link && <p className="note"><Out href={link.href}>{link.label}</Out></p>}
    </div>
  );
}

/**
 * Presents a stored enum value.
 *
 * Only roles need it, and only to capitalise them: the stored values are
 * lowercase and a dropdown reading "user" looks like a bug. The branches this
 * used to carry -- risk levels, and an empty option meaning "never require a
 * second person" -- described settings that no longer exist.
 */
function optionLabel(key: string, option: string): string {
  if (!key.endsWith("role")) return option;
  return { user: "User", admin: "Admin" }[option] ?? option;
}

/** Strips the internal prefix off a validation message. */
function tidy(problem: string): string {
  return problem.replace(/^settings:\s*/, "");
}
