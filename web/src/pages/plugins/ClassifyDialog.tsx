import { useState } from "react";
import { ShieldQuestionMark, TriangleAlert } from "lucide-react";
import { api, ApiError, type MCPTool, type MCPToolState } from "@/lib/api";
import { pretty } from "@/lib/format";
import { CodeBlock, Notice } from "@/components/chrome";
import { Chip } from "@/components/status";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Separator } from "@/components/ui/separator";

/**
 * What the far end *claims* about a tool. The specification says a client must
 * not rely on these, so the source is named on the same line as the value.
 */
function Annotations({ tool }: { tool: MCPTool }) {
  const a = tool.descriptor.annotations;
  const claims: [string, boolean | undefined][] = [
    ["Read-only", a?.readOnlyHint],
    ["Destructive", a?.destructiveHint],
    ["Idempotent", a?.idempotentHint],
    ["Reaches the open world", a?.openWorldHint],
  ];
  const stated = claims.filter(([, v]) => v !== undefined);

  return (
    <div className="space-y-2">
      <p className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
        What the server says about it
      </p>
      {stated.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          The server offered no annotations for this tool.
        </p>
      ) : (
        <div className="flex flex-wrap gap-1.5">
          {stated.map(([label, value]) => (
            <Chip key={label} tone={value ? "info" : "neutral"}>
              {label}: {value ? "yes" : "no"}
            </Chip>
          ))}
        </div>
      )}
      <p className="text-xs text-muted-foreground">
        These are the remote server's own claims about its tool, not findings.
        The MCP specification says a client must not rely on them, and nothing
        in mcpd branches on them. Read the description and the schema below and
        decide from those.
      </p>
    </div>
  );
}

/**
 * Deciding whether one remote tool may be served.
 *
 * What is classified is a *descriptor*, not a name: its hash travels with the
 * decision and is part of the `WHERE` clause. A 409 means discovery replaced it
 * while this was open, and the answer is to make the operator read it again --
 * never to resend with the newer hash.
 */
export function ClassifyDialog({ server, tool, open, onOpenChange, onDone }: {
  server: string;
  tool: MCPTool | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: () => void;
}) {
  const notify = useNotify();
  const [busy, setBusy] = useState<MCPToolState | null>(null);
  /**
   * A refusal, keyed to the descriptor it refused. The list mounts this dialog
   * once and swaps `tool`, so a bare flag followed the operator to the next
   * tool and killed both decisions. `current` is legitimately null when the
   * server withdrew the tool.
   */
  const [conflict, setConflict] =
    useState<{ hash: string; current: MCPTool | null } | null>(null);

  if (!tool) return null;

  async function classify(state: MCPToolState) {
    if (!tool) return;
    setBusy(state);
    try {
      await api.classifyMCPTool(server, tool.name, state, tool.descriptor_hash);
      notify("good", state === "enabled"
        ? `${tool.name} is now served.`
        : `${tool.name} will not be served.`);
      setConflict(null);
      onOpenChange(false);
      onDone();
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        // Re-read: the operator needs the descriptor that is there now.
        const current = await api.mcpServerTools(server)
          .then((r) => (r.tools ?? []).find((t) => t.name === tool.name) ?? null)
          .catch(() => null);
        setConflict({ hash: tool.descriptor_hash, current });
        onDone();
        return;
      }
      notify("problem", e instanceof ApiError ? e.detail : "Couldn't record that.");
    } finally {
      setBusy(null);
    }
  }

  // Only the descriptor that was actually refused is treated as stale.
  const changed = conflict?.hash === tool.descriptor_hash;
  const current = changed ? conflict.current : null;

/** Every dismissal goes through here, so none can forget to drop the refusal. */
  function setOpen(next: boolean) {
    if (!next) setConflict(null);
    onOpenChange(next);
  }
  const close = () => setOpen(false);

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="font-mono">{tool.name}</DialogTitle>
          <DialogDescription>
            {tool.descriptor.title && tool.descriptor.title !== tool.name
              ? tool.descriptor.title
              : `A tool offered by ${server}. Nothing is served until you say so.`}
          </DialogDescription>
        </DialogHeader>

        {changed && (
          <Notice tone="attention" icon={<TriangleAlert />}>
            <strong>This tool changed while you were reading it.</strong> The
            decision was not recorded, because it would have been a decision
            about the description and schema below rather than the ones the
            server is offering now. Close this, re-read the tool, and decide
            again.
            <span className="mt-2 block font-mono text-xs">
              was {tool.descriptor_hash.slice(0, 16)}…
              {current
                ? ` · now ${current.descriptor_hash.slice(0, 16)}…`
                : " · it is no longer in the snapshot"}
            </span>
            {current && <StaleDiff was={tool} now={current} />}
          </Notice>
        )}

        <div className="space-y-4">
          <div className="space-y-1.5">
            <p className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
              Description
            </p>
            <p className="text-sm">
              {tool.descriptor.description || (
                <span className="text-muted-foreground">
                  The server gave none. A tool a model is expected to choose,
                  with nothing saying when to choose it.
                </span>
              )}
            </p>
          </div>

          <Annotations tool={tool} />

          <div className="space-y-1.5">
            <p className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
              Input schema
            </p>
            {tool.descriptor.inputSchema
              ? <CodeBlock>{pretty(tool.descriptor.inputSchema)}</CodeBlock>
              : (
                <p className="text-sm text-muted-foreground">
                  None given. This is the only argument validation there is.
                </p>
              )}
          </div>

          {tool.problem && (
            <Notice tone="problem">
              <strong>This tool cannot be served.</strong> {tool.problem}
            </Notice>
          )}

          <Separator />

          <p className="flex items-start gap-2 text-xs text-muted-foreground">
            <ShieldQuestionMark className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
            <span>
              Your decision is recorded against this exact descriptor
              (<code className="font-mono">{tool.descriptor_hash.slice(0, 16)}…</code>).
              If the server changes the tool later it returns to pending and has
              to be read again.
            </span>
          </p>
        </div>

        <DialogFooter className="sm:justify-start">
          <Button
            disabled={busy !== null || changed || Boolean(tool.problem)}
            onClick={() => classify("enabled")}
          >
            {busy === "enabled" ? "Saving…" : "Serve this tool"}
          </Button>
          <Button
            variant="outline"
            disabled={busy !== null || changed}
            onClick={() => classify("disabled")}
          >
            {busy === "disabled" ? "Saving…" : "Do not serve it"}
          </Button>
          {/* The same path as ESC and the overlay; calling the prop skips the reset. */}
          <Button variant="ghost" onClick={() => close()}>
            {changed ? "Close and re-read" : "Cancel"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** What actually differs between the descriptor read and the one stored now. */
function StaleDiff({ was, now }: { was: MCPTool; now: MCPTool }) {
  const rows: [string, string, string][] = [];
  if (was.descriptor.description !== now.descriptor.description) {
    rows.push([
      "Description",
      was.descriptor.description || "(none)",
      now.descriptor.description || "(none)",
    ]);
  }
  const wasSchema = pretty(was.descriptor.inputSchema);
  const nowSchema = pretty(now.descriptor.inputSchema);
  if (wasSchema !== nowSchema) rows.push(["Input schema", wasSchema, nowSchema]);

  const wasNotes = pretty(was.descriptor.annotations);
  const nowNotes = pretty(now.descriptor.annotations);
  if (wasNotes !== nowNotes) rows.push(["Annotations", wasNotes, nowNotes]);

  if (rows.length === 0) {
    return (
      <p className="mt-2 text-xs">
        The parts shown here read the same, so the difference is elsewhere in
        the descriptor. Re-read it from the list.
      </p>
    );
  }

  return (
    <dl className="mt-3 space-y-3">
      {rows.map(([label, before, after]) => (
        <div key={label} className="space-y-1">
          <dt className="text-xs font-medium">{label}</dt>
          <dd className="grid gap-1 sm:grid-cols-2">
            <pre className="scroll-x rounded border border-current/20 p-2 font-mono text-[11px] whitespace-pre-wrap">
              {before}
            </pre>
            <pre className="scroll-x rounded border border-current/20 p-2 font-mono text-[11px] whitespace-pre-wrap">
              {after}
            </pre>
          </dd>
        </div>
      ))}
    </dl>
  );
}
