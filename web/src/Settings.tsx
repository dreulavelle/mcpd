import { useCallback, useEffect, useState } from "react";
import {
  api,
  ApiError,
  type SettingField,
  type SettingsPayload,
} from "./api";
import { Banner } from "./components";

/**
 * Settings page.
 *
 * Everything that can change while mcpd is running is editable here. The few
 * things that cannot -- addresses and file paths, which have to be known
 * before anything starts -- are shown read-only at the bottom, so someone
 * looking for a setting finds out where it lives rather than concluding it
 * doesn't exist.
 */
export function Settings() {
  const [payload, setPayload] = useState<SettingsPayload | null>(null);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [clearing, setClearing] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string[]>([]);
  const [saved, setSaved] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const p = await api.settings();
      setPayload(p);
      setDraft({});
      setClearing([]);
    } catch (e) {
      setError([e instanceof Error ? e.message : "Could not load settings."]);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  if (!payload) {
    return error.length ? <Banner tone="error">{error[0]}</Banner> : <p className="empty">Loading…</p>;
  }

  const dirty = Object.keys(draft).length > 0 || clearing.length > 0;

  function valueOf(field: SettingField): string {
    if (field.key in draft) return draft[field.key]!;
    const stored = payload!.values[field.key];
    if (stored === undefined || stored === null) return "";
    if (Array.isArray(stored)) return stored.join(", ");
    return String(stored);
  }

  function set(key: string, value: string) {
    setDraft((d) => ({ ...d, [key]: value }));
    setSaved(null);
  }

  async function save() {
    setBusy(true);
    setError([]);
    setSaved(null);
    try {
      const result = await api.saveSettings(draft, clearing);

      const notes: string[] = [];
      if (result.reconnected?.length) {
        notes.push(`Reconnected: ${result.reconnected.join(", ")}.`);
      }
      if (result.restart_required?.length) {
        notes.push("Some changes need mcpd restarted before they take effect.");
      }
      setSaved(notes.length ? `Saved. ${notes.join(" ")}` : "Saved.");
      await load();
    } catch (e) {
      if (e instanceof ApiError) {
        const problems = (e as unknown as { problems?: string[] }).problems;
        setError(problems?.length ? problems : [e.detail]);
      } else {
        setError(["Could not save. Is mcpd still running?"]);
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <h1>Settings</h1>
      <p className="subtitle">
        Change how mcpd behaves. Most of it takes effect straight away — you'll
        be told when something needs a restart.
      </p>

      {error.map((e, i) => (
        <Banner tone="error" key={i}>{e}</Banner>
      ))}
      {saved && <Banner tone="ok">{saved}</Banner>}

      {!payload.encryption_available && (
        <Banner tone="warn">
          Passwords and keys can't be saved here yet, because mcpd has nowhere
          safe to keep them. Add <code>secret_key_ref</code> to your startup
          file pointing at a key — <code>openssl rand -base64 32</code> makes a
          good one — and restart.
        </Banner>
      )}

      {payload.groups.map((group) => {
        const enabled =
          !group.enabled_by || valueOf({ key: group.enabled_by } as SettingField) === "true";

        return (
          <div className="card" key={group.name}>
            <div className="card-body">
              <h2 style={{ marginTop: 0 }}>{group.title}</h2>
              {group.help && <p className="hint">{group.help}</p>}

              {group.fields.map((field) => {
                // Hide the detail of a switched-off group rather than showing
                // a form that does nothing.
                if (group.enabled_by && field.key !== group.enabled_by && !enabled) {
                  return null;
                }
                return (
                  <FieldInput
                    key={field.key}
                    field={field}
                    value={valueOf(field)}
                    secretSet={payload.secrets_set[field.key] ?? false}
                    clearing={clearing.includes(field.key)}
                    onChange={(v) => set(field.key, v)}
                    onClear={() =>
                      setClearing((c) =>
                        c.includes(field.key)
                          ? c.filter((k) => k !== field.key)
                          : [...c, field.key],
                      )
                    }
                  />
                );
              })}
            </div>
          </div>
        );
      })}

      <div className="save-bar">
        <button className="btn primary" disabled={!dirty || busy} onClick={save}>
          {busy ? "Saving…" : "Save changes"}
        </button>
        {dirty && (
          <button className="btn" disabled={busy} onClick={() => { setDraft({}); setClearing([]); }}>
            Discard
          </button>
        )}
        {!dirty && <span className="hint" style={{ margin: 0 }}>No unsaved changes.</span>}
      </div>

      <div className="card">
        <div className="card-body">
          <h2 style={{ marginTop: 0 }}>Set at startup</h2>
          <p className="hint">
            These have to be known before mcpd can start, so they live in its
            startup file rather than here. Editing them means editing that file
            and restarting.
          </p>
          <dl className="settings">
            {payload.bootstrap.map((b) => (
              <div className="setting" key={b.key}>
                <dt title={b.key}>{b.label}</dt>
                <dd>
                  <code>{b.value || "(not set)"}</code>
                  {b.help && <span className="hint inline">{b.help}</span>}
                </dd>
              </div>
            ))}
          </dl>
        </div>
      </div>
    </>
  );
}

function FieldInput({
  field,
  value,
  secretSet,
  clearing,
  onChange,
  onClear,
}: {
  field: SettingField;
  value: string;
  secretSet: boolean;
  clearing: boolean;
  onChange: (v: string) => void;
  onClear: () => void;
}) {
  const id = `set-${field.key}`;

  if (field.kind === "bool") {
    return (
      <div className="field toggle">
        <label htmlFor={id}>
          <input
            id={id}
            type="checkbox"
            checked={value === "true"}
            onChange={(e) => onChange(String(e.target.checked))}
          />
          <span>{field.label}</span>
        </label>
        {field.help && <p className="hint">{field.help}</p>}
      </div>
    );
  }

  return (
    <div className="field">
      <label htmlFor={id}>
        {field.label}
        {field.apply === "restart" && <span className="pill">needs restart</span>}
      </label>

      {field.kind === "enum" ? (
        <select id={id} value={value} onChange={(e) => onChange(e.target.value)}>
          {field.options?.map((o) => (
            <option key={o} value={o}>
              {o === "" ? "Never require a second person" : o}
            </option>
          ))}
        </select>
      ) : field.kind === "secret" ? (
        <div className="secret-field">
          <input
            id={id}
            type="password"
            autoComplete="new-password"
            placeholder={secretSet ? "Saved — type to replace" : field.placeholder ?? ""}
            value={value}
            disabled={clearing}
            onChange={(e) => onChange(e.target.value)}
          />
          {secretSet && (
            <button className="btn small" type="button" onClick={onClear}>
              {clearing ? "Keep" : "Remove"}
            </button>
          )}
        </div>
      ) : (
        <input
          id={id}
          type={field.kind === "int" || field.kind === "duration" ? "number" : "text"}
          value={value}
          placeholder={field.placeholder ?? ""}
          min={field.min}
          max={field.max}
          onChange={(e) => onChange(e.target.value)}
        />
      )}

      {field.help && <p className="hint">{field.help}</p>}
      {field.kind === "duration" && <p className="hint">Measured in minutes.</p>}
    </div>
  );
}
