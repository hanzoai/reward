# reward

The rewards rules engine. Programs of rules, claims that staff verify, an
append-only points ledger, levels, badges, budgets and caps. admin.hanzo.ai
configures it; cloud feeds it events and mounts it as the `reward` capability at
`/v1/reward`.

```go
e, err := reward.Mount(app, reward.Config{
    DB:      db,                                  // hanzoai/orm: the shared SQL, or SQLite
    Credits: credit.Client{Base: commerceURL},    // commerce's POST /v1/billing/credit
    Mail:    sender,                              // optional
})
```

## How it works

A **rule** reads one event from the typed **catalog**, tests its fields, and says
what an approved claim does: points, a badge, credit, an email. An event arriving at
`Evaluate` makes a **claim** for every active rule it satisfies, inside the rule's
limits and the program's budget. A manual rule (the default) waits for staff, who
`Approve`, `Reject` or `Reverse` it, each audited. Nothing is edited: claims, steps,
ledger rows and badge rows are written once under ids that are hashes of what they
are about, so every act is exactly-once on SQLite and on the shared SQL alike.

| Package | Is |
|---|---|
| `catalog` | the typed events, under five pillars: use, share, community, contribute, expert |
| `rule` | rules, and the program that holds them with budget, levels, badges, templates |
| `claim` | a claim, and the steps that decide it |
| `ledger` | the append-only points ledger, legacy backfill, the trail of staff acts |
| `engine` | `Evaluate`, `Approve`, `Reject`, `Reverse`, `Backfill`, the configuration |
| `profile`, `emblem` | public profiles, and the live README badge |
| `credit` | the client for commerce's one credit path |
| `http` | the routes |

See [LLM.md](LLM.md) for the design and the routes.

## License

MIT OR Apache-2.0.
