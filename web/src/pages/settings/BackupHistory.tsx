import { useCallback, useState } from "react";
import { api, problemText, type BackupRun } from "@/lib/api";
import {
  kindShort, runLabel, runMeaning, runTone, sizeWords, triggerWords,
} from "@/lib/backup";
import { when } from "@/lib/format";
import { usePoll } from "@/lib/hooks";
import { useCan } from "@/lib/session";
import { Loading, Notice } from "@/components/chrome";
import { Evidence } from "@/components/evidence";
import { Chip, StatusDot } from "@/components/status";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import {
  Card, CardContent, CardHeader, CardTitle,
} from "@/components/ui/card";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";

/**
 * What happened last night.
 *
 * The question an operator asks about backups is whether the last one worked,
 * and the honest answer has to survive a restart -- so it is a row the host
 * wrote rather than a log line, and this is that row.
 *
 * A run is not one outcome but several: one archive, and a result per
 * destination. "Sent to some" is not a failure and is not a success, and
 * collapsing it into either would have somebody either ignore a NAS that has
 * stopped taking backups or go looking for one that does not exist.
 */
export function BackupHistory({ onRan }: { onRan?: () => void }) {
  const admin = useCan("system:write");
  const notify = useNotify();
  const [runs, setRuns] = useState<BackupRun[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    api.backupRuns()
      .then((r) => { setRuns(r.runs ?? []); setError(""); })
      .catch((e) => setError(
        problemText(e, "Couldn't read the record of past backups.")));
  }, []);
  usePoll(load, 30_000);

  async function runNow() {
    setBusy(true);
    try {
      await api.runBackup();
      notify("good", "A backup has started. It appears below as it goes.");
      load();
      onRan?.();
    } catch (e) {
      // The host's own sentence for a run already going, a destination that is
      // not there, or a passphrase that is not set. Each says what to do.
      notify("problem", problemText(e, "The backup couldn't be started."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader className="flex flex-row items-start justify-between gap-4">
        <div className="space-y-1.5">
          <CardTitle className="text-base">History</CardTitle>
          <p className="text-sm text-muted-foreground">
            Every run, and what happened at each destination.
          </p>
        </div>
        {admin && (
          <Button variant="outline" disabled={busy} onClick={() => void runNow()}>
            {busy ? "Starting…" : "Back up now"}
          </Button>
        )}
      </CardHeader>
      <CardContent>
        {error && <Notice tone="problem">{error}</Notice>}

        {!runs ? <Loading rows={2} /> : runs.length === 0 ? (
          <Notice tone="neutral">
            No backup has been sent to a destination yet. Press Back up now to
            take one, or switch the schedule on.
          </Notice>
        ) : (
          <div className="scroll-x">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Started</TableHead>
                  <TableHead>Started by</TableHead>
                  <TableHead>What happened</TableHead>
                  <TableHead>Size</TableHead>
                  <TableHead>Destinations</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {runs.map((run) => <RunRow key={run.id} run={run} />)}
              </TableBody>
            </Table>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function RunRow({ run }: { run: BackupRun }) {
  const size = sizeWords(run.size_bytes);
  return (
    <>
      <TableRow>
        <TableCell className="whitespace-nowrap">{when(run.started_at)}</TableCell>
        <TableCell className="text-xs text-muted-foreground">
          {triggerWords(run.trigger)}
        </TableCell>
        <TableCell>
          <Tooltip>
            <TooltipTrigger asChild>
              <span tabIndex={0} className="rounded-full">
                {/* The same chip every status is drawn with, and never
                    animated: a spinner beside a row that is one of five
                    outcomes reads as a page that is loading. */}
                <Chip tone={runTone(run.status)}>
                  <StatusDot tone={runTone(run.status)} />
                  {runLabel(run.status)}
                </Chip>
              </span>
            </TooltipTrigger>
            <TooltipContent className="max-w-xs">{runMeaning(run.status)}</TooltipContent>
          </Tooltip>
        </TableCell>
        <TableCell className="font-mono text-xs tabular-nums whitespace-nowrap">
          {size || "—"}
        </TableCell>
        <TableCell>
          {run.destinations.length === 0 ? (
            <span className="text-xs text-muted-foreground">None</span>
          ) : (
            <ul className="space-y-1">
              {run.destinations.map((d) => (
                <li key={d.id || d.name} className="text-xs">
                  <span className="flex flex-wrap items-center gap-1.5">
                    <StatusDot tone={d.ok ? "good" : "problem"} />
                    <span className="font-medium">{d.name}</span>
                    <Chip>{kindShort(d.kind)}</Chip>
                  </span>
                  <span className="mt-0.5 block text-muted-foreground">
                    {d.ok ? keptWords(d.removed, d.held) : d.error || "It did not take this backup."}
                  </span>
                  <Evidence detail={d.detail} />
                </li>
              ))}
            </ul>
          )}
        </TableCell>
      </TableRow>

      {run.error && (
        <TableRow>
          <TableCell colSpan={5} className="bg-muted/30">
            <p className="text-xs text-problem">{run.error}</p>
            <Evidence detail={run.detail} />
          </TableCell>
        </TableRow>
      )}
    </>
  );
}

/**
 * What retention did here.
 *
 * `held` is the host saying it declined to prune on a listing it did not
 * believe, which is a fact worth reading: it means older archives are still
 * there, deliberately.
 */
function keptWords(removed: number, held?: string): string {
  if (held) return held;
  if (removed <= 0) return "Sent. Nothing older was removed.";
  return `Sent. ${removed} older backup${removed === 1 ? "" : "s"} removed.`;
}
