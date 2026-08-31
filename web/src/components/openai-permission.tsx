import { useState } from "react";
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
      "Making another key will not help. The permission comes from the OpenAI " +
      "role of whoever created the key, not from the key itself — so a " +
      "replacement made by the same person is refused identically.",
    steps: [
      {
        do: "Give that person a role carrying Tunnels: Read and Manage. Permissions are organisation-wide, not per project.",
        where: { label: "Organization → People → Roles", href: ROLES_URL },
      },
      { do: "Then try the same admin key again. There is no need to issue a new one." },
    ],
    handoff:
      "Please add the tunnel permissions to my OpenAI role: " +
      "api.organization.tunnel.read and api.organization.tunnel.write " +
      "(shown as Tunnels: Read and Manage). They are organisation-level, at " +
      ROLES_URL + ". I need them to create an MCP tunnel.",
    footnote:
      "Only an organisation owner can change roles. If that is not you, copy the request above and send it to someone who is.",
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
  const [copied, setCopied] = useState(false);
  const it = EXPLANATIONS[reason];

  async function copy() {
    if (!it.handoff) return;
    try {
      await navigator.clipboard.writeText(it.handoff);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // Clipboard access is refused in some browsers and over plain HTTP.
      // The text is on screen and selectable either way, so there is nothing
      // to recover from and nothing worth saying about it.
    }
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
            <p className="text-xs text-muted-foreground">{it.handoff}</p>
            <Button type="button" variant="outline" size="sm" onClick={copy}>
              {copied ? "Copied" : "Copy request"}
            </Button>
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
