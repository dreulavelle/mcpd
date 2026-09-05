import { SettingsSection } from "./SettingsSection";

/**
 * The knobs somebody turns while diagnosing something, not while setting this
 * host up.
 *
 * Apart from General because the two are read in different moods. Nobody
 * arrives at "how long to wait for a locked database" by browsing; they arrive
 * because something timed out, and every field they had to scroll past to get
 * here was in the way.
 */
export function Advanced() {
  return (
    <SettingsSection
      section="advanced"
      title="Advanced"
      lede="Timeouts and how data is saved. The defaults are right until something says otherwise."
    />
  );
}
