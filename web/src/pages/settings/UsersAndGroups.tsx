import { PageHeader } from "@/components/chrome";
import { Groups } from "./Groups";
import { SettingsTabs } from "./SettingsTabs";
import { Users } from "./Users";

/**
 * Who is here, and what each of them can reach.
 *
 * Two destinations for one subject, until now. An account and a group are read
 * together and edited together -- adding somebody means deciding which group
 * they land in, and widening a group means knowing who is already in it -- so
 * having to navigate between them made the second half of a job feel like a
 * different job.
 *
 * Merged rather than tabbed because neither list gets long. A host with a
 * dozen accounts and half a dozen groups is the ordinary case, and two short
 * tables on one page beat two pages holding one each.
 *
 * Roles are a tab of their own rather than a third section here. A role is
 * composed once and handed out many times, and the matrix that composes one
 * is a page's worth on its own.
 */
export function UsersAndGroups() {
  return (
    <>
      <SettingsTabs />
      <PageHeader
        title="Users & Groups"
        lede="Who can sign in, what each may do, and which systems they can reach."
      />
      <div className="space-y-8">
        <Users embedded />
        <Groups embedded />
      </div>
    </>
  );
}
