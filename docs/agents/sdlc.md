# OpenMOS delivery rules

## Scope and decisions

The Sponsor selects outcomes and priorities, directly in a task or through a specific GitHub
issue. Existing backlog order and handoff suggestions do not select work. GitHub Issues and
PRs in `phattailed/OpenMOS` hold public engineering decisions and delivery evidence. Private
operational facts stay in the originating task or gitignored `tmp/` evidence.

COMMS relays Sponsor intent, obtains status directly, and reports the strongest proved state.
ORCH coordinates selected work and reconciles evidence. A separately assigned implementation
owner changes product code. T0/T1 work may remain in one task; no extra tasks or ledger are
required. Direct Sponsor-to-ORCH instructions remain valid.

Bootstrap covers policy adoption and missing initial ORCH/COMMS roles only. Its brownfield
stocktake is read-only and selects no product work. Durable App workers, scheduled intake,
task archival and a forge bridge are not bound by this adapter.

## Existing grants and limits

For selected work, the Sponsor's standing Git grant covers branches, commits, pushes to change
branches, PR creation, merge after checks, and deletion of owned merged branches. Local builds,
tests, dependency operations and scratch files under `tmp/` are permitted. Make reversible
technical choices within that scope without renewed approval. Large dependency changes and
changes of scope still require a Sponsor decision.

Existing grants for the privately identified development reference NCS remain limited to that
exact host: back up and edit its MOS device configuration, restart the watchdog-managed MOS
process for reload, reset IIS when needed, and manage owned reverse tunnels. They do not cover
another estate. Confirm the current target and grant before use; redact all public evidence.
No such operation is part of bootstrap.

Destruction, data/schema migration, shared history rewrites, production/customer changes,
firewall/DNS/routing changes, and credential or key changes require explicit authorization.
Routine development/QA sign-in follows existing Sponsor grants; it permits neither disclosure
nor credential changes. Read-only, docs-only and do-not-send boundaries are literal. Public
disclosure review applies to every tracked file and every external message, including fixtures
and code comments; old disclosures are not permission to repeat them.

## Checks and acceptance

Use The Fold T0-T3 risk model directly; stricter local requirements win. Policy/documentation
changes require link and diff checks, disclosure review, and applicable policy validation.
Policy adoption additionally requires semantic review of every declared grant and the global
adapter audit against the exact live remote default branch. A structural pass alone is not
adoption.

For executable, test, build or configuration changes, and before claiming product behavior
works, run from `src/`:

```sh
gofmt -l .
go build ./...
go vet ./...
go test -count=1 ./...
go test -count=1 ./...
go test -race -count=1 ./...
```

Formatting must be clean except the pre-existing `internal/xml/element_action_test.go`.
Do not reformat it incidentally. Confirm each bug fix by deliberately reverting the fix,
observing the regression check fail for the intended reason, then restoring and checking the
candidate. Use synthetic or sanitized inputs. T2 work requires independent review or QA of a
fixed candidate; T3 keeps the human, environment and release gates separate. Live NCS claims
require actual NCS evidence in `doc/interop/README.md`; loopback and unit tests prove less.

## Git delivery and closeout

The authoritative forge is GitHub, repository `phattailed/OpenMOS`, default branch `master`.
At adoption, its active default-branch ruleset requires a PR, blocks deletion and force-pushes,
and requires zero approving reviews; it has no bypass actors. No checked-in CI workflow exists.
Recheck live protection and checks before merge. Record local validation with the PR candidate.
Do not bypass a newly applicable check or protection rule.

Avoid deep PR stacks. A parent may not be deleted while another PR depends on its branch.
At closeout, record a disposition for each created branch and worktree; delete an owned merged
branch only after verifying merge, no open dependent PR, no active owner, clean state, no
worktree dependency and no unique unmerged work. Keep unexplained or unrelated work intact.
Automatic deletion is not enabled at adoption: use explicit per-PR deletion for proved
disposable heads, retaining historical and unclassified branches. Repository-wide automatic
deletion can replace this exception after branch dependencies are reviewed. Historical cleanup
requires a separate manifest and deletion grant. App-managed worktree disposal and task
archival are not granted here.

## Release and external deployment gates

There are no Git tags or GitHub Releases at adoption. The configurable `app.version` and
`mos.swrev` defaults in `src/internal/config/config.go`, and the draft
`doc/releasenotes.md`, do not establish an accepted release. A code merge is not a deployment
or release.

Before release work, the Sponsor must identify the canonical version source, reserved version
record and abandonment writer, acceptance lane, tag/Release writer, artifact identity evidence
and user-visible version surface, following the installed
`docs/agents/product-release-versioning.md` in Codex home. These choices are unresolved;
bootstrap does not make them. Keep exact source SHA and executable digest with any later
candidate evidence.

The existing appliance does not have a checked-in authoritative deployment-target source or
shared preflight. An external deployment remains gated on that source (or an exact pointer to
an equivalent authoritative private source), expected provider identity and location, permitted
principal, current inventory, and a shared preflight/receipt. An instance ID, profile label or
old handoff is insufficient. Preserve exact scoped development-rig grants above; they do not
select an appliance deployment target. Bootstrap performs no deployment or network change.

## Coordination and policy precedence

Use native Codex task messaging to the verified current holder. A successful send means queued;
require explicit acknowledgement before reporting admission or activity. The sender retains
the pending request and next check. If Agent IRC already carries the instruction, use its exact
delivery ID in a body-free native wake and inspect the response; do not send a second work order.
Do not retire an unresolved delivery merely because a task is idle or old.

For ambiguous ownership, preserve the surface and use the global recovery procedure. Titles,
pins, IRC presence and local handoffs are projections or evidence, never appointments. Explicit
Sponsor instructions establish initial roles; `$handoff-control-plane` handles separately
authorized replacement. Bootstrap alone may present its accepted initial holders; it cannot
replace an occupied role. Standalone tasks retain their own title; the bootstrap coordinator
owns initial control-plane title/pin projection, then each accepted holder maintains its own.

The merged `AGENTS.md`, adapter and this file govern delivery. Existing coding guidance in
`doc/developer-reference.md` still applies where consistent; its old branch, transport and
capability descriptions and `doc/devtasks.md` checklist are historical references. Current
`README.md`, `doc/interop/README.md` and source evidence settle implementation facts. Ignored
`.kiro/steering/` and `tmp/` preserve private operating knowledge and scoped Sponsor grants,
without becoming alternate role or lifecycle policy. No tracked role pilot existed at the
adoption base `361d75ff5ef79bb7e395eff4260c90ab5f034d22`.
