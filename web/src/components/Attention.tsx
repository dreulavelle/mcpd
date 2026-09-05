import { useCallback, useState } from "react";
import {
  api, type Certificate, type MCPServer, type Operation, type PendingRegistration,
  type Plugin, type PluginInstance, type TunnelStatus, type UpdateStatus,
} from "@/lib/api";
import { usePoll } from "@/lib/hooks";
import { Link } from "@/lib/router";
import { useCanFn } from "@/lib/session";
import { Section } from "./chrome";
import { StatusDot, type Tone } from "./status";
import { Card, CardContent } from "@/components/ui/card";

/** The first sentence of a paragraph, for a line that has room for one. */
export function firstSentence(text: string): string {
  const m = /^(.+?[.!?])(\s|$)/.exec(text.trim());
  if (!m) return text.length > 140 ? text.slice(0, 140).trimEnd() + "…" : text;
  const first = m[1]!;
  return first.length < text.trim().length ? first + " …" : first;
}

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
      // The message is written by the plugin or by whatever it talks to, so
      // it is quoted rather than run into this line: the dashboard says what
      // happened, and the quote is somebody else speaking. The first sentence
      // only -- the whole diagnosis is on the plugin's page.
      text: `${p.name} is ${p.health === "degraded" ? "having trouble" : "not working"}.${
        p.health_message?.trim() ? ` It said: “${firstSentence(p.health_message)}”` : ""}`,
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
  const name = (t: TunnelStatus) => t.plugin ? `The ${t.plugin} connector` : "The connector for everything";
  for (const t of input.tunnels) {
    if (t.upstream === "missing") {
      out.push({
        key: `tunnel-gone:${t.tunnel_id}`, tone: "problem",
        text: `${name(t)} points at a tunnel this account cannot use. Move it to the account that owns it, or forget it.`,
        to: "/tunnels", linkLabel: "Tunnels",
      });
    } else if (t.state === "failed" && !t.next_retry_at) {
      out.push({
        key: `tunnel-failed:${t.tunnel_id}`, tone: "problem",
        text: `${name(t)} has stopped.${t.message ? ` ${t.message}` : ""}`,
        to: "/tunnels", linkLabel: "Tunnels",
      });
    } else if (t.state === "failed") {
      out.push({
        key: `tunnel-retrying:${t.tunnel_id}`, tone: "attention",
        text: `${name(t)} is down. mcpd is trying again (attempt ${t.attempts ?? 1}).${t.message ? ` ${t.message}` : ""}`,
        to: "/tunnels", linkLabel: "Tunnels",
      });
    } else if (t.degraded) {
      out.push({
        key: `tunnel-degraded:${t.tunnel_id}`, tone: "attention",
        text: `${name(t)} is connected but nothing is getting through, and the connection keeps reporting errors.`,
        to: "/tunnels", linkLabel: "Tunnels",
      });
    }
  }
  for (const s of input.servers) {
    if (s.pending > 0) {
      out.push({
        key: `pending:${s.name}`, tone: "attention",
        text: `${s.name} offers ${plural(s.pending, "tool", "tools")} nobody has classified yet. Unclassified tools are not served.`,
        to: `/plugins/${encodeURIComponent(s.name)}`, linkLabel: s.name,
      });
    }
    if (s.enabled && s.discovery.error?.trim()) {
      out.push({
        key: `discovery:${s.name}`, tone: "attention",
        text: `${s.name} could not be asked what it offers. It said: “${firstSentence(s.discovery.error)}”`,
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
        ? `${plural(expired, "trusted certificate has", "trusted certificates have")} expired. Anything that relies on it can no longer connect.`
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
 * What needs somebody, given what the overview already holds plus the sources
 * only this list asks about.
 *
 * A hook rather than state inside the component, because the sentence at the
 * top of the overview counts these items and a component that kept them to
 * itself would have the page saying "everything is working" above a list of
 * things that are not.
 *
 * Each source is asked on its own and a failure drops that source rather than
 * the list -- a certificate list that cannot be read is not a reason to hide a
 * failed connector.
 */
export function useAttention({ plugins, instances, tunnels }: {
  plugins: Plugin[];
  instances: PluginInstance[];
  tunnels: TunnelStatus[];
}): Item[] {
  const can = useCanFn();
  const access = can("access:read");
  const pluginsWrite = can("plugins:read");
  const history = can("history:read");
  const admin = access;
  const [unknown, setUnknown] = useState<Operation[]>([]);
  const [registrations, setRegistrations] = useState<PendingRegistration[]>([]);
  const [servers, setServers] = useState<MCPServer[]>([]);
  const [certificates, setCertificates] = useState<Certificate[]>([]);
  const [updates, setUpdates] = useState<UpdateStatus | null>(null);
  const [chainBrokenAt, setChainBrokenAt] = useState<number | null>(null);

  const load = useCallback(() => {
    // Each question only where the answer would be given. A refusal is
    // swallowed either way, but a page that asks for what it cannot see is
    // a page that logs a 403 on every visit.
    if (can("approvals:read")) {
      api.operations("indeterminate", 50).then((r) => setUnknown(r.operations ?? [])).catch(() => undefined);
    }
    if (pluginsWrite) {
      api.mcpServers().then((r) => setServers(r.servers ?? [])).catch(() => undefined);
      api.certificates().then((r) => setCertificates(r.certificates ?? [])).catch(() => undefined);
    }
    if (can("system:read")) api.updates().then(setUpdates).catch(() => undefined);
    if (access) api.registrations().then((r) => setRegistrations(r.registrations ?? [])).catch(() => undefined);
    if (history) api.verifyAudit().then((c) => setChainBrokenAt(c.intact ? null : c.broken_at)).catch(() => undefined);
  }, [can, access, pluginsWrite, history]);
  usePoll(load, 30_000);

  return attention({
    admin, plugins, instances, tunnels, unknown, registrations, servers,
    certificates, updates, chainBrokenAt,
  });
}

/**
 * The list itself. Nothing at all renders when nothing needs anybody: the
 * sentence at the top of the overview already says so, and a card headed
 * "Needs attention" reporting none is the loudest way to say quiet.
 *
 * Nothing here is an action; every line is a place to go and read.
 */
export function Attention({ items }: { items: Item[] }) {
  if (items.length === 0) return null;

  return (
    <Section title="Needs attention" description="Worst first.">
      <Card>
        <CardContent>
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
        </CardContent>
      </Card>
    </Section>
  );
}
