import type { ToolCall } from "@/lib/api";
import { Link } from "@/lib/router";
import { Copyable } from "@/components/chrome";
import { Evidence } from "@/components/evidence";

/** What each outcome means, said for somebody reading a list of calls. */
const SENTENCE: Record<Exclude<ToolCall["outcome"], "ok">, string> = {
  error: "The tool ran and failed. The assistant was told why, in these words:",
  denied: "Refused before it ran: this caller is not allowed to use this tool. The assistant was told:",
  rate_limited: "Refused before it ran: this caller was over its rate limit. The assistant was told:",
};

/**
 * Why one call failed.
 *
 * The call list said "failed" and nothing else. The reason is what the
 * assistant itself was sent, so it is quoted rather than run into a sentence
 * of ours -- somebody else wrote it, and it may name an upstream, a status or
 * an id. The correlation id, and a way into the logs by it, go under
 * Technical details for when the reason is not enough.
 */
export function CallDetail({ call }: { call: ToolCall }) {
  if (call.outcome === "ok") return null;
  return (
    <div className="space-y-2 text-sm">
      <p>{SENTENCE[call.outcome]}</p>
      {call.reason ? (
        <blockquote className="rounded-md border-l-2 border-problem/60 bg-muted/40 px-3 py-2 text-sm whitespace-pre-wrap break-words">
          {call.reason}
        </blockquote>
      ) : (
        <p className="text-muted-foreground">
          No reason was kept for this call. Calls recorded before mcpd 0.31
          kept only whether they failed; the logs may still have it.
        </p>
      )}
      {call.correlation_id && (
        <Evidence>
          <div className="flex flex-wrap items-center gap-2 text-xs">
            <span className="text-muted-foreground">Correlation id</span>
            <Copyable value={call.correlation_id} />
            <Link
              to={`/logs?q=${encodeURIComponent(call.correlation_id)}`}
              className="text-primary hover:underline"
            >
              Find it in the logs
            </Link>
          </div>
        </Evidence>
      )}
    </div>
  );
}
