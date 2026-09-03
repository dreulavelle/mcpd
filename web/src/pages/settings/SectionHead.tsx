import type { ReactNode } from "react";
import { Search } from "lucide-react";
import { Input } from "@/components/ui/input";

/**
 * The line above a table: what it lists, how many, one sentence on what a
 * row means, and the one thing you can do to the list. Shared by every
 * access page so the three tables read as one family.
 */
export function SectionHead({ title, count, description, search, action }: {
  title: string;
  count?: number;
  description?: ReactNode;
  search?: { value: string; onChange: (next: string) => void; placeholder: string };
  action?: ReactNode;
}) {
  return (
    <div className="mb-3 flex flex-wrap items-end justify-between gap-3">
      <div className="min-w-0">
        <h2 className="flex items-baseline gap-2 text-base font-semibold tracking-tight">
          {title}
          {count !== undefined && (
            <span className="text-sm font-normal text-muted-foreground tabular-nums">{count}</span>
          )}
        </h2>
        {description && <p className="mt-0.5 text-sm text-muted-foreground">{description}</p>}
      </div>
      <div className="flex items-center gap-2">
        {search && (
          <div className="relative">
            <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
            <Input
              aria-label={search.placeholder}
              placeholder={search.placeholder}
              value={search.value}
              onChange={(e) => search.onChange(e.target.value)}
              className="h-8 w-48 pl-8 text-sm"
            />
          </div>
        )}
        {action}
      </div>
    </div>
  );
}
