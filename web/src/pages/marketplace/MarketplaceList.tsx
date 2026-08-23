import { useCallback, useState } from "react";
import { Store } from "lucide-react";
import { api, type MCPServer } from "@/lib/api";
import { when } from "@/lib/format";
import { useLoader } from "@/lib/hooks";
import { Link } from "@/lib/router";
import { EmptyState, Loading, Notice, PageHeader } from "@/components/chrome";
import { Chip, StatusDot } from "@/components/status";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { ImportDialog } from "./ImportDialog";

/**
 * Remote MCP servers.
 *
 * The counts are the point of the list: a server with pending tools is one
 * somebody has to look at, and it is the only thing here that needs a person.
 * Everything else — is it enabled, is it mounted — is state to glance at.
 */
export function MarketplaceList() {
  const load = useCallback(() => api.mcpServers(), []);
  const { data, error, reload } = useLoader(load, "Couldn't load remote servers.", 20_000);
  const [importing, setImporting] = useState(false);
  const servers = data?.servers ?? [];
  const pending = servers.reduce((n, s) => n + s.pending, 0);

  return (
    <>
      <PageHeader
        title="Marketplace"
        lede="Servers somebody else runs, mounted here as ordinary plugins. Nothing a remote server offers is served until an administrator has read it and said yes."
        actions={<Button onClick={() => setImporting(true)}>Import a server</Button>}
      />

      <ImportDialog
        open={importing}
        onOpenChange={setImporting}
        onImported={reload}
      />

      {error && <Notice tone="problem">{error}</Notice>}

      {pending > 0 && (
        <Notice tone="attention">
          {pending} {pending === 1 ? "tool is" : "tools are"} waiting to be
          classified. Until then they are not served.
        </Notice>
      )}

      {data === null && !error ? (
        <Loading rows={3} />
      ) : servers.length === 0 ? (
        <EmptyState mark={<Store />} title="No remote servers">
          Import one from its published <code className="font-mono">server.json</code>{" "}
          and it becomes a plugin like any other — same endpoint shape, same
          scoping, same audit.
        </EmptyState>
      ) : (
        <Card className="mt-4 overflow-hidden p-0">
          <div className="scroll-x">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Server</TableHead>
                  <TableHead>State</TableHead>
                  <TableHead className="text-right">Served</TableHead>
                  <TableHead className="text-right">Waiting</TableHead>
                  <TableHead className="text-right">Refused</TableHead>
                  <TableHead>Last change</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {servers.map((s) => <Row key={s.name} server={s} />)}
              </TableBody>
            </Table>
          </div>
        </Card>
      )}
    </>
  );
}

function Row({ server: s }: { server: MCPServer }) {
  return (
    <TableRow>
      <TableCell>
        <Link
          to={`/marketplace/${encodeURIComponent(s.name)}`}
          className="font-medium hover:underline"
        >
          {s.name}
        </Link>
        <div className="max-w-[46ch] truncate text-xs text-muted-foreground">
          {s.title && s.title !== s.name ? `${s.title} — ` : ""}
          {s.description || s.url}
        </div>
      </TableCell>
      <TableCell><ServerState server={s} /></TableCell>
      <TableCell className="text-right tabular-nums">{s.enabled_tools}</TableCell>
      <TableCell className="text-right tabular-nums">
        {s.pending > 0
          ? <span className="font-medium text-attention">{s.pending}</span>
          : <span className="text-faint">0</span>}
      </TableCell>
      <TableCell className="text-right tabular-nums text-muted-foreground">
        {s.disabled}
      </TableCell>
      <TableCell className="whitespace-nowrap text-muted-foreground">
        {when(s.updated_at)}
      </TableCell>
    </TableRow>
  );
}

/**
 * Where a server stands, in one chip.
 *
 * "Unreadable" first, because a row this build can no longer parse can only be
 * listed and removed — none of the other states apply to it, and offering them
 * would be offering things that do nothing.
 */
export function ServerState({ server: s }: { server: MCPServer }) {
  if (!s.readable) {
    return <Chip tone="problem">Unreadable document</Chip>;
  }
  if (!s.enabled) {
    return <Chip tone="neutral">Switched off</Chip>;
  }
  if (s.mounted) {
    return (
      <Chip tone="good">
        <StatusDot tone="good" />
        Serving
      </Chip>
    );
  }
  return (
    <Chip tone="attention">
      <StatusDot tone="attention" />
      {s.enabled_tools === 0 ? "No tools enabled" : "Not mounted"}
    </Chip>
  );
}
