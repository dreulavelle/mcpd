import type { ChatGPTAccount, Pairing } from "@/lib/api";
import { when } from "@/lib/format";
import { Chip, type Tone } from "@/components/status";

/**
 * What OpenAI last said about an organisation and a workspace named together.
 *
 * OpenAI verifies that a ChatGPT workspace belongs to the Platform
 * organisation on a tunnel, and refuses a tunnel it cannot verify. There is no
 * endpoint that says which pairings it accepts, so mcpd learns by making a
 * tunnel -- on save, on Check and on Make -- and keeps the answer. "Not
 * checked" is its own state: nobody has asked, which is not the same as
 * asking and hearing no.
 */
const LOOK: Record<Pairing["status"], { tone: Tone; label: string }> = {
  verified: { tone: "good", label: "Verified" },
  unverified: { tone: "problem", label: "Needs OpenAI Support" },
  refused: { tone: "problem", label: "Refused" },
};

export function PairingChip({ pairing }: { pairing?: Pairing }) {
  if (!pairing) {
    return <Chip tone="neutral" title="Not asked about yet. A Check asks.">Not checked</Chip>;
  }
  const look = LOOK[pairing.status];
  const title = [`Checked ${when(pairing.checked_at)}`, pairing.problem].filter(Boolean).join(". ");
  return <Chip tone={look.tone} title={title}>{look.label}</Chip>;
}

/** The pairing for one of an account's workspaces, "" for the organisation. */
export function pairingFor(account: ChatGPTAccount | undefined, workspace: string): Pairing | undefined {
  return account?.pairings?.find((p) => p.workspace_id === workspace);
}

/** The workspaces OpenAI has said it cannot verify, which a Make will refuse. */
export function unverifiedWorkspaces(account: ChatGPTAccount | undefined): string[] {
  return (account?.workspaces ?? []).filter(
    (ws) => pairingFor(account, ws)?.status === "unverified",
  );
}
