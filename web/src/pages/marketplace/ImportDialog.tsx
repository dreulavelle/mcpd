import { useRef, useState } from "react";
import { api, type CatalogEntry, problemText } from "@/lib/api";
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

  // Open only when the document is the thing being supplied.
  //
  // Pasting a server.json by hand is the whole task, so the box is the form.
  // Adding a catalogued server is a different job -- the document is already
  // filled in and correct, and almost nobody reads it -- so it starts folded
  // away and the short form is all that is on screen.
  const [showDocument, setShowDocument] = useState(seedDocument === undefined);

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
      // An error about a box nobody can see is an error nobody can act on.
      setShowDocument(true);
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
      const detail = problemText(e, "Couldn't import that.");
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
      {/* Wider only while the document is open, so the two columns each get
          room. Folded away, the form is short and a wide dialog around it
          would be mostly empty. */}
      <DialogContent className={showDocument ? "sm:max-w-5xl" : "sm:max-w-2xl"}>
        <DialogHeader>
          <DialogTitle>Add a remote MCP server</DialogTitle>
          <DialogDescription>
            {seedDocument === undefined
              ? <>Paste the server's published <code className="font-mono">server.json</code>, or the <code className="font-mono">mcpServers</code> block from an editor's <code className="font-mono">mcp.json</code>. Any server in it that runs a local command is named and skipped — this host reaches servers over the network rather than running them itself.</>
              : <>Give it a name on this host. Nothing is served until you allow its tools.</>}
          </DialogDescription>
        </DialogHeader>

        {problem && <Notice tone="problem">{problem}</Notice>}

        {seedEntry && <CatalogFacts entry={seedEntry} />}

        <div className={showDocument ? "grid gap-6 lg:grid-cols-2" : ""}>
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
              What this server is called everywhere else in mcpd. Short and
              lowercase, with no dots or slashes.
            </p>
          </div>

          <Notice tone="info">
            Adding doesn't switch anything on. Next, on its plugin page: fill in
            what it asks for, press Discover, then choose which tools to allow.
          </Notice>
          </div>

          <div className="space-y-1.5">
            <div className="flex items-center justify-between gap-2">
              <Label htmlFor="mcp-doc" className={showDocument ? "" : "sr-only"}>
                server.json
              </Label>
              <Button
                type="button" variant="ghost" size="sm"
                aria-expanded={showDocument}
                aria-controls="mcp-doc"
                onClick={() => setShowDocument((open) => !open)}
              >
                {showDocument ? "Hide server.json" : "Show server.json"}
              </Button>
            </div>
            {showDocument && (
              <Textarea
                id="mcp-doc" value={documentText} rows={18}
                className="h-full min-h-72 font-mono text-xs"
                placeholder={'{\n  "$schema": "…",\n  "name": "com.example/weather",\n  "version": "1.0.0",\n  "remotes": [{ "type": "streamable-http", "url": "https://…" }]\n}\n\nor\n\n{\n  "mcpServers": {\n    "weather": { "url": "https://…/mcp", "headers": { "Authorization": "…" } }\n  }\n}'}
                onChange={(e) => setDocumentText(e.target.value)}
              />
            )}
          </div>
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
  // "unknown" is its own answer, not a missing one. The catalogue's document
  // declared no header and no variable, which is silence rather than a claim
  // that the server is open -- and most servers whose documents look like this
  // answer 401. Saying so here is the difference between an operator who adds
  // it knowing they may have to supply a credential, and one who reads "No
  // credential", adds it, and gets a 401 with nothing naming the cause.
  const credential = entry.auth === "api_key"
    ? "Needs an API key"
    : entry.auth === "none"
      ? "No credential"
      : entry.auth === "unknown"
        ? "Not stated — may need one"
        : "";
  return (
    <dl className="grid gap-x-6 gap-y-3 rounded-md border bg-muted/30 p-3 sm:grid-cols-2">
      {entry.version && (
        <Detail label="Version"><span className="font-mono">{entry.version}</span></Detail>
      )}
      {entry.transport && <Detail label="How it connects">{entry.transport}</Detail>}
      {credential && <Detail label="Credential">{credential}</Detail>}
      {entry.updated_at && (
        <Detail label="Updated">
          <span title={when(entry.updated_at)}>{relative(entry.updated_at)}</span>
        </Detail>
      )}
      {entry.source && <Detail label="Catalogue">{entry.source}</Detail>}
      {entry.url && (
        <Detail label="Address" className="sm:col-span-2">
          <span className="block truncate font-mono text-xs" title={entry.url}>
            {entry.url}
          </span>
        </Detail>
      )}
    </dl>
  );
}
