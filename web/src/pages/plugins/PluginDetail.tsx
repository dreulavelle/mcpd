import { useCallback, useState } from "react";
import {
  api,
  type Plugin,
  type PluginType,
  type PluginInstance,
  type SettingsPayload,
  type TunnelInfo,
  type TunnelStatus,
  problemText,
} from "@/lib/api";
import { principalWords, unprefixed, when } from "@/lib/format";
import { usePoll } from "@/lib/hooks";
import { Link, useRouter } from "@/lib/router";
import { parseHealth, statusTone, statusWords } from "@/lib/health";
import { useCan } from "@/lib/session";
import {
  Copyable, Detail, Loading, Notice, PageHeader, Section,
} from "@/components/chrome";
import { SettingsForm } from "@/components/SettingsForm";
import { Chip, healthTone, healthWords, StatusDot } from "@/components/status";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { AddDeclaredButton } from "./PluginsList";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { useConfirm } from "@/components/confirm";
import { OpenAIPermissionDialog, type OpenAIReason } from "@/components/openai-permission";
import { showFailure } from "@/pages/tunnels/Tunnels";
import { RemoteServer } from "./RemoteServer";

/** One plugin, whatever kind it is. A remote MCP server is managed here too. */
export function PluginDetail({ name }: { name: string }) {
  const [plugin, setPlugin] = useState<Plugin | null>(null);
  const [instance, setInstance] = useState<PluginInstance | null>(null);
  const [settings, setSettings] = useState<SettingsPayload | null>(null);
  const [types, setTypes] = useState<PluginType[]>([]);
  const [tunnels, setTunnels] = useState<TunnelInfo | null>(null);
  // Absent from the plugin list alone means unmounted, which this page exists
  // to fix. Absent from both is the only thing that means "not a plugin here".
  const [asked, setAsked] = useState({ plugins: false, instances: false });

  const load = useCallback(() => {
    api.plugins()
      .then((r) => setPlugin((r.plugins ?? []).find((p) => p.name === name) ?? null))
      .catch(() => setPlugin(null))
      .finally(() => setAsked((a) => (a.plugins ? a : { ...a, plugins: true })));
    api.instances()
      .then((r) => setInstance((r.instances ?? []).find((i) => i.name === name) ?? null))
      .catch(() => setInstance(null))
      .finally(() => setAsked((a) => (a.instances ? a : { ...a, instances: true })));
    api.settings().then(setSettings).catch(() => setSettings(null));
    api.tunnel().then(setTunnels).catch(() => setTunnels(null));
    api.pluginTypes().then((r) => setTypes(r.types ?? [])).catch(() => setTypes([]));
  }, [name]);
  usePoll(load, 15_000);

  const resolved = !asked.plugins || !asked.instances ? "loading"
    : plugin || instance ? "ready" : "missing";

  if (resolved === "loading") {
    return (
      <>
        <PageHeader title={name} back={{ to: "/plugins", label: "Plugins" }} />
        <Loading rows={5} />
      </>
    );
  }
  if (resolved === "missing") {
    return (
      <>
        <PageHeader title={name} back={{ to: "/plugins", label: "Plugins" }} />
        <Notice tone="problem">
          No plugin named <code className="font-mono">{name}</code> is configured
          here, or your account cannot reach it.
        </Notice>
      </>
    );
  }

  return (
    <Body
      name={name} plugin={plugin} instance={instance}
      settings={settings} tunnels={tunnels} onChanged={load}
      guide={types.find((t) => t.name === (instance?.type ?? name))?.guide}
    />
  );
}

function Body({ name, plugin, instance, settings, tunnels, onChanged, guide }: {
  name: string;
  plugin: Plugin | null;
  instance: PluginInstance | null;
  settings: SettingsPayload | null;
  tunnels: TunnelInfo | null;
  onChanged: () => void;
  guide?: PluginType["guide"];
}) {
  const mayAdminister = useCan("plugins:write");
  // Activity needs history, so the link to it does too.
  const mayRead = useCan("history:read");
  const running = plugin !== null && plugin.endpoint !== "";
  const runtime = instance?.runtime ?? "builtin";
  const [toolQuery, setToolQuery] = useState("");
  const toolMatches = (t: { name: string; description?: string }) => {
    const q = toolQuery.trim().toLowerCase();
    return !q || t.name.toLowerCase().includes(q) || (t.description ?? "").toLowerCase().includes(q);
  };
  const reads = (plugin?.tools ?? []).filter((t) => t.kind === "read" && toolMatches(t));
  const writes = (plugin?.tools ?? []).filter((t) => t.kind !== "read" && toolMatches(t));
  const toolCount = plugin?.tools?.length ?? 0;
  const tunnel = tunnels?.tunnels.find((t) => t.plugin === name);
  const group = settings?.groups.find(
    (g) => g.name === (plugin?.settings_group ?? `plugin:${name}`),
  );
  const health = plugin?.health ?? "unhealthy";
  const healthMessage = plugin?.health_message
    ?? (instance?.removed
      ? "Removed from this host. The configuration file still lists it."
      : instance?.missing?.length
        ? `Waiting on ${instance.missing.join(", ")}.`
        : instance?.problem
          || (instance?.enabled === false ? "Switched off." : "Not running yet."));

  return (
    <>
      <PageHeader
        title={name}
        back={{ to: "/plugins", label: "Plugins" }}
        lede={plugin?.description || undefined}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            {runtime === "mcp" && <Chip tone="info">Remote MCP server</Chip>}
            {instance?.removed ? (
              <Chip>Removed</Chip>
            ) : running ? (
              <Chip tone={healthTone(health)}>
                <StatusDot tone={healthTone(health)} />
                {health === "healthy" ? "Serving" : healthWords(health)}
              </Chip>
            ) : (
              <Chip tone="attention">Not running</Chip>
            )}
          </div>
        }
      />

      <div className="space-y-6">
        {instance?.removed ? (
          <RemovedNotice
            name={name} instance={instance} mayManage={mayAdminister}
            onChanged={onChanged}
          />
        ) : health !== "healthy" && healthMessage && (
          <HealthNotice health={health} message={healthMessage} />
        )}

        {runtime === "mcp" && (
          <Notice tone="info">
            This server is run by somebody else. It cannot suggest changes, and
            only the tools an administrator has allowed are served.
          </Notice>
        )}

        {/* Settings first: it is why the page of a broken plugin gets opened. */}
        {group && settings && (
          <Section title="Settings">
            <SettingsForm
              groups={[group]} settings={settings}
              onSaved={onChanged} readOnly={!mayAdminister}
            />
          </Section>
        )}

        {/* After the settings, which are what it is usually waiting on. */}
        {runtime === "mcp" && <RemoteServer name={name} onChanged={onChanged} />}

        {/* The remote panel lists tools in more detail, and in three states. */}
        {running && runtime !== "mcp" && (
          <Section
            title="Tools"
            description={`${toolCount} ${toolCount === 1 ? "tool" : "tools"}. Hover one for what it does.`}
            actions={
              <div className="flex flex-wrap items-center gap-2">
                {toolCount > 12 && (
                  <Input
                    aria-label="Find a tool"
                    className="w-48"
                    placeholder="Find a tool…"
                    value={toolQuery}
                    onChange={(e) => setToolQuery(e.target.value)}
                  />
                )}
                {mayRead && (
                  <Link
                    to={`/activity?plugin=${encodeURIComponent(name)}`}
                    className="text-sm text-primary hover:underline"
                  >
                    What has called it
                  </Link>
                )}
              </div>
            }
          >
            <div className="grid gap-4 lg:grid-cols-2">
              <Card>
                <CardContent className="space-y-2">
                  <p className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
                    Read
                  </p>
                  <ToolList tools={reads.map((t) => ({ name: unprefixed(t.name, name), description: t.description }))} />
                </CardContent>
              </Card>
              <Card>
                <CardContent className="space-y-2">
                  <p className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
                    Write — approval required
                  </p>
                  <ToolList
                    tone="attention"
                    tools={writes.map((t) => ({ name: unprefixed(t.name, name), description: t.description }))}
                  />
                </CardContent>
              </Card>
            </div>
          </Section>
        )}

        {running && plugin && (
          <>
            <Section title="Address">
              <Copyable value={plugin.connect_url} label="address" />
            </Section>

            {guide && <HowToUse guide={guide} />}

            <Section title="ChatGPT">
              <Card>
                <CardContent>
                  <TunnelControl
                    plugin={plugin} tunnels={tunnels} tunnel={tunnel}
                    onChanged={onChanged}
                  />
                </CardContent>
              </Card>
            </Section>
          </>
        )}

        <EnabledControl
          name={name} instance={instance} runtime={runtime} onChanged={onChanged}
        />

        <Declaration instance={instance} />

        <RemoveControl
          name={name} instance={instance} runtime={runtime} onChanged={onChanged}
        />
      </div>
    </>
  );
}

function ToolList({ tools, tone }: {
  tools: { name: string; description?: string }[];
  tone?: "attention";
}) {
  if (tools.length === 0) {
    return <p className="text-sm text-muted-foreground">Nothing.</p>;
  }
  return (
    <div className="flex flex-wrap gap-1.5">
      {tools.map((t) => (
        <Chip key={t.name} tone={tone ?? "neutral"} className={t.description ? "cursor-help" : undefined}>
          <span className="font-mono" title={t.description}>{t.name}</span>
        </Chip>
      ))}
    </div>
  );
}

/**
 * What the configuration file says, read-only, because the honest answer to "I
 * removed it, is it gone?" is "not from your file". Keys without values: this
 * is a read-capability endpoint and `settings:` is where a credential lives.
 */
function Declaration({ instance }: { instance: PluginInstance | null }) {
  const d = instance?.declaration;
  if (!d) return null;
  return (
    <Section
      title="In the configuration file"
      description="What the configuration file says. Nothing on this page changes that file."
    >
      <Card>
        <CardContent className="grid gap-4 sm:grid-cols-3">
          <Detail label="Type">
            <code className="font-mono">{d.type}</code>
          </Detail>
          <Detail label="Enabled">{d.enabled ? "true" : "false"}</Detail>
          <Detail label="Required">
            {d.required ? "true — this host is not meant to run without it" : "false"}
          </Detail>
          {d.settings_keys && d.settings_keys.length > 0 && (
            <Detail label="Settings it sets" className="sm:col-span-3">
              <span className="flex flex-wrap gap-1.5">
                {d.settings_keys.map((k) => (
                  <Chip key={k}><span className="font-mono">{k}</span></Chip>
                ))}
              </span>
              <span className="mt-1 block text-xs text-muted-foreground">
                Names only. Their values are on the Settings page, with the
                secret ones hidden.
              </span>
            </Detail>
          )}
        </CardContent>
      </Card>
    </Section>
  );
}

/**
 * A plugin removed here that the file still lists. The wording is the point:
 * the removal took its settings, so what "add it again" offers is an empty
 * plugin, and assuming the file changed leads to a surprise on the next deploy.
 */
function RemovedNotice({ name, instance, mayManage, onChanged }: {
  name: string;
  instance: PluginInstance;
  mayManage: boolean;
  onChanged: () => void;
}) {
  return (
    <Notice tone="attention">
      <div className="space-y-2">
        <p>
          This plugin is not on this host.{" "}
          {instance.removed_by && (
            <>{principalWords(instance.removed_by)} removed it
              {instance.removed_at ? ` on ${when(instance.removed_at)}` : ""}.{" "}
            </>
          )}
          The configuration file still lists it, so it can be added again — as
          the file lists it, with only what the file provides.
        </p>
        {mayManage && (
          <AddDeclaredButton
            name={name} label="Add" pending="Adding…"
            done={`Added ${name}. Set it up below.`}
            failed="Couldn't add it."
            onChanged={onChanged}
          />
        )}
      </div>
    </Notice>
  );
}

/**
 * Switching a plugin off without removing it. Not for a remote MCP server: its
 * own panel owns the column, and a record written here would be shadowed on the
 * next read -- reporting success and changing nothing.
 */
function EnabledControl({ name, instance, runtime, onChanged }: {
  name: string;
  instance: PluginInstance | null;
  runtime: "builtin" | "mcp";
  onChanged: () => void;
}) {
  const mayManage = useCan("plugins:write");
  const notify = useNotify();
  const [busy, setBusy] = useState(false);

  if (!mayManage || !instance || runtime === "mcp") return null;
  // The notice at the top of the page owns the only control that applies.
  if (instance.removed) return null;

  const on = instance.enabled;

  async function toggle() {
    setBusy(true);
    try {
      await api.setInstanceEnabled(name, !on);
      notify("good", on
        ? `Switched ${name} off.`
        : `Switched ${name} on. It serves as soon as it has what it needs.`);
    } catch (e) {
      notify("problem", problemText(e, "Couldn't change it."));
    } finally {
      setBusy(false);
      onChanged();
    }
  }

  return (
    <Card>
      <CardContent className="flex flex-wrap items-center justify-between gap-3">
        <p className="max-w-[52ch] text-sm text-muted-foreground">
          {on
            ? "Switching it off stops it being served. Its settings are kept."
            : "Switched off. Its settings are still here, and switching it back on serves it again."}
          {instance.from_file && (
            <>
              {" "}This is saved here, not in the configuration file, which does
              not change.
            </>
          )}
        </p>
        <Button variant="outline" size="sm" disabled={busy} onClick={toggle}>
          {busy ? "Saving…" : on ? "Switch off" : "Switch on"}
        </Button>
      </CardContent>
    </Card>
  );
}

/** Removing an instance: what that means here, which is not the same thing
 * for one the dashboard added and one the configuration file declares. */
function RemoveControl({ name, instance, runtime, onChanged }: {
  name: string;
  instance: PluginInstance | null;
  runtime: "builtin" | "mcp";
  onChanged: () => void;
}) {
  const confirm = useConfirm();
  const mayRemove = useCan("plugins:write");
  const notify = useNotify();
  const { navigate } = useRouter();
  const [busy, setBusy] = useState(false);

  if (!mayRemove || !instance) return null;

  // `DELETE /api/instances` refuses a remote server outright: it is removed by
  // the endpoint that owns its document, from the panel above.
  if (runtime === "mcp") return null;
  if (instance.removed) return null;

  const fromFile = instance.from_file;
  const required = fromFile && instance.required === true;

  async function remove() {
    // The settings go either way, so both confirmations say so. What differs
    // is the file: it still lists a declared plugin afterwards, and a value it
    // supplies itself comes back with the plugin -- so the file's version says
    // which settings are forgotten rather than claiming all of them are.
    const question = fromFile
      ? "This takes it off this host, now and after every restart. Settings "
        + "entered here, including credentials, are forgotten; anything the "
        + "configuration file itself provides stays. Your configuration file "
        + "does not change, and it can be added again later."
        + (required
          ? "\n\nThe file marks it required: true, so this host is not meant "
            + "to run without it. Removing it overrides that."
          : "")
      : "Its settings, including credentials, go with it.";
    if (!(await confirm({ title: `Remove ${name}?`, description: question, action: "Remove" }))) return;
    setBusy(true);
    try {
      await api.removeInstance(name, required);
      // "The settings entered here" rather than "its settings": a declared
      // plugin's file may supply some of its own, and those are still there.
      notify("good", `Removed ${name}. The settings entered here are forgotten.`);
      // A plugin the file lists still has a page -- it is removed, not gone --
      // and that page is where it can be added again. Leaving for the list
      // would hide the way back behind the Add dialog.
      if (fromFile) onChanged();
      else navigate("/plugins");
    } catch (e) {
      notify("problem", problemText(e, "Couldn't remove it."));
      setBusy(false);
      onChanged();
    }
  }

  return (
    <Card>
      <CardContent className="flex flex-wrap items-center justify-between gap-3">
        <div className="max-w-[52ch] space-y-1 text-sm text-muted-foreground">
          {fromFile ? (
            <>
              <p>
                This plugin is listed in the configuration file. Removing it
                takes it off this host, now and after every restart, and it can
                be added again later.
              </p>
              <p>
                Settings entered here, including credentials, are forgotten.
                Anything the configuration file itself provides stays.{" "}
                <strong className="text-foreground">
                  The file is unchanged
                </strong>{" "}
                — if you redeploy from it, the removal still holds.
              </p>
              {required && (
                <p className="text-attention">
                  The file marks this <code className="font-mono">required: true</code>,
                  so this host is not meant to run without it. You will be asked
                  to confirm.
                </p>
              )}
            </>
          ) : (
            <p>
              Removing this forgets its settings, including any credentials. A
              name reused later cannot inherit them.
            </p>
          )}
        </div>
        <Button
          variant="outline" size="sm" disabled={busy}
          className="text-destructive hover:text-destructive"
          onClick={remove}
        >
          {busy ? "Removing…" : "Remove"}
        </Button>
      </CardContent>
    </Card>
  );
}

/**
 * The plugin's diagnosis, laid out: the upstream's status and reference as
 * facts to quote, the first sentence as the headline, and the explanation
 * as the paragraph it is. This is the page with room for it; the table has
 * only the number.
 */
function HealthNotice({ health, message }: { health: string; message: string }) {
  const h = parseHealth(message);
  return (
    <Notice tone={health === "degraded" ? "attention" : "problem"}>
      <span className="block space-y-2">
        {(h.status !== undefined || h.reference) && (
          <span className="flex flex-wrap items-center gap-2">
            {h.status !== undefined && (
              <Chip tone={statusTone(h.status)} title={`HTTP ${h.status}`}>
                {statusWords(h.status)}
              </Chip>
            )}
            {h.reference && (
              <span className="text-xs">
                their reference <code className="font-mono select-all">{h.reference}</code>
              </span>
            )}
          </span>
        )}
        <strong className="block font-medium">{h.title}</strong>
        {h.body && <span className="block text-sm opacity-90">{h.body}</span>}
      </span>
    </Notice>
  );
}

/** Make or remove the connector that serves this one plugin. */
function TunnelControl({ plugin, tunnels, tunnel, onChanged }: {
  plugin: Plugin;
  tunnels: TunnelInfo | null;
  tunnel?: TunnelStatus;
  onChanged: () => void;
}) {
  const confirm = useConfirm();
  const mayManage = useCan("plugins:write");
  const notify = useNotify();
  const [busy, setBusy] = useState(false);
  const [refused, setRefused] = useState<{ reason: OpenAIReason; detail: string } | null>(null);
  const accounts = tunnels?.accounts ?? [];
  // With one account there is nothing to choose; with several, a tunnel made
  // without one lands as "No account" on the Tunnels page and never starts.
  const [account, setAccount] = useState("");
  // The account a running tunnel connects with, so a removal goes to the
  // organisation it actually lives in. Two accounts are two organisations,
  // and deleting from the wrong one cannot be undone.
  const ownAccount = tunnel?.tunnel_id
    ? tunnels?.account_assignments?.[tunnel.tunnel_id]
    : undefined;

  async function create() {
    setBusy(true);
    try {
      // The host lists it wherever the account's other tunnels are.
      await api.createTunnel(
        plugin.name,
        account || (accounts.length === 1 ? accounts[0]!.id : undefined),
        plugin.title,
      );
      notify("good", "Made. Give it about 30 seconds to become active in ChatGPT.");
    } catch (e) {
      showFailure(e, "Couldn't make it.", notify, setRefused);
    } finally {
      setBusy(false);
      onChanged();
    }
  }

  async function remove() {
    if (!(await confirm("Remove this connector? Anything using it stops working."))) return;
    setBusy(true);
    try {
      await api.deleteTunnel(tunnel!.tunnel_id!, ownAccount);
      notify("good", "Removed.");
    } catch (e) {
      showFailure(e, "Couldn't remove it.", notify, setRefused);
    } finally {
      setBusy(false);
      onChanged();
    }
  }

  const permission = refused && (
    <OpenAIPermissionDialog
      reason={refused.reason}
      detail={refused.detail}
      onClose={() => setRefused(null)}
    />
  );

  if (tunnel) {
    const tone = tunnel.state === "connected" ? "good"
      : tunnel.state === "failed" ? "problem" : "info";
    return (
      <div className="flex flex-wrap items-center gap-3">
        {permission}
        <StatusDot tone={tone} />
        <span className="flex-1 text-sm text-muted-foreground">
          Its own connector, {describe(tunnel.state)}.
        </span>
        {mayManage && (
          <Button
            variant="outline" size="sm" disabled={busy}
            className="text-destructive hover:text-destructive"
            onClick={remove}
          >
            {busy ? "Working…" : "Remove"}
          </Button>
        )}
      </div>
    );
  }

  // Somebody who cannot manage tunnels sees how the plugin is reached and
  // cannot change it. Offering the button and refusing the call would be a
  // worse way to say the same thing.
  if (!mayManage) {
    return (
      <p className="text-sm text-muted-foreground">
        Reachable through any connector that covers everything.
      </p>
    );
  }

  if (!tunnels?.can_manage) {
    return (
      <p className="text-sm text-muted-foreground">
        Add {tunnels?.missing ?? "an OpenAI admin key"} in Settings to give this
        plugin its own connector.
      </p>
    );
  }

  const needsAccount = accounts.length > 1 && !account;
  return (
    <div className="flex flex-wrap items-center gap-3">
      {permission}
      <span className="flex-1 text-sm text-muted-foreground">
        Reachable through any connector that covers everything.
      </span>
      {accounts.length > 1 && (
        <NativeSelect
          aria-label="ChatGPT account"
          className="w-44"
          value={account}
          onChange={(e) => setAccount(e.target.value)}
        >
          <option value="">Which account…</option>
          {accounts.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
        </NativeSelect>
      )}
      <Button size="sm" disabled={busy || needsAccount} onClick={create}>
        {busy ? "Making…" : "Give it its own connector"}
      </Button>
    </div>
  );
}

function describe(state: TunnelStatus["state"]): string {
  switch (state) {
    case "connected": return "ready";
    case "starting": return "connecting";
    case "failed": return "not connecting";
    case "stopped": return "switched off";
    default: return String(state);
  }
}

/**
 * How to use this integration, for the person about to ask their first
 * question: a few things worth asking, and the notes that save a wrong first
 * attempt. Declared by the plugin type, not written here.
 */
function HowToUse({ guide }: { guide: NonNullable<PluginType["guide"]> }) {
  return (
    <Section title="How to use">
      <Card>
        <CardContent className="space-y-4">
          {guide.questions.length > 0 && (
            <div className="space-y-1.5">
              <p className="text-sm font-medium">Things to ask</p>
              <ul className="list-disc space-y-1 pl-5 text-sm text-muted-foreground">
                {guide.questions.map((q) => <li key={q}>“{q}”</li>)}
              </ul>
            </div>
          )}
          {guide.notes.length > 0 && (
            <div className="space-y-1.5">
              <p className="text-sm font-medium">Good to know</p>
              <ul className="list-disc space-y-1 pl-5 text-sm text-muted-foreground">
                {guide.notes.map((n) => <li key={n}>{n}</li>)}
              </ul>
            </div>
          )}
        </CardContent>
      </Card>
    </Section>
  );
}
