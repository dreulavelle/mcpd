import { useCallback, useState } from "react";
import {
  api,
  type Group,
  type PendingRegistration,
  type ProviderName,
  problemText,
} from "@/lib/api";
import { useLoader, usePoll } from "@/lib/hooks";
import { Loading, Notice, Out, PageHeader } from "@/components/chrome";
import { SettingsForm } from "@/components/SettingsForm";
import { Chip } from "@/components/status";
import { useNotify, type Notify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { NativeSelect } from "@/components/ui/native-select";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { useConfirm } from "@/components/confirm";

/**
 * Where to register a redirect address, per provider.
 *
 * Partial on purpose: a provider the operator runs themselves has no console
 * at a fixed address, and a link guessed from the issuer would be a link to
 * somewhere that may not exist.
 */
const CONSOLES: Partial<Record<ProviderName, { href: string; label: string }>> = {
  google: {
    href: "https://console.cloud.google.com/apis/credentials",
    label: "Google Cloud credentials",
  },
  github: {
    href: "https://github.com/settings/developers",
    label: "GitHub OAuth apps",
  },
  entra: {
    href: "https://entra.microsoft.com/",
    label: "Microsoft Entra admin centre",
  },
};

/** What each provider is called in a table, where "oidc" would mean nothing. */
const PROVIDER_NAMES: Partial<Record<ProviderName, string>> = {
  google: "Google",
  github: "GitHub",
  entra: "Microsoft Entra",
  oidc: "Your own provider",
};

/**
 * Who can sign in, and how. The providers, the sign-up rules, and the people
 * waiting to be let in — the last is on this page because it is the question
 * the second raises.
 */
export function Authentication() {
  const load = useCallback(() => api.settings(), []);
  const { data, error, reload } = useLoader(load, "Couldn't load settings.");
  const groups = (data?.groups ?? []).filter((g) => g.section === "authentication");

  return (
    <>
      <PageHeader
        title="Sign-in"
        lede="How people sign in, and who is let in."
      />

      {error && <Notice tone="problem">{error}</Notice>}

      <PendingQueue />
      <RedirectURIs />

      {!data ? <Loading rows={6} /> : (
        <SettingsForm groups={groups} settings={data} onSaved={reload} />
      )}
    </>
  );
}

/**
 * The addresses to paste into each provider's console.
 *
 * Shown rather than described, because getting one wrong produces a failure at
 * the provider that says nothing useful. They come from the server, which
 * builds them from the same configured address the sign-in flow uses — one
 * assembled here from the browser's location would be right on the machine an
 * operator tested it from and wrong everywhere else.
 */
function RedirectURIs() {
  const load = useCallback(() => api.redirectURIs(), []);
  const { data } = useLoader(load, "");
  if (!data) return null;

  if (!data.base) {
    return (
      <Notice tone="attention">
        <strong>Nobody can be redirected back here yet.</strong> A provider
        sends people back to an address you register with it, and this host does
        not know its own yet. Set <strong>Dashboard address</strong> under
        Settings, and the exact addresses to paste will appear here.
      </Notice>
    );
  }

  const entries = Object.entries(data.redirect_uris) as [ProviderName, string][];
  if (entries.length === 0) return null;

  return (
    <Card className="mt-4">
      <CardHeader>
        <CardTitle>Redirect addresses</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <p className="text-sm text-muted-foreground">
          Paste these into the provider, exactly as they are.
        </p>
        <div className="scroll-x">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Provider</TableHead>
                <TableHead>Address to register</TableHead>
                <TableHead className="w-px">Where</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {entries.map(([provider, uri]) => (
                <TableRow key={provider}>
                  <TableCell>{PROVIDER_NAMES[provider] ?? provider}</TableCell>
                  <TableCell>
                    <code className="font-mono text-xs break-all">{uri}</code>
                    {/* Said here rather than left to surface as a refusal on
                        the provider's own screen, after the operator has gone. */}
                    {data.refusals?.[provider] && (
                      <p className="mt-1 text-xs text-attention">
                        {data.refusals[provider]}
                      </p>
                    )}
                  </TableCell>
                  <TableCell className="whitespace-nowrap text-sm">
                    {CONSOLES[provider]
                      ? (
                        <Out href={CONSOLES[provider].href}>
                          {CONSOLES[provider].label}
                        </Out>
                      )
                      : <span className="text-muted-foreground">Your provider</span>}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      </CardContent>
    </Card>
  );
}

/**
 * People who have asked for an account and are waiting.
 *
 * They can sign in already — that is how they proved who they are — and they
 * can do nothing else until somebody here says yes. Approving is a privilege
 * grant and is recorded as one.
 */
function PendingQueue() {
  const [waiting, setWaiting] = useState<PendingRegistration[] | null>(null);
  const [groups, setGroups] = useState<Group[]>([]);
  const [error, setError] = useState("");
  const notify = useNotify();

  const load = useCallback(() => {
    api.registrations()
      .then((r) => { setWaiting(r.registrations ?? []); setError(""); })
      .catch(() => setError("Couldn't load who is waiting."));
    // Offered beside Approve so that saying yes and saying what they may reach
    // are one action rather than two pages.
    api.groups().then((r) => setGroups(r.groups ?? [])).catch(() => undefined);
  }, []);
  usePoll(load, 30_000);

  if (error) return <Notice tone="problem">{error}</Notice>;
  // Nothing to show and nothing to explain: a host that has never had a
  // registration should not carry an empty table saying so.
  if (!waiting || waiting.length === 0) return null;

  return (
    <Card className="mt-4 overflow-hidden p-0">
      <CardHeader className="p-4 pb-0">
        <CardTitle>Waiting for you</CardTitle>
      </CardHeader>
      <CardContent className="p-0 pt-4">
        <div className="scroll-x">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Email</TableHead>
                <TableHead>Signs in with</TableHead>
                <TableHead>Asked</TableHead>
                <TableHead>Put them in</TableHead>
                <TableHead className="w-px" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {waiting.map((u) => (
                <PendingRow
                  key={u.id} user={u} groups={groups}
                  onChanged={load} notify={notify}
                />
              ))}
            </TableBody>
          </Table>
        </div>
      </CardContent>
    </Card>
  );
}

function PendingRow({ user, groups, onChanged, notify }: {
  user: PendingRegistration;
  groups: Group[];
  onChanged: () => void;
  notify: Notify;
}) {
  const confirm = useConfirm();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [group, setGroup] = useState("");

  const run = async (what: string, fn: () => Promise<unknown>) => {
    setBusy(true);
    setError("");
    try {
      await fn();
      onChanged();
      notify("good", what);
    } catch (e) {
      setError(problemText(e, "That didn't work."));
    } finally {
      setBusy(false);
    }
  };

  return (
    <TableRow>
      <TableCell>
        <span className="flex flex-wrap items-center gap-2">
          {user.name !== user.email && <span className="font-medium">{user.name}</span>}
          <span className={user.name !== user.email ? "text-muted-foreground" : undefined}>
            {user.email}
          </span>
          <Chip>waiting</Chip>
        </span>
        {error && <div className="mt-1 text-xs text-problem">{error}</div>}
      </TableCell>
      {/* What proved the address, which is what the decision turns on. A
          provider checked it before mcpd saw it; the form checked nothing. */}
      <TableCell className="text-muted-foreground">
        {user.providers.length > 0 ? user.providers.join(", ") : "A password — unchecked"}
      </TableCell>
      <TableCell className="whitespace-nowrap text-muted-foreground">
        {new Date(user.created_at).toLocaleString()}
      </TableCell>
      {/* An approved account with no grants sees an empty console, which used
          to make this two decisions wearing the appearance of one. A group
          chosen here is assigned in the same write as the approval. */}
      <TableCell>
        {groups.length === 0 ? (
          <span className="text-xs text-muted-foreground">No groups yet</span>
        ) : (
          <div className="w-44">
            <NativeSelect
              aria-label={`Group for ${user.email}`} value={group}
              disabled={busy} onChange={(e) => setGroup(e.target.value)}
            >
              <option value="">Nothing</option>
              {groups.map((g) => (
                <option key={g.id} value={g.id}>{g.name}</option>
              ))}
            </NativeSelect>
          </div>
        )}
      </TableCell>
      <TableCell className="whitespace-nowrap">
        <Button
          size="sm" disabled={busy}
          onClick={() => run(`Approved ${user.email}.`,
            () => api.approveRegistration(user.id, group ? [group] : []))}
        >
          Approve
        </Button>
        <Button
          variant="ghost" size="sm" disabled={busy}
          onClick={async () => {
            if (!(await confirm({
              title: `Turn down ${user.email}?`,
              description: "The account is removed. They can register again.",
              action: "Turn down",
            }))) return;
            run("Turned down.", () => api.rejectRegistration(user.id));
          }}
        >
          Turn down
        </Button>
      </TableCell>
    </TableRow>
  );
}
