import { useState } from "react";
import { api, ApiError } from "@/lib/api";
import { SettingsSection } from "./SettingsSection";
import { Section } from "@/components/chrome";
import { useNotify } from "@/components/toast";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";

export function Diagnostics() {
  return (
    <>
      <SettingsSection
        section="diagnostics"
        title="Diagnostics"
        lede="Logging, notifications and crash reports. Nothing leaves this machine until you set a destination."
      />
      <TestNotification />
    </>
  );
}

/**
 * Does the address work?
 *
 * The failure this catches is silent by construction. Every real notification
 * is queued and never blocks its caller, so a mistyped address costs nothing
 * when it is typed and everything at the moment somebody needed to hear from
 * this host. Pressing a button is the only way to find out in between.
 */
function TestNotification() {
  const [busy, setBusy] = useState(false);
  const notify = useNotify();

  async function send() {
    setBusy(true);
    try {
      await api.testNotification();
      notify("good", "Sent. If nothing arrives, the address answered but delivered nowhere.");
    } catch (e) {
      notify("problem", e instanceof ApiError ? e.detail : "Couldn't send it.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Section
      title="Check the address"
      description="Sends one message now, so a wrong address is found here rather than the first time something goes wrong."
    >
      <Card>
        <CardContent className="pt-6">
          <Button variant="outline" disabled={busy} onClick={send}>
            {busy ? "Sending…" : "Send a test"}
          </Button>
        </CardContent>
      </Card>
    </Section>
  );
}
