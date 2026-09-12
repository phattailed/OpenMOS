# OpenMOS agent guidance

Agent architecture convention: The Fold v3.4

Orchestration: use the global `$orchestrate-repo` skill with `docs/agents/orchestration-adapter.md`.

Read [the delivery rules](docs/agents/sdlc.md) before changing files. Direct Sponsor
instructions take precedence. Use one implementation owner per exact mutable surface and
preserve unrelated work. An ORCH appointment is coordination, not an implementation assignment.

- This repository is public. Keep credentials, editorial content, person/site identifiers,
  vendor product names and private infrastructure details out of tracked files and public
  issues, PRs and comments. Sanitize fixtures and inspect the entire staged diff before
  every commit; run the private disclosure denylist when available.
- `origin` is `https://github.com/phattailed/OpenMOS.git`, and the default branch is `master`.
  Verify both. Specify `phattailed/OpenMOS` explicitly in forge commands: other remotes are
  not delivery targets. Never push directly to `master`; use a branch and PR.
- Read [the protocol source synthesis](doc/mos-protocol-source-synthesis.md) before changing
  protocol behavior. Preserve both transports over one message core, transport-owned envelope
  rules, original-ack replay, lenient inbound/strict outbound handling, and Profile 0-only
  advertisement. Carry external metadata without interpreting its payload.
- [The capability table](README.md) and [interop evidence](doc/interop/README.md) define what
  is proven. A passing local test is not live NCS proof. Trace wire fields through conversion,
  storage and emission; declarations alone do not establish support.
- Downstream application integration stays outside this MOS implementation. A new local
  interface requires a Sponsor decision on its shape before implementation.
- Local handoffs and ignored steering are operational references. Their stricter safety rules
  and exact Sponsor grants remain applicable; they cannot appoint roles, expand scope or
  replace the merged delivery policy. Revalidate stale technical claims against current code.
