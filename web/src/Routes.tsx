import { useEffect, type ReactNode } from "react";
import { capabilityFor, redirectFor } from "@/lib/nav";
import { useRouter, useSegments } from "@/lib/router";
import { useCan } from "@/lib/session";
import { Notice, PageHeader } from "@/components/chrome";
import { ApprovalsList } from "@/pages/approvals/ApprovalsList";
import { OperationDetail } from "@/pages/approvals/OperationDetail";
import { Audit } from "@/pages/audit/Audit";
import { MarketplaceList } from "@/pages/marketplace/MarketplaceList";
import { Overview } from "@/pages/overview/Overview";
import { PluginDetail } from "@/pages/plugins/PluginDetail";
import { PluginsList } from "@/pages/plugins/PluginsList";
import { Profile } from "@/pages/profile/Profile";
import { General } from "@/pages/settings/General";
import { Users } from "@/pages/settings/Users";
import { Tunnels } from "@/pages/tunnels/Tunnels";

/**
 * What a path renders, gated by what it takes to render it.
 *
 * Three things are deliberately separate here. `redirectFor` decides *where*,
 * `page()` decides *what* and knows nothing about permissions, and `Gate`
 * decides *whether* by reading the answer out of `lib/nav.ts` -- the same map
 * the sidebar is built from, so there is one table of capabilities rather than
 * two that happen to agree.
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
  const { path, navigate } = useRouter();
  const segments = useSegments();
  const [section, param] = segments;

  // Before the gate rather than after it: a bookmark of a path that moved
  // should land where the page went, not on a refusal for the capability the
  // old section happened to need.
  const moved = redirectFor(path);
  useEffect(() => {
    if (moved) navigate(moved, { replace: true });
  }, [moved, navigate]);

  function page(): ReactNode {
    switch (section) {
      case undefined:
        return <Overview />;

      case "approvals":
        return param ? <OperationDetail id={param} /> : <ApprovalsList />;

      case "audit":
        return <Audit />;

      // Where everything mcpd serves is managed, remote servers included.
      case "plugins":
        return param ? <PluginDetail name={param} /> : <PluginsList />;

      // Discovery only, and nothing below it. /marketplace/{name} used to be an
      // installed server and is redirected above; anything deeper never named
      // a page here.
      case "marketplace":
        return param ? null : <MarketplaceList />;

      case "tunnels":
        return <Tunnels />;

      case "profile":
        return <Profile />;

      case "settings":
        switch (param) {
          case undefined: return <General />;
          case "users": return <Users />;
          default: return null;
        }

      default:
        return null;
    }
  }

  // Nothing while the redirect above lands. Rendering the old page for a frame
  // would fetch for a section the operator is already leaving.
  if (moved) return null;

  const body = page();
  if (body === null) return <NotFound />;
  return <Gate path={path}>{body}</Gate>;
}

function Gate({ path, children }: { path: string; children: ReactNode }) {
  const required = capabilityFor(path);
  // `useCan` is a hook, so it is asked unconditionally. "read" stands in for
  // the two answers that are not a capability; neither of them uses the reply.
  const holds = useCan(
    required === null || required === "signed-in" ? "read" : required,
  );

  // A path the map does not cover is not a path this console serves, whatever
  // the switch above was willing to build for it.
  if (required === null) return <NotFound />;
  // Being signed in is the whole requirement. Your own profile is not an
  // administrative surface, and this is the one answer the map can give that
  // means "no capability", rather than an omission that reads the same way.
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
