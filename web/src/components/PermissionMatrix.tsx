import { CAPABILITIES, CAPABILITY_LABELS, type Capability } from "@/lib/api";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";

/**
 * What a group permits its members to do.
 *
 * A role grants capabilities and a group can only take them away, so this is a
 * *ceiling* rather than a grant, and the wording throughout says so. Presenting
 * it as though ticking a box hands somebody a right would be a lie: an ordinary
 * user in a group permitting "admin" is still not an administrator.
 *
 * `value === null` means the group imposes no ceiling — the ordinary case, and
 * what every group created before this feature means. That is a genuinely
 * different state from a group permitting nothing, so it gets its own control
 * rather than being represented as "all boxes ticked", which would silently
 * turn the first into the second the moment somebody untick one.
 */
export function PermissionMatrix({
  id,
  value,
  onChange,
  disabled,
}: {
  id: string;
  value: Capability[] | null;
  onChange: (next: Capability[] | null) => void;
  disabled?: boolean;
}) {
  const restricted = value !== null;

  function toggle(cap: Capability, on: boolean) {
    const current = value ?? [];
    onChange(on ? [...current, cap] : current.filter((c) => c !== cap));
  }

  return (
    <div className="space-y-3">
      <div className="flex items-start gap-3">
        <Switch
          id={`${id}-restrict`}
          checked={restricted}
          disabled={disabled}
          onCheckedChange={(on) => onChange(on ? [] : null)}
        />
        <div className="space-y-0.5">
          <Label htmlFor={`${id}-restrict`}>Restrict what this group may do</Label>
          <p className="text-sm text-muted-foreground">
            {restricted
              ? "Members may only do the things ticked below, even where their role allows more."
              : "Members do whatever their own role allows. Turn this on to take things away."}
          </p>
        </div>
      </div>

      {restricted && (
        <div className="space-y-2 rounded-md border p-3">
          {CAPABILITIES.map((cap) => (
            <div key={cap} className="flex items-center gap-3">
              <Switch
                id={`${id}-${cap}`}
                checked={(value ?? []).includes(cap)}
                disabled={disabled}
                onCheckedChange={(on) => toggle(cap, on)}
              />
              <Label htmlFor={`${id}-${cap}`} className="font-normal">
                {CAPABILITY_LABELS[cap]}
              </Label>
            </div>
          ))}
          {(value ?? []).length === 0 && (
            <p className="pt-1 text-sm text-muted-foreground">
              Nothing is ticked, so members of this group may do nothing at all.
              That is a way to suspend people without removing them.
            </p>
          )}
          <p className="pt-1 text-sm text-muted-foreground">
            This can only take rights away. Ticking something a member's role
            does not already allow does not give it to them.
          </p>
        </div>
      )}
    </div>
  );
}
