import { useCallback, useMemo, useState, type ReactNode } from "react";
import { Search } from "lucide-react";
import { api } from "@/lib/api";
import { useLoader } from "@/lib/hooks";
import { useCan } from "@/lib/session";
import { Loading, Notice, PageHeader } from "@/components/chrome";
import { SettingsForm } from "@/components/SettingsForm";
import { Input } from "@/components/ui/input";
import { tabForSection } from "./SettingsTabs";

const OPENAI_API_KEYS = "https://platform.openai.com/settings/organization/api-keys";
const OPENAI_ADMIN_KEYS = "https://platform.openai.com/settings/organization/admin-keys";
const OPENAI_ORG = "https://platform.openai.com/settings/organization/general";

/** Where a value has to be fetched from, for the fields that are somebody else's. */
export const SETTING_LINKS = {
  "tunnel.api_key": { href: OPENAI_API_KEYS, label: "API keys" },
  "tunnel.admin_key": { href: OPENAI_ADMIN_KEYS, label: "Admin keys" },
  "tunnel.organization_id": { href: OPENAI_ORG, label: "Organization settings" },
};

/**
 * One tab of the settings, and the search that reaches all of them.
 *
 * The page this replaces put every group this host has into one column behind
 * a row of filter chips, under a row of tabs -- two navigations stacked, one
 * of which had to be used before the other made sense. Thirty-one settings in
 * one scroll is not a page, and a chip row is a tab row that has been told it
 * is not allowed to be one.
 *
 * So a group declares its section in Go and this renders one section. What is
 * left on each tab is small enough to read: eight fields, or nine, rather than
 * thirty-one.
 *
 * Search is deliberately not scoped to the tab. Somebody who cannot find a
 * setting does not know which tab it is on -- that is what not finding it
 * means -- so a search that only looked here would confirm their belief that
 * it does not exist. Matches come from every section and each says where it
 * lives.
 */
export function SettingsSection({ section, title, lede, children }: {
  section: string;
  title: string;
  lede: ReactNode;
  /** Rendered above the form, for a tab that is more than its settings. */
  children?: ReactNode;
}) {
  const mayWrite = useCan("settings:write");
  const load = useCallback(() => api.settings(), []);
  const { data, error, reload } = useLoader(load, "Couldn't load settings.");
  const [query, setQuery] = useState("");

  const searching = query.trim() !== "";

  // Every group this host has, by section, so a search can cross tabs.
  const matches = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return [];
    return (data?.groups ?? [])
      .filter((g) => g.section !== "plugins")
      .map((g) => {
        // A group whose own title matches stays whole, rather than showing
        // the one field whose help text repeated the section's name.
        if (g.title.toLowerCase().includes(q)) return g;
        const fields = g.fields.filter((f) =>
          f.label.toLowerCase().includes(q)
          || f.key.toLowerCase().includes(q)
          || (f.help ?? "").toLowerCase().includes(q));
        return fields.length ? { ...g, fields } : null;
      })
      .filter((g): g is NonNullable<typeof g> => g !== null);
  }, [data, query]);

  const mine = useMemo(
    () => (data?.groups ?? []).filter((g) => g.section === section),
    [data, section],
  );

  return (
    <>
      <PageHeader
        title={title}
        lede={mayWrite ? lede : "Changing this takes an administrator."}
      />

      {error && <Notice tone="problem">{error}</Notice>}

      {!data ? <Loading rows={5} /> : (
        <>
          <div className="relative mb-4">
            <Search
              className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground"
              aria-hidden="true"
            />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search every setting"
              aria-label="Search every setting"
              className="pl-9"
            />
          </div>

          {searching ? (
            matches.length === 0 ? (
              <Notice tone="neutral">
                Nothing matches “{query}”. A plugin's own settings are on its
                page under Plugins.
              </Notice>
            ) : (
              <div className="space-y-6">
                {matches.map((g) => (
                  <div key={`${g.section}/${g.name}`} className="space-y-2">
                    {/* Which tab this lives on. Without it a match found from
                        another section looks like it was here all along, and
                        the reader learns nothing about where to look next
                        time. */}
                    <p className="text-xs text-muted-foreground">
                      {tabForSection(g.section)}
                    </p>
                    <SettingsForm
                      groups={[g]} settings={data} links={SETTING_LINKS}
                      onSaved={reload} readOnly={!mayWrite}
                    />
                  </div>
                ))}
              </div>
            )
          ) : (
            <>
              {children}
              {mine.length > 0 && (
                <SettingsForm
                  groups={mine} settings={data} links={SETTING_LINKS}
                  onSaved={reload} readOnly={!mayWrite}
                />
              )}
            </>
          )}
        </>
      )}
    </>
  );
}
