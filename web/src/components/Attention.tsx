import { useCallback, useState } from "react";
import { BellOff } from "lucide-react";
import {
  api, type Certificate, type MCPServer, type Operation, type PendingRegistration,
  type Plugin, type PluginInstance, type TunnelStatus, type UpdateStatus,
} from "@/lib/api";
import { usePoll } from "@/lib/hooks";
import { Link } from "@/lib/router";
import { useCan } from "@/lib/session";
import { Section } from "./chrome";
import { StatusDot, type Tone } from "./status";
import { Card, CardContent } from "@/components/ui/card";

/** One thing somebody should look at, and where to look. */
export interface Item {
  key: string;
  tone: Tone;
  text: string;
  to: string;
  linkLabel: string;
}

/**
 * What an operator needs, given everything the console has to hand. Pure,
 * so a test can hand it a deployment and read the list.
 */
export function attention(input: {
  admin: boolean;
  plugins: Plugin[];
  instances: PluginInstance[];
  tunnels: TunnelStatus[];
  unknown: Operation[];
  registrations: PendingRegistration[];
  servers: MCPServer[];
  certificates: Certificate[];
  updates: UpdateStatus | null;
  chainBrokenAt: number | null;
}): Item[] {
  const out: Item[] = [];
  const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

  // The order is the order of consequence: a change that may have landed
  // and nobody knows comes before a version number.
  if (input.chainBrokenAt !== null) {
    out.push({
      key: "chain", tone: "problem",
      text: `The audit history has been altered: entry ${input.chainBrokenAt} does not follow the one before it.`,
      to: "/audit", linkLabel: "Audit",
    });
  }
  if (input.unknown.length > 0) {
    out.push({
      key: "unknown", tone: "attention",
      text: `${plural(input.unknown.length, "change", "changes")} ended in an unknown state. Each may have landed; read the target before proposing it again.`,
      to: "/approvals?state=indeterminate", linkLabel: "See them",
    });
  }
  for (const p of input.plugins) {
    if (p.health === "healthy") continue;
    out.push({
      key: `plugin:${p.name}`, tone: p.health === "degraded" ? "attention" : "problem",
      text: `${p.name} is ${p.health}${p.health_message ? ` — ${p.health_message}` : "."}`,
      to: `/plugins/${encodeURIComponent(p.name)}`, linkLabel: p.name,
    });
  }
  const waiting = input.instances.filter((i) => i.enabled && !i.mounted);
  if (waiting.length > 0) {
    out.push({
      key: "instances", tone: "attention",
      text: `${plural(waiting.length, "plugin is", "plugins are")} switched on but not serving: ${waiting.map((i) => i.name).join(", ")}. Each is waiting on a setting.`,
      to: "/plugins", linkLabel: "Plugins",
    });
  }
  const failed = input.tunnels.filter((t) => t.state === "failed");
  if (failed.length > 0) {
    out.push({
      key: "tunnels", tone: "problem",
      text: `${plural(failed.length, "connector has", "connectors have")} failed${failed[0]?.message ? `: ${failed[0].message}` : "."}`,
      to: "/tunnels", linkLabel: "Tunnels",
    });
  }
  for (const s of input.servers) {
    if (s.pending > 0) {
      out.push({
        key: `pending:${s.name}`, tone: "attention",
        text: `${s.name} offers ${plural(s.pending, "tool", "tools")} nobody has classified yet. Unclassified tools are not served.`,
        to: `/plugins/${encodeURIComponent(s.name)}`, linkLabel: s.name,
      });
    }
    if (s.enabled && s.discovery.error) {
      out.push({
        key: `discovery:${s.name}`, tone: "attention",
        text: `${s.name} could not be asked what it offers: ${s.discovery.error}`,
        to: `/plugins/${encodeURIComponent(s.name)}`, linkLabel: s.name,
      });
    }
  }
  if (input.admin && input.registrations.length > 0) {
    out.push({
      key: "registrations", tone: "info",
      text: `${plural(input.registrations.length, "person is", "people are")} waiting for an account: ${input.registrations.map((r) => r.email).join(", ")}.`,
      to: "/settings/authentication", linkLabel: "Decide",
    });
  }
  const certs = input.certificates.filter((c) => c.status === "expiring" || c.status === "expired");
  if (input.admin && certs.length > 0) {
    const expired = certs.filter((c) => c.status === "expired").length;
    out.push({
      key: "certificates", tone: expired > 0 ? "problem" : "attention",
      text: expired > 0
        ? `${plural(expired, "trusted certificate has", "trusted certificates have")} expired; anything relying on it will fail its handshake.`
        : `${plural(certs.length, "trusted certificate", "trusted certificates")} expiring soon.`,
      to: "/settings/certificates", linkLabel: "Certificates",
    });
  }
  if (input.updates?.update_available && input.updates.latest) {
    out.push({
      key: "update", tone: "info",
      text: `mcpd ${input.updates.latest} is available; this host runs ${input.updates.current}.`,
      to: "/system", linkLabel: "System",
    });
  }
  return out;
}

/**
 * The first thing on the first screen: what needs somebody, in one list.
 *
 * Each source is asked on its own and a failure drops that source rather
 * than the section -- a certificate list that cannot be read is not a reason
 * to hide a failed connector. Nothing here is an action; every line is a
 * place to go and read.
 */
export function Attention({ plugins, instances, tunnels }: {
  plugins: Plugin[];
  instances: PluginInstance[];
  tunnels: TunnelStatus[];
}) {
  const admin = useCan("admin");
  const [unknown, setUnknown] = useState<Operation[]>([]);
  const [registrations, setRegistrations] = useState<PendingRegistration[]>([]);
  const [servers, setServers] = useState<MCPServer[]>([]);
  const [certificates, setCertificates] = useState<Certificate[]>([]);
  const [updates, setUpdates] = useState<UpdateStatus | null>(null);
  const [chainBrokenAt, setChainBrokenAt] = useState<number | null>(null);

  const load = useCallback(() => {
    api.operations("indeterminate", 50).then((r) => setUnknown(r.operations ?? [])).catch(() => undefined);
    api.mcpServers().then((r) => setServers(r.servers ?? [])).catch(() => undefined);
    api.updates().then(setUpdates).catch(() => undefined);
    if (!admin) return;
    api.registrations().then((r) => setRegistrations(r.registrations ?? [])).catch(() => undefined);
    api.certificates().then((r) => setCertificates(r.certificates ?? [])).catch(() => undefined);
    api.verifyAudit().then((c) => setChainBrokenAt(c.intact ? null : c.broken_at)).catch(() => undefined);
  }, [admin]);
  usePoll(load, 30_000);

  const items = attention({
    admin, plugins, instances, tunnels, unknown, registrations, servers,
    certificates, updates, chainBrokenAt,
  });

  return (
    <Section
      title="Needs attention"
      description={items.length === 0 ? undefined : "Worst first. Each line is a place to go and read, not a thing to click through."}
    >
      <Card>
        <CardContent>
          {items.length === 0 ? (
            <p className="flex items-center gap-2 text-sm text-muted-foreground">
              <BellOff className="size-4" aria-hidden="true" />
              Nothing needs you. Every plugin is healthy, every connector is up,
              and nothing is waiting on a decision.
            </p>
          ) : (
            <ul className="space-y-2">
              {items.map((item) => (
                <li key={item.key} className="flex items-start gap-2.5 text-sm">
                  <StatusDot tone={item.tone} className="mt-2" />
                  <span className="min-w-0 flex-1">{item.text}</span>
                  <Link
                    to={item.to}
                    className="shrink-0 text-primary hover:underline"
                  >
                    {item.linkLabel}
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </Section>
  );
}
