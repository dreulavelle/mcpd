import { useEffect, type ReactNode } from "react";
import { capabilityFor, entryFor, redirectFor } from "@/lib/nav";
import { useRouter, useSegments } from "@/lib/router";
import { useCan } from "@/lib/session";
import { Notice, PageHeader } from "@/components/chrome";
import { ApprovalsList } from "@/pages/approvals/ApprovalsList";
import { OperationDetail } from "@/pages/approvals/OperationDetail";
import { Audit } from "@/pages/audit/Audit";
import { Clients } from "@/pages/clients/Clients";
import { Logs } from "@/pages/logs/Logs";
import { MarketplaceList } from "@/pages/marketplace/MarketplaceList";
import { Overview } from "@/pages/overview/Overview";
import { PluginDetail } from "@/pages/plugins/PluginDetail";
import { PluginsList } from "@/pages/plugins/PluginsList";
import { Profile } from "@/pages/profile/Profile";
import { ApprovalPolicy } from "@/pages/settings/ApprovalPolicy";
import { Authentication } from "@/pages/settings/Authentication";
import { Advanced } from "@/pages/settings/Advanced";
import { Certificates } from "@/pages/settings/Certificates";
import { Diagnostics } from "@/pages/settings/Diagnostics";
import { ChatGPT } from "@/pages/settings/ChatGPT";
import { General } from "@/pages/settings/General";

import { Keys } from "@/pages/settings/Keys";
import { Activity } from "@/pages/activity/Activity";
import { BackupRestore } from "@/pages/settings/BackupRestore";
import { UsersAndGroups } from "@/pages/settings/UsersAndGroups";
import { Performance } from "@/pages/performance/Performance";
import { System } from "@/pages/system/System";
import { Tunnels } from "@/pages/tunnels/Tunnels";

/**
 * What a path renders. `Gate` reads the capability out of `lib/nav.ts` and
 * gates at the top, so a section cannot be added without one. Not access
 * control: the server authorises every call again.
 */
export function Routes() {
  const { path, navigate } = useRouter();
  const segments = useSegments();
  const [section, param] = segments;

  // Before the gate: a bookmark of a path that moved should land where the page
  // went, not on a refusal for the old section's capability.
  const moved = redirectFor(path);
  useEffect(() => {
    if (moved) navigate(moved, { replace: true });
  }, [moved, navigate]);

  // The tab and the history say where somebody is, not "mcpd" six times
  // over. The section's own label, and the thing within it where there is
  // one -- a plugin's name, a change's reference.
  useEffect(() => {
    const entry = entryFor(path);
    const parts = [entry?.label ?? "mcpd"];
    if (param && (section === "plugins" || section === "approvals")) parts.unshift(decodeURIComponent(param));
    if (section === "settings" && param) parts.unshift(param[0]!.toUpperCase() + param.slice(1));
    document.title = `${parts.join(" · ")} · mcpd`;
    return () => { document.title = "mcpd"; };
  }, [path, section, param]);

  function page(): ReactNode {
    switch (section) {
      case undefined:
        return <Overview />;

      case "approvals":
        return param ? <OperationDetail id={param} /> : <ApprovalsList />;

      case "audit":
        return <Audit />;

      case "plugins":
        return param ? <PluginDetail name={param} /> : <PluginsList />;

      // Discovery only. /marketplace/{name} is redirected above.
      case "marketplace":
        return param ? null : <MarketplaceList />;

      case "tunnels":
        return <Tunnels />;

      case "clients":
        return <Clients />;

      case "system":
        return <System />;

      case "performance":
        return <Performance />;

      case "activity":
        return <Activity />;

      case "logs":
        return <Logs />;

      case "profile":
        return <Profile />;

      case "settings":
        switch (param) {
          case undefined: return <General />;
          case "policy": return <ApprovalPolicy />;
          case "authentication": return <Authentication />;
          // One page for the pair. /settings/groups is kept as a way in
          // rather than a page of its own: links to it exist.
          case "users": return <UsersAndGroups />;
          case "groups": return <UsersAndGroups />;
          case "keys": return <Keys />;
          case "certificates": return <Certificates />;
          case "advanced": return <Advanced />;
          case "diagnostics": return <Diagnostics />;
          case "chatgpt": return <ChatGPT />;
          case "backup": return <BackupRestore />;
          default: return null;
        }

      default:
        return null;
    }
  }

  // Rendering the old page for a frame would fetch for a section being left.
  if (moved) return null;

  const body = page();
  if (body === null) return <NotFound />;
  return <Gate path={path}>{body}</Gate>;
}

function Gate({ path, children }: { path: string; children: ReactNode }) {
  const required = capabilityFor(path);
  // Asked unconditionally, because it is a hook. "read" stands in for the two
  // answers that are not a capability; neither uses the reply.
  const holds = useCan(
    required === null || required === "signed-in" ? "read" : required,
  );

  // A path the map does not cover is not one this console serves, whatever the
  // switch above was willing to build.
  if (required === null) return <NotFound />;
  if (required === "signed-in" || holds) return <>{children}</>;

  return (
    <>
      <PageHeader title="Not for this account" />
      <Notice tone="problem">
        This part of the console needs the <code className="font-mono">{required}</code>{" "}
        capability, which your account does not carry. Ask an administrator if
        you need it.
      </Notice>
    </>
  );
}

function NotFound() {
  return (
    <>
      <PageHeader title="Nothing here" />
      <Notice tone="neutral">
        That address does not match anything in this console. Pick a section
        from the sidebar.
      </Notice>
    </>
  );
}
