# How a change gets made

Every infrastructure change an assistant proposes passes through the same
path. See [approval-policy.md](approval-policy.md) for the rules that decide
when a change can skip the queue.


```
  echo_set_label                   →  operation_id, state=pending_approval
       (nothing has changed)

  echo_approve_operation           →  state=approved
       (a human decides)

  executor                         →  reload, revalidate, claim, apply, verify
       (at most once)              →  state=succeeded, verified=true
```

Between approval and execution mcpd re-plans against live upstream state and
compares preconditions. If the target changed after approval, the change is
refused rather than applied over someone else's work.

Most of the time you will not see those as two steps. When the assistant can
ask you directly it does, and confirming in the conversation is the approval.
"Approve in the conversation up to", on the Settings page, sets how
consequential a change may be before it has to be shown in full and approved
explicitly instead.

Either way the record is the same. mcpd will not execute a change without an
approval stored in its database, so an assistant has no path that skips it.
