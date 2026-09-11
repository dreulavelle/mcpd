import { useCallback, useMemo, useState, type ReactNode } from "react";
import {
  api,
  ApiError,
  type Group,
  type PendingRegistration,
  type ProviderName,
  problemText,
  type SettingsPayload,
} from "@/lib/api";
import { useLoader, usePoll } from "@/lib/hooks";
import { Link } from "@/lib/router";
import { useCan } from "@/lib/session";
import { Copyable, Loading, Notice, Out, PageHeader } from "@/components/chrome";
import { SettingsForm, tidy, type GroupExtras } from "@/components/SettingsForm";
import { Chip, type Tone } from "@/components/status";
import { useNotify, type Notify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { useConfirm } from "@/components/confirm";

/** The one setting every provider waits on, and which lives on General. */
const ADDRESS_KEY = "server.frontend_public_url";

/** In the order the form below draws them, so the two read as one list. */
const PROVIDERS: ProviderName[] = ["google", "github", "entra", "oidc"];

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
const PROVIDER_NAMES: Record<ProviderName, string> = {
  google: "Google",
  github: "GitHub",
  entra: "Microsoft Entra",
  oidc: "Your own provider",
};

/**
 * Who can sign in, and how. The providers, the sign-up rules, and the people
 * waiting to be let in — the last is on this page because it is the question
 * the second raises.
 *
 * Each provider's card carries everything that provider needs: whether it is
 * on the sign-in page, the exact address to register with it, and the steps
 * at its console. They used to be split between a table at the top and the
 * cards at the bottom, with the setup text pointing at "the redirect address
 * above" -- a table that was not drawn at all while this host had no address.
 */
export function Authentication() {
  const load = useCallback(() => api.settings(), []);
  const { data, error, reload } = useLoader(load, "Couldn't load settings.");
  const loadAddresses = useCallback(() => api.redirectURIs(), []);
  const {
    data: addresses, error: addressError, reload: reloadAddresses,
  } = useLoader(loadAddresses, "Couldn't load the addresses to register with a provider.");
  // What the signed-out page is actually offering, asked of the same endpoint
  // it asks. Whether a provider is switched on is in the settings; whether it
  // appears is the server's decision, and the two differ exactly when
  // somebody is left wondering where their button went.
  const loadOptions = useCallback(() => api.authOptions(), []);
  const { data: options, reload: reloadOptions } = useLoader(loadOptions, "");
  // Whether this dashboard already serves https itself, so a provider that
  // refuses an http address can be answered with the setting that fixes it.
  const loadTLS = useCallback(() => api.tlsStatus(), []);
  const { data: tls } = useLoader(loadTLS, "");

  // The providers first, straight under the address they all depend on; who
  // may register and how long a session lasts after them.
  const groups = (data?.groups ?? [])
    .filter((g) => g.section === "authentication")
    .sort((a, b) => Number(!isProvider(a.name)) - Number(!isProvider(b.name)));

  // Every save can change all three: the address decides what each provider
  // card shows, and a provider's details decide whether it is offered.
  const reloadAll = useCallback(() => {
    reload();
    reloadAddresses();
    reloadOptions();
  }, [reload, reloadAddresses, reloadOptions]);

  const offered = useMemo(
    () => options ? new Set((options.providers ?? []).map((p) => p.provider)) : null,
    [options],
  );

  const extras = useMemo(() => {
    const out: Record<string, GroupExtras> = {};
    if (!data) return out;
    const base = addresses?.base ?? "";
    for (const p of PROVIDERS) {
      const state = standing(p, data, base, offered);
      out[p] = {
        badge: <Chip tone={state.tone}>{state.label}</Chip>,
        lead: (
          <CallbackAddress
            provider={p}
            uri={addresses?.redirect_uris[p] ?? ""}
            refusal={addresses?.refusals?.[p] ?? ""}
            offerOwnCertificate={!!tls && !tls.dashboard.on}
            why={state.why}
            loaded={!!addresses}
          />
        ),
        whileOn: <SetupSteps provider={p} base={base} />,
      };
    }
    return out;
  }, [data, addresses, offered, tls]);

  return (
    <>
      <PageHeader
        title="Sign-in"
        lede="How people sign in, and who is let in."
      />

      {error && <Notice tone="problem">{error}</Notice>}

      <PendingQueue />

      {/* Said rather than left out. This used to draw nothing at all when it
          could not load, and an empty space where the addresses belong reads
          as a host with none rather than one that failed to say. */}
      {addressError && <Notice tone="problem">{addressError}</Notice>}
      {addresses && (
        addresses.base
          ? (
            <p className="mt-4 mb-4 text-sm text-muted-foreground">
              Callback addresses are built from{" "}
              <code className="font-mono text-foreground">{addresses.base}</code>. To
              change it, edit <strong>Address this page is on</strong> on the{" "}
              <Link to="/settings" className="text-primary underline underline-offset-4">
                General
              </Link>{" "}
              tab.
            </p>
          )
          : <AddressStep onSaved={reloadAll} />
      )}

      {!data ? <Loading rows={6} /> : (
        <SettingsForm
          groups={groups} settings={data} extras={extras} onSaved={reloadAll}
        />
      )}
    </>
  );
}

/**
 * Where a provider stands, from what is saved and what the sign-in page offers.
 *
 * "Switched on" and "offered" are different facts. A provider is only offered
 * once it has every detail it needs and this host knows where to send people
 * back to, and the gap between the two used to be a switch that was on and a
 * button that never appeared, with nothing anywhere saying why.
 */
function standing(
  provider: ProviderName,
  settings: SettingsPayload,
  base: string,
  offered: Set<ProviderName> | null,
): { tone: Tone; label: string; why?: string } {
  const group = settings.groups.find((g) => g.name === provider);
  const on = !group?.enabled_by || String(settings.values[group.enabled_by]) === "true";
  if (!group || !on) return { tone: "neutral", label: "Off" };
  // Not known, because the sign-in page's list did not load. Saying "not
  // offered" on no evidence would send somebody hunting for a problem.
  if (!offered) return { tone: "info", label: "On" };
  if (offered.has(provider)) return { tone: "good", label: "On the sign-in page" };

  const missing = group.fields
    .filter((f) => f.required && !isFilled(settings, f.key, f.kind === "secret"))
    .map((f) => f.label);
  if (missing.length > 0) {
    return {
      tone: "attention", label: "Not offered yet",
      why: `Not on the sign-in page until ${listed(missing)} ${missing.length > 1 ? "are" : "is"} saved.`,
    };
  }
  if (!base) {
    return {
      tone: "attention", label: "Not offered yet",
      why: "Not on the sign-in page until the address this page is on is saved.",
    };
  }
  // Every box is filled and the address is set, so what is left is a value
  // the flow will not take: a tenant of `common`, an issuer it cannot use.
  return {
    tone: "attention", label: "Not offered yet",
    why: "One of the details below isn't accepted. Check each one against the provider.",
  };
}

function isProvider(name: string): name is ProviderName {
  return (PROVIDERS as string[]).includes(name);
}

function isFilled(settings: SettingsPayload, key: string, secret: boolean): boolean {
  if (secret) return settings.secrets_set[key] ?? false;
  const v = settings.values[key];
  if (v === undefined || v === null) return false;
  return String(v).trim() !== "";
}

function listed(items: string[]): string {
  return new Intl.ListFormat("en", { type: "conjunction" }).format(items);
}

/**
 * The address this page is on, asked for where it is needed.
 *
 * It lives on General, and the page that needs it is this one: somebody
 * setting up a provider used to be told to go and set it somewhere else, under
 * a name the field did not have. It is still their decision, not a guess --
 * the browser's address is offered as a suggestion to fill the box, never
 * saved on its own, because what is right from this desk may be wrong through
 * a proxy.
 */
function AddressStep({ onSaved }: { onSaved: () => void }) {
  const canSave = useCan("settings:write");
  const notify = useNotify();
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState("");
  const here = window.location.origin;

  if (!canSave) {
    return (
      <div className="mt-4 mb-4">
        <Notice tone="attention">
          <strong>Nobody can sign in with a provider yet.</strong> Ask somebody
          who can change settings to fill in <strong>Address this page is on</strong>{" "}
          on the General tab.
        </Notice>
      </div>
    );
  }

  async function save() {
    setBusy(true);
    setProblem("");
    try {
      await api.saveSettings({ [ADDRESS_KEY]: value.trim() });
      notify("good", "Saved.");
      setValue("");
      onSaved();
    } catch (e) {
      setProblem(e instanceof ApiError && e.problems?.length
        ? e.problems.map(tidy).join(" ")
        : problemText(e, "Couldn't save. Try again in a moment."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="mt-4 mb-4 space-y-3 rounded-xl border border-attention/25 bg-attention-soft p-4">
      <p className="text-sm">
        <strong>Save the address this page is on first.</strong> Each provider
        sends people back to it after they sign in, and none is offered until
        it is set.
      </p>
      <form
        className="space-y-1.5"
        onSubmit={(e) => { e.preventDefault(); if (value.trim()) void save(); }}
      >
        <Label htmlFor="sign-in-address">Address this page is on</Label>
        <div className="flex flex-wrap gap-2">
          <Input
            id="sign-in-address" className="min-w-0 flex-1 basis-64 bg-background"
            placeholder="https://mcpd.example.net" value={value} disabled={busy}
            onChange={(e) => setValue(e.target.value)}
          />
          <Button type="submit" disabled={busy || !value.trim()}>
            {busy ? "Saving…" : "Save"}
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          The address a browser uses to reach this dashboard, including https://.
          {value.trim() !== here && (
            <>
              {" "}
              <button
                type="button" disabled={busy}
                className="text-primary underline underline-offset-4 hover:no-underline"
                onClick={() => setValue(here)}
              >
                Use {here}
              </button>
            </>
          )}
        </p>
        {problem && <p className="text-xs text-problem">{problem}</p>}
      </form>
    </div>
  );
}

/**
 * The address to register, in the provider's own card, shown whether or not
 * the provider is switched on: registering the app at the provider is what
 * produces the client ID, so the address is needed before anything here is
 * filled in.
 *
 * It comes from the server, which builds it from the same configured address
 * the sign-in flow uses. One assembled here from the browser's location would
 * be right on the machine an operator tested it from and wrong everywhere
 * else.
 */
function CallbackAddress({ provider, uri, refusal, offerOwnCertificate, why, loaded }: {
  provider: ProviderName;
  uri: string;
  refusal: string;
  /**
   * Whether to say mcpd can serve https itself, beside a refusal. Only when
   * it is not already doing so: then the refusal is about the address, not
   * the certificate.
   */
  offerOwnCertificate: boolean;
  why?: string;
  /** False while the addresses are loading, or when they could not be. */
  loaded: boolean;
}) {
  return (
    <div className="space-y-1.5">
      <p className="text-sm font-medium">Callback address</p>
      {uri ? (
        <Copyable value={uri} label={`the ${PROVIDER_NAMES[provider]} callback address`} />
      ) : loaded ? (
        <p className="text-xs text-muted-foreground">
          Appears once the address this page is on is saved, at the top of this page.
        </p>
      ) : null}
      {/* Said here rather than left to surface as a refusal on the provider's
          own screen, after the operator has gone. */}
      {refusal && (
        <p className="text-xs text-attention">
          {refusal}
          {offerOwnCertificate && (
            <>
              {" "}mcpd can serve https itself: set <strong>Certificate for this
              dashboard</strong> to mcpd's own on the{" "}
              <Link to="/settings" className="underline underline-offset-4">General</Link>{" "}
              tab, then change the address to https.
            </>
          )}
        </p>
      )}
      {why && <p className="text-xs text-muted-foreground">{why}</p>}
    </div>
  );
}

/** A console's own label, set apart from the sentence around it. */
function B({ children }: { children: ReactNode }) {
  return <strong className="font-medium text-foreground">{children}</strong>;
}

/**
 * Registering mcpd at one provider, step by step, in that console's own
 * words.
 *
 * Each provider asks for its answers before mcpd ever sees a token, and a wrong
 * one surfaces as a refusal on the provider's screen, in its words, with the
 * operator no longer on this page. The steps name the answers people get
 * wrong: Entra's platform -- a single-page application cannot hold the secret
 * this host sends -- and its secret's Value, which it shows once and then
 * replaces with an id; a provider of your own that does not say an address is
 * verified, which this host refuses.
 */
function SetupSteps({ provider, base }: { provider: ProviderName; base: string }) {
  const at = CONSOLES[provider];
  const console_ = at ? <Out href={at.href}>{at.label}</Out> : null;
  const name = provider === "entra" ? "Microsoft" : PROVIDER_NAMES[provider];

  const steps: Record<ProviderName, ReactNode[]> = {
    google: [
      <>In {console_}, choose <B>Create credentials</B>, then <B>OAuth client ID</B>.
        If Google asks you to set up the consent screen first, do that and come back.</>,
      <>Choose <B>Web application</B> as the application type.</>,
      <>Under <B>Authorized redirect URIs</B>, add the callback address above.</>,
      <>Copy the <B>Client ID</B> and <B>Client secret</B> into the boxes below.</>,
    ],
    github: [
      <>In {console_}, choose <B>New OAuth app</B>. For an organisation's app, start
        from the organisation's settings instead.</>,
      <>Set <B>Homepage URL</B> to{" "}
        {base ? <code className="font-mono text-xs text-foreground">{base}</code> : "this dashboard's address"}
        {" "}and <B>Authorization callback URL</B> to the callback address above.</>,
      <>Register the app, copy its <B>Client ID</B>, then choose <B>Generate a new
        client secret</B> and copy that too.</>,
      <>Paste both into the boxes below.</>,
    ],
    entra: [
      <>In the {console_}, open <B>App registrations</B> and choose <B>New registration</B>.</>,
      <>Under <B>Supported account types</B>, choose <B>Accounts in this
        organizational directory only</B>.</>,
      <>Under <B>Redirect URI</B>, choose the <B>Web</B> platform — not Single-page
        application — and paste the callback address above.</>,
      <>From the registration's <B>Overview</B>, copy the <B>Application (client)
        ID</B> and the <B>Directory (tenant) ID</B> into the boxes below.</>,
      <>Under <B>Certificates &amp; secrets</B>, add a client secret and copy
        its <B>Value</B>, not its Secret ID, into the box below. Entra shows the
        Value only once.</>,
      <>Under <B>Token configuration</B>, add the optional claims <B>email</B> and{" "}
        <B>xms_edov</B> to the ID token, and allow the Microsoft Graph email
        permission when it asks.</>,
    ],
    oidc: [
      <>In your provider, create an OpenID Connect client for mcpd that has a
        client secret. Providers call this confidential, or web.</>,
      <>Add the callback address above as its redirect URI.</>,
      <>Let it use the scopes <B>openid</B>, <B>email</B> and <B>profile</B>, and
        make sure it sends <B>email_verified</B>. An address the provider does
        not say is verified is refused.</>,
      <>Copy its issuer URL, client ID and client secret into the boxes below.</>,
    ],
  };

  return (
    <div className="space-y-2 rounded-md border bg-muted/40 p-3">
      <p className="text-sm font-medium">Register mcpd with {name}</p>
      <ol className="list-decimal space-y-2 pl-5 text-sm text-muted-foreground marker:text-foreground">
        {steps[provider].map((step, i) => <li key={i}>{step}</li>)}
        <li>Save changes. {name} then appears on the sign-in page.</li>
      </ol>
    </div>
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
