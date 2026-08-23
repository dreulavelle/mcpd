import type { Capability } from "@/lib/capabilities";
import { useSegments } from "@/lib/router";
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
 * Where a path goes.
 *
 * A switch rather than a route table, because every section takes at most one
 * parameter and a table of two-segment patterns would be a config file for
 * something a switch says plainly.
 *
 * `required` is the capability the whole section needs, checked here as well as
 * in the sidebar: hiding a link is not access control, and a URL typed by hand
 * has to meet the same rule. The server refuses again either way -- this is so
 * the refusal is a sentence rather than a blank page full of failed fetches.
 */
export function Routes() {
  const segments = useSegments();
  const [section, param] = segments;

  switch (section) {
    case undefined:
      return <Overview />;

    case "approvals":
      return param
        ? <Gate required="read"><OperationDetail id={param} /></Gate>
        : <Gate required="read"><ApprovalsList /></Gate>;

    case "audit":
      return <Gate required="read"><Audit /></Gate>;

    case "plugins":
      return param
        ? <Gate required="read"><PluginDetail name={param} /></Gate>
        : <Gate required="read"><PluginsList /></Gate>;

    case "marketplace":
      return param
        ? <Gate required="admin"><ServerDetail name={param} /></Gate>
        : <Gate required="admin"><MarketplaceList /></Gate>;

    case "tunnels":
      return <Gate required="read"><Tunnels /></Gate>;

    case "settings":
      switch (param) {
        case undefined: return <Gate required="read"><General /></Gate>;
        case "users": return <Gate required="admin"><Users /></Gate>;
        case "account": return <Gate required="read"><Account /></Gate>;
        default: return <NotFound />;
      }

    default:
      return <NotFound />;
  }
}

function Gate({ required, children }: {
  required: Capability;
  children: React.ReactNode;
}) {
  const allowed = useCan(required);
  if (allowed) return <>{children}</>;
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
