import { useState } from "react";
import { api, problemText, type Pairing, type WorkspaceCandidate } from "@/lib/api";
import { PairingChip } from "@/components/pairing";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";

/**
 * The ChatGPT workspaces an account's tunnels are listed in, and which is the
 * default.
 *
 * It used to be a comma-separated text box, which asked somebody to find a
 * workspace id they had no way to see. OpenAI publishes no endpoint that lists
 * workspaces, so "Find workspaces" offers the ones the account's organisation
 * already uses -- the workspaces its tunnels sit in -- and nothing is kept
 * until the account is saved, when OpenAI is asked whether each belongs to
 * the organisation. Typing an id still works, for a workspace with no tunnel
 * in it yet.
 */
export function WorkspacePicker({
  accountId, canLookUp, value, onChange, defaultWorkspace, onDefault, pairings,
}: {
  /** The saved account, when editing one; a new account has nothing to look up. */
  accountId?: string;
  /** Whether the account has an admin key to read its organisation with. */
  canLookUp: boolean;
  value: string[];
  onChange: (workspaces: string[]) => void;
  defaultWorkspace: string;
  onDefault: (workspace: string) => void;
  pairings?: Pairing[];
}) {
  const [typed, setTyped] = useState("");
  const [found, setFound] = useState<WorkspaceCandidate[] | null>(null);
  const [picked, setPicked] = useState("");
  const [looking, setLooking] = useState(false);
  const [error, setError] = useState("");

  function add(ws: string) {
    const id = ws.trim();
    if (!id || value.includes(id)) return;
    onChange([...value, id]);
  }

  function remove(ws: string) {
    onChange(value.filter((w) => w !== ws));
    if (defaultWorkspace === ws) onDefault("");
  }

  async function look() {
    if (!accountId) return;
    setLooking(true);
    setError("");
    try {
      const r = await api.workspaceCandidates(accountId);
      setFound(r.workspaces ?? []);
      setPicked("");
    } catch (e) {
      setError(problemText(e, "Couldn't read this account's workspaces."));
    } finally {
      setLooking(false);
    }
  }

  const offered = (found ?? []).filter((c) => !value.includes(c.workspace_id));

  return (
    <div className="space-y-2">
      {value.length === 0 && (
        <p className="text-xs text-muted-foreground">
          None. Tunnels are made in the organisation alone, and a ChatGPT
          Enterprise or Edu workspace may not offer them.
        </p>
      )}
      {value.length > 0 && (
        <ul className="space-y-1" role="radiogroup" aria-label="Default workspace">
          {value.length > 1 && (
            <li className="flex items-center gap-2 text-sm">
              <input
                type="radio" name="default-ws" id="default-ws-all"
                checked={defaultWorkspace === ""} onChange={() => onDefault("")}
              />
              <label htmlFor="default-ws-all" className="text-muted-foreground">
                No default: every workspace below
              </label>
            </li>
          )}
          {value.map((ws) => (
            <li key={ws} className="flex flex-wrap items-center gap-2 text-sm">
              <input
                type="radio" name="default-ws" id={`default-ws-${ws}`}
                aria-label={`Make ${ws} the default`}
                checked={defaultWorkspace === ws || value.length === 1}
                disabled={value.length === 1}
                onChange={() => onDefault(ws)}
              />
              <label htmlFor={`default-ws-${ws}`} className="font-mono text-xs">{ws}</label>
              {pairings && <PairingChip pairing={pairings.find((p) => p.workspace_id === ws)} />}
              <Button type="button" variant="ghost" size="sm" aria-label={`Remove ${ws}`}
                      onClick={() => remove(ws)}>
                Remove
              </Button>
            </li>
          ))}
        </ul>
      )}

      {accountId && canLookUp && (
        found === null ? (
          <Button type="button" variant="outline" size="sm" disabled={looking} onClick={() => void look()}>
            {looking ? "Looking…" : "Find workspaces"}
          </Button>
        ) : offered.length === 0 ? (
          <p className="text-xs text-muted-foreground">
            This organisation's tunnels are in no other workspace.
          </p>
        ) : (
          <div className="flex gap-2">
            <NativeSelect aria-label="Workspaces this organisation uses" value={picked}
                          onChange={(e) => setPicked(e.target.value)}>
              <option value="">Workspaces this organisation uses…</option>
              {offered.map((c) => (
                <option key={c.workspace_id} value={c.workspace_id}>
                  {c.workspace_id} · {c.tunnels === 1 ? "1 tunnel" : `${c.tunnels} tunnels`}
                  {c.pairing ? ` · ${c.pairing.status === "verified" ? "verified" : "refused before"}` : ""}
                </option>
              ))}
            </NativeSelect>
            <Button type="button" variant="outline" size="sm" disabled={!picked}
                    aria-label="Add the selected workspace"
                    onClick={() => { add(picked); setPicked(""); }}>
              Add
            </Button>
          </div>
        )
      )}
      {error && <p className="text-xs text-problem">{error}</p>}

      <div className="flex gap-2">
        <Input aria-label="Workspace id" value={typed} placeholder="Or type a workspace id"
               onChange={(e) => setTyped(e.target.value)}
               onKeyDown={(e) => {
                 if (e.key === "Enter") { e.preventDefault(); add(typed); setTyped(""); }
               }} />
        <Button type="button" variant="outline" size="sm" disabled={!typed.trim()}
                aria-label="Add the typed workspace"
                onClick={() => { add(typed); setTyped(""); }}>
          Add
        </Button>
      </div>
    </div>
  );
}
