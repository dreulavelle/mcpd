import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import * as apiModule from "@/lib/api";
import { api, ApiError, type BackupStatus } from "@/lib/api";
import { renderWith } from "@/test/render";
import { BackupRestore } from "./BackupRestore";

function statusView(overrides: Partial<BackupStatus> = {}): BackupStatus {
  return {
    database_bytes: 401408,
    tls_files: 3,
    config_included: true,
    key_fingerprint: "0badcafe0badcafe",
    schema_version: 19,
    mcpd_version: "0.6.1",
    instance: "https://mcp.example",
    min_passphrase: 12,
    plugin_files: 0,
    plugin_bytes: 0,
    ...overrides,
  };
}

/**
 * The page mounts three sections that load themselves. Their own tests are
 * beside this one; here they are stubbed empty so that a test about the
 * download form is not also a test of the destination list, and so that an
 * unstubbed fetch cannot reject in the background.
 */
function stub(status: BackupStatus) {
  vi.spyOn(api, "backupStatus").mockResolvedValue(status);
  vi.spyOn(api, "backupDestinations").mockResolvedValue({ destinations: [], kinds: [] });
  vi.spyOn(api, "backupRuns").mockResolvedValue({ runs: [] });
  vi.spyOn(api, "settings").mockResolvedValue({
    groups: [], values: {}, secrets_set: {},
    encryption_available: true, bootstrap: [],
  });
}

describe("the backup and restore page", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
    window.sessionStorage.clear();
  });

  // The fingerprint is the one fact that decides whether an archive will
  // restore anywhere else, so it is on the page rather than in a document.
  it("names the encryption key an archive would be readable under", async () => {
    stub(statusView());
    renderWith(<BackupRestore />);

    expect(await screen.findByText("0badcafe0badcafe")).toBeInTheDocument();
    expect(screen.getByText(/needs a host using this same\s+key/)).toBeInTheDocument();
  });

  // config.yaml travels so the archive is a complete record, and is not
  // restored because it holds the machine's paths rather than the instance's
  // settings. Saying only the first half would be a promise the restore breaks.
  it("says config.yaml is carried but not restored", async () => {
    stub(statusView());
    renderWith(<BackupRestore />);

    expect(await screen.findByText("config.yaml")).toBeInTheDocument();
    expect(screen.getByText(/Not restored/)).toBeInTheDocument();
  });

  it("will not take a backup until the passphrase is long enough and matches", async () => {
    stub(statusView());
    renderWith(<BackupRestore />);

    const form = within(await screen.findByRole("form", { name: "Back up" }));
    const download = form.getByRole("button", { name: "Download backup" });
    expect(download).toBeDisabled();

    const user = userEvent.setup();
    await user.type(form.getByLabelText("Passphrase"), "short");
    expect(download).toBeDisabled();
    expect(screen.getByText("At least 12 characters.")).toBeInTheDocument();

    await user.clear(form.getByLabelText("Passphrase"));
    await user.type(form.getByLabelText("Passphrase"), "a-long-enough-passphrase");
    expect(download).toBeDisabled();

    await user.type(form.getByLabelText("Again"), "a-long-enough-passphras");
    expect(screen.getByText("These don't match.")).toBeInTheDocument();
    expect(download).toBeDisabled();

    await user.type(form.getByLabelText("Again"), "e");
    await waitFor(() => expect(download).toBeEnabled());
  });

  // A staged restore has changed nothing yet, and the page has to say so. An
  // operator who believes it has already happened will not press restart.
  it("says a staged restore is waiting and has changed nothing", async () => {
    stub(statusView({
      pending: {
        staged_at: "2026-08-29T12:00:00Z",
        actor: "user:admin@example.com",
        manifest: {
          created_at: "2026-08-28T09:00:00Z",
          mcpd_version: "0.6.1",
          schema_version: 19,
          instance: "https://mcp.old-box",
          files: [],
        },
      },
    }));
    renderWith(<BackupRestore />);

    expect(await screen.findByText(/Nothing has changed so far/)).toBeInTheDocument();
    expect(screen.getByText("https://mcp.old-box")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Restart and apply" })).toBeInTheDocument();
  });

  // A restore restarts the host itself, so the page has to say the reconnect
  // is coming rather than leaving somebody to press something else.
  it("says the host is restarting once an archive is accepted", async () => {
    stub(statusView());
    vi.spyOn(apiModule, "stageRestore").mockResolvedValue({
      status: "restoring",
      pending: {
        staged_at: "2026-08-29T12:00:00Z",
        actor: "user:admin@example.com",
        manifest: {
          created_at: "2026-08-28T09:00:00Z",
          mcpd_version: "0.6.1", schema_version: 19, files: [],
        },
      },
      note: "The archive has been checked. mcpd is restarting to apply it, and this page will reconnect on its own.",
    });
    renderWith(<BackupRestore />);

    const user = userEvent.setup();
    const form = within(await screen.findByRole("form", { name: "Restore" }));
    await user.upload(form.getByLabelText("Archive"),
      new File(["mcpd-backup/1\n"], "backup.mcpdbak"));
    await user.type(form.getByLabelText("Passphrase"), "a-long-enough-passphrase");
    await user.click(form.getByRole("button", { name: "Restore and restart" }));

    expect(await screen.findByText(/restarting to apply it/)).toBeInTheDocument();
  });

  // When the restart could not be asked for, the archive is still staged and
  // will apply on the next start. Reporting a reconnect that is not coming
  // would leave somebody watching a page that never changes.
  it("says so when the host could not restart itself", async () => {
    stub(statusView());
    vi.spyOn(apiModule, "stageRestore").mockResolvedValue({
      status: "staged",
      pending: {
        staged_at: "2026-08-29T12:00:00Z",
        actor: "user:admin@example.com",
        manifest: {
          created_at: "2026-08-28T09:00:00Z",
          mcpd_version: "0.6.1", schema_version: 19, files: [],
        },
      },
      note: "The archive is checked and ready, but this host could not restart itself: a restart is already under way. It will be applied the next time mcpd starts.",
    });
    renderWith(<BackupRestore />);

    const user = userEvent.setup();
    const form = within(await screen.findByRole("form", { name: "Restore" }));
    await user.upload(form.getByLabelText("Archive"),
      new File(["mcpd-backup/1\n"], "backup.mcpdbak"));
    await user.type(form.getByLabelText("Passphrase"), "a-long-enough-passphrase");
    await user.click(form.getByRole("button", { name: "Restore and restart" }));

    expect(await screen.findByText(/could not restart itself/)).toBeInTheDocument();
  });

  // Two restores staged at once is one archive overwriting the other's staging
  // directory, so the second is refused at the button rather than at the host.
  it("does not offer a second restore while one is staged", async () => {
    stub(statusView({
      pending: {
        staged_at: "2026-08-29T12:00:00Z",
        actor: "user:admin@example.com",
        manifest: {
          created_at: "2026-08-28T09:00:00Z",
          mcpd_version: "0.6.1", schema_version: 19, files: [],
        },
      },
    }));
    renderWith(<BackupRestore />);

    expect(await screen.findByText("A restore is already staged. Cancel it first."))
      .toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Restore and restart" })).toBeDisabled();
  });

  // The host's refusals are the useful ones -- a wrong passphrase, a different
  // encryption key -- so they have to reach the operator rather than being
  // replaced with a generic failure.
  it("shows what the host said when an archive is refused", async () => {
    stub(statusView());
    vi.spyOn(apiModule, "stageRestore").mockRejectedValue(
      new ApiError(409, "key_mismatch",
        "this archive was written by an instance using a different settings encryption key"),
    );
    renderWith(<BackupRestore />);

    const user = userEvent.setup();
    const form = within(await screen.findByRole("form", { name: "Restore" }));
    const file = new File(["mcpd-backup/1\n"], "backup.mcpdbak");
    await user.upload(form.getByLabelText("Archive"), file);
    await user.type(form.getByLabelText("Passphrase"), "a-long-enough-passphrase");
    await user.click(form.getByRole("button", { name: "Restore and restart" }));

    expect(await screen.findByText(/different settings encryption key/))
      .toBeInTheDocument();
  });

  it("reports a host that cannot write backups", async () => {
    vi.spyOn(api, "backupStatus").mockRejectedValue(
      new ApiError(501, "not_configured", "this host is not configured to write backups"),
    );
    renderWith(<BackupRestore />);

    expect(await screen.findByText("this host is not configured to write backups"))
      .toBeInTheDocument();
  });
});
