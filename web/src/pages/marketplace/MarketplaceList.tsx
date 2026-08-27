import { useCallback, useMemo, useState } from "react";
import { api } from "@/lib/api";
import { useLoader } from "@/lib/hooks";
import { useRouter } from "@/lib/router";
import { Notice, PageHeader } from "@/components/chrome";
import { Button } from "@/components/ui/button";
import { CatalogList, type Installed } from "./CatalogList";
import type { CatalogChoice } from "./catalog";
import { ImportDialog } from "./ImportDialog";

/**
 * Where a remote MCP server is found and added. Managing one is the plugin
 * page's job.
 *
 * What is already installed is read so the catalogue can say "already added"
 * and so a name collision is caught in the form rather than by the import.
 */
export function MarketplaceList() {
  const { navigate } = useRouter();
  const loadServers = useCallback(() => api.mcpServers(), []);
  const { data, error } = useLoader(loadServers, "Couldn't read what is already added.", 20_000);
  const [seed, setSeed] = useState<CatalogChoice | null>(null);
  const [importing, setImporting] = useState(false);

  const installed = useMemo<Installed>(() => {
    const servers = data?.servers ?? [];
    return {
      names: new Set(servers.map((s) => s.name)),
      // By the address it dials: the local name was chosen here and the
      // catalogue's belongs to somebody else, so neither identifies the other.
      byAddress: new Map(
        servers.filter((s) => s.url).map((s) => [s.url, s.name] as const),
      ),
    };
  }, [data]);

  function addCustom() {
    setSeed(null);
    setImporting(true);
  }

  // The same dialog with the document filled in. There is no second import path.
  function addFromCatalog(choice: CatalogChoice) {
    setSeed(choice);
    setImporting(true);
  }

  return (
    <>
      <PageHeader
        title="Marketplace"
        lede="Ready-made MCP servers you can add to this host. Nothing a new server offers is available until you've looked at its tools and approved them."
        actions={<Button onClick={addCustom}>Add Custom MCP</Button>}
      />

      {/* Keyed on the seed, or the last attempt's paste stays underneath. */}
      <ImportDialog
        key={seed?.name ?? "custom"}
        open={importing}
        onOpenChange={setImporting}
        seedName={seed?.suggested_name}
        seedDocument={seed?.document}
        seedEntry={seed?.entry}
        taken={installed.names}
        // Straight to where it is managed, and where the next steps live.
        onImported={(name) => navigate(`/plugins/${encodeURIComponent(name)}`)}
      />

      {error && <Notice tone="problem">{error}</Notice>}

      <CatalogList installed={installed} onAdd={addFromCatalog} />
    </>
  );
}
