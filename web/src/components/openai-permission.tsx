import { useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";

/**
 * Reasons OpenAI refused, as `internal/tunnel` names them.
 *
 * The server sends a reason and one short sentence. Everything below is the
 * laid-out version, and it lives here rather than in the error because a
 * paragraph flattened into a toast is worse than no explanation: newlines
 * collapse, numbered steps run together, and the one fact that decides what to
 * do next ends up in the middle of a wall.
 */
export const OPENAI_REASONS = [
  "openai_admin_key_rejected",
  "openai_tunnels_manage_required",
  "openai_org_id_rejected",
] as const;

export type OpenAIReason = (typeof OPENAI_REASONS)[number];

export function isOpenAIReason(code: string): code is OpenAIReason {
  return (OPENAI_REASONS as readonly string[]).includes(code);
}

const ROLES_URL = "https://platform.openai.com/settings/organization/people/roles";
const ADMIN_KEYS_URL = "https://platform.openai.com/settings/organization/admin-keys";
const GENERAL_URL = "https://platform.openai.com/settings/organization/general";

type Step = { do: string; where?: { label: string; href: string } };

type Explanation = {
  title: string;
  /** The one fact that decides what to do next. Shown before the steps. */
  lede: string;
  steps: Step[];
  /** Pre-written and copyable, because the reader is often not the fixer. */
  handoff?: string;
  footnote?: string;
};

const EXPLANATIONS: Record<OpenAIReason, Explanation> = {
  openai_tunnels_manage_required: {
    title: "That key cannot manage tunnels",
    lede:
      "Two separate things gate this, and if you are an organisation owner it " +
      "is almost certainly the second: the org role of whoever created the " +
      "key, and the scopes chosen for the key itself. A key can lack a " +
      "permission its creator holds.",
    steps: [
      {
        do:
          "Check the key's own permissions. A key created as Restricted without " +
          "the tunnel scopes is refused even for an owner. Set it to All, or " +
          "include api.organization.tunnel.read and .write.",
        where: { label: "Organization → Admin keys", href: ADMIN_KEYS_URL },
      },
      {
        do:
          "If it already says All, regenerate the key. OpenAI has issued keys " +
          "missing scopes they should have had, and regenerating is their own " +
          "advice for it.",
        where: { label: "Organization → Admin keys", href: ADMIN_KEYS_URL },
      },
      {
        do:
          "Only if you are not an owner: check your role carries Tunnels: Read " +
          "and Manage. Owners have it by default. Permissions are " +
          "organisation-wide, not per project.",
        where: { label: "Organization → People → Roles", href: ROLES_URL },
      },
    ],
    handoff:
      "Please add the tunnel permissions to my OpenAI role: " +
      "api.organization.tunnel.read and api.organization.tunnel.write " +
      "(shown as Tunnels: Read and Manage). They are organisation-level, at " +
      ROLES_URL + ". I need them to create an MCP tunnel.",
    footnote:
      "The request above is only needed if your role is the problem — an owner already has these, and only an owner can grant them to someone else.",
  },
  openai_admin_key_rejected: {
    title: "OpenAI did not recognise that admin key",
    lede:
      "An admin key is not the same thing as the runtime key a tunnel carries " +
      "traffic with. Pasting a runtime key here is always refused, and it is " +
      "the most common cause of this.",
    steps: [
      {
        do: "Check the key was made as an admin key, and copy the whole value.",
        where: { label: "Organization → Admin keys", href: ADMIN_KEYS_URL },
      },
      { do: "Check it has not been revoked since it was created." },
    ],
    footnote:
      "Creating an admin key needs the platform admin-key permission, which is separate from the tunnel permissions.",
  },
  openai_org_id_rejected: {
    title: "OpenAI did not accept that organization ID",
    lede:
      "It begins with org_. An organisation name, an email address or a " +
      "project ID will not work in its place.",
    steps: [
      {
        do: "Copy the ID exactly as shown.",
        where: { label: "Organization → General", href: GENERAL_URL },
      },
    ],
  },
};

/**
 * OpenAIPermissionDialog lays out what to do about a refusal from OpenAI.
 *
 * Opened by the page that caught the error rather than shown inline, because
 * this is several paragraphs and a form is the wrong place for them.
 */
export function OpenAIPermissionDialog({
  reason, detail, onClose,
}: {
  reason: OpenAIReason;
  detail?: string;
  onClose: () => void;
}) {
  // "" until pressed, then what actually happened. A button that reports
  // nothing is indistinguishable from one that is broken.
  const [copied, setCopied] = useState<"" | "done" | "select">("");
  const box = useRef<HTMLTextAreaElement>(null);
  const it = EXPLANATIONS[reason];

  /**
   * Copy, by whichever route this browser allows.
   *
   * navigator.clipboard exists only in a secure context, and mcpd's dashboard
   * is served over plain HTTP on purpose -- it is an internal interface and
   * the TLS is on the MCP listener instead. So on every ordinary install,
   * reached by LAN address, the modern API is simply absent and the older
   * execCommand path is the one that runs.
   *
   * If both are refused the text is left selected and the button says to press
   * the shortcut, because the failure the user must never meet is a button
   * that appears to do nothing.
   */
  async function copy() {
    if (!it.handoff) return;

    if (window.isSecureContext && navigator.clipboard?.writeText) {
      try {
        await navigator.clipboard.writeText(it.handoff);
        return done("done");
      } catch {
        // Refused despite being available -- a permissions policy, usually.
        // Fall through rather than give up.
      }
    }

    const field = box.current;
    if (field) {
      field.focus();
      field.select();
      try {
        if (document.execCommand("copy")) return done("done");
      } catch {
        // Deprecated and occasionally disabled. The selection stands either
        // way, which is what makes the last resort usable.
      }
    }
    done("select");
  }

  function done(how: "done" | "select") {
    setCopied(how);
    setTimeout(() => setCopied(""), 4000);
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{it.title}</DialogTitle>
          {detail && <DialogDescription>{detail}</DialogDescription>}
        </DialogHeader>

        <p className="text-sm text-muted-foreground">{it.lede}</p>

        <ol className="space-y-3 text-sm">
          {it.steps.map((step, i) => (
            <li key={i} className="flex gap-3">
              <span
                aria-hidden
                className="mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full bg-muted text-xs font-medium"
              >
                {i + 1}
              </span>
              <span className="space-y-1">
                <span className="block">{step.do}</span>
                {step.where && (
                  <a
                    href={step.where.href}
                    target="_blank"
                    rel="noreferrer noopener"
                    className="inline-block text-xs underline underline-offset-2"
                  >
                    {step.where.label} ↗
                  </a>
                )}
              </span>
            </li>
          ))}
        </ol>

        {it.handoff && (
          <div className="space-y-2 rounded-md border bg-muted/40 p-3">
            <p className="text-xs font-medium">Send this to your organisation owner</p>
            {/* A textarea rather than a paragraph: it is selectable, and it
                is what execCommand can copy from when the clipboard API is
                not available. Read-only, so nobody edits what they send. */}
            <textarea
              ref={box}
              readOnly
              value={it.handoff}
              rows={4}
              aria-label="Request to send to your organisation owner"
              className="w-full resize-none rounded border bg-background p-2 text-xs text-muted-foreground"
            />
            <div className="flex items-center gap-2">
              <Button type="button" variant="outline" size="sm" onClick={copy}>
                {copied === "done" ? "Copied" : "Copy request"}
              </Button>
              {copied === "select" && (
                <span className="text-xs text-muted-foreground">
                  Selected — press {navigator.platform?.startsWith("Mac") ? "⌘C" : "Ctrl+C"} to copy.
                </span>
              )}
            </div>
          </div>
        )}

        {it.footnote && (
          <p className="text-xs text-muted-foreground">{it.footnote}</p>
        )}

        <DialogFooter>
          <Button type="button" onClick={onClose}>Close</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
