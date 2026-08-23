import { useRef, useState } from "react";
import { api, ApiError, type CatalogEntry } from "@/lib/api";
import { relative, when } from "@/lib/format";
import { Detail, Notice } from "@/components/chrome";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";

/**
 * Importing a remote server from its server.json, and the only way one is
 * added -- a catalogued entry just seeds the fields.
 *
 * The seeds are read once at mount, so the caller keys this component on the
 * entry rather than pushing new values under a half-edited paste.
 */
export function ImportDialog({
  open, onOpenChange, onImported, seedName, seedDocument, seedEntry, taken,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onImported: (name: string) => void;
  /** A name the catalog suggests. Editable: it is this host's name for it. */
  seedName?: string;
  /** The catalog's copy of the published document, shown for reading. */
  seedDocument?: unknown;
  /** For reading only; what is imported is the text in the box. */
  seedEntry?: CatalogEntry;
  /** Plugin names already in use here. */
  taken?: Set<string>;
}) {
  const notify = useNotify();
  const [name, setName] = useState(seedName ?? "");
  const [documentText, setDocumentText] = useState(
    seedDocument === undefined ? "" : JSON.stringify(seedDocument, null, 2),
  );
  const [problem, setProblem] = useState("");
  const [nameProblem, setNameProblem] = useState("");
  const [busy, setBusy] = useState(false);
  const nameField = useRef<HTMLInputElement>(null);

  const trimmed = name.trim();
  // Many registry names end in `/mcp`, so the suggestion collides often. Caught
  // here, the refusal is a field to change rather than a round trip.
  const collides = trimmed !== "" && (taken?.has(trimmed) ?? false);

  function reset() {
    setName(seedName ?? "");
    setDocumentText(
      seedDocument === undefined ? "" : JSON.stringify(seedDocument, null, 2),
    );
    setProblem("");
  }

  async function submit() {
    setProblem("");
    setNameProblem("");
    let parsed: unknown;
    try {
      parsed = JSON.parse(documentText);
    } catch {
      // The browser names the position; the server can only say it failed.
      setProblem("That is not valid JSON. Paste the server.json exactly as published.");
      return;
    }
    setBusy(true);
    try {
      const result = await api.importMCPServer(name.trim(), parsed);
      notify("good", result.note ?? "Imported.");
      reset();
      onOpenChange(false);
      onImported(name.trim());
    } catch (e) {
      const detail = e instanceof ApiError ? e.detail : "Couldn't import that.";
      // Next to the box, with the cursor, because it is a field to change.
      if (/already exists/i.test(detail)) {
        setNameProblem(detail);
        nameField.current?.focus();
        nameField.current?.select();
      } else {
        setProblem(detail);
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) {
          setProblem("");
          setNameProblem("");
        }
        onOpenChange(next);
      }}
    >
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Add a remote MCP server</DialogTitle>
          <DialogDescription>
            {seedDocument === undefined
              ? <>Paste the server's published <code className="font-mono">server.json</code>. It is stored verbatim and checked against a schema this build carries; nothing is fetched while you do it.</>
              : <>The catalog's copy of this server's <code className="font-mono">server.json</code>. Read it before adding — it is stored verbatim and checked against a schema this build carries, and it is editable if you need to change something.</>}
          </DialogDescription>
        </DialogHeader>

        {problem && <Notice tone="problem">{problem}</Notice>}

        {seedEntry && <CatalogFacts entry={seedEntry} />}

        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="mcp-name">Name</Label>
            <Input
              id="mcp-name" value={name} placeholder="weather"
              ref={nameField}
              aria-invalid={collides || nameProblem !== "" ? true : undefined}
              aria-describedby="mcp-name-help"
              onChange={(e) => {
                setName(e.target.value);
                setNameProblem("");
              }}
            />
            {(collides || nameProblem) && (
              <p className="text-xs text-problem">
                {nameProblem || (
                  <>
                    A plugin called <code className="font-mono">{trimmed}</code>{" "}
                    is already here. Pick another — this is only what to call it
                    on this host, so anything legal will do.
                  </>
                )}
              </p>
            )}
            <p id="mcp-name-help" className="text-xs text-muted-foreground">
              Its endpoint path, its tool prefix, and its entry in a credential's
              plugin list. Not the document's own reverse-DNS name — that is not
              a legal path segment.
            </p>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="mcp-doc">server.json</Label>
            <Textarea
              id="mcp-doc" value={documentText} rows={12}
              className="font-mono text-xs"
              placeholder={'{\n  "$schema": "…",\n  "name": "com.example/weather",\n  "version": "1.0.0",\n  "remotes": [{ "type": "streamable-http", "url": "https://…" }]\n}'}
              onChange={(e) => setDocumentText(e.target.value)}
            />
          </div>

          <Notice tone="info">
            Adding mounts nothing. On its plugin page: fill in whatever the
            document asks for, run discovery, then decide tool by tool what may
            be served.
          </Notice>
        </div>

        <DialogFooter className="sm:justify-start">
          <Button
            disabled={busy || collides || !trimmed || !documentText.trim()}
            onClick={submit}
          >
            {busy ? "Adding…" : "Add"}
          </Button>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>Cancel</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/**
 * What the catalogue says about this server. A field it did not fill in is left
 * out rather than dashed, which would read as a value somebody chose.
 */
function CatalogFacts({ entry }: { entry: CatalogEntry }) {
  const credential = entry.auth === "api_key"
    ? "Needs an API key"
    : entry.auth === "none" ? "No credential" : "";
  return (
    <dl className="grid gap-x-6 gap-y-3 rounded-md border bg-muted/30 p-3 sm:grid-cols-2">
      {entry.version && (
        <Detail label="Version"><span className="font-mono">{entry.version}</span></Detail>
      )}
      {entry.transport && <Detail label="Transport">{entry.transport}</Detail>}
      {credential && <Detail label="Credential">{credential}</Detail>}
      {entry.updated_at && (
        <Detail label="Updated">
          <span title={when(entry.updated_at)}>{relative(entry.updated_at)}</span>
        </Detail>
      )}
      {entry.source && <Detail label="Catalogue">{entry.source}</Detail>}
      {entry.url && (
        <Detail label="Endpoint" className="sm:col-span-2">
          <span className="block truncate font-mono text-xs" title={entry.url}>
            {entry.url}
          </span>
        </Detail>
      )}
    </dl>
  );
}
