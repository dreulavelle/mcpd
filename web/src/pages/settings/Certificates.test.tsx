import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { api, ApiError, type Certificate } from "@/lib/api";
import { renderWith } from "@/test/render";
import { Certificates } from "./Certificates";

function certView(overrides: Partial<Certificate> = {}): Certificate {
  return {
    id: "crt_1",
    name: "Work CA",
    pem: "-----BEGIN CERTIFICATE-----\nMIIDazCCAlOg\n-----END CERTIFICATE-----",
    subject: "CN=Work CA",
    issuer: "CN=Work CA",
    fingerprint: "ab".repeat(32),
    not_before: "2026-01-01T00:00:00Z",
    not_after: "2036-01-01T00:00:00Z",
    status: "valid",
    self_signed: true,
    is_ca: true,
    anchors: true,
    added_by: "user:admin@example.com",
    added_at: "2026-08-26T09:00:00Z",
    ...overrides,
  };
}

function stub(certificates: Certificate[]) {
  vi.spyOn(api, "certificates").mockResolvedValue({
    certificates, count: certificates.length,
  });
}

describe("the certificates page", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
    window.sessionStorage.clear();
  });

  // Nothing stored is the right state for most deployments, so the empty page
  // explains what would need one rather than reading as a task left undone.
  it("says what an empty list means", async () => {
    stub([]);
    renderWith(<Certificates />);

    expect(await screen.findByText(/Nothing added, which is right/)).toBeInTheDocument();
  });

  it("warns about a certificate that is about to expire", async () => {
    stub([certView({ status: "expiring" })]);
    renderWith(<Certificates />);

    expect(await screen.findByText("expiring soon")).toBeInTheDocument();
  });

  // A leaf that says it is not an authority cannot verify anything. Trusting
  // it changes nothing, and somebody who is not told that waits for a
  // handshake to start working that never will.
  it("says outright when a certificate cannot anchor a chain", async () => {
    stub([certView({ anchors: false, is_ca: false, self_signed: false })]);
    renderWith(<Certificates />);

    expect(await screen.findByText("cannot anchor a chain")).toBeInTheDocument();
  });

  // Paste and upload fill the same box, and what is sent is the text as typed.
  it("sends a pasted certificate as text", async () => {
    stub([]);
    const add = vi.spyOn(api, "addCertificate")
      .mockResolvedValue(certView({ name: "Work CA" }));
    renderWith(<Certificates />);

    await userEvent.click(await screen.findByRole("button", { name: "Add certificate" }));
    await userEvent.type(screen.getByLabelText("Name"), "Work CA");
    await userEvent.type(
      screen.getByLabelText("Certificate"),
      "-----BEGIN CERTIFICATE-----x-----END CERTIFICATE-----",
    );
    await userEvent.click(screen.getByRole("button", { name: "Add" }));

    expect(add).toHaveBeenCalledWith({
      name: "Work CA",
      pem: "-----BEGIN CERTIFICATE-----x-----END CERTIFICATE-----",
    });
  });

  // The server's refusal is the useful sentence -- it names the file as a
  // PKCS#7 bundle, or says which name the certificate is already under -- so
  // the form shows it rather than a generic failure of its own.
  it("shows the server's reason for refusing one", async () => {
    stub([]);
    vi.spyOn(api, "addCertificate").mockRejectedValue(new ApiError(
      400, "bad_request",
      "trust: that is a PKCS#7 bundle: convert it first with `openssl pkcs7`",
    ));
    renderWith(<Certificates />);

    await userEvent.click(await screen.findByRole("button", { name: "Add certificate" }));
    await userEvent.type(screen.getByLabelText("Name"), "Bundle");
    await userEvent.type(screen.getByLabelText("Certificate"), "not a certificate");
    await userEvent.click(screen.getByRole("button", { name: "Add" }));

    expect(await screen.findByText(/PKCS#7 bundle/)).toBeInTheDocument();
  });
});
