import { useCallback, useEffect, useState } from "react";
import { api, ApiError, type SettingField, type SettingsPayload } from "./api";
import { Message, Out, Pill, Skeleton, useToasts } from "./components";

const OPENAI_TUNNELS = "https://platform.openai.com/settings/organization/tunnels";
const OPENAI_API_KEYS = "https://platform.openai.com/settings/organization/api-keys";

/** Where a field should link to, when the value comes from somewhere else. */
const LINKS: Record<string, { href: string; label: string }> = {
  "tunnel.tunnel_id": { href: OPENAI_TUNNELS, label: "Get one from your OpenAI account" },
  "tunnel.api_key": { href: OPENAI_API_KEYS, label: "Create one on the API keys page" },
};

/**
 * Settings.
 *
 * Everything that can change while mcpd is running is here. The few things
 * that can't — addresses and file paths, which have to be known before
 * anything starts — are shown read-only at the bottom, so someone hunting for
 * a setting finds out where it lives instead of assuming it isn't there.
 */
export function Settings() {
  const [data, setData] = useState<SettingsPayload | null>(null);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [clearing, setClearing] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [problems, setProblems] = useState<string[]>([]);
  const { show, view } = useToasts();

  const load = useCallback(async () => {
    try {
      setData(await api.settings());
      setDraft({});
      setClearing([]);
    } catch {
      setProblems(["Couldn't load settings."]);
    }
  }, []);

  useEffect(() => { load(); }, [load]);

  if (!data) {
    return <><h1>Settings</h1>{problems.length
      ? <Message tone="problem">{problems[0]}</Message>
      : <Skeleton rows={5} />}</>;
  }

  const dirty = Object.keys(draft).length > 0 || clearing.length > 0;

  const valueOf = (key: string): string => {
    if (key in draft) return draft[key]!;
    const stored = data.values[key];
    if (stored === undefined || stored === null) return "";
    if (Array.isArray(stored)) return stored.join(", ");
    return String(stored);
  };

  async function save() {
    setBusy(true);
    setProblems([]);
    try {
      const result = await api.saveSettings(draft, clearing);
      const restart = result.restart_required?.length;
      show("good", restart ? "Saved — some of it needs a restart." : "Saved.");
      await load();
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
      {view}
      <h1>Settings</h1>
      <p className="lede">
        How mcpd behaves. Most changes apply straight away — you'll be told if
        one needs a restart.
      </p>

      {problems.map((p, i) => <Message tone="problem" key={i}>{tidy(p)}</Message>)}

      {!data.encryption_available && (
        <Message tone="attention">
          <strong>Passwords and keys can't be saved yet.</strong> mcpd has
          nowhere safe to keep them. Add <code>secret_key_ref</code> to your
          startup file pointing at a key — <code>openssl rand -base64 32</code>{" "}
          makes a good one — then restart.
        </Message>
      )}

      {data.groups.map((group) => {
        const on = !group.enabled_by || valueOf(group.enabled_by) === "true";
        return (
          <div className="card" key={group.name}>
            <div className="card-body">
              <h3>{group.title}</h3>
              {group.help && <p className="note" style={{ marginBottom: "var(--s5)" }}>{group.help}</p>}

              {group.fields.map((f) => {
                if (group.enabled_by && f.key !== group.enabled_by && !on) return null;
                return (
                  <Field
                    key={f.key} field={f} value={valueOf(f.key)}
                    isSet={data.secrets_set[f.key] ?? false}
                    clearing={clearing.includes(f.key)}
                    onChange={(v) => setDraft((d) => ({ ...d, [f.key]: v }))}
                    onToggleClear={() =>
                      setClearing((c) => c.includes(f.key) ? c.filter((k) => k !== f.key) : [...c, f.key])}
                  />
                );
              })}
            </div>
          </div>
        );
      })}

      <div className="savebar">
        <button className="btn primary" disabled={!dirty || busy} onClick={save}>
          {busy ? "Saving…" : "Save changes"}
        </button>
        {dirty
          ? <button className="btn quiet" disabled={busy}
                    onClick={() => { setDraft({}); setClearing([]); }}>Discard</button>
          : <span className="note tight">Nothing to save.</span>}
      </div>

      <h2>Set when mcpd starts</h2>
      <div className="card">
        <div className="card-body">
          <p className="note" style={{ marginBottom: "var(--s4)" }}>
            These have to be known before mcpd can start, so they live in its
            startup file rather than here. Changing one means editing that file
            and restarting.
          </p>
          <dl className="kv">
            {data.bootstrap.map((b) => (
              <div key={b.key}>
                <dt title={b.key}>{b.label}</dt>
                <dd>
                  <code>{b.value || "not set"}</code>
                  {b.help && <span className="note tight">{b.help}</span>}
                </dd>
              </div>
            ))}
          </dl>
        </div>
      </div>
    </>
  );
}

function Field({ field, value, isSet, clearing, onChange, onToggleClear }: {
  field: SettingField;
  value: string;
  isSet: boolean;
  clearing: boolean;
  onChange: (v: string) => void;
  onToggleClear: () => void;
}) {
  const id = `s-${field.key}`;
  const link = LINKS[field.key];

  if (field.kind === "bool") {
    return (
      <div className="field">
        <label className="switch" htmlFor={id}>
          <input id={id} type="checkbox" checked={value === "true"}
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
        <select id={id} value={value} onChange={(e) => onChange(e.target.value)}>
          {field.options?.map((o) => (
            <option key={o} value={o}>{optionLabel(field.key, o)}</option>
          ))}
        </select>
      ) : field.kind === "secret" ? (
        <div className="row">
          <input id={id} type="password" autoComplete="new-password" disabled={clearing}
                 placeholder={isSet ? "Saved — type to replace" : field.placeholder ?? ""}
                 value={value} onChange={(e) => onChange(e.target.value)} />
          {isSet && (
            <button className="btn sm" type="button" onClick={onToggleClear}>
              {clearing ? "Keep" : "Remove"}
            </button>
          )}
        </div>
      ) : (
        <input id={id}
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

function optionLabel(key: string, option: string): string {
  if (option === "") return "Never require a second person";
  if (key.endsWith("role")) {
    return {
      viewer: "Look things up only",
      operator: "Look things up and suggest changes",
      approver: "Also approve its own changes",
    }[option] ?? option;
  }
  return { low: "Minor", medium: "Moderate", high: "Significant", critical: "Major" }[option] ?? option;
}

/** Strips the internal prefix off a validation message. */
function tidy(problem: string): string {
  return problem.replace(/^settings:\s*/, "");
}
