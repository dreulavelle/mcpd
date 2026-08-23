import { useRef, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Notice } from "@/components/chrome";
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
 * Importing a remote server from its server.json.
 *
 * Importing records how to reach a server and nothing about what it offers.
 * Saying so here matters more than anywhere else on this page: an operator who
 * expects tools to appear reads the empty list as a failure, and the next
 * thing they do is import it again.
 *
 * The one way a server is added, whether the document was pasted by hand or
 * came from the catalog. A catalogued entry seeds the two fields and is
 * otherwise identical -- same JSON check here, same schema check on the
 * server, same settings derived from the document's inputs, same tools waiting
 * to be classified. A second, smoother path for catalogued servers is exactly
 * how one of them would end up skipping a step.
 *
 * The seeds are read once, at mount. The caller keys this component on which
 * entry it is for, so a fresh dialog is built rather than new values being
 * pushed underneath a half-edited paste.
 *
 * The name collides more often than it looks like it should. Many registry
 * names end in `/mcp`, so the catalogue suggests `mcp` for a great many
 * unrelated servers, and the second one an operator adds is refused. Being
 * told before pressing Add beats being told afterwards, so the names already
 * taken come in as `taken` and the refusal is pre-empted -- and when the
 * server refuses anyway, for a name this page could not know about, the
 * complaint is attached to the field rather than left at the top of the
 * dialog.
 */
export function ImportDialog({
  open, onOpenChange, onImported, seedName, seedDocument, taken,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onImported: (name: string) => void;
  /** A name the catalog suggests. Editable: it is this host's name for it. */
  seedName?: string;
  /** The catalog's copy of the published document, shown for reading. */
  seedDocument?: unknown;
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
  // Known to be taken before anything is sent. The server would refuse it too,
  // and its refusal costs a round trip and reads as a failure rather than as a
  // field to change.
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
      // Caught here rather than sent: a JSON error the browser can name
      // precisely reads better than "the document could not be read".
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
      // A name already in use is a field to change, not a failure to read
      // about. It is the one refusal an operator will hit repeatedly, so it
      // goes next to the box and takes the cursor with it.
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
