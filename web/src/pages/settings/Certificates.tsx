import { useCallback, useRef, useState, type FormEvent } from "react";
import { api, ApiError, type Certificate } from "@/lib/api";
import { usePoll } from "@/lib/hooks";
import { Loading, Notice, PageHeader } from "@/components/chrome";
import { Chip } from "@/components/status";
import { useNotify, type Notify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { useConfirm } from "@/components/confirm";

/**
 * Authorities this host trusts, on top of the ones it ships with.
 *
 * Everything added here is trusted by every outbound connection mcpd makes.
 * That is deliberate and it is what the page says: the alternative — adding a
 * certificate and then naming it again on each integration — fails in the way
 * nobody catches, with the certificate stored and the handshake still failing.
 */
export function Certificates() {
  const [rows, setRows] = useState<Certificate[] | null>(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const notify = useNotify();

  const load = useCallback(() => {
    api.certificates()
      .then((r) => { setRows(r.certificates ?? []); setError(""); })
      .catch(() => setError("Couldn't load certificates."));
  }, []);
  usePoll(load, 60_000);

  return (
    <>
      <PageHeader
        title="Certificates"
        lede="Authorities every integration trusts, on top of the public ones."
        actions={rows && <Button onClick={() => setAdding(true)}>Add certificate</Button>}
      />

      {error && <Notice tone="problem">{error}</Notice>}

      {adding && (
        <AddCertificate
          onClose={() => setAdding(false)}
          onAdded={() => { setAdding(false); load(); }}
        />
      )}

      {!rows ? <Loading rows={3} /> : rows.length === 0 ? (
        <Notice tone="neutral">
          Nothing added, which is right until something needs it. A plugin whose
          address fails with a certificate this host does not trust — a company
          authority, or an appliance that issued its own — is what this page is
          for. Adding one takes effect immediately; there is nothing to restart.
        </Notice>
      ) : (
        <Card className="mt-4 overflow-hidden p-0">
          <div className="scroll-x">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Issued to</TableHead>
                  <TableHead>Expires</TableHead>
                  <TableHead className="w-px" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((c) => (
                  <CertificateRow
                    key={c.id} cert={c} notify={notify} onChanged={load}
                  />
                ))}
              </TableBody>
            </Table>
          </div>
        </Card>
      )}
    </>
  );
}

function CertificateRow({ cert, notify, onChanged }: {
  cert: Certificate;
  notify: Notify;
  onChanged: () => void;
}) {
  const confirm = useConfirm();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [open, setOpen] = useState(false);
  const dead = cert.status === "expired" || cert.status === "not_yet_valid";

  async function remove() {
    if (!(await confirm(
      `Remove ${cert.name}? Anything reaching an address that relies on it ` +
      `stops verifying on its next connection.`,
    ))) return;
    setBusy(true);
    setError("");
    try {
      await api.deleteCertificate(cert.id);
      onChanged();
      notify("good", "Certificate removed.");
    } catch (e) {
      setError(e instanceof ApiError ? e.detail : "That didn't work.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <TableRow className={dead ? "opacity-55" : undefined}>
      <TableCell>
        <span className="flex flex-wrap items-center gap-2">
          <span className="font-medium">{cert.name}</span>
          {cert.status === "expired" && <Chip tone="problem">expired</Chip>}
          {cert.status === "expiring" && <Chip tone="attention">expiring soon</Chip>}
          {cert.status === "not_yet_valid" && (
            <Chip tone="attention">not valid yet</Chip>
          )}
          {cert.self_signed && <Chip>self-signed</Chip>}
          {/* Said plainly, because trusting one of these changes nothing and
              the operator would otherwise be left wondering why. */}
          {!cert.anchors && <Chip tone="problem">cannot anchor a chain</Chip>}
        </span>
        <button
          type="button"
          className="mt-0.5 text-xs text-muted-foreground hover:underline"
          onClick={() => setOpen((v) => !v)}
        >
          {open ? "Hide details" : "Details"}
        </button>
        {open && (
          <div className="mt-2 space-y-1 text-xs text-muted-foreground">
            <div>
              <span className="font-medium">Issued by</span> {cert.issuer}
            </div>
            <div>
              <span className="font-medium">Fingerprint (SHA-256)</span>{" "}
              <span className="break-all font-mono">{cert.fingerprint}</span>
            </div>
            <div>
              Added by {cert.added_by} on{" "}
              {new Date(cert.added_at).toLocaleString()}
            </div>
            {!cert.anchors && (
              <p className="text-problem">
                This certificate says it is not an authority, so nothing can be
                verified against it. Trusting it will not stop the handshake
                failing — the authority that signed it is the one to add.
              </p>
            )}
            <pre className="scroll-x rounded-md border bg-muted/30 p-2 font-mono">
              {cert.pem.trim()}
            </pre>
          </div>
        )}
        {error && <div className="mt-1 text-xs text-problem">{error}</div>}
      </TableCell>
      <TableCell className="text-muted-foreground">{cert.subject}</TableCell>
      <TableCell className="whitespace-nowrap text-muted-foreground">
        {new Date(cert.not_after).toLocaleDateString()}
      </TableCell>
      <TableCell className="whitespace-nowrap">
        <Button variant="ghost" size="sm" disabled={busy} onClick={remove}>
          Remove
        </Button>
      </TableCell>
    </TableRow>
  );
}

/**
 * Pasting and uploading are the same act, so they fill the same box.
 *
 * A certificate arrives as a file about as often as it arrives on a clipboard,
 * and a Windows authority exports a binary DER under a .crt extension without
 * mentioning it. A text file is read into the box, where it can be seen before
 * it is sent; anything binary is sent as it is, because showing somebody a
 * screen of mojibake to confirm would be worse than showing them the file name.
 */
function AddCertificate({ onClose, onAdded }: {
  onClose: () => void;
  onAdded: () => void;
}) {
  const [name, setName] = useState("");
  const [pem, setPem] = useState("");
  const [file, setFile] = useState<{ name: string; base64: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const picker = useRef<HTMLInputElement>(null);
  const notify = useNotify();

  const ready = name.trim() !== "" && (pem.trim() !== "" || file !== null);

  async function pick(chosen: File) {
    setError("");
    const buffer = await chosen.arrayBuffer();
    const bytes = new Uint8Array(buffer);
    const text = new TextDecoder().decode(bytes);

    if (text.includes("-----BEGIN")) {
      // Text: put it in the box, where somebody can see what they picked.
      setPem(text);
      setFile(null);
    } else {
      let binary = "";
      bytes.forEach((b) => { binary += String.fromCharCode(b); });
      setFile({ name: chosen.name, base64: btoa(binary) });
      setPem("");
    }
    if (name.trim() === "") {
      setName(chosen.name.replace(/\.(pem|crt|cer|der|p7b|txt)$/i, ""));
    }
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const added = await api.addCertificate(
        file
          ? { name: name.trim(), base64: file.base64 }
          : { name: name.trim(), pem },
      );
      notify("good", `Trusting ${added.name}. Plugins are picking it up now.`);
      onAdded();
    } catch (e) {
      setError(e instanceof ApiError ? e.detail : "That didn't work.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open onOpenChange={(o) => { if (!o) onClose(); }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add a certificate</DialogTitle>
          <DialogDescription>
            Paste it, or pick the file. It is trusted by every integration this
            host runs, in addition to the public authorities — never instead of
            them.
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={submit} className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="cert-name">Name</Label>
            <Input
              id="cert-name" autoFocus value={name} placeholder="Work CA"
              onChange={(e) => setName(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">
              What you will recognise it by. No commas.
            </p>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="cert-pem">Certificate</Label>
            {file ? (
              <div className="flex items-center justify-between rounded-md border bg-muted/30 p-2 text-sm">
                <span className="truncate font-mono text-xs">{file.name}</span>
                <Button
                  type="button" variant="ghost" size="sm"
                  onClick={() => setFile(null)}
                >
                  Clear
                </Button>
              </div>
            ) : (
              <textarea
                id="cert-pem"
                className="scroll-x h-40 w-full rounded-md border bg-transparent p-2 font-mono text-xs"
                spellCheck={false}
                value={pem}
                placeholder={"-----BEGIN CERTIFICATE-----\nMIIDazCCAlOgAwIBAgIU…\n-----END CERTIFICATE-----"}
                onChange={(e) => setPem(e.target.value)}
              />
            )}
            <div className="flex items-center gap-2">
              <Button
                type="button" variant="ghost" size="sm"
                onClick={() => picker.current?.click()}
              >
                Choose a file
              </Button>
              <span className="text-xs text-muted-foreground">
                .pem, .crt, .cer or .der — one certificate.
              </span>
              <input
                ref={picker} type="file" className="hidden"
                accept=".pem,.crt,.cer,.der,.txt,application/x-x509-ca-cert"
                onChange={(e) => {
                  const chosen = e.target.files?.[0];
                  if (chosen) void pick(chosen);
                  e.target.value = "";
                }}
              />
            </div>
          </div>

          {error && <Notice tone="problem">{error}</Notice>}

          <DialogFooter className="sm:justify-start">
            <Button type="submit" disabled={busy || !ready}>
              {busy ? "Adding…" : "Add"}
            </Button>
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
