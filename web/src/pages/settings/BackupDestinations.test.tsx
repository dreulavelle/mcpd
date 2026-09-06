import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, type BackupDestination, type BackupKind } from "@/lib/api";
import { renderWith, sessionFor } from "@/test/render";
import { BackupDestinations } from "./BackupDestinations";

function destination(overrides: Partial<BackupDestination> = {}): BackupDestination {
  return {
    id: "dst_1",
    name: "NAS",
    kind: "sftp",
    where: "nas.example.com:/volume1/backups/mcpd",
    settings: {
      host: "nas.example.com", port: 22, username: "mcpd",
      remote_path: "/volume1/backups/mcpd",
    },
    enabled: true,
    policy: { keep_last: 6, keep_daily: 0, keep_weekly: 0, keep_monthly: 0 },
    host_key: "SHA256:pinned0000000000000000000000000000000000000",
    has_secret: true,
    created_at: "2026-08-27T09:00:00Z",
    ...overrides,
  };
}

const ALL_KINDS: BackupKind[] = ["local", "sftp", "s3", "webdav"];

function stub(destinations: BackupDestination[], kinds: BackupKind[] = ALL_KINDS) {
  vi.spyOn(api, "backupDestinations").mockResolvedValue({ destinations, kinds });
}

describe("the backup destinations list", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
    window.sessionStorage.clear();
  });

  // A schedule with nowhere to send a backup looks configured and does
  // nothing, so the empty state says that rather than being blank.
  it("says an empty list is why a scheduled backup has nowhere to go", async () => {
    stub([]);
    renderWith(<BackupDestinations />);
    expect(await screen.findByText(/nowhere to go/i)).toBeInTheDocument();
  });

  /**
   * Never having run is not a failure. Collapsing the two would have a
   * destination added a minute ago read exactly like one that has stopped
   * working, and somebody would go looking for a problem that is not there.
   */
  it("tells a destination that has never run apart from one that failed", async () => {
    stub([
      destination({ id: "dst_1", name: "NAS" }),
      destination({
        id: "dst_2", name: "Bucket", kind: "s3", where: "s3.example.com/backups",
        last_ok: false,
        last_error: "The bucket refused the upload.",
        last_detail: "AccessDenied: s3.example.com",
      }),
    ]);
    renderWith(<BackupDestinations />);

    expect(await screen.findByText("No backup has been sent here yet.")).toBeInTheDocument();
    expect(screen.getByText("The bucket refused the upload.")).toBeInTheDocument();
  });

  // The rule the whole page follows: an error code or a host name goes under
  // Technical details, never into the sentence somebody reads first.
  it("keeps the evidence for a failure out of its sentence", async () => {
    stub([destination({
      last_ok: false,
      last_error: "The bucket refused the upload.",
      last_detail: "AccessDenied: s3.example.com",
    })]);
    renderWith(<BackupDestinations />);

    const sentence = await screen.findByText("The bucket refused the upload.");
    expect(sentence).not.toHaveTextContent("AccessDenied");
    const block = screen.getByText("Technical details").closest("details")!;
    expect(block).toHaveTextContent("AccessDenied: s3.example.com");
  });

  it("says how many archives a destination keeps", async () => {
    stub([destination({
      policy: { keep_last: 6, keep_daily: 7, keep_weekly: 0, keep_monthly: 3 },
    })]);
    renderWith(<BackupDestinations />);

    expect(await screen.findByText(
      "Keeps the last 6, and the newest in each of the last 7 days and 3 months.",
    )).toBeInTheDocument();
  });

  /**
   * Four kinds, one form. The fields for the kind that is chosen are the only
   * ones on screen: a hidden host name would still be submitted, and a form
   * showing every field of every transport is one nobody can fill in.
   */
  it("asks for what the chosen kind needs and nothing else", async () => {
    stub([]);
    renderWith(<BackupDestinations />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole("button", { name: "Add destination" }));

    // Local is first, and asks only where to write.
    expect(screen.getByLabelText("Folder")).toBeInTheDocument();
    expect(screen.queryByLabelText("User name")).not.toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText("Kind"), "sftp");
    expect(screen.getByLabelText("Address")).toBeInTheDocument();
    expect(screen.getByLabelText("User name")).toBeInTheDocument();
    expect(screen.getByLabelText("Port")).toBeInTheDocument();
    expect(screen.queryByLabelText("Bucket")).not.toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText("Kind"), "s3");
    expect(screen.getByLabelText("Bucket")).toBeInTheDocument();
    expect(screen.getByLabelText("Secret key")).toBeInTheDocument();
    expect(screen.queryByLabelText("Port")).not.toBeInTheDocument();
    // B2 hides a deleted file rather than removing it, and keeps charging for
    // it, so retention looks like it is working and the bill disagrees.
    expect(screen.getByText(/Backblaze B2 keeps a version/)).toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText("Kind"), "webdav");
    expect(screen.getByLabelText("Address")).toBeInTheDocument();
    expect(screen.getByLabelText("Password")).toBeInTheDocument();
    expect(screen.queryByLabelText("Bucket")).not.toBeInTheDocument();
  });

  // The settings of the kind being left do not belong to the one arriving. A
  // host name kept behind an S3 form is a field nobody can see and the request
  // still carries.
  it("drops the fields of a kind that was changed away from", async () => {
    stub([]);
    const add = vi.spyOn(api, "addBackupDestination").mockResolvedValue(destination());
    renderWith(<BackupDestinations />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole("button", { name: "Add destination" }));
    await user.type(screen.getByLabelText("Name"), "Bucket");
    await user.selectOptions(screen.getByLabelText("Kind"), "sftp");
    await user.type(screen.getByLabelText("Address"), "nas.example.com");
    await user.selectOptions(screen.getByLabelText("Kind"), "s3");
    await user.type(screen.getByLabelText("Address"), "s3.example.com");
    await user.type(screen.getByLabelText("Bucket"), "backups");
    await user.click(screen.getByRole("button", { name: "Add destination" }));

    expect(add).toHaveBeenCalled();
    const body = add.mock.calls[0]![0];
    expect(body.kind).toBe("s3");
    expect(body.settings).not.toHaveProperty("host");
    expect(body.settings?.bucket).toBe("backups");
  });

  /**
   * The bug this guards: the page never reads a credential back, so an edit
   * that changes only the retention carries none. Sending an empty string
   * would erase the stored password and the next backup would not be sent.
   */
  it("omits the credential from an edit that did not retype it", async () => {
    stub([destination()]);
    const update = vi.spyOn(api, "updateBackupDestination")
      .mockResolvedValue(destination());
    renderWith(<BackupDestinations />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole("button", { name: "Edit" }));
    const keep = await screen.findByLabelText("Keep the last");
    await user.clear(keep);
    await user.type(keep, "3");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(update).toHaveBeenCalled();
    const [, body] = update.mock.calls[0]!;
    expect(body).not.toHaveProperty("secret");
    expect(body.policy?.keep_last).toBe(3);
  });

  // And the box says so, rather than looking like a password that was lost.
  it("says a blank credential box keeps the stored one", async () => {
    stub([destination()]);
    renderWith(<BackupDestinations />);

    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    expect(await screen.findByPlaceholderText("leave blank to keep the stored one"))
      .toBeInTheDocument();
  });

  /**
   * The stored credential is one column and the switch above it is what says
   * how to read it. Flipping the switch and saving without replacing the
   * credential left a password being offered to the server as a PEM key --
   * accepted by the form, saved by the host, and failing every night after.
   *
   * Refused at the button rather than saved half-done, and rather than
   * clearing the stored credential: a destination that cannot sign in says
   * nothing about it until four in the morning either.
   */
  it("will not switch SFTP between a password and a key without a new one", async () => {
    stub([destination()]);
    const update = vi.spyOn(api, "updateBackupDestination")
      .mockResolvedValue(destination());
    renderWith(<BackupDestinations />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(await screen.findByLabelText("Sign in with a private key"));

    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(screen.getByText(/stored credential is the other kind/)).toBeInTheDocument();
    expect(update).not.toHaveBeenCalled();
  });

  // With the replacement typed in, both halves of the change go together.
  it("sends the new credential with the auth mode it belongs to", async () => {
    stub([destination()]);
    const update = vi.spyOn(api, "updateBackupDestination")
      .mockResolvedValue(destination());
    renderWith(<BackupDestinations />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(await screen.findByLabelText("Sign in with a private key"));
    await user.type(await screen.findByLabelText("Private key"), "-----BEGIN KEY-----");
    await user.click(screen.getByRole("button", { name: "Save" }));

    const [, body] = update.mock.calls[0]!;
    expect(body.settings?.key_auth).toBe(true);
    expect(body.secret).toBe("-----BEGIN KEY-----");
  });

  // Switching and switching back lands where it started, so there is nothing
  // to replace. Tracking "was toggled" rather than comparing with what is
  // stored would demand a credential for a change that was never made.
  it("asks for nothing when the auth mode ends where it started", async () => {
    stub([destination()]);
    renderWith(<BackupDestinations />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole("button", { name: "Edit" }));
    const toggle = await screen.findByLabelText("Sign in with a private key");
    await user.click(toggle);
    await user.click(toggle);

    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
  });

  // A retyped one is sent, or there would be no way to replace a password.
  it("sends a credential that was retyped", async () => {
    stub([destination()]);
    const update = vi.spyOn(api, "updateBackupDestination")
      .mockResolvedValue(destination());
    renderWith(<BackupDestinations />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.type(await screen.findByLabelText("Password"), "a-new-password");
    await user.click(screen.getByRole("button", { name: "Save" }));

    const [, body] = update.mock.calls[0]!;
    expect(body.secret).toBe("a-new-password");
  });

  /**
   * Test connection is the one path that records an SFTP server's identity,
   * and it does it while somebody is watching. Showing the fingerprint is the
   * whole point: it is compared with what the server says of itself before the
   * destination is switched on.
   */
  it("shows the key an SFTP server presented", async () => {
    stub([destination({ host_key: "" })]);
    vi.spyOn(api, "testBackupDestination").mockResolvedValue({
      ok: true,
      message: "mcpd reached this destination, and wrote and removed a test file.",
      host_key: "SHA256:abcdef0123456789",
      host_key_recorded: true,
    });
    renderWith(<BackupDestinations />);

    await userEvent.click(await screen.findByRole("button", { name: "Test connection" }));
    expect(await screen.findByText("SHA256:abcdef0123456789")).toBeInTheDocument();
    expect(screen.getByText(/Compare it with what the server says/)).toBeInTheDocument();
    // The command that asks the server itself, so the comparison can be made.
    expect(screen.getByText(/ssh-keyscan nas\.example\.com/)).toBeInTheDocument();
  });

  /**
   * The bug this guards, and the reason the page keeps what was pinned when
   * the button was pressed.
   *
   * RecordHostKey declines by matching zero rows rather than by failing, so a
   * second administrator pinning a key between this request reading the row
   * and writing to it gets an answer that reached the destination, carries a
   * fingerprint, and did not store it. Reading that as a match told an
   * operator the key on their screen was the one mcpd would check against --
   * when the stored one may be a different key entirely, and every backup from
   * then on would be refused for exactly the reason the page had just denied.
   */
  it("does not call an unstored key a match", async () => {
    stub([destination({ host_key: "" })]);
    vi.spyOn(api, "testBackupDestination").mockResolvedValue({
      ok: true,
      message: "This destination already has a host key recorded, so the one "
        + "the server just presented was not stored.",
      detail: "presented SHA256:abcdef0123456789",
      host_key: "SHA256:abcdef0123456789",
      host_key_recorded: false,
    });
    renderWith(<BackupDestinations />);

    await userEvent.click(await screen.findByRole("button", { name: "Test connection" }));
    expect(await screen.findByText(/was not stored/)).toBeInTheDocument();
    expect(screen.getByText(/not what a backup will be checked against/))
      .toBeInTheDocument();
    expect(screen.queryByText(/it is the one already recorded/)).not.toBeInTheDocument();
  });

  // A destination that already had a key pinned was checked against it by the
  // handshake, so a test that worked does prove the server presented that one.
  // This is the only case where a match may be claimed.
  it("says a key matches only when the handshake checked it", async () => {
    stub([destination()]);
    vi.spyOn(api, "testBackupDestination").mockResolvedValue({
      ok: true,
      message: "mcpd reached this destination, and wrote and removed a test file.",
      host_key: "SHA256:pinned0000000000000000000000000000000000000",
      host_key_recorded: false,
    });
    renderWith(<BackupDestinations />);

    await userEvent.click(await screen.findByRole("button", { name: "Test connection" }));
    expect(await screen.findByText(/it is the one already recorded/)).toBeInTheDocument();
  });

  /**
   * A Test connection answer describes the destination as it was when it was
   * asked. Forgetting the key it had just recorded left "it is now the only
   * one mcpd will accept" on screen about a key no longer stored -- the exact
   * opposite of what had happened, beside the button that had just done it.
   */
  it("drops a test answer once the row it describes has changed", async () => {
    // The row as it really moves: unpinned when the button is pressed, pinned
    // once the answer has landed and the list has been reloaded.
    vi.spyOn(api, "backupDestinations")
      .mockResolvedValueOnce({ destinations: [destination({ host_key: "" })], kinds: ALL_KINDS })
      .mockResolvedValue({ destinations: [destination()], kinds: ALL_KINDS });
    vi.spyOn(api, "testBackupDestination").mockResolvedValue({
      ok: true,
      message: "mcpd reached this destination, and wrote and removed a test file.",
      host_key: "SHA256:abcdef0123456789",
      host_key_recorded: true,
    });
    vi.spyOn(api, "updateBackupDestination")
      .mockResolvedValue(destination({ host_key: "", enabled: false }));
    renderWith(<BackupDestinations />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole("button", { name: "Test connection" }));
    expect(await screen.findByText(/it is now the only one mcpd will accept/))
      .toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.click(await screen.findByRole("button", { name: "Forget host key" }));
    await user.click(await screen.findByRole("button", { name: "Forget" }));

    await waitFor(() => expect(
      screen.queryByText(/it is now the only one mcpd will accept/),
    ).not.toBeInTheDocument());
  });

  /**
   * The command has to name the port when it is not 22, or the operator scans
   * whatever else is listening on 22 and compares the fingerprint of a
   * different service -- which is the one way this exercise can pass or fail
   * for no reason at all.
   */
  it("names a non-standard port in the command that asks the server", async () => {
    stub([destination({
      settings: { host: "nas.example.com", port: 2222, username: "mcpd" },
    })]);
    vi.spyOn(api, "testBackupDestination").mockResolvedValue({
      ok: true,
      message: "mcpd reached this destination, and wrote and removed a test file.",
      host_key: "SHA256:pinned0000000000000000000000000000000000000",
      host_key_recorded: false,
    });
    renderWith(<BackupDestinations />);

    await userEvent.click(await screen.findByRole("button", { name: "Test connection" }));
    expect(await screen.findByText(
      "ssh-keyscan -p 2222 nas.example.com | ssh-keygen -lf -")).toBeInTheDocument();
  });

  // Port 22 is the default and naming it is noise in a line meant to be copied.
  it("leaves the port out of the command when it is the usual one", async () => {
    stub([destination()]);
    renderWith(<BackupDestinations />);

    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    expect(await screen.findByText(
      "ssh-keyscan nas.example.com | ssh-keygen -lf -")).toBeInTheDocument();
  });

  // The editor shows it too, and that is where somebody looks after a NAS has
  // been rebuilt and the recorded key has to be checked again.
  it("names a non-standard port in the host key editor", async () => {
    stub([destination({
      settings: { host: "nas.example.com", port: 2222, username: "mcpd" },
    })]);
    renderWith(<BackupDestinations />);

    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    expect(await screen.findByText(
      "ssh-keyscan -p 2222 nas.example.com | ssh-keygen -lf -")).toBeInTheDocument();
  });

  // A failed test is still an answer, so the sentence is shown and the
  // evidence goes where evidence goes.
  it("says what went wrong when a test did not work", async () => {
    stub([destination()]);
    vi.spyOn(api, "testBackupDestination").mockResolvedValue({
      ok: false,
      message: "mcpd could not reach this destination.",
      detail: "dial tcp 10.0.0.9:22: connect: connection refused",
      host_key_recorded: false,
    });
    renderWith(<BackupDestinations />);

    await userEvent.click(await screen.findByRole("button", { name: "Test connection" }));
    expect(await screen.findByText("mcpd could not reach this destination."))
      .toBeInTheDocument();
    const block = screen.getAllByText("Technical details")[0]!.closest("details")!;
    expect(block).toHaveTextContent("connection refused");
  });

  /**
   * Clearing a pinned key has to switch the destination off in the same
   * request. The host refuses to hold an enabled SFTP destination with nothing
   * pinned, so sending only the cleared key would be refused and the operator
   * would be left reading a rejection about a field they did not touch.
   */
  it("switches a destination off when its host key is forgotten", async () => {
    stub([destination()]);
    const update = vi.spyOn(api, "updateBackupDestination")
      .mockResolvedValue(destination({ host_key: "", enabled: false }));
    renderWith(<BackupDestinations />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole("button", { name: "Edit" }));
    await user.click(await screen.findByRole("button", { name: "Forget host key" }));
    await user.click(await screen.findByRole("button", { name: "Forget" }));

    expect(update).toHaveBeenCalledWith("dst_1", { host_key: "", enabled: false });
  });

  // A new SFTP destination cannot be switched on: nothing is pinned yet, so
  // mcpd could not tell the server apart from anything else answering there.
  it("will not switch on an SFTP destination with no recorded key", async () => {
    stub([]);
    renderWith(<BackupDestinations />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole("button", { name: "Add destination" }));
    await user.selectOptions(screen.getByLabelText("Kind"), "sftp");
    expect(screen.getByLabelText("Send backups here")).toBeDisabled();
    expect(screen.getByText(/press Test connection/)).toBeInTheDocument();
  });

  // Removing a destination stops backups going there and cannot be undone from
  // the page, so it is asked about first.
  it("asks before removing a destination", async () => {
    stub([destination()]);
    const remove = vi.spyOn(api, "removeBackupDestination").mockResolvedValue(undefined);
    renderWith(<BackupDestinations />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole("button", { name: "Remove" }));
    // What is already at the destination is not touched, and saying so is what
    // stops somebody assuming their archives have gone with the row.
    expect(await screen.findByText(/Nothing already written there is touched/))
      .toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Remove" }));
    expect(remove).toHaveBeenCalledWith("dst_1");
  });

  // Somebody who cannot change this host is not shown controls that would meet
  // a 403. The list itself is still worth reading.
  it("shows no controls to somebody who cannot change this host", async () => {
    stub([destination()]);
    renderWith(<BackupDestinations />, { session: sessionFor("user") });

    expect(await screen.findByText("NAS")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add destination" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Test connection" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove" })).not.toBeInTheDocument();
  });
});
