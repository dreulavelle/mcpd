import { useCallback, useState } from "react";
import {
  api, ApiError, type Plugin, type PluginInstance, type SettingsPayload,
  type TunnelInfo, type TunnelStatus,
} from "@/lib/api";
import { unprefixed } from "@/lib/format";
import { usePoll } from "@/lib/hooks";
import { useRouter } from "@/lib/router";
import { useCan } from "@/lib/session";
import {
  Copyable, Loading, Notice, PageHeader, Section,
} from "@/components/chrome";
import { SettingsForm } from "@/components/SettingsForm";
import { Chip, healthTone, StatusDot } from "@/components/status";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
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
    ?? (instance?.missing?.length
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
            {running ? (
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
        {health !== "healthy" && healthMessage && (
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

/** Removing an instance, where it can be done and where it cannot. */
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

  // An instance from the file would come back on the next start, so offering
  // to remove it here would be offering something that does not stick.
  if (instance.from_file) {
    return (
      <p className="text-xs text-muted-foreground">
        Defined in the configuration file. Remove it there rather than here, or
        it returns on the next start.
      </p>
    );
  }

  async function remove() {
    if (!confirm(`Remove ${name}? Its settings, including credentials, go with it.`)) return;
    setBusy(true);
    try {
      await api.removeInstance(name);
      notify("good", `Removed ${name}.`);
      navigate("/plugins");
    } catch (e) {
      notify("problem", e instanceof ApiError ? e.detail : "Couldn't remove it.");
      setBusy(false);
      onChanged();
    }
  }

  return (
    <Card>
      <CardContent className="flex flex-wrap items-center justify-between gap-3">
        <p className="max-w-[52ch] text-sm text-muted-foreground">
          Removing this forgets its settings, including any credentials. A name
          reused later cannot inherit them.
        </p>
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
