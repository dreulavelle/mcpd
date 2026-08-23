import { useCallback, useMemo, useState } from "react";
import { api } from "@/lib/api";
import { useLoader } from "@/lib/hooks";
import { Link, useRouter } from "@/lib/router";
import { Notice, PageHeader, Section } from "@/components/chrome";
import { Button } from "@/components/ui/button";
import { CatalogList } from "./CatalogList";
import type { CatalogEntry } from "./catalog";
import { ImportDialog } from "./ImportDialog";

/**
 * Where a remote MCP server is found and added. Not where one is managed.
 *
 * The two used to be the same page, so an installed server appeared here and
 * again under Plugins, and looking after one meant bouncing between them. A
 * server that is installed is a plugin -- same endpoint shape, same scoping,
 * same audit -- so it is managed with the other plugins, and this page is only
 * the way in. /marketplace/{name} redirects to the plugin page for anyone who
 * bookmarked the old arrangement.
 *
 * The imported list is still read, for one reason: so the catalog can say
 * "already added" instead of offering a name the import would refuse.
 */
export function MarketplaceList() {
  const { navigate } = useRouter();
  const loadServers = useCallback(() => api.mcpServers(), []);
  const { data, error } = useLoader(loadServers, "Couldn't read what is already added.", 20_000);
  const [seed, setSeed] = useState<CatalogEntry | null>(null);
  const [importing, setImporting] = useState(false);

  const installed = useMemo(
    () => new Set((data?.servers ?? []).map((s) => s.name)),
    [data],
  );

  function addCustom() {
    setSeed(null);
    setImporting(true);
  }

  // The catalog's Add is the same dialog with the document filled in. There is
  // deliberately no second import path: whatever the catalog hands over is
  // validated, has its settings derived and has its tools classified exactly
  // as a pasted document does.
  function addFromCatalog(entry: CatalogEntry) {
    setSeed(entry);
    setImporting(true);
  }

  return (
    <>
      <PageHeader
        title="Marketplace"
        lede="Servers somebody else runs, which you can add to this host. Adding one makes it a plugin: the same endpoint shape, the same scoping, the same audit — and nothing it offers is served until an administrator has read it and said yes."
        actions={<Button onClick={addCustom}>Add Custom MCP</Button>}
      />

      {/* Keyed on the seed so the dialog is built fresh for each one. Feeding
          new values into the state it already holds would leave a half-edited
          paste from the last attempt underneath. */}
      <ImportDialog
        key={seed?.id ?? "custom"}
        open={importing}
        onOpenChange={setImporting}
        seedName={seed?.suggestedName}
        seedDocument={seed?.document}
        // Straight to where it is managed. The next steps -- fill in what it
        // asks for, discover, classify -- all live on that page.
        onImported={(name) => navigate(`/plugins/${encodeURIComponent(name)}`)}
      />

      {error && <Notice tone="problem">{error}</Notice>}

      <Section
        title="Catalog"
        description="Published servers, ready to add. Everything already added lives under Plugins."
      >
        <CatalogList installed={installed} onAdd={addFromCatalog} />
      </Section>

      <p className="mt-6 text-sm text-muted-foreground">
        Anything already added is on the{" "}
        <Link to="/plugins" className="text-primary hover:underline">Plugins</Link>{" "}
        page, where its tools are classified and its credentials are typed in.
      </p>
    </>
  );
}
