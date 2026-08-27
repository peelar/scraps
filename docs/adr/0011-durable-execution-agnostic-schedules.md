# ADR 0011: Durable execution-agnostic schedules

## Status

Accepted

## Context

Scraps needs an always-on notion of scheduled work, but it is not yet clear
whether Pi orchestration belongs in the Scraps CLI, scrapd, or a separate
software-factory harness. Encoding repositories, prompts, models, or pull
request behavior in a schedule would prematurely make Scraps that harness.

A clock also has different reliability requirements from an agent runner. A
scheduled firing must survive consumer disconnects, support retries after a
consumer dies, and remain auditable independently of whatever eventually
executes it.

## Decision

`scrapd` owns two execution-agnostic resources:

- A **schedule** contains a five-field cron expression, IANA timezone,
  enabled state, opaque JSON payload, and `queue` or `skip` concurrency policy.
- An **occurrence** is the durable record of one scheduled firing. Occurrences
  move through `pending`, `leased`, and `completed` or `failed`; `skip`
  schedules may also record `skipped` occurrences.

The daemon advances due schedules transactionally with occurrence creation.
Consumers claim the oldest available occurrence with a bounded lease and an
opaque lease token. An expired lease is claimable again and increments the
attempt count. Completion requires the current lease token so a stale consumer
cannot complete work after another consumer has reclaimed it.

Scraps does not interpret the payload or execute an occurrence. A future
software-factory service can claim occurrences, provision Scraps workspaces,
run an agent, and report completion.

Missed firings are retained rather than silently collapsed. Each scheduler tick
creates at most 100 occurrences so a large backlog cannot monopolize scrapd.

## Consequences

- Schedules remain useful regardless of the eventual agent harness.
- Agent credentials and workflow policy do not enter the schedule store.
- A consumer must exist before occurrences cause useful work.
- The API is sufficient for a separate harness; CLI schedule management can be
  added later without changing the persisted model.
- `queue` preserves every firing. `skip` records a skipped firing while any
  prior occurrence for that schedule remains pending or leased.
