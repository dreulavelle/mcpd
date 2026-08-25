import { useCallback, useMemo, useState } from "react";
import { Search } from "lucide-react";
import { api, type BootstrapSetting } from "@/lib/api";
import { useLoader } from "@/lib/hooks";
import { useCan } from "@/lib/session";
import { Loading, Notice, PageHeader } from "@/components/chrome";
import { SettingsForm } from "@/components/SettingsForm";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";

const OPENAI_API_KEYS = "https://platform.openai.com/settings/organization/api-keys";
const OPENAI_ADMIN_KEYS = "https://platform.openai.com/settings/organization/admin-keys";
const OPENAI_ORG = "https://platform.openai.com/settings/organization/general";

const LINKS = {
  "tunnel.api_key": { href: OPENAI_API_KEYS, label: "API keys" },
  "tunnel.admin_key": { href: OPENAI_ADMIN_KEYS, label: "Admin keys" },
  "tunnel.organization_id": { href: OPENAI_ORG, label: "Organization settings" },
};

/**
 * The host's own configuration: read to see, admin to change, and read-only
 * rather than a form that meets a 403. A plugin's settings live on its page.
 */
export function General() {
  const mayWrite = useCan("admin");
  const load = useCallback(() => api.settings(), []);
  const { data, error, reload } = useLoader(load, "Couldn't load settings.");

  // A plugin's settings live on its page, and the sign-in ones live on
  // Authentication beside the queue of people waiting to be let in — which is
  // the question they raise.
  const groups = useMemo(
    () => (data?.groups ?? [])
      .filter((g) => g.section !== "plugins" && g.section !== "authentication"),
    [data],
  );

  // A hundred and thirty settings in one column is a scroll, not a page. Two
  // ways in rather than one: the section names for somebody who knows roughly
  // where a setting lives, and a search for somebody who only remembers a word
  // from its label. Both narrow the same list — there is no separate view to
  // get lost in, and clearing them puts everything back.
  const [section, setSection] = useState<string | null>(null);
  const [query, setQuery] = useState("");

  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    let out = section ? groups.filter((g) => g.name === section) : groups;
    if (q) {
      // Matching a group by its own title keeps a searched-for section whole,
      // rather than showing the one field whose help text happened to repeat
      // the section's name.
      out = out
        .map((g) => {
          if (g.title.toLowerCase().includes(q)) return g;
          const fields = g.fields.filter((f) =>
            f.label.toLowerCase().includes(q)
            || f.key.toLowerCase().includes(q)
            || (f.help ?? "").toLowerCase().includes(q));
          return fields.length ? { ...g, fields } : null;
        })
        .filter((g): g is NonNullable<typeof g> => g !== null);
    }
    return out;
  }, [groups, section, query]);

  const filtered = section !== null || query.trim() !== "";

  return (
    <>
      <PageHeader
        title="Settings"
        lede={mayWrite
          ? "Changes apply straight away unless a field says otherwise."
          : "What this host is configured to do. Changing it takes an administrator."}
      />

      {error && <Notice tone="problem">{error}</Notice>}

      {!data ? <Loading rows={6} /> : (
        <>
          <div className="mb-4 space-y-3">
            <div className="relative">
              <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="Search settings"
                aria-label="Search settings"
                className="pl-9"
              />
            </div>
            <div className="flex flex-wrap gap-1.5">
              <Button
                variant={section === null ? "secondary" : "ghost"} size="sm"
                onClick={() => setSection(null)}
              >
                All
              </Button>
              {groups.map((g) => (
                <Button
                  key={g.name} size="sm"
                  variant={section === g.name ? "secondary" : "ghost"}
                  onClick={() => setSection(section === g.name ? null : g.name)}
                >
                  {g.title}
                </Button>
              ))}
            </div>
          </div>

          {shown.length === 0 ? (
            <Notice tone="neutral">
              Nothing matches “{query}”. Settings for a plugin are on that
              plugin's page, and the sign-in ones are under Authentication.
            </Notice>
          ) : (
            <SettingsForm
              groups={shown} settings={data} links={LINKS}
              onSaved={reload} readOnly={!mayWrite}
            />
          )}

          {/* Only with everything shown: a filtered page is answering a
              narrower question, and this is a footnote about the whole set. */}
          {!filtered && <StartupFile values={data.bootstrap} />}
        </>
      )}
    </>
  );
}

/**
 * The handful of values that are not on this page, and where they are instead.
 *
 * Everything else moved into the database so a change could be recorded
 * against whoever made it. These four could not, and each says why. Showing
 * them is the point: "everything is on this page" is only useful to know if
 * the exceptions are named, and an operator hunting for a setting that isn't
 * here should find out where it lives rather than conclude it doesn't exist.
 */
function StartupFile({ values }: { values: BootstrapSetting[] }) {
  if (values.length === 0) return null;
  return (
    <Card className="mt-4">
      <CardHeader>
        <CardTitle className="text-base">In the startup file</CardTitle>
        <p className="text-sm text-muted-foreground">
          These four can't live in the database, so they stay in{" "}
          <code className="font-mono">config.yaml</code> and take a restart.
          Everything else on this page is stored here, and every change to it is
          recorded against whoever made it.
        </p>
      </CardHeader>
      <CardContent>
        <dl className="space-y-4">
          {values.map((v) => (
            <div key={v.key} className="space-y-0.5">
              <dt className="text-sm font-medium">{v.label}</dt>
              <dd className="break-all font-mono text-sm text-muted-foreground">
                {v.value}
              </dd>
              {v.help && (
                <dd className="text-xs text-muted-foreground">{v.help}</dd>
              )}
            </div>
          ))}
        </dl>
      </CardContent>
    </Card>
  );
}
