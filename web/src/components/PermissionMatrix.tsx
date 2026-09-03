import {
  AREA_HINTS, AREA_LABELS, AREAS, LEVEL_LABELS, levelsOf,
  type Area, type Level, type PermissionSet,
} from "@/lib/permissions";
import { NativeSelect } from "@/components/ui/native-select";
import { cn } from "@/lib/utils";

/**
 * What a role permits, one level per area.
 *
 * Eight rows, each a dropdown of none, read and write (decide, for
 * approvals). Write includes read, and the row says so rather than offering
 * a "write but not read" nobody has ever meant. The vocabulary comes from
 * lib/permissions, which mirrors the server's; the server refuses anything
 * outside it, so a typo here is a refused save rather than a stored
 * permission nobody holds.
 *
 * `readOnly` draws the same table as a description rather than a form, for
 * a built-in role and for anybody who may read roles but not write them.
 */
export function PermissionMatrix({ id, value, onChange, disabled, readOnly }: {
  id: string;
  value: PermissionSet;
  onChange?: (next: PermissionSet) => void;
  disabled?: boolean;
  readOnly?: boolean;
}) {
  function set(area: Area, level: Level | "") {
    const next: PermissionSet = { ...value };
    if (level === "" || level === "none") delete next[area];
    else next[area] = level;
    onChange?.(next);
  }

  return (
    <div className="overflow-hidden rounded-md border">
      <table className="w-full text-sm">
        <tbody>
          {AREAS.map((area) => {
            const held = value[area] ?? "none";
            return (
              <tr key={area} className="border-b last:border-b-0">
                <td className="w-44 px-3 py-2 align-top">
                  <div className="font-medium">{AREA_LABELS[area]}</div>
                  <div className="text-xs text-muted-foreground">{AREA_HINTS[area]}</div>
                </td>
                <td className="px-3 py-2 align-top">
                  {readOnly ? (
                    <span className={cn(
                      "inline-block rounded-full border px-2 py-0.5 text-xs font-medium",
                      held === "none" ? "text-muted-foreground" : "border-primary/30 bg-primary/10 text-primary",
                    )}>
                      {LEVEL_LABELS[held]}
                    </span>
                  ) : (
                    <NativeSelect
                      id={`${id}-${area}`}
                      aria-label={`${AREA_LABELS[area]} level`}
                      value={held}
                      disabled={disabled}
                      className="w-40"
                      onChange={(e) => set(area, e.target.value as Level)}
                    >
                      <option value="none">None</option>
                      {levelsOf(area).map((l) => (
                        <option key={l} value={l}>
                          {l === "read" ? "Read" : l === "decide" ? "Read and decide" : "Read and write"}
                        </option>
                      ))}
                    </NativeSelect>
                  )}
                  {area === "access" && held !== "none" && held !== "read" && (
                    <p className="mt-1 text-xs text-attention">
                      Access at write can hand out any role, including Administrator.
                    </p>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
