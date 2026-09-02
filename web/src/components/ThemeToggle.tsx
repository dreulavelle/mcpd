import { Monitor, Moon, Sun } from "lucide-react";
import { THEMES, useTheme, type Theme } from "@/lib/theme";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";

const ICON: Record<Theme, typeof Sun> = { system: Monitor, light: Sun, dark: Moon };

const LABEL: Record<Theme, string> = {
  system: "Follow the system",
  light: "Light",
  dark: "Dark",
};

/**
 * One button that cycles the appearance. In the sidebar, where a person
 * reaches for it once and then not again, a cycle is fewer pixels than a
 * menu and the tooltip says which of the three is next.
 */
export function ThemeToggle({ className }: { className?: string }) {
  const [theme, choose] = useTheme();
  const next = THEMES[(THEMES.indexOf(theme) + 1) % THEMES.length]!;
  const Icon = ICON[theme];

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          variant="ghost" size="icon-sm" className={className}
          aria-label={`Appearance: ${LABEL[theme]}. Switch to ${LABEL[next].toLowerCase()}`}
          onClick={() => choose(next)}
        >
          <Icon className="size-4" aria-hidden="true" />
        </Button>
      </TooltipTrigger>
      <TooltipContent>Appearance: {LABEL[theme]}</TooltipContent>
    </Tooltip>
  );
}

/** The three choices side by side, for a page with room to name them. */
export function ThemePicker() {
  const [theme, choose] = useTheme();
  return (
    <div role="radiogroup" aria-label="Appearance" className="inline-flex rounded-md border p-0.5">
      {THEMES.map((t) => {
        const Icon = ICON[t];
        const on = t === theme;
        return (
          <button
            key={t}
            type="button"
            role="radio"
            aria-checked={on}
            onClick={() => choose(t)}
            className={cn(
              "inline-flex items-center gap-1.5 rounded-sm px-2.5 py-1 text-sm transition-colors",
              on ? "bg-accent font-medium text-accent-foreground" : "text-muted-foreground hover:text-foreground",
            )}
          >
            <Icon className="size-3.5" aria-hidden="true" />
            {LABEL[t]}
          </button>
        );
      })}
    </div>
  );
}
