import { useCallback, useState } from "react";
import { api, ApiError, type Role, type User } from "./api";
import { Message, Skeleton, usePoll, useToasts, type Notify } from "./components";

const ROLES: [Role, string][] = [
  ["user", "User"],
  ["admin", "Admin"],
];

/**
 * Users.
 *
 * An account is an email address, a role, and the systems it may reach. The
 * page is a list of them because that is all an account is, and because the
 * question an operator arrives with is almost always "who can do what here".
 */
export function Users() {
  const [users, setUsers] = useState<User[] | null>(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const { show, view } = useToasts();

  const load = useCallback(() => {
    api.users()
      .then((r) => { setUsers(r.users ?? []); setError(""); })
      .catch(() => setError("Couldn't load accounts."));
  }, []);
  usePoll(load, 30_000);

  return (
    <>
      <div className="row">
        <h1 className="grow">Users</h1>
        {users && (
          <button className="btn primary" onClick={() => setAdding(true)}>Add user</button>
        )}
      </div>
      <p className="lede">
        Everyone signs in with their own email and password. Roles decide what
        they may do; the systems list decides what they can see.
      </p>

      {error && <Message tone="problem">{error}</Message>}

      {adding && (
        <AddUser
          onClose={() => setAdding(false)}
          onAdded={(email) => { setAdding(false); load(); show("good", `Added ${email}.`); }}
        />
      )}

      {!users ? <Skeleton rows={4} /> : (
        <div className="card">
          <div className="tablewrap">
            <table>
              <thead>
                <tr>
                  <th>Email</th><th>Role</th><th>Can reach</th>
                  <th>Last signed in</th><th />
                </tr>
              </thead>
              <tbody>
                {users.map((u) => (
                  <UserRow key={u.id} user={u} onChanged={load} notify={show} />
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {view}
    </>
  );
}

function UserRow({ user, onChanged, notify }: {
  user: User;
  onChanged: () => void;
  notify: Notify;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const run = async (what: string, fn: () => Promise<unknown>) => {
    setBusy(true);
    setError("");
    try {
      await fn();
      onChanged();
      notify("good", what);
    } catch (e) {
      // The API's refusals here are all things a person can act on -- the last
      // administrator, a duplicate address -- so the text is shown rather than
      // replaced with something generic.
      setError(e instanceof ApiError ? e.detail : "That didn't work.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <tr style={user.disabled ? { opacity: 0.55 } : undefined}>
      <td>
        {user.email}
        {user.self && <span className="pill info" style={{ marginLeft: 8 }}>you</span>}
        {user.disabled && <span className="pill" style={{ marginLeft: 8 }}>disabled</span>}
        {error && <div className="note problem">{error}</div>}
      </td>
      <td>
        <select
          value={user.role} disabled={busy}
          onChange={(e) => run("Role changed.", () =>
            api.updateUser(user.id, { role: e.target.value as Role }))}
        >
          {ROLES.map(([id, label]) => <option key={id} value={id}>{label}</option>)}
        </select>
      </td>
      <td className="dim">
        {user.plugins.includes("*") ? "Everything" : user.plugins.join(", ") || "Nothing"}
      </td>
      <td className="dim" style={{ whiteSpace: "nowrap" }}>
        {user.last_login_at ? new Date(user.last_login_at).toLocaleString() : "Never"}
      </td>
      <td style={{ whiteSpace: "nowrap" }}>
        <button className="btn sm quiet" disabled={busy}
                onClick={() => run(user.disabled ? "Enabled." : "Disabled.", () =>
                  api.updateUser(user.id, { disabled: !user.disabled }))}>
          {user.disabled ? "Enable" : "Disable"}
        </button>
        <button className="btn sm quiet" disabled={busy || user.self}
                title={user.self ? "You cannot delete the account you are signed in as" : undefined}
                onClick={() => {
                  if (!confirm(`Delete ${user.email}? This cannot be undone.`)) return;
                  run("Account deleted.", () => api.deleteUser(user.id));
                }}>
          Delete
        </button>
      </td>
    </tr>
  );
}

function AddUser({ onClose, onAdded }: {
  onClose: () => void;
  onAdded: (email: string) => void;
}) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<Role>("user");
  const [everything, setEverything] = useState(true);
  const [plugins, setPlugins] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const granted = everything
        ? ["*"]
        : plugins.split(",").map((p) => p.trim()).filter(Boolean);
      await api.createUser({ email: email.trim(), password, role, plugins: granted });
      onAdded(email.trim());
    } catch (err) {
      setError(err instanceof ApiError ? err.detail : "Couldn't add that account.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="card">
      <div className="card-head"><h3>Add a user</h3></div>
      <div className="card-body">
        {error && <Message tone="problem">{error}</Message>}
        <form onSubmit={submit}>
          <div className="field">
            <label htmlFor="new-email">Email</label>
            <input id="new-email" type="email" autoComplete="off" value={email}
                   onChange={(e) => setEmail(e.target.value)} placeholder="them@example.com" />
          </div>

          <div className="field">
            <label htmlFor="new-password">Password</label>
            <input id="new-password" type="password" autoComplete="new-password"
                   value={password} onChange={(e) => setPassword(e.target.value)}
                   placeholder="At least 12 characters" />
          </div>

          <div className="field">
            <label htmlFor="new-role">Role</label>
            <select id="new-role" value={role} onChange={(e) => setRole(e.target.value as Role)}>
              {ROLES.map(([id, label]) => <option key={id} value={id}>{label}</option>)}
            </select>
          </div>

          <div className="field">
            <label htmlFor="new-scope">Can reach</label>
            <select id="new-scope" value={everything ? "all" : "some"}
                    onChange={(e) => setEverything(e.target.value === "all")}>
              <option value="all">Every system on this host</option>
              <option value="some">Only the systems I list</option>
            </select>
            {!everything && (
              <input style={{ marginTop: "var(--s2)" }} value={plugins}
                     onChange={(e) => setPlugins(e.target.value)}
                     placeholder="cnmaestro, netbox" />
            )}
          </div>

          <div className="row">
            <button className="btn primary" type="submit"
                    disabled={busy || !email.trim() || !password}>
              {busy ? "Adding…" : "Add user"}
            </button>
            <button className="btn quiet" type="button" onClick={onClose}>Cancel</button>
          </div>
        </form>
      </div>
    </div>
  );
}
