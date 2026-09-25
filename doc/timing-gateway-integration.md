# Timing-play gateway: in-process integration

This is the narrow, in-process integration between this appliance's
standing `mosService` (constructed once in `main.go`) and the separately
maintained timing-control HTTP API (`POST /api/timing/play`) from
`automatrix.local/mosgateway`. It supersedes the earlier design-only draft
(automatrix-mos-gateway MR !2, prose analysis with no diff) with the actual
`main.go` wiring, adapters, and a real subprocess integration test.

Gated by `Gateway.Enabled` (config/env `GATEWAY_ENABLED`), off by default.
Disabled, this process behaves exactly as it did before this change --
proven by `TestGatewayIntegrationEntrypoint/disabled_configuration_preserves_existing_startup_behavior`
in `gateway_integration_test.go`.

## What this is not

- Not a second MOS session or a second registered device. The gateway
  reuses the exact `mosService` instance `main.go` already constructs, and
  the exact `cfg.MOS.ID` / `cfg.WSClient.PeerURL` this process already
  uses.
- Not a change to `TCPServer`/`WSServer`/`WSClient`/MOS message handling.
  The only OpenMOS-side change is configuration, a small adapter package
  (`internal/gatewayintegration`), and `main.go`'s construction/shutdown
  wiring.
- Not a fork of `automatrix.local/mosgateway`'s logic. `pkg/timingplay` and
  `pkg/httpapi` stay in that repository; only the small glue-adapter types
  (verbatim, from `automatrix-mos-gateway`'s own `cmd/gateway/main.go`,
  commit `1fa51ad`) live here, in `internal/gatewayintegration`.

## Outbound connection shape (traced, not inferred)

`internal/timingsend.Client.Send` opens a fresh, one-shot, non-passive
WebSocket connection **per call** -- a genuinely new TCP socket and MOS
handshake every time `POST /api/timing/play` dispatches, not a reused or
shared connection. This is a distinct third connection shape alongside the
appliance's other two: the standing passive `WSClient.requestConn`, and the
existing one-shot `StoryActionClient`. It is not a novel risk: it is the
same shape as the already-shipped `WSClient.RequestLane` feature (a
second, non-passive connection alongside the passive one, carrying the
device's own requests) and the already-reviewed `StoryActionClient`
one-shot lifecycle, both already exercised by this repository's own tests
(`internal/server/client_lanes_test.go`). Message IDs on the one-shot
connection are timestamp-derived, not drawn from `WSClient`'s persistent
`messageid.Sequence` -- a deliberate, already-established choice mirrored
from `StoryActionClient`, not an oversight introduced here.

No second MOS *session* is created: `cfg.MOS.ID`/`cfg.MOS.NCSID` (the
identity pair a MOS session is keyed on) are read from the exact same
`*config.Config` this process already loaded once at startup, so every
connection -- passive, `StoryActionClient`'s, and `timingsend`'s -- presents
the identical `mosID`/`ncsID` pair. "Reuses the appliance's identity" is
the accurate claim; "reuses the appliance's connection" is not, and this
document previously risked being read that way.

## Findings from independent review, fixed in this revision

Three concrete defects were found by a second pass over the adapters and
`main.go`'s shutdown sequence, distinct from the earlier subprocess
integration test's own findings. All three are fixed in this revision, each
with a regression test that fails against the pre-fix code (verified by
reverting each fix locally and re-running its test before restoring it):

1. **`IsLocalBind` accepted a wildcard bind as "local."** The original
   implementation hand-rolled a `strings.LastIndex(addr, ":")` host split
   and treated an EMPTY host -- `":8091"`, Go's own convention for
   "listen on every interface" -- as one of its accepted loopback cases.
   `GATEWAY_BIND_ADDR=":8091"` (or the numeric wildcards `0.0.0.0:8091`,
   `[::]:8091`) would have passed the guard this milestone requires to
   keep the gateway's HTTP listener local-only, and the appliance would
   have bound a private timing-control API -- protected only by bearer
   tokens, with no rate limiting or TLS -- to every network interface.
   Fixed using `net.SplitHostPort` + `net.ParseIP(...).IsLoopback()`
   instead of hand-rolled string parsing: this also fixes a latent
   false-negative (a bracketed IPv6 loopback literal, `[::1]:8091`, or a
   non-`.1` address in `127.0.0.0/8`, would have been wrongly rejected by
   the original code). Regression: `TestIsLocalBind_WildcardAddressesAreRejected`
   in `src/internal/gatewayintegration/adapters_test.go`.

2. **A malformed auth-token entry's error message echoed the token
   itself.** `ParseAuthTokens`'s error path was
   `fmt.Errorf("malformed caller:token pair %q", pair)`, where `pair` is
   the raw `"caller:token"` string -- including a syntactically-odd but
   still-secret token value. That error reaches
   `log.Fatalf("Gateway.AuthTokens: %v", err)` in `main.go`, i.e. it would
   have written a real secret straight into process logs on a config
   typo. Fixed to report only the 1-based position of the bad entry and
   which part was missing (separator, caller name, or token) -- never the
   raw entry text. Regression:
   `TestParseAuthTokens_MalformedEntryErrorNeverContainsTheToken`, which
   uses a synthetic canary token (`sk-live-CANARY-do-not-leak-9f3a7b21c4`)
   and asserts it appears in no returned error across every malformed-input
   shape the parser rejects -- verified to fail (with the canary visible in
   the failure output) against the original error message before the fix
   was restored.

3. **Shutdown's grace period was shorter than the service's own dispatch
   timeout, and the store was closed unconditionally regardless of
   whether shutdown actually finished waiting.** The original code used a
   fixed 5-second `context.WithTimeout` for `gatewayHTTPServer.Shutdown`,
   while `timingplay.NewService` was constructed with a 30-second dispatch
   timeout -- so a request whose outbound send legitimately took longer
   than 5 seconds (well within its own allowed 30) would still be
   in-flight when `Shutdown` gave up and returned. `gwStore.Close()` was
   registered via an unconditional `defer` set up when the store was
   opened, which runs at `main()`'s exit regardless of `Shutdown`'s
   outcome -- so the journal file would be closed while that handler
   goroutine was still running, and its eventual
   `TransitionSent`/`TransitionUncertain` call would write to an
   already-closed `*os.File`, silently losing the durable outcome record
   this store exists to guarantee. Fixed by: (a) naming the shutdown grace
   from the same dispatch-timeout constant plus margin
   (`gatewayShutdownGrace = gatewayDispatchTimeout + 10s`), so the two can
   no longer drift apart, and (b) removing the premature `defer`, closing
   `gwStore` explicitly, only after `gatewayHTTPServer.Shutdown` has
   returned. Regression:
   `TestGatewayIntegrationEntrypoint/a_request_outlasting_the_old_short_shutdown_grace_still_completes_and_its_outcome_is_durably_recorded`
   in `src/gateway_integration_test.go` -- holds a real outbound send open
   for 8 seconds (past the old 5s grace, within the fixed ~40s one),
   signals shutdown while it is in flight, and then reads the on-disk
   journal *after the process has fully exited* to prove the request's
   `"sent"` transition was actually, durably recorded -- which is only
   possible if the store was still open when that write happened.
   Verified to fail (a severed connection, `EOF`) against the original 5s
   grace before the fix was restored.

## Identity mapping

A caller's `storyId` in the HTTP request must be this appliance's internal
composite persistence key, **not** the bare MOS wire story ID:

```
storyId = url.PathEscape(roID) + "/" + url.PathEscape(storyID)
```

(see `internal/service/mos.go`'s `storyPersistenceID`, and
`timingplay.Resolver.Resolve`'s story-matching loop, which compares against
exactly this composite form.) `rundownId` is the running order's raw MOS
`roID` directly -- `model.RunningOrder.ID` is set verbatim from the
`roCreate`'s `roID` on ingestion, with no separate synthetic key.

## Rollback

`Gateway.Enabled: false` (or omit the `Gateway` config block) and restart.
The integration is entirely inside one `if cfg.Gateway.Enabled` branch in
`main.go` that touches no existing transport construction or `mosService`
setup, and the shutdown wiring is symmetric: `gatewayHTTPServer` is only
non-nil, and only then torn down, when it was started.

`http.Server.Shutdown` is used for the gateway's own listener, which blocks
until an in-flight handler returns -- a request already inside
`POST /api/timing/play` completes and its response reaches the caller
normally; nothing here forcibly severs it or re-sends it. A request
arriving after shutdown begins is refused with a connection error, not
silently dropped. Proven by
`TestGatewayIntegrationEntrypoint/shutdown_waits_for_an_in-flight_request_instead_of_severing_it`.

## What MR !1 (automatrix-mos-gateway) proves independently

The HTTP contract, the resolver's identity logic, and the outbound sender
against a real WebSocket peer (`fakeNCS` in `internal/timingsend`) -- all
already reviewed and tested in that repository, unchanged here.

## What this integration adds proof of, that MR !1 could not

- The adapters (`internal/gatewayintegration`) actually satisfy
  `timingplay.RunningOrderLookup` / `timingplay.Sender` against this
  appliance's real `*service.MOSService` and real "file" backend, not a
  fake.
- A real running order/story, seeded the same way ENPS would (a real
  `roCreate` over the real TCP transport), resolves correctly end-to-end
  through to a real outbound `roElementStat` reaching a real peer.
- Disabled configuration is behaviorally identical to the
  pre-integration binary.
- Graceful shutdown's documented in-flight behavior actually holds for
  this wiring, not just in the standard library's own tests.

## Still untested (deliberately, per the reserved live-MOS-send authorization)

Whether sending `roElementStat` PLAY to a **real ENPS** instance actually
moves its timing bar. Everything above tests this appliance's own
integration against a controlled fake peer; the fake peer proves the wire
shape and code paths, not ENPS's actual reaction to them.

## Reproducible build layout

This integration spans two separately versioned repositories. A
developer's ad hoc `go.work` (relative paths into whatever two directories
happen to be checked out) is not a deployment artifact -- rebuilding this
exact integration requires exactly these two pinned revisions:

| Repository | Remote | Revision used for this integration |
|---|---|---|
| OpenMOS (this repo) | `https://github.com/phattailed/OpenMOS.git` | `d2aa213a8492e4b3c363387a6cab04b7d94e725b` (`master`), plus this integration's uncommitted/branch changes on top |
| `automatrix-mos-gateway` | `https://git.ap.org/workflow-solutions/automatrix-mos-gateway.git` | `156a648c5df0ea12a30b83f5c44124720f6cb8d6` (branch `codex/inprocess-integration-support`) |

Module identities (from each repo's own `go.mod`):

- This repo: `module airshift/openmos` (`src/go.mod`, Go 1.24.1)
- Gateway: `module automatrix.local/mosgateway` (`gateway/go.mod`, Go 1.26.5)

To build this integration reproducibly from a clean checkout of both pinned
revisions above:

```sh
# 1. Clone both repositories at the pinned revisions.
git clone https://github.com/phattailed/OpenMOS.git openmos
git -C openmos checkout d2aa213a8492e4b3c363387a6cab04b7d94e725b

git clone https://git.ap.org/workflow-solutions/automatrix-mos-gateway.git mosgateway
git -C mosgateway checkout 156a648c5df0ea12a30b83f5c44124720f6cb8d6

# 2. internal/timingsend currently exists only inside mosgateway's vendored
#    snapshot (vendor/openmos/src/internal/timingsend), not upstream OpenMOS
#    -- copy it in byte-for-byte (no logic changes) so this repo's own
#    main.go can import it directly.
cp -R mosgateway/vendor/openmos/src/internal/timingsend openmos/src/internal/timingsend

# 3. Apply this integration's diff to openmos/src (config.go, main.go,
#    internal/gatewayintegration, gateway_integration_test.go) -- see the
#    accompanying diff for the exact changes.

# 4. A go.work at the OpenMOS checkout's root, naming both modules by their
#    real relative positions from this clone layout:
cat > openmos/go.work <<'EOF'
go 1.26.5

use (
	./src
	../mosgateway/gateway
)
EOF

# 5. Build and test.
cd openmos
go build ./src/...
go test ./src/... -timeout 180s
```

`go.work` itself is intentionally NOT committed to either repository (it
names a relative path to a sibling checkout, which is a local build
convenience, not a versioned dependency declaration). A future packaging
step -- outside this integration's scope -- would replace steps 4-5 with a
tagged `automatrix.local/mosgateway` release consumed via each module's own
`go.mod require` + `replace`, removing the need for a workspace file
entirely; that is a decision for whoever owns the release process for both
repositories, not made here.
