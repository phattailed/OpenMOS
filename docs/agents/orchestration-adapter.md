# OpenMOS orchestration adapter

```yaml
adapter_version: 1
repository: phattailed/OpenMOS
forge_host: github.com
profile: github-native
scope:
  owned_decisions_and_surfaces:
    - Sponsor-selected engineering delivery through GitHub PRs
    - Initial control-plane bootstrap and read-only stocktake
  explicit_non_authority:
    - Unselected product work or downstream integration design
    - Production, customer, network, credential and deployment changes without their exact grants
    - Release selection, role replacement, task archival and scheduled work
  delegated_or_upstream_authority_pointers:
    - docs/agents/sdlc.md
    - doc/developer-reference.md
    - doc/devtasks.md
    - doc/interop/README.md
    - doc/mos-protocol-source-synthesis.md
    - doc/releasenotes.md
authority:
  intake_and_product_backlog:
    record: Sponsor task and GitHub Issues in phattailed/OpenMOS
    decision_holder: Sponsor
    permitted_writers_and_actions:
      - Sponsor selects and prioritizes outcomes
      - COMMS relays decisions without changing their scope
    orch_automatic_transitions: []
    required_candidate_and_evidence:
      - Exact Sponsor instruction or selected issue revision
    external_or_mechanical_gate: Sponsor selection; bootstrap creates no backlog
    allowed_projection: Sanitized issue links and task status
  engineering_selection:
    record: Sponsor-selected task or GitHub issue
    decision_holder: Sponsor
    permitted_writers_and_actions:
      - Sponsor admits a bounded outcome or finite ordered queue
    orch_automatic_transitions: []
    required_candidate_and_evidence:
      - Outcome, scope, fixed invariants and acceptance evidence
    external_or_mechanical_gate: Sponsor decision for unselected work or scope changes
    allowed_projection: Accepted task scope
  selected_engineering_delivery:
    record: Selected task and GitHub PR
    decision_holder: Sponsor-selected outcome
    permitted_writers_and_actions:
      - ORCH coordinates one implementation owner and checks the result
      - Assigned implementation owner changes only the selected surface
      - Bootstrap coordinator changes only adoption policy under the explicit invocation
    orch_automatic_transitions:
      - Advance selected work after its required evidence
      - Recheck ownership and gates before the next named item in a Sponsor-accepted finite queue
    required_candidate_and_evidence:
      - Exact change head and checks required by docs/agents/sdlc.md
    external_or_mechanical_gate: Required review and checks; unresolved target or permission stops its dependent action
    allowed_projection: Evidence-bound task status and PR link
  merge_release_and_closeout:
    record: GitHub PR and source commit
    decision_holder: Sponsor standing Git grant; separate release decision
    permitted_writers_and_actions:
      - Selected change owner may branch, commit, push a change branch, open and merge its PR after checks
      - Selected change owner may delete its proved owned merged branch under docs/agents/sdlc.md
      - ORCH records branch and worktree disposition
    orch_automatic_transitions:
      - Reconcile accepted merge evidence and report closeout
    required_candidate_and_evidence:
      - Exact PR head, passing checks and live branch-rule readback
      - Ownership, merge, dependent PR, clean state, worktree and unique-work checks before branch deletion
    external_or_mechanical_gate: PR required into master; release and deployment decisions in docs/agents/sdlc.md
    allowed_projection: Merge SHA and explicit branch or worktree disposition
  task_titles_and_control_plane_transfer:
    worker_title_writer: "none: task title projection is not used by this adapter"
    worker_pin_writer: "none: task pin projection is not used by this adapter"
    transfer_coordinator: Sponsor-designated bootstrap or succession coordinator
    control_plane_presentation_writer: writer:transfer-coordinator:task-presentation
  external_projection:
    record: "none: no forge bridge or external status mirror is bound"
    decision_holder: "none: no external projection decision is delegated"
    permitted_writers_and_actions:
      - "none: external messaging requires its own explicit instruction"
    orch_automatic_transitions: []
    required_candidate_and_evidence:
      - "none: no external projection is part of this workflow"
    external_or_mechanical_gate: Separate explicit authorization
    allowed_projection: "none: no external mirror is bound"
mapping:
  intake_to_delivery_identity: Sponsor task and turn ID or GitHub issue number, linked to PR number
  accepted_source_revision: Exact instruction or issue revision recorded with the accepted task scope
  later_source_edits: Reconcile changes with the Sponsor-selected outcome before proceeding
  product_state_writeback: "none: code merge does not authorize product or external state changes"
delivery:
  canonical_code_forge: GitHub at github.com/phattailed/OpenMOS
  change_review_surface: GitHub pull request
  ci_system: "none: no checked-in CI workflow at adoption; local checks are required"
  protected_default_branch: docs/agents/sdlc.md#git-delivery-and-closeout
  release_authority: Sponsor must designate a release writer and acceptance lane before release work
  required_checks_by_risk: docs/agents/sdlc.md#checks-and-acceptance
ownership_and_concurrency:
  lease_record: Task-local selected scope for T0/T1; exact mutable surface and owner recorded with the selected task and PR for escalated work
  mutation_owner_rule: one owner per exact mutable surface
  conflict_or_serialization_keys:
    - Exact file surface and change branch
    - Merge into master
    - Exact authorized live host and operation
  parent_join_rules:
    - Merge after candidate checks and applicable independent review
    - Preserve any branch with an active owner, dependent PR, dirty state or unique unmerged work
    - Use explicit per-PR deletion while repository-wide automatic deletion awaits branch-dependency review
    - Historical branch cleanup and App-managed worktree disposal are outside ordinary closeout
  browser_leases: "none: no shared-browser writer is bound; the inherited task forbids Chrome DevTools"
  live_data_leases: docs/agents/sdlc.md#existing-grants-and-limits
  cloud_environment_and_deployment_leases: docs/agents/sdlc.md#release-and-external-deployment-gates
handoff_and_routing:
  control_plane_succession: $handoff-control-plane
  coordination_settlement_writer: Sender resolves its own request after direct acknowledgement; ORCH reconciles unresolved evidence
  coordination_attention_policy: Report sent, queued, acknowledged or complete only with exact evidence; sender retains retry and next check
  routine_route: Native Codex message to the verified current holder; for an existing Agent IRC request send one body-free native wake pointer and inspect its acknowledgement
  stale_owner_recovery: Global orchestrate-repo recovery procedure under docs/agents/sdlc.md#coordination-and-policy-precedence
gates:
  default_risk_tier: T1
  repository_risk_model_and_global_mapping:
    policy_pointer: docs/agents/sdlc.md#checks-and-acceptance
    tier_map:
      T0: T0
      T1: T1
      T2: T2
      T3: T3
    stricter_gate_wins: true
  mandatory_external_or_mechanical_gates:
    - PR into master and current branch rules
    - Public disclosure check before each commit or external message
    - Independent review or QA for T2; exact human and environment gates for T3
    - No product selection, deployment or runtime mutation during bootstrap
  credential_and_secret_handoff_gate: docs/agents/sdlc.md#existing-grants-and-limits
  approved_tools_and_skill_sources:
    - Installed global orchestrate-repo and fold-bootstrap skills
    - Native Codex task tools and Agent IRC for bounded coordination
    - Repository Go toolchain and GitHub CLI with explicit canonical repository
```
