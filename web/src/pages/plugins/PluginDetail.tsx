import { useCallback, useState } from "react";
import {
  api, ApiError, type Plugin, type PluginInstance, type SettingsPayload,
  type TunnelInfo, type TunnelStatus,
} from "@/lib/api";
import { unprefixed, when } from "@/lib/format";
import { usePoll } from "@/lib/hooks";
import { useRouter } from "@/lib/router";
import { useCan } from "@/lib/session";
import {
  Copyable, Detail, Loading, Notice, PageHeader, Section,
} from "@/components/chrome";
import { SettingsForm } from "@/components/SettingsForm";
import { Chip, healthTone, StatusDot } from "@/components/status";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { RestoreButton } from "./PluginsList";
import { RemoteServer } from "./RemoteServer";

/**
 * One plugin, whatever kind it is.
 *
 * A page of its own rather than an expander in the list, because everything
 * here is long: a settings form, two tool catalogues, an address, and a
 * connector. The console this replaces put all of it inside a row and the file
 * that did so grew to twenty kilobytes doing it.
 *
 * A remote MCP server is managed here too. It used to send the operator to a
 * Marketplace page for its tools while its credentials stayed here, which is
 * one thing in two places and a link to bounce between them. Marketplace is
 * for finding a server; this is for running one.
 */
export function PluginDetail({ name }: { name: string }) {
  const [plugin, setPlugin] = useState<Plugin | null>(null);
  const [instance, setInstance] = useState<PluginInstance | null>(null);
  const [settings, setSettings] = useState<SettingsPayload | null>(null);
  const [tunnels, setTunnels] = useState<TunnelInfo | null>(null);
  // Both the plugin list and the instance list have answered at least once.
  // Absent from the plugin list alone means unmounted, which is the case whose
  // settings form is the whole reason this page exists; absent from both is
  // the only thing that means the name is not a plugin here.
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
    />
  );
}

function Body({ name, plugin, instance, settings, tunnels, onChanged }: {
  name: string;
  plugin: Plugin | null;
  instance: PluginInstance | null;
  settings: SettingsPayload | null;
  tunnels: TunnelInfo | null;
  onChanged: () => void;
}) {
  const mayAdminister = useCan("admin");
  const running = plugin !== null && plugin.endpoint !== "";
  const runtime = instance?.runtime ?? "builtin";
  const reads = (plugin?.tools ?? []).filter((t) => t.kind === "read");
  const writes = (plugin?.tools ?? []).filter((t) => t.kind !== "read");
  const tunnel = tunnels?.tunnels.find((t) => t.plugin === name);
  const group = settings?.groups.find(
    (g) => g.name === (plugin?.settings_group ?? `plugin:${name}`),
  );
  const health = plugin?.health ?? "unhealthy";
  const healthMessage = plugin?.health_message
    ?? (instance?.removed
      ? "Removed here. The configuration file still declares it."
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
                {health === "healthy" ? "Serving" : health}
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
          <Notice tone={health === "degraded" ? "attention" : "problem"}>
            {healthMessage}
          </Notice>
        )}

        {runtime === "mcp" && (
          <Notice tone="info">
            This is somebody else's server, mounted here. It cannot propose
            changes, and only the tools an administrator classified are served.
          </Notice>
        )}

        {/* Settings first. It is why somebody opens the page of a plugin that
            is not working, and everything below it is reference. */}
        {group && settings && (
          <Section title="Settings">
            <SettingsForm
              groups={[group]} settings={settings}
              onSaved={onChanged} readOnly={!mayAdminister}
            />
          </Section>
        )}

        {/* Everything that is true of this plugin *because* it is somebody
            else's server: its tool snapshot, what has been classified, the
            document it was added from, and whether it is switched on. It goes
            after the settings, which are the credentials the document asked
            for and the reason it is not serving yet. */}
        {runtime === "mcp" && <RemoteServer name={name} onChanged={onChanged} />}

        {/* The remote panel lists tools in far more detail -- and in three
            states rather than two -- so this pair of cards would be a worse
            second copy of it. */}
        {running && runtime !== "mcp" && (
          <Section title="Tools">
            <div className="grid gap-4 lg:grid-cols-2">
              <Card>
                <CardContent className="space-y-2">
                  <p className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
                    Read
                  </p>
                  <ToolList tools={reads.map((t) => unprefixed(t.name, name))} />
                </CardContent>
              </Card>
              <Card>
                <CardContent className="space-y-2">
                  <p className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
                    Write — approval required
                  </p>
                  <ToolList
                    tone="attention"
                    tools={writes.map((t) => unprefixed(t.name, name))}
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

function ToolList({ tools, tone }: { tools: string[]; tone?: "attention" }) {
  if (tools.length === 0) {
    return <p className="text-sm text-muted-foreground">Nothing.</p>;
  }
  return (
    <div className="flex flex-wrap gap-1.5">
      {tools.map((t) => (
        <Chip key={t} tone={tone ?? "neutral"}>
          <span className="font-mono">{t}</span>
        </Chip>
      ))}
    </div>
  );
}

/**
 * What the configuration file says about this plugin, read-only.
 *
 * Shown because the honest answer to "I removed it, is it gone?" is "not from
 * your file" -- and the next question is which lines to delete when somebody
 * next touches the YAML. Keys without values: this rides on a read-capability
 * endpoint and a `settings:` block is usually where a credential is.
 */
function Declaration({ instance }: { instance: PluginInstance | null }) {
  const d = instance?.declaration;
  if (!d) return null;
  return (
    <Section
      title="In the configuration file"
      description="What the file declares. mcpd does not write this file; nothing on this page changes it."
    >
      <Card>
        <CardContent className="grid gap-4 sm:grid-cols-3">
          <Detail label="Type">
            <code className="font-mono">{d.type}</code>
          </Detail>
          <Detail label="Enabled">{d.enabled ? "true" : "false"}</Detail>
          <Detail label="Required">
            {d.required ? "true — the host is meant not to run without it" : "false"}
          </Detail>
          {d.settings_keys && d.settings_keys.length > 0 && (
            <Detail label="Settings it sets" className="sm:col-span-3">
              <span className="flex flex-wrap gap-1.5">
                {d.settings_keys.map((k) => (
                  <Chip key={k}><span className="font-mono">{k}</span></Chip>
                ))}
              </span>
              <span className="mt-1 block text-xs text-muted-foreground">
                Names only. Their values are on the Settings page, where the
                secret ones are redacted.
              </span>
            </Detail>
          )}
        </CardContent>
      </Card>
    </Section>
  );
}

/**
 * A plugin removed here that the configuration file still declares.
 *
 * The wording is the point. An operator who reads "removed" and assumes their
 * file changed will be surprised twice: once when they cannot find the edit,
 * and once when a colleague redeploys and nothing comes back. So it says which
 * of the two happened, and offers the way out of it.
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
          <strong>Removed.</strong> mcpd is not serving this plugin, now or
          after a restart.{" "}
          {instance.removed_by && (
            <>Removed by {instance.removed_by}
              {instance.removed_at ? ` on ${when(instance.removed_at)}` : ""}.{" "}
            </>
          )}
          The configuration file is unchanged — if you redeploy from it, the
          removal still holds.
        </p>
        {mayManage && (
          <RestoreButton name={name} label="Restore" onChanged={onChanged} />
        )}
      </div>
    </Notice>
  );
}

/**
 * Switching a plugin off without removing it.
 *
 * This works on a file-declared instance for the same reason removing one
 * does: `enabled: false` in a file nobody on this host can edit is the same
 * dead end one step smaller, and the store already beats the file everywhere
 * else. A remote MCP server is switched from its own panel, which owns the
 * column that decides it -- a record written here would be shadowed on the
 * next read, so the toggle would report success and change nothing.
 */
function EnabledControl({ name, instance, runtime, onChanged }: {
  name: string;
  instance: PluginInstance | null;
  runtime: "builtin" | "mcp";
  onChanged: () => void;
}) {
  const mayManage = useCan("admin");
  const notify = useNotify();
  const [busy, setBusy] = useState(false);

  if (!mayManage || !instance || runtime === "mcp") return null;
  // Nothing to switch: it is not being served either way, and the notice at
  // the top of the page owns the one control that changes that.
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
      notify("problem", e instanceof ApiError ? e.detail : "Couldn't change it.");
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
            ? "Switching it off stops mcpd serving it and keeps everything it is configured with."
            : "Switched off. Its settings are still here; switching it back on serves it again."}
          {instance.from_file && (
            <>
              {" "}This is recorded here rather than in the configuration file,
              which is unchanged and stays that way.
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
  const mayRemove = useCan("admin");
  const notify = useNotify();
  const { navigate } = useRouter();
  const [busy, setBusy] = useState(false);

  if (!mayRemove || !instance) return null;

  // A remote server is removed by the endpoint that owns its document, which
  // takes the tool approvals and the settings with it. `DELETE /api/instances`
  // refuses one outright -- there is no instances. key to delete -- so this
  // button would have been an error message with a delay in front of it. The
  // remote panel above carries the one that works.
  if (runtime === "mcp") return null;

  // Already removed: the notice at the top of the page owns this, and a second
  // control offering to remove it again would be a button with nothing to do.
  if (instance.removed) return null;

  const fromFile = instance.from_file;
  const required = fromFile && instance.required === true;

  async function remove() {
    // Two different acts, so two different confirmations. Telling somebody
    // their credentials are about to be forgotten when they are not is as
    // wrong as not telling them when they are.
    const question = fromFile
      ? `Remove ${name}? mcpd stops serving it, now and after every restart. `
        + "Your configuration file is not changed — it still declares it, and "
        + "you can restore it here."
        + (required
          ? "\n\nThe file also marks it required: true, meaning this host is "
            + "meant not to run without it. Removing it overrides that."
          : "")
      : `Remove ${name}? Its settings, including credentials, go with it.`;
    if (!confirm(question)) return;
    setBusy(true);
    try {
      await api.removeInstance(name, required);
      notify("good", fromFile
        ? `Removed ${name}. The configuration file is unchanged.`
        : `Removed ${name}.`);
      // A file-declared plugin still has a page -- it is removed, not gone --
      // and that page is where the restore is. Leaving for the list would hide
      // the undo behind a search.
      if (fromFile) onChanged();
      else navigate("/plugins");
    } catch (e) {
      notify("problem", e instanceof ApiError ? e.detail : "Couldn't remove it.");
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
                This plugin is declared in the configuration file. Removing it
                here stops mcpd serving it, now and on every restart.{" "}
                <strong className="text-foreground">
                  The file is unchanged
                </strong>{" "}
                — if you redeploy from it, the removal still holds.
              </p>
              <p>
                Its settings are kept, so restoring it brings it back as it was.
              </p>
              {required && (
                <p className="text-attention">
                  The file marks this <code className="font-mono">required: true</code>:
                  this host is meant not to run without it. You will be asked to
                  confirm that.
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

/** Make or remove the connector that serves this one plugin. */
function TunnelControl({ plugin, tunnels, tunnel, onChanged }: {
  plugin: Plugin;
  tunnels: TunnelInfo | null;
  tunnel?: TunnelStatus;
  onChanged: () => void;
}) {
  const mayManage = useCan("admin");
  const notify = useNotify();
  const [busy, setBusy] = useState(false);

  async function create() {
    setBusy(true);
    try {
      // Same default as the Tunnels page: a tunnel scoped only to the
      // organisation is invisible in an Enterprise or Edu workspace.
      await api.createTunnel(plugin.title, plugin.name, tunnels?.workspaces?.[0]);
      notify("good", "Made. Give it about 30 seconds to become active in ChatGPT.");
    } catch (e) {
      notify("problem", e instanceof ApiError ? e.detail : "Couldn't make it.");
    } finally {
      setBusy(false);
      onChanged();
    }
  }

  async function remove() {
    if (!confirm("Remove this connector? Anything using it stops working.")) return;
    setBusy(true);
    try {
      await api.deleteTunnel(tunnel!.tunnel_id!);
      notify("good", "Removed.");
    } catch (e) {
      notify("problem", e instanceof ApiError ? e.detail : "Couldn't remove it.");
    } finally {
      setBusy(false);
      onChanged();
    }
  }

  if (tunnel) {
    const tone = tunnel.state === "connected" ? "good"
      : tunnel.state === "failed" ? "problem" : "info";
    return (
      <div className="flex flex-wrap items-center gap-3">
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

  return (
    <div className="flex flex-wrap items-center gap-3">
      <span className="flex-1 text-sm text-muted-foreground">
        Reachable through any connector that covers everything.
      </span>
      <Button size="sm" disabled={busy} onClick={create}>
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
