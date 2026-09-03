import { PageHeader } from "@/components/chrome";
import { Groups } from "./Groups";
import { Users } from "./Users";

/**
 * Who is here, and what each of them holds.
 *
 * One page for the pair. An account and a group are read together and
 * edited together -- adding somebody means deciding which group they land
 * in, and widening a group means knowing who is already in it -- and neither
 * list gets long. Roles are a tab of their own: a role is composed once and
 * handed out many times, and the matrix that composes one is a page's worth.
 */
export function UsersAndGroups() {
  return (
    <>
      <PageHeader
        title="Users & Groups"
        lede="A role says what someone may do. Grants and groups say what they reach."
      />
      <div className="space-y-10">
        <Users embedded />
        <Groups embedded />
      </div>
    </>
  );
}
