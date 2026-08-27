import { SettingsSection } from "./SettingsSection";

/**
 * What this host says about itself, and to whom.
 *
 * Logging and crash reporting are one subject rather than two: both decide
 * what leaves this machine. Keeping them together is what makes the second one
 * findable -- an operator checking what is sent off the box should not have to
 * know that "errors" is a different group from "logging".
 */
export function Diagnostics() {
  return (
    <SettingsSection
      section="diagnostics"
      title="Diagnostics"
      lede="How much this host writes down, and whether a crash report leaves the machine. Crash reporting is off until you set a destination."
    />
  );
}
