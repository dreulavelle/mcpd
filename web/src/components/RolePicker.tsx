import { useEffect, useState } from "react";
import { api, type RoleDef } from "@/lib/api";
import { describe } from "@/lib/permissions";
import { Link } from "@/lib/router";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";

/**
 * Which role a subject holds.
 *
 * The list comes from the server, so a custom role composed on the Roles
 * tab is offered everywhere a role is chosen without anything here knowing
 * about it. The line under the select says what the chosen role means in
 * words, because a name like "Tunnel desk" is somebody's shorthand and the
 * form is where the reader finds out what it stands for.
 *
 * `allowNone` offers "no role", for a group that only hands out reach.
 */
export function RolePicker({ id, value, onChange, disabled, allowNone, label = "Role" }: {
  id: string;
  value: string;
  onChange: (next: string) => void;
  disabled?: boolean;
  allowNone?: boolean;
  label?: string;
}) {
  const [roles, setRoles] = useState<RoleDef[] | null>(null);

  useEffect(() => {
    let live = true;
    api.roles()
      .then((r) => { if (live) setRoles(r.roles ?? []); })
      .catch(() => { if (live) setRoles([]); });
    return () => { live = false; };
  }, []);

  const chosen = roles?.find((r) => r.id === value);

  return (
    <div className="space-y-1.5">
      <Label htmlFor={id}>{label}</Label>
      <NativeSelect id={id} value={value} disabled={disabled || roles === null} onChange={(e) => onChange(e.target.value)}>
        {allowNone && <option value="">No role, reach only</option>}
        {roles === null && !allowNone && <option value={value}>Loading roles…</option>}
        {(roles ?? []).map((r) => (
          <option key={r.id} value={r.id}>{r.name}</option>
        ))}
      </NativeSelect>
      <p className="text-xs text-muted-foreground">
        {chosen
          ? `${chosen.description || describe(chosen.permissions)}${chosen.builtin ? "" : " (a role made on this host)"}`
          : value === "" && allowNone
            ? "Members keep whatever their own role allows, and gain only the reach below."
            : ""}
        {" "}
        <Link to="/settings/roles" className="text-primary hover:underline">What each role means</Link>
      </p>
    </div>
  );
}
