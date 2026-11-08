# reward — LLM.md

The rewards rules engine. `github.com/hanzoai/reward`, public, MIT OR Apache-2.0.
admin.hanzo.ai configures it; cloud feeds it events and mounts it as the `reward`
capability at `/v1/reward` (singular, like `referral`). It began as a package in
hanzoai/commerce and moved out by owner decision; commerce keeps the one credit path
and nothing else of it.

## Layers (each imports only those above it)

| Package | Is |
|---|---|
| `refuse` | the one error for input the engine will not take (HTTP 400) |
| `catalog` | the typed events a rule may read, under five pillars (use, share, community, contribute, expert); `Event` and its validation |
| `rule` | `Rule`, `Condition`, `Action`, `Limits`; `Program` (rules, budget, upline, levels, badges, templates) |
| `claim` | `Claim` and `Step`; `Fold` turns steps into a status |
| `ledger` | `Entry` (the append-only ledger), `Standing` (balances, level, badges), `Trail` (staff acts), `Past` (legacy backfill rows) |
| `sponsor` | `Link`: who brought a member in, one per member |
| `profile` | a subject's handle, public flag, links |
| `emblem` | the SVG README badge, a pure function of a card |
| `engine` | `Evaluate`, `Simulate`, `Approve`, `Reject`, `Reverse`, `Backfill`, program configuration, `Seed`, `Show`, `Explore` |
| `credit` | `Client`: `engine.Credits` over commerce's `POST /v1/billing/credit` |
| `http` | the routes (native zip, `Raw` handlers, gates read inside each handler) |
| `reward` (root) | `Mount(app, Config) (*engine.Engine, error)` |

## Everything is written once

Storage is `hanzoai/orm` (`Model[T]`, a JSON document store; `orm.DB` is SQLite
locally and the shared SQL over ZAP in production). orm has `CreateIfAbsent`
(first-writer-wins, race-safe on both) and no compare-and-swap update, so there is no
read-modify-write on any claim, step, ledger row or badge row.

- A **claim** id is `hash(event id, rule id, rule version)`. The same event twice is one
  claim. A claim for the same event under a *newer* version of the rule is also the
  same happening (`Origin` = `hash(event id, rule id)`), so an edit between two
  deliveries cannot award twice.
- A claim row never changes. What is decided is a **Step** row, id
  `hash(claim, act)`, inserted by `CreateIfAbsent`: `decision` (approve|reject),
  `award` (everything written), `reversal`. Exactly one writer wins each, so approve
  and reject racing have one winner. `claim.Fold(steps)` is the status. The `Status`
  column on the claim only lets the queue be listed; it is healed to the fold
  whenever the claim is read for a decision (`engine.read`).
- **Ledger** rows (`Entry`): id `hash(claim, slot, act)`. Points and badges are
  written by `Approve` after the decision step; a retry rewrites the same ids. A
  reversal is more rows (`Act=reversal`, negative points, `Reverses` = the row it
  undoes), never an edit. Credit goes through `Credits.Grant` with key
  `reward:<claim>:<action index>`.
- **Ids live in one column for all kinds** (`_entities.id` is a bare PK), so every kind
  has its own id space: `prg:` program, `clm_` claim, `stp_` step, `ent_` entry,
  `p:` profile, `h:` handle, `s:` sponsor link. A subject named alice and a handle
  named alice are two rows. Never use a bare string as an id.
- Filters use the camelCase of the Go field name (`Subject=` is `$.subject`), so a
  filtered field's JSON tag must equal it.
- Configuration (programs, rules) is edited by people in admin, one at a time:
  last save wins, every save is on the trail. A rule's `Version` is the store's and
  rises only when the rule's content changes.
- Not atomic across replicas: the check-then-claim of **limits and budget** is
  serialized by a process mutex (`engine.reserve`). Two replicas can both pass a
  limit that only one fits; staff reject the extra in the queue. Do not "fix" this
  with a second mutex; fix it with a guard row if it ever matters.

## State machine

```
pending --Approve--> approved --(ledger, badge, credit written)--> awarded --Reverse--> reversed
pending --Reject--> rejected
```

`approved` is a decision made whose effects are still being written (a failed credit
leaves a claim here; `Approve` again finishes it, and `Reject` is refused). Auto rules
approve as they are claimed (reviewer `auto`). Every rule is `manual` unless an admin
sets `auto`. A claim that granted credit cannot be reversed here (no debit path is
built); the rest can. Rejected and reversed claims free their limits and budget.

## Rules

`{id, name, pillar, active, version, approval: manual|auto, when:{event}, if:[{field,op,value}], then:[action], limits:{once, keyField, perDay, perMonth}}`

- Ops: `eq ne in gte lte contains`, checked against the field's kind (`contains` is a
  substring on text, membership on lists; both ignore case).
- Actions: `points` (amount, `redeemable`), `badge`, `credit` (cents; only through
  commerce's credit path), `email` (template of the program). **No `revshare` or
  `payout` action**, by decision, until the owner sets the money terms.
- Limits count claims per subject that are pending, approved or awarded. `once` is
  `subject` or `subject+key` (with `keyField`); `perDay`/`perMonth` are UTC calendar
  day/month counts of claims, not points.
- Program: `budget{pointsPerMonth, creditCentsPerMonth}` (0 = no bound), `upline`
  (see Upline), `levels` (Hacker 0, Builder 100, Shipper 500, Core 2500, Legend 10000
  in the seed), `badges` (each with a pillar and a rarity, common by default),
  `templates`.
- **Pillar** is on every rule (defaults to its event's) and every badge.

## Catalog

`catalog.events` is THE list; an event is a typed addition there. Ingest refuses any
event that is not exactly the listed fields. `usage.tokens.tokens` is the subject's
*lifetime* verified total when the event was emitted, so a milestone is a `gte` rule
with `once: subject`. `model.used.model` is the canonical id; aliases never arrive.
`github.fork {repo, forker}` and `github.badge {repo, owner, badge: profile|built|deploy}` are
contribute events; cloud emits `github.badge` only after its verifier confirmed the
repo is public, owned by the subject's linked GitHub identity, and carries the badge.

## Evidence

An event that takes place on someone else's site (`social.post.verified`, `github.*`,
`bounty.completed`, `submission.video`, `deploy.button`; the
catalog marks them `evidence: true`) is refused without `evidence`:

```
{source, url, needs_review?, checks?: [{fetched_at, status, public, ref_found?, author_match}], recheck?: {at, result}}
```

`source` is the network or site, `url` its canonical https URL. `checks` is the host's
history of automated looks, oldest first (a post is looked at every day of its week);
`author_match` is `yes`, `no` or `unknown`; `ref_found` is stated only where a ref code
is expected. `recheck` is the last look a held post waited for (`ok` or `removed`). A
source the host cannot read arrives `needs_review: true` with no checks and no recheck;
a host never claims a check it did not run. The claim keeps the evidence verbatim, and
`GET /v1/admin/reward/claims` shows it beside the claim it decides. Every rule stays
manual: the evidence is for the staff member, not a substitute for one.

`Limits.KeyField` may name several fields joined by `+` (`network+week`): the key is
their values together, so a subject earns once per network per week.

## Seed (`engine/seed/rewards.json`, program `hanzo-rewards`)

Created once on first mount (`Seed`), never overwritten: after that admin owns it.
Active rules are only the values in the owner-approved 2026-10-26 spec plus the owner's
later values; everything else is inactive at 0 points. All are manual.

- Spec: token milestones 100K +10, 1M +20, 5M +30, 10M +40, 100M +75, 1B +100, 10B
  +150, 100B +200, 1T +300 (not redeemable); referral activated +25, retained +25
  (redeemable); merged PR +50, fix +75, docs +30, translation +30, accepted issue +10,
  bounties +75/+150/+300 (redeemable). The spec's caps are seeded as per-rule claim
  counts (referral `perMonth` 4; PR kinds `perDay` 300/points). The spec states them as
  point caps across rules, which `Limits` cannot express; each rule alone respects the
  cap, together they may not. Levels as above. Credit ceiling $500/month.
- Owner's later values: stars, forks, follows +1 each, `once` per subject+repo (or
  +target), no list and no cap; README badge `github.badge` +10, once per subject+repo.
  These override the spec's "star/follow 0, badge only".
- Inactive, 0 points, staff set the value: a post per product and per model family,
  `weekly-share` (a verified post, once per subject, network and ISO week, so a later
  week's post earns again),
  a demo video per app, a test report per core product and beta, benchmark added,
  tests PR merged, "try" badges per product (18) and model family (Zen, Enso, Kai),
  and four expert rules.
- Known gap: "five jobs delivered" and "try 3 / 5 / 10 / 25 models" are counts over
  many events; `Limits` cannot count. The seed rule `expert-five-jobs-delivered` fires
  on a delivered job rated 4.5 or more, once per subject; the emitter must send it
  on the fifth. Core and Legend also need non-referral activity (spec); not modeled.

## Upline

Points only, never money. A member has one **sponsor**, set once and never changed:
the row `reward-sponsor` (`sponsor.Link`), id `s:<subject>`, written by `CreateIfAbsent`
so the first writer wins. It is made by the catalog event `referral.linked`
(pillar share, field `sponsor`, no evidence; the event's subject is the new member),
which `Evaluate` handles before the rules run, so a staff-priced rule may read it like
any event. It is refused (400) when the sponsor is the member or when the member is
already above the sponsor in the chain (a cycle; the climb is 64 links). A second
`referral.linked` for a member who has a sponsor changes nothing and is not an error;
the event id still dedupes claims. The check-then-link is under `engine.reserve`, like
limits: two replicas can still link a cycle at the same instant, and an award climbs
only three links, so it ends.

`Program.Upline` (`upline`) is the percentage for levels 1, 2 and 3 (1 = the member's
sponsor, 2 = theirs, 3 = one more), at most three entries, each 0 to 100, refused
otherwise when the program is saved; admin edits it with the header like budget and
levels. Empty pays nothing. The seed is `[10, 5, 2]`.

When `Approve` writes a claim with points P > 0, each level with a sponsor gets one
ledger row, `Act=upline`, `Level` 1 to 3, on the sponsor, for `floor(P * pct / 100)`
points (a share of 0 writes nothing), `Redeemable` false, id `EntryID(claim, level,
"upline")`, carrying the claim. Retries rewrite the same ids. The chain and the
percentages are read when the claim is written. Only claims pay: upline rows are not
claims and pay none, and legacy backfill rows make no claims. The rows count toward
lifetime points, levels and rank and never toward redeemable (the fold ignores the
flag on them, as on legacy rows).

`Reverse` undoes them with the claim's own rows: `Act=reversal`, negative points,
`Reverses` the upline row, `Level` copied. `GET /v1/reward/me` adds per program
`network: {level1, level2, level3, points}`: the members one, two and three links
below the caller (each level counted from the previous level's members, stopping at
10,000), and the caller's upline rows less their reversals. It is for the caller only;
the public profile and the badge never show it.

## Legacy backfill

`Backfill(ctx, program, rows, by)`: rows `{subject, source, points, evidence}`,
`source` one of `ledger.Sources` (`usage.tokens`, `referral.signup`, `referral.paying`,
`github.star`, `github.follow`, `github.fork`, `bounty`). Entry `Act=legacy`, id
`hash(program, subject, source)`: idempotent per (subject, source), the first count
stands. Counts toward lifetime points, levels and rank; **never redeemable** (the fold
ignores the flag on a legacy row). One call is one `ledger.backfilled` trail record
naming who ran it, however many rows (max 5000 per call); it makes no claims. The
beta-board scoring cloud applies: +1 per 100K tokens, +1 per referred signup, +20 per
referred org with spend, +1 per star, +1 per follow, +1 per fork, bounty points.

## Profile, explore, badge

`profile` is private until its owner turns it on (`PUT /v1/reward/me/profile`). The
public view (`GET /v1/reward/profile/:handle`) is level, level name, lifetime points,
badges `{id, name, pillar, rarity, earnedAt}`, claim counts per pillar, links (https
only). Never an email, org, subject id or redeemable balance; a private profile and an
unknown handle are the same 404. Handles are unique by the conditional insert of an
`h:` row. `GET /v1/reward/me/explore` is the active rules the caller has no live claim
under, by pillar in order. `GET /v1/reward/badge/:handle.svg` (`?theme=dark|light&style=flat|card`)
is `no-cache, max-age=0` with an ETag and 304s; all text is escaped; goldens in
`emblem/testdata` (`go test ./emblem -update` rewrites them).

## Routes

Public/self under `/v1/reward`, admin under `/v1/admin/reward` (cloud's convention:
`/v1/admin/<capability>`):

```
GET  /v1/reward/health
GET  /v1/reward/catalog                       public
POST /v1/reward/events                        platform: ingest
GET  /v1/reward/me                            caller (balances, level, badges, network)
GET  /v1/reward/me/claims                     caller
GET  /v1/reward/me/explore                    caller
GET|PUT /v1/reward/me/profile                 caller
GET  /v1/reward/profile/:handle               public once turned on
GET  /v1/reward/badge/:handle.svg             public once turned on
GET|POST /v1/admin/reward/programs            SuperAdmin
GET|PUT|DELETE /v1/admin/reward/programs/:program
PUT|DELETE /v1/admin/reward/programs/:program/rules/:rule
POST /v1/admin/reward/programs/:program/backfill
GET  /v1/admin/reward/claims                  the queue (?status= ?program= ?subject= ?limit= ?offset=)
POST /v1/admin/reward/claims/:claim/approve|reject|reverse   (reject, reverse need {"note"})
POST /v1/admin/reward/simulate                a dry run
```

Delete is refused (400) once any claim or ledger row names the program or rule; retire
with `active=false`.

## Auth

IAM owns it; this module checks no token and holds no secret. It reads the identity
headers the edge writes (`authz.Header*`, stripped from clients at the edge):
`X-User-IsAdmin: true` (exactly) with a subject is the platform principal (the edge's
SuperAdmin verdict, owner `admin`; an org admin or an admin-org application is not
one); `X-User-Id` is the subject. The gate runs inside every handler. **There is no
service token.** The brief said the ingest uses commerce's service token, but
commerce deleted `COMMERCE_SERVICE_TOKEN` ("commerce doing IAM's job with a secret of
its own"); cloud's in-process transport states the platform principal itself, and
ingest and the admin routes accept that one principal. Run it behind the edge: with
none, the headers are forgeable.

## Credit

`credit.Client{Base, Doer, Max}` posts `{org, amountCents, reason, tag, idempotencyKey}`
to `POST {Base}/v1/billing/credit` and treats 200 and 201 as granted. `Doer` is the
host's transport (in cloud, the in-process one that carries the platform principal).
The event must name the `org` to credit; a credit rule with no org on the event is
skipped. Rules above `Ceiling()` are refused when saved.

## Not built

Redemption (points for credit) needs the owner's conversion terms and the $20/member
cap; `Entry` is shaped for it (lifetime never falls by redeeming), but nothing writes
it. No revshare or payout. No debit of granted credit.

## Test and release

`go vet ./... && go test -count=1 ./...` (a SQLite file per test, no service). Release
is a `v*` tag; the module ships from the tag and builds no image.
