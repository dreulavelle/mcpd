import { cn } from "@/lib/utils";

/**
 * A person, a key or a group as a small mark beside its name.
 *
 * Initials for a person, because a list of thirty rows needs something the
 * eye can land on besides text; a fixed glyph for the other two kinds, so
 * that a key and a group are told apart before the label is read. The hue
 * comes from the name, deterministic, so the same person is the same colour
 * on every page.
 */
export function Avatar({ name, kind = "person", className }: {
  name: string;
  kind?: "person" | "key" | "group" | "role";
  className?: string;
}) {
  const initials = kind === "person" ? initialsOf(name) : kind === "key" ? "⌘" : kind === "group" ? "⁂" : "◆";
  const hue = hueOf(name);
  return (
    <span
      aria-hidden="true"
      className={cn(
        "inline-flex size-8 shrink-0 items-center justify-center rounded-full text-xs font-semibold",
        kind === "person" ? "" : "rounded-md",
        className,
      )}
      style={{
        backgroundColor: `hsl(${hue} 45% 92%)`,
        color: `hsl(${hue} 40% 30%)`,
      }}
    >
      {initials}
    </span>
  );
}

function initialsOf(name: string): string {
  const local = name.includes("@") ? name.split("@")[0]! : name;
  const parts = local.split(/[\s._-]+/).filter(Boolean);
  const first = parts[0]?.[0] ?? "?";
  const second = parts.length > 1 ? parts[parts.length - 1]![0] : "";
  return (first + (second ?? "")).toUpperCase();
}

function hueOf(name: string): number {
  let h = 0;
  for (const c of name) h = (h * 31 + c.charCodeAt(0)) % 360;
  return h;
}
