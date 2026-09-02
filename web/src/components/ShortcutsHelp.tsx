import { describeKeys, type Shortcut } from "@/lib/shortcuts";
import {
  Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";

/** What the keyboard can do here. Reached with "?", which is on the list. */
export function ShortcutsHelp({ open, onOpenChange, shortcuts }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  shortcuts: Shortcut[];
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Keyboard shortcuts</DialogTitle>
          <DialogDescription>
            Two-key sequences are pressed one after the other, not together.
            None of them fire while you are typing in a field.
          </DialogDescription>
        </DialogHeader>
        <dl className="grid grid-cols-[auto_1fr] items-center gap-x-4 gap-y-2 text-sm">
          {shortcuts.map((s) => (
            <div key={s.keys} className="contents">
              <dt className="flex gap-1">
                {describeKeys(s.keys).map((k, i) => (
                  <kbd
                    key={i}
                    className="inline-flex min-w-6 items-center justify-center rounded border bg-muted px-1.5 py-0.5 font-mono text-xs"
                  >
                    {k}
                  </kbd>
                ))}
              </dt>
              <dd className="text-muted-foreground">{s.label}</dd>
            </div>
          ))}
        </dl>
      </DialogContent>
    </Dialog>
  );
}
