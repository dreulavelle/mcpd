import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, ApiError, type BackupRun } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { BackupHistory } from "./BackupHistory";

function run(overrides: Partial<BackupRun> = {}): BackupRun {
  return {
    id: "run_1",
    started_at: "2026-09-05T04:00:00Z",
    finished_at: "2026-09-05T04:02:00Z",
    trigger: "schedule",
    archive_name: "mcpd-host-20260905-040000Z.mcpdbak",
    size_bytes: 4_194_304,
    status: "ok",
    destinations: [
      { id: "dst_1", name: "NAS", kind: "sftp", ok: true, removed: 1 },
    ],
    ...overrides,
  };
}

function stub(runs: BackupRun[]) {
  vi.spyOn(api, "backupRuns").mockResolvedValue({ runs });
}

describe("the backup history", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
    window.sessionStorage.clear();
  });

  it("says what to do when nothing has run yet", async () => {
    stub([]);
    renderWith(<BackupHistory />);
    expect(await screen.findByText(/Press Back up now/)).toBeInTheDocument();
  });

  /**
   * Five outcomes, and each is a different thing to do about it. The two that
   * matter most are the ones that are neither success nor failure: "sent to
   * some" means there is a backup and one destination has stopped taking it,
   * and "interrupted" means a write may have landed.
   */
  it("names each outcome rather than collapsing them into worked or failed", async () => {
    stub([
      run({ id: "r1", status: "ok" }),
      run({ id: "r2", status: "partial" }),
      run({ id: "r3", status: "failed" }),
      run({ id: "r4", status: "interrupted" }),
      run({ id: "r5", status: "running", size_bytes: 0, finished_at: undefined }),
    ]);
    renderWith(<BackupHistory />);

    expect(await screen.findByText("Sent everywhere")).toBeInTheDocument();
    expect(screen.getByText("Sent to some")).toBeInTheDocument();
    expect(screen.getByText("Not sent")).toBeInTheDocument();
    expect(screen.getByText("Interrupted")).toBeInTheDocument();
    expect(screen.getByText("Running")).toBeInTheDocument();
  });

  // A run still going has no size, and rendering the zero would read as a
  // backup of nothing rather than as a number that does not exist yet.
  it("shows no size for a run that has not finished", async () => {
    stub([run({ status: "running", size_bytes: 0, finished_at: undefined })]);
    renderWith(<BackupHistory />);

    const row = (await screen.findByText("Running")).closest("tr")!;
    expect(row).toHaveTextContent("—");
    expect(row).not.toHaveTextContent("0 bytes");
  });

  it("says how big the archive was and what started the run", async () => {
    stub([
      run({ id: "r1", trigger: "schedule" }),
      run({ id: "r2", trigger: "manual", size_bytes: 1024 }),
    ]);
    renderWith(<BackupHistory />);

    expect(await screen.findByText("On the schedule")).toBeInTheDocument();
    expect(screen.getByText("Asked for")).toBeInTheDocument();
    expect(screen.getByText("4.0 MB")).toBeInTheDocument();
  });

  /**
   * A run is not one outcome but several. "Sent to some" is only useful with
   * the destination that did not take it named beside the ones that did.
   */
  it("says what happened at each destination", async () => {
    stub([run({
      status: "partial",
      destinations: [
        { id: "dst_1", name: "NAS", kind: "sftp", ok: true, removed: 2 },
        {
          id: "dst_2", name: "Bucket", kind: "s3", ok: false, removed: 0,
          error: "The bucket refused the upload.",
          detail: "AccessDenied: s3.example.com",
        },
      ],
    })]);
    renderWith(<BackupHistory />);

    expect(await screen.findByText("NAS")).toBeInTheDocument();
    expect(screen.getByText("Sent. 2 older backups removed.")).toBeInTheDocument();
    expect(screen.getByText("The bucket refused the upload.")).toBeInTheDocument();
    // The evidence goes under Technical details, never into the sentence.
    const block = screen.getByText("Technical details").closest("details")!;
    expect(block).toHaveTextContent("AccessDenied: s3.example.com");
  });

  /**
   * Retention declining to prune is a fact worth reading: it means older
   * archives are still there deliberately, because the listing was not one
   * mcpd believed.
   */
  it("says when retention held off rather than reporting nothing removed", async () => {
    stub([run({
      destinations: [{
        id: "dst_1", name: "NAS", kind: "sftp", ok: true, removed: 0,
        held: "The listing came back with fewer archives than last time, so nothing was removed.",
      }],
    })]);
    renderWith(<BackupHistory />);

    expect(await screen.findByText(/fewer archives than last time/)).toBeInTheDocument();
  });

  // A run that could not even take an archive has a sentence of its own, above
  // the per-destination lines it never got to.
  it("shows why a run never reached a destination", async () => {
    stub([run({
      status: "failed",
      size_bytes: 0,
      destinations: [],
      error: "The archive could not be written.",
      detail: "open /data/tmp: no space left on device",
    })]);
    renderWith(<BackupHistory />);

    expect(await screen.findByText("The archive could not be written.")).toBeInTheDocument();
    const block = screen.getByText("Technical details").closest("details")!;
    expect(block).toHaveTextContent("no space left on device");
  });

  it("starts a backup and shows it", async () => {
    stub([]);
    const trigger = vi.spyOn(api, "runBackup").mockResolvedValue(run());
    renderWith(<BackupHistory />);

    await userEvent.click(await screen.findByRole("button", { name: "Back up now" }));
    expect(trigger).toHaveBeenCalled();
    expect(await screen.findByText(/A backup has started/)).toBeInTheDocument();
  });

  /**
   * Two runs at once is two snapshots pretending to be one backup, so the host
   * refuses the second. Its sentence says what to do, and the page passes it
   * through rather than replacing it with a generic failure.
   */
  it("says a backup is already running when the host refuses a second", async () => {
    stub([run({ status: "running", finished_at: undefined })]);
    vi.spyOn(api, "runBackup").mockRejectedValue(new ApiError(
      409, "conflict",
      "A backup is already running. Wait for it to finish, then try again.",
    ));
    renderWith(<BackupHistory />);

    await userEvent.click(await screen.findByRole("button", { name: "Back up now" }));
    expect(await screen.findByText(/A backup is already running\./)).toBeInTheDocument();
  });

  // The host's other refusals are just as useful: each says what is missing.
  it("passes on the host's reason for refusing a run", async () => {
    stub([]);
    vi.spyOn(api, "runBackup").mockRejectedValue(new ApiError(
      400, "bad_request",
      "There is nowhere to send a backup. Add a destination and switch it on.",
    ));
    renderWith(<BackupHistory />);

    await userEvent.click(await screen.findByRole("button", { name: "Back up now" }));
    expect(await screen.findByText(/Add a destination and switch it on/)).toBeInTheDocument();
  });

  it("offers no way to start one to somebody who cannot change this host", async () => {
    stub([run()]);
    renderWith(<BackupHistory />, { session: sessionFor("user") });

    expect(await screen.findByText("Sent everywhere")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Back up now" })).not.toBeInTheDocument();
  });
});
