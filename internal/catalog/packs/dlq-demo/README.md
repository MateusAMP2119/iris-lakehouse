# dlq-demo

A lane doomed on purpose: `boom` and `aftershock` exit 1 every turn, so their runs
dead-letter and the no-retry brake holds. Use it to explore the triage surface:

```
iris catalog install dlq-demo --apply
iris ps                      # watch the dead letters land
iris deadletter list
iris deadletter replay <run>
```

`boom` deliberately declares no `logs:` block, so its apply surfaces the advisory
warning and its runs keep the legacy raw capture — the contrast to a framed lane.

Pack conventions: no secrets, frames on stdout, stderr for logs.
