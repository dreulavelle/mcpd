import { useCallback, useState } from "react";
import { AlertTriangle, RefreshCw, RotateCw } from "lucide-react";
import { api, type Resources, type UpdateStatus, problemText } from "@/lib/api";
import { useLoader } from "@/lib/hooks";
import { useCan } from "@/lib/session";
import { Loading, Notice, PageHeader } from "@/components/chrome";
import { Markdown } from "@/components/markdown";
import { Chip } from "@/components/status";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

/**
 * The host itself: what it is running, what it is costing, and the two
 * operations that act on the process rather than on what it serves.
 *
 * Separate from Settings because nothing here is a setting. Restarting is not
 * a configuration change, and resource usage is not something anybody edits —
 * putting them on a page of forms would make both harder to find.
 */
export function System() {
  const mayAdmin = useCan("system:write");
  return (
    <>
      <PageHeader
        title="System"
        lede="What this host is running, what it is using, and how to restart it."
      />
      <div className="space-y-4">
        <Version mayAdmin={mayAdmin} />
        <Usage />
        {mayAdmin && <Restart />}
      </div>
    </>
  );
}

/** The running version, and what has been published since. */
function Version({ mayAdmin }: { mayAdmin: boolean }) {
  const load = useCallback(() => api.updates(), []);
  const { data, error, reload } = useLoader(load, "Couldn't check for updates.");
  const [busy, setBusy] = useState(false);
  const notify = useNotify();

  async function checkNow() {
    setBusy(true);
    try {
      await api.checkUpdates();
      reload();
      notify("good", "Checked.");
    } catch (e) {
      notify("problem", problemText(e, "Couldn't check."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader className="flex flex-row items-start justify-between gap-4">
        <div>
          <CardTitle className="text-base">Version</CardTitle>
          <p className="text-sm text-muted-foreground">
            mcpd does not install updates. Replacing a running host is your
            deployment's job.
          </p>
        </div>
        {mayAdmin && (
          <Button variant="outline" size="sm" onClick={checkNow} disabled={busy}>
            <RefreshCw className={busy ? "animate-spin" : undefined} />
            Check now
          </Button>
        )}
      </CardHeader>
      <CardContent className="space-y-4">
        {error && <Notice tone="problem">{error}</Notice>}
        {!data ? <Loading rows={2} /> : <VersionBody status={data} />}
      </CardContent>
    </Card>
  );
}

function VersionBody({ status }: { status: UpdateStatus }) {
  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm text-muted-foreground">Running</span>
        <code className="font-mono text-sm">{status.current}</code>
        {status.update_available && status.latest && (
          <Chip tone="attention">{status.latest} available</Chip>
        )}
        {status.enabled && !status.update_available && status.comparable
          && status.latest && <Chip tone="good">up to date</Chip>}
        {!status.comparable && (
          <Chip tone="neutral">not a release number</Chip>
        )}
      </div>

      {!status.enabled && (
        <Notice tone="neutral">
          Update checking is off, so nothing knows whether a newer version
          exists. Turn it on under Settings → Updates. It checks github.com on a
          timer.
        </Notice>
      )}

      {status.error && (
        <Notice tone="problem">
          The last check failed. It said: “{status.error}”
        </Notice>
      )}

      {!status.comparable && status.enabled && (
        <Notice tone="neutral">
          This host reports <code className="font-mono">{status.current}</code>,
          which is not a release number, so it cannot be compared with one.
          Nothing here is calling it out of date.
        </Notice>
      )}

      {(status.newer?.length ?? 0) > 0 && (
        <div className="space-y-3">
          <p className="text-sm font-medium">
            {status.newer!.length === 1
              ? "One release since this one"
              : `${status.newer!.length} releases since this one`}
          </p>
          {status.newer!.map((r) => (
            <div key={r.version} className="rounded-md border p-3">
              <div className="flex flex-wrap items-baseline gap-2">
                <code className="font-mono text-sm font-medium">{r.version}</code>
                {r.published_at && (
                  <span className="text-xs text-muted-foreground">
                    {new Date(r.published_at).toLocaleDateString()}
                  </span>
                )}
                {r.url && (
                  <a
                    href={r.url} target="_blank" rel="noreferrer"
                    className="text-xs underline underline-offset-2"
                  >
                    release notes
                  </a>
                )}
              </div>
              {r.notes && (
                <Markdown
                  text={r.notes}
                  className="mt-2 max-h-64 overflow-auto text-xs break-words text-muted-foreground"
                />
              )}
            </div>
          ))}
        </div>
      )}

      {status.checked_at && (
        <p className="text-xs text-muted-foreground">
          Last checked {new Date(status.checked_at).toLocaleString()}.
        </p>
      )}
    </>
  );
}

/**
 * Resource usage, refreshed on a timer.
 *
 * Ten seconds rather than one: this is a page somebody leaves open while
 * looking at something else, and a per-second poll would be this host's
 * busiest endpoint by a wide margin.
 */
function Usage() {
  const load = useCallback(() => api.resources(), []);
  const { data, error } = useLoader(load, "Couldn't read resource usage.", 10_000);

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Resources</CardTitle>
        <p className="text-sm text-muted-foreground">
          What mcpd is using on this host. Refreshes every ten seconds.
        </p>
      </CardHeader>
      <CardContent>
        {error && <Notice tone="problem">{error}</Notice>}
        {!data ? <Loading rows={3} /> : <UsageBody r={data} />}
      </CardContent>
    </Card>
  );
}

function UsageBody({ r }: { r: Resources }) {
  const memory = r.resident_bytes ?? r.heap_in_use_bytes;
  const pressure = r.memory_limit_bytes
    ? Math.min(100, (memory / r.memory_limit_bytes) * 100)
    : null;

  return (
    <div className="space-y-5">
      {pressure !== null && (
        <div className="space-y-1.5">
          <div className="flex items-baseline justify-between text-sm">
            <span className="font-medium">Memory</span>
            <span className="text-muted-foreground">
              {bytes(memory)} of {bytes(r.memory_limit_bytes!)} ({pressure.toFixed(0)}%)
            </span>
          </div>
          <div
            className="h-2 w-full overflow-hidden rounded-full bg-muted"
            role="progressbar" aria-valuenow={Math.round(pressure)}
            aria-valuemin={0} aria-valuemax={100}
            aria-label="Memory used against this host's limit"
          >
            <div
              className={pressure > 85 ? "h-full bg-destructive" : "h-full bg-primary"}
              style={{ width: `${pressure}%` }}
            />
          </div>
        </div>
      )}

      <dl className="grid grid-cols-2 gap-x-6 gap-y-4 sm:grid-cols-3">
        <Stat label="Running for" value={duration(r.uptime_seconds)} />
        <Stat label="Memory in use" value={r.resident_bytes ? bytes(r.resident_bytes) : "—"} />
        <Stat
          label="Memory holding data" value={bytes(r.heap_in_use_bytes)}
          help="Part of the memory in use."
        />
        <Stat
          label="Reserved from this host" value={bytes(r.sys_bytes)}
          help="Asked for from the host, and not always handed back."
        />
        <Stat
          label="Concurrent tasks" value={String(r.goroutines)}
          help="How many exist right now, including ones waiting. Not a measure of load."
        />
        <Stat label="Threads" value={r.os_threads ? String(r.os_threads) : "—"} />
        <Stat
          label="CPU time used"
          value={r.cpu_seconds ? `${r.cpu_seconds.toFixed(1)}s` : "—"}
          help="Total since it started, not a rate."
        />
        <Stat label="Open files" value={r.open_files ? String(r.open_files) : "—"} />
        <Stat
          label="Memory clean-ups" value={String(r.gc_cycles)}
          help={`${r.gc_pause_total_ms.toFixed(0)}ms paused in total, ${r.gc_cpu_percent.toFixed(2)}% of CPU.`}
        />
        <Stat label="CPUs it may use" value={`${r.gomaxprocs} of ${r.num_cpu}`} />
        <Stat
          label="Memory allocated in total" value={bytes(r.total_alloc_bytes)}
          help="Every allocation since it started, including memory long since given back. It only ever goes up."
        />
        <Stat label="Memory for running jobs" value={bytes(r.stack_in_use_bytes)} />
      </dl>

      {!r.memory_limit_bytes && (
        <p className="text-xs text-muted-foreground">
          No memory limit is set on this host, so the figures above are totals
          rather than a share of anything.
        </p>
      )}
    </div>
  );
}

function Stat({ label, value, help }: { label: string; value: string; help?: string }) {
  return (
    <div className="space-y-0.5">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="font-mono text-sm tabular-nums">{value}</dd>
      {help && <dd className="text-xs text-muted-foreground">{help}</dd>}
    </div>
  );
}

/**
 * Restarting, behind a confirmation.
 *
 * Two steps because the cost is not obvious from the button: every connector
 * drops and reconnects, and an assistant mid-call gets an error. That is worth
 * a deliberate second press rather than a misclick.
 */
function Restart() {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [sent, setSent] = useState(false);
  const notify = useNotify();

  async function restart() {
    setBusy(true);
    try {
      await api.restart();
      setSent(true);
      notify("good", "Restarting. This page will reconnect on its own.");
    } catch (e) {
      notify("problem", problemText(e, "Couldn't restart."));
      setConfirming(false);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Restart</CardTitle>
        <p className="text-sm text-muted-foreground">
          mcpd stops, and whatever runs it starts it again. Some settings say
          they need this. Most changes take effect without one.
        </p>
      </CardHeader>
      <CardContent>
        {sent ? (
          <Notice tone="neutral">
            Restarting. Connectors reconnect on their own. If this page does not
            come back within a minute, whatever runs mcpd did not start it
            again.
          </Notice>
        ) : confirming ? (
          <div className="space-y-3">
            <Notice tone="attention">
              <AlertTriangle className="mr-1 inline size-4 align-text-bottom" />
              Every connector drops and reconnects, and a call in progress will
              fail. Changes already running are finished first.
            </Notice>
            <div className="flex gap-2">
              <Button variant="destructive" onClick={restart} disabled={busy}>
                <RotateCw className={busy ? "animate-spin" : undefined} />
                Restart now
              </Button>
              <Button variant="outline" onClick={() => setConfirming(false)} disabled={busy}>
                Cancel
              </Button>
            </div>
          </div>
        ) : (
          <Button variant="outline" onClick={() => setConfirming(true)}>
            <RotateCw />
            Restart mcpd
          </Button>
        )}
      </CardContent>
    </Card>
  );
}

/** Bytes as a person reads them. Binary units, because a memory limit is. */
function bytes(n: number): string {
  if (n <= 0) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  const i = Math.min(units.length - 1, Math.floor(Math.log(n) / Math.log(1024)));
  const value = n / 1024 ** i;
  return `${value.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

/** Uptime, to the two units that matter at whatever scale it has reached. */
function duration(seconds: number): string {
  if (seconds < 60) return `${seconds}s`;
  const m = Math.floor(seconds / 60) % 60;
  const h = Math.floor(seconds / 3600) % 24;
  const d = Math.floor(seconds / 86400);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}
