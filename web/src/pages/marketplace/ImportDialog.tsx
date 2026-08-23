import { useState } from "react";
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
 */
export function ImportDialog({ open, onOpenChange, onImported }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onImported: (name: string) => void;
}) {
  const notify = useNotify();
  const [name, setName] = useState("");
  const [documentText, setDocumentText] = useState("");
  const [problem, setProblem] = useState("");
  const [busy, setBusy] = useState(false);

  function reset() {
    setName("");
    setDocumentText("");
    setProblem("");
  }

  async function submit() {
    setProblem("");
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
      setProblem(e instanceof ApiError ? e.detail : "Couldn't import that.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) setProblem("");
        onOpenChange(next);
      }}
    >
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Import a remote MCP server</DialogTitle>
          <DialogDescription>
            Paste the server's published <code className="font-mono">server.json</code>.
            It is stored verbatim and checked against a schema this build carries;
            nothing is fetched while you do it.
          </DialogDescription>
        </DialogHeader>

        {problem && <Notice tone="problem">{problem}</Notice>}

        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="mcp-name">Name</Label>
            <Input
              id="mcp-name" value={name} placeholder="weather"
              onChange={(e) => setName(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">
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
            Importing mounts nothing. Fill in whatever the document asks for,
            run discovery, then decide tool by tool what may be served.
          </Notice>
        </div>

        <DialogFooter className="sm:justify-start">
          <Button disabled={busy || !name.trim() || !documentText.trim()} onClick={submit}>
            {busy ? "Importing…" : "Import"}
          </Button>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>Cancel</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
