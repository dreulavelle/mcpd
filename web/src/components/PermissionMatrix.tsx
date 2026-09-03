import {
  AREA_HINTS, AREA_LABELS, AREAS, levelsOf,
  type Area, type Level, type PermissionSet,
} from "@/lib/permissions";
import { Segmented } from "@/components/Segmented";

/**
 * What a role permits, one level per area.
 *
 * Eight rows, each a segmented control of None, Read and Write (Decide, for
 * approvals). Every option is visible and the held one is filled, so the
 * whole role is read down the page without opening anything. Write includes
 * read, and the control says so by its order rather than by a sentence.
 *
 * The vocabulary comes from lib/permissions, which mirrors the server's; the
 * server refuses anything outside it, so a typo here is a refused save
 * rather than a stored permission nobody holds.
 *
 * `readOnly` renders the same rows with nothing pressable, for a built-in
 * role and for anybody who may read roles but not write them.
 */
export function PermissionMatrix({ id, value, onChange, disabled, readOnly }: {
  id: string;
  value: PermissionSet;
  onChange?: (next: PermissionSet) => void;
  disabled?: boolean;
  readOnly?: boolean;
}) {
  function set(area: Area, level: Level) {
    const next: PermissionSet = { ...value };
    if (level === "none") delete next[area];
    else next[area] = level;
    onChange?.(next);
  }

  return (
    <div id={id} className="divide-y rounded-lg border">
      {AREAS.map((area) => {
        const held = value[area] ?? "none";
        const top = levelsOf(area)[1]!;
        return (
          <div key={area} className="flex flex-wrap items-center gap-x-6 gap-y-2 px-4 py-3">
            <div className="min-w-0 flex-1">
              <div className="text-sm font-medium">{AREA_LABELS[area]}</div>
              <div className="text-xs text-muted-foreground">{AREA_HINTS[area]}</div>
            </div>
            <Segmented<Level>
              label={`${AREA_LABELS[area]} level`}
              value={held}
              disabled={disabled}
              readOnly={readOnly}
              onChange={(l) => set(area, l)}
              options={[
                { value: "none", label: "None" },
                { value: "read", label: "Read" },
                { value: top, label: top === "decide" ? "Decide" : "Write", title: top === "decide" ? "Read, and approve or reject" : "Read and change" },
              ]}
            />
            {area === "access" && held === "write" && !readOnly && (
              <p className="basis-full text-xs text-attention">
                Access at write can hand out any role, including Administrator.
              </p>
            )}
          </div>
        );
      })}
    </div>
  );
}
