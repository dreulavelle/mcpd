import type { ReactNode } from "react";
import { capabilityFor } from "@/lib/nav";
import { useRouter, useSegments } from "@/lib/router";
import { useCan } from "@/lib/session";
import { Notice, PageHeader } from "@/components/chrome";
import { ApprovalsList } from "@/pages/approvals/ApprovalsList";
import { OperationDetail } from "@/pages/approvals/OperationDetail";
import { Audit } from "@/pages/audit/Audit";
import { MarketplaceList } from "@/pages/marketplace/MarketplaceList";
import { ServerDetail } from "@/pages/marketplace/ServerDetail";
import { Overview } from "@/pages/overview/Overview";
import { PluginDetail } from "@/pages/plugins/PluginDetail";
import { PluginsList } from "@/pages/plugins/PluginsList";
import { Account } from "@/pages/settings/Account";
import { General } from "@/pages/settings/General";
import { Users } from "@/pages/settings/Users";
import { Tunnels } from "@/pages/tunnels/Tunnels";

/**
 * What a path renders, gated by what it takes to render it.
 *
 * Two things are deliberately separate here. `page()` decides *what*, and
 * knows nothing about permissions. `Gate` decides *whether*, and reads the
 * answer out of `lib/nav.ts` -- the same map the sidebar is built from, so
 * there is one table of capabilities rather than two that happen to agree.
 *
 * Gating at the top rather than per case also means a section cannot be added
 * without one. The arrangement this replaces wrote `required=` into each
 * branch, and Overview had already been missed.
 *
 * None of this is access control. The server authorises every call again; this
 * is so a URL typed by hand meets a sentence rather than a page that renders
 * its chrome and then fails every fetch.
 */
export function Routes() {
  const { path } = useRouter();
  const segments = useSegments();
  const [section, param] = segments;

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

      case "marketplace":
        return param ? <ServerDetail name={param} /> : <MarketplaceList />;

      case "tunnels":
        return <Tunnels />;

      case "settings":
        switch (param) {
          case undefined: return <General />;
          case "users": return <Users />;
          case "account": return <Account />;
          default: return null;
        }

      default:
        return null;
    }
  }

  const body = page();
  if (body === null) return <NotFound />;
  return <Gate path={path}>{body}</Gate>;
}

function Gate({ path, children }: { path: string; children: ReactNode }) {
  const required = capabilityFor(path);
  // A path the map does not cover is not a path this console serves, whatever
  // the switch above was willing to build for it.
  const allowed = useCan(required ?? "admin") && required !== null;

  if (allowed) return <>{children}</>;
  if (required === null) return <NotFound />;

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
