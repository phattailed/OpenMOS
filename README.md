# OpenMOS Media Object Server

![MOS Project Official Logo](/res/mosproject-logo.jpg)

A minimal, honest MOS Protocol implementation providing both the classic MOS 2.x
raw TCP transport and the MOS 4.0 WebSocket transport over a shared message core.

> **Current status:** Profile 0 and Profile 2 running-order construction, verified
> against a live AP ENPS on both transports. Not a complete MOS implementation —
> see the table below for exactly what is and is not proven.

The MOS 4.0 specification expects the two generations to coexist rather than
replace one another:

> It is expected that an NCS will support MOS v4.0 **in addition to** earlier
> families of the Protocol, i.e.: MOS v2.x (socket) and MOS 3.x (WebService).
> — MOS 4.0 §1

Our reference NCS does exactly that, running MOS 2.x sockets, MOS 3.8.4
WebService and MOS 4.0 WebSocket concurrently on one host. OpenMOS therefore
implements the transports side by side. Only framing and envelope rules differ;
the message layer is shared.

## Implementation Status

"Live NCS proof" means exercised against a real AP ENPS, not a loopback fixture.
Full evidence, reproduction scripts and the remaining defect list are in
[`doc/interop/README.md`](doc/interop/README.md).

| Capability | Implemented | Automated proof | Live NCS proof | Remaining risk |
|---|---|---|---|---|
| MOS 2.x transport (raw TCP, UCS-2BE) | Yes | Integration tests | **Yes** | — |
| MOS 4.0 transport (WebSocket, UCS-2BE binary frames) | Yes | Loopback tests | **Yes** | TLS untested with real certs |
| Both transports concurrently, one shared service | Yes | — | **Yes** | — |
| MOS envelope, generation-aware `messageID` | Yes | Unit tests | **Yes** | — |
| `messageID` format: strict outbound, lenient inbound | Yes | Unit + integration tests | **Yes** | Echoed IDs reproduce the peer's value verbatim |
| Profile 0: `heartbeat` | Yes | Unit tests | **Yes** | — |
| Profile 0: `reqMachInfo`/`listMachInfo` | Yes | Unit tests | **Yes** | — |
| Profile 0: `keepAlive` (no response) | Yes | Unit tests | **Yes** | — |
| Profile 0: heartbeat timeout (bounded) | Yes | Server closes on timeout | No | — |
| `roCreate`: validate, persist, ack after persist | Yes | Integration tests | **Yes** | — |
| `roReplace`, `roStorySend`, `roDelete` | Both transports | Integration + loopback tests | **Yes** (2.x) | Shared handlers, so the two cannot diverge |
| `roMetadataReplace`, `roReadyToAir`, `roElementAction` | Both transports | Loopback tests | No | Routed through the same shared dispatcher |
| `roStorySend` refuses an unknown `roID` | Yes | Integration test | **Yes** | Reports rather than fabricating a running order |
| Pull recovery: unknown `roID` triggers `roReq` | Both transports | Integration + loopback tests | **Yes** | Fires on unknown story and item too, not just running order (`doc/interop` §42) |
| Recovery suppressed on a lane that cannot originate | Yes | Unit tests | **Yes** | A passive connection cannot carry a request, and NOM enters a permanent retry loop if one is sent (`doc/interop` §43) |
| Story items and stories share one identity scheme | Yes | Unit tests | **Yes** | The element-action family used raw wire IDs as storage keys, so MOVE/DELETE/SWAP silently did nothing (`doc/interop` §42) |
| Originate `roReqStoryAction` (Profile 7) | Yes | Spec-example tests | **Yes** | A live NCS applied a MOVE and pushed the change back; gated by the rundown's `AllowExternalMod` (`doc/interop` §41) |
| Inbound `roList` rebuilds local state | Both transports | Integration tests | **Yes** | Two running orders rebuilt from nothing, 23 stories (`doc/interop` §45) |
| Authentic captured fixtures (Profile 0 and 2) | Yes | Live-frame tests | **Yes** | Sanitized; raw captures never committed |
| Cross-vendor frames (4 other vendors) | Yes | Real-traffic tests | **Yes** | From ~90k logged messages, not synthesised |
| `listMachInfo` flat **and** container profiles | Yes | Real-traffic tests | **Yes** | Same NCS uses each on a different transport |
| `roReq` answered with `roList` for one running order | Yes | Integration tests | **Yes** | Was inverted with `roReqAll`; see `doc/interop` §17 |
| `roReqAll` answered with `roListAll` summaries | Yes | Integration tests | **Yes** | Discovery only, as the spec requires |
| Inbound `roListAll` drives `roReq` per running order | Yes | Unit tests, both transports | **Yes** | Sequential: one request outstanding at a time, per MOS 4.0 §4.1 |
| One `roReq` outstanding per lane, recovery included | Yes | Unit tests | **Yes** | Recovery enqueues ahead of discovery rather than sending concurrently (`doc/interop` §34) |
| Every parseable message classified as handled or not | Yes | Inventory test reads the parser | — | Adding a message type fails the build until classified |
| Frame splits, coalescing, misaligned terminators, junk | Yes | Unit tests, incl. odd-offset decoy | **Yes** | Bounded at 4 MiB; non-MOS roots refused, not discarded |
| `roElementStat` parses, routes and acks on both | Yes | Real-traffic + loopback tests | Partly | `element` attribute now preserved; not yet acted on |
| Retry deduplication, original ack replayed | Yes | Unit + integration tests | **Yes** | — |
| Dedup receipts persist across restart | Yes | Unit tests | No | Append log, compacted; no fsync per record, so a crash can lose the tail |
| Unfinished discovery work persists across restart | Yes | Unit tests | No | An interrupted walk resumes rather than leaving state divergent |
| `messageID` conflict detection | Yes | Unit tests | **Yes** | — |
| `messageID` counter persists across restart | Yes | Unit tests | No | Reserved in blocks, so a crash skips rather than repeats |
| Multiple envelopes in one TCP read | Yes | Integration test | **Yes** | — |
| `mosID` persisted on running orders and items | Yes | Integration test | **Yes** | — |
| `mosExternalMetadata` preserved verbatim | Yes | Unit + integration tests | **Yes** | Opaque payload kept as raw XML at RO, story and item level |
| `mosExternalMetadata` survives the round trip to the wire | Yes | Round-trip test | No | Emission was dead code; storage was faithful, the wire was not (`doc/interop` §32) |
| `mosScope` enforced per level on emission | Yes | Unit + round-trip tests | No | Hierarchy `OBJECT`⊂`STORY`⊂`PLAYLIST`; storage stays lenient |
| MongoDB backing | Yes | Not covered in CI | **Yes** | No MongoDB in CI; in-memory used for tests |
| Running orders persist across restart by default | Yes | Unit tests | **Yes** | File-backed snapshot wrapping the in-memory repositories (`doc/interop` §33) |
| MOS 4 channels `mom`, `ro`, `aux` | Yes | Unit + loopback tests | No | Object and search messages route correctly but are not implemented |
| MOS 4 outbound client, standard mode | Yes | Loopback tests | **Yes** | Profile 0 completed against a live NCS; framing verified against two NOM versions |
| OpenMOS runnable as a purely outbound client | Yes | — | **Yes** | Previously refused to start without a listener, contradicting passive mode |
| Peer refusals (`mosAck`) parsed and reported | Yes | Unit tests | **Yes** | Accepted without a `messageID`, as real servers send them |
| One message vocabulary across both transports | Yes | Envelope-reachability test | **Yes** | Sixteen messages were socket-unreachable; see `doc/interop` §28 |
| MOS 4 outbound client, passive mode | Yes | Loopback tests | **Yes** | NOM 9.6 delivered unsolicited `roCreate`, `roStorySend` and `roReadyToAir` with distinct production-style IDs (`doc/interop` §35, §38) |
| Two client lanes: passive delivery plus a request lane | Yes | Unit tests | **Yes** | Neither lane alone is a complete transport; recovery crosses from one to the other (`doc/interop` §44) |
| Pull on connect: `roReqAll` after Profile 0 | Yes | Unit tests | **Yes** | What real devices do; a device that only waits to be pushed to starts empty after every restart |
| Discovery walk completes unaided | Yes | Unit tests | **Yes** | `roReqAll`→`roListAll`→`roReq` per RO→`roList` applied, against a live NCS (`doc/interop` §45) |
| Item fields nested in `mosItem` | Yes | Live-frame tests | **Yes** | ENPS nests what the spec declares flat; one such item rejected a whole 13-story `roList` (`doc/interop` §45) |
| `roElementAction` applied, not just acknowledged | Yes | Live-frame tests | **Yes** | A MOVE originated by us came back and converged our stored order (`doc/interop` §42) |
| Pushed `roCreate` applied on the client path | Yes | Unit tests | **Yes** | Was unhandled, dropping the whole rundown; now shared (`doc/interop` §38) |
| Story items persisted from `roStorySend` | Yes | Live-frame tests | **Yes** | Items were dropped three ways; a rundown without them is headlines only (`doc/interop` §40) |
| `mosExternalMetadata` on items survives a resend | Yes | Live-frame tests | **Yes** | Set on create, not on update -- and update is the common path (`doc/interop` §40) |
| Inbound frames captured on the passive path | Yes | Unit tests | **Yes** | Capture ran only on the handshake reader, so passive recorded nothing (`doc/interop` §38) |
| Device-initiated passive topology on NOM | — | — | **Yes** | Device row `MOSVersion=4.0`, `Passive=0`; the device connects with `passive=true` (`doc/interop` §35) |
| Graceful shutdown, idempotent and race-free | Yes | Unit tests, incl. concurrent | **Yes** | Panicked on every run; `Shutdown` is called twice by design |
| MOS booleans as `YES`/`NO` | Yes | Unit + live-frame tests | **Yes** | — |
| MOS timestamps with comma fractions | Yes | Unit tests (spec examples) | **Yes** | `ParseMOSTime`; Go's stdlib cannot read them |
| Story and item order preserved | Yes | Unit tests, both backends | No | In-memory backend previously returned map order |
| Spec-clean `heartbeat` with no invented attributes | Yes | Unit + live-frame tests | **Yes** | — |
| MOS 4 authentication (HTTP Basic over TLS) | Yes | Unit tests | No | Live NCS cert is for an unrelated domain |
| Raw frame capture for fixtures | Yes | Unit tests | **Yes** | Off unless a directory is configured; `keepAlive` excluded so a standing appliance cannot exhaust the cap on noise (`doc/interop` §39) |
| MOS 3.x WebService transport | **No** | — | — | Blocked on WSDL (#15) |
| Profiles 1, 3, 4, 5, 6, 7 | **No** | — | — | Some message types parse, none exercised |
| Multi-instance HA | **No** | — | — | Single process per identity |

### What this is NOT

- Not a complete MOS implementation. Profile 0 and Profile 2 running-order
  construction only, on the `ro` channel.
- Not claiming Profiles 1, 3, 4, 5, 6 or 7. `listMachInfo` advertises Profile 0
  alone, which is deliberate.
- Able to initiate a MOS 4 connection to an NCS. Standard mode has completed
  Profile 0 against a real NCS; device-initiated passive mode has received
  NCS-originated running-order traffic from a live NOM 9.6.
- The MOS 2.x transport is listener-only: the NCS connects to us.
- Not MOS 3.x capable.
- Not suitable for production without durable storage, TLS, and authentication on
  the MOS 4 transport.
- Not safe to expose directly on a network on the MOS 2.x port: that protocol has
  no authentication, encryption, or integrity protection of any kind.

## Architecture

One message core behind two transports. Transports own framing and their own
protocol generation; they do not own message semantics.

- **MOS 2.x TCP server** — the NCS connects to `host:10541`, the MOS Upper Port.
  Per the spec, "MOS Upper Port (10541) is defined as the default TCP/IP port on
  which the MOS will accept connections from the NCS." Messages are UCS-2
  big-endian XML, framed by scanning for a complete `</mos>`.
- **MOS 4.0 WebSocket server** — the NCS connects to
  `ws://host:port/mos?mosID=X&ncsID=Y&channel=ro`. Messages are UCS-2BE in
  **binary** frames; text frames are accepted on receipt but never emitted, since
  the reference ENPS rejects text with `InvalidMessageType`.
- **MOS 4.0 WebSocket client** — dials a configured peer URL with `mosID`, `ncsID`
  and `channel`, adding `passive=true` for passive mode. Sends HTTP Basic
  credentials when configured, verifies TLS certificates by default, and
  reconnects with capped backoff. Disabled unless a peer URL is set.
- **Shared message core** — envelope handling, Profile 0, the running-order family,
  deduplication and persistence, used identically by both. The running-order handlers
  are transport-agnostic functions that take a responder, so a message type either
  works on both transports or on neither; that is asserted by test rather than left to
  discipline.
- **Repositories** — in-memory or MongoDB, selected by `storage.backend`.
- **Event bus** — internal pub-sub for running order change notifications.
- **Sentry** (optional) — error tracking when a DSN is configured.

Both servers operate in **standard mode**: the peer initiates the connection to
us. OpenMOS can also dial *out* on the MOS 4 transport — see the **MOS 4.0
WebSocket client** below — including *passive mode*, where a device behind a
firewall opens the connection itself with `passive=true` so the peer can reply
through the hole punched in the initiator's firewall. That is the main reason MOS
4.0 exists.

## Connection

MOS 2.x, raw TCP:

```
host:10541
```

MOS 4.0, WebSocket:

```
ws://host:8080/mos?mosID=<configured_mos_id>&ncsID=<your_ncs_id>&channel=ro
```

The path is configurable — MOS 4.0 §1 shows `/mos/Communication`, and our
reference ENPS publishes its own endpoint at `/MOS4NCS/`.

```
```

Query parameters:
- `mosID` — must match the server's configured MOS ID (rejected with 403 otherwise)
- `ncsID` — identifies the connecting NCS (required, rejected with 400 if empty)
- `channel` — one of `mom`, `ro` or `aux`, each standing in for the MOS 2.x port it
  replaces (`mom`=10540, `ro`=10541, `aux`=10542). Standard mode opens one
  connection per channel, so a peer may hold all three at once. A message arriving
  on the wrong channel is refused with a `NACK` naming the channel it belongs on.

The WebSocket port defaults to 8080 rather than 10541, because 10541 belongs to
the MOS 2.x transport. MOS 4.0 places its transport on standard web ports and
carries the old port distinction in the `channel` parameter instead, so set this
to 80 or 443 in production.

## Configuration

```yaml
app:
    name: OpenMOS
    version: 1.0.0
    environment: development
server:
    enabled: true          # MOS 2.x raw TCP transport
    host: 0.0.0.0
    port: 10541            # MOS Upper Port: the NCS connects here
    readtimeout: 5s
    writetimeout: 5s
    shutdowntimeout: 30s
websocket:
    enabled: true          # MOS 4.0 transport
    port: 8080             # use 80 or 443 in production
    path: /mos             # endpoint peers connect to; site-specific
    tlscertfile: ""
    tlskeyfile: ""
websocket_client:
    passive: false         # receive NCS-originated traffic; the NCS will not push down a standard lane
    requestlane: false     # with passive, ALSO open a standard lane to carry our requests
                           # ignored when passive is false, which already carries them
storage:
    backend: file          # "file" (durable, default), "memory" (tests), or "mongo"
capture:
    dir: ""                # set to record raw MOS frames; off when empty
state:
    dir: state             # messageID counter, dedup receipts, unfinished discovery
                           # work; empty disables persistence
mongo:
    uri: "mongodb://localhost:27017"
    database: openmos
    timeout: 10s
mos:
    id: OpenMOS_Server
    ncsid: ""              # empty accepts any NCS ID
    heartbeatinterval: 30s
    clienttimeout: 2m0s
    manufacturer: OpenMOS Project
    model: OpenMOS Server
    hwrev: "1.0"
    swrev: "1.0.0"
    dom: "2024-01-01"
    sn: OPENMOS-001
logging:
    level: info
sentry:
    dsn: ""
    environment: development
    debug: false
    attachstacktrace: true
    samplerate: 1
    tracessamplerate: 0.2
```

Environment variables override YAML (e.g., `WS_PORT`, `MOS_ID`, `MOS_NCS_ID`, `WS_TLS_CERT_FILE`, `WS_TLS_KEY_FILE`).

### Generate default configuration file:
```bash
./openmos --generate-config=config.yaml
```

## Running OpenMOS

```bash
# With default configuration search
./openmos

# With specific configuration file
./openmos --config=/path/to/config.yaml
```

## Building from Source

```bash
cd src
go build -o openmos
```

## Capturing frames for interop work

Setting `capture.dir` (or `CAPTURE_DIR`) records every MOS frame, in both
directions and on both transports, as a file plus a line in `manifest.jsonl`:

```bash
CAPTURE_DIR=./capture ./openmos --config=config.yaml
```

```
capture/
  0001-mos2-tcp-in.xml       the frame, verbatim, usable as a fixture
  0002-mos2-tcp-out.xml
  0003-mos4-ws-ro-in.xml
  manifest.jsonl             timestamp, transport, direction, peer,
                             wire bytes and encoding per frame
```

`wireBytes` is the size before decoding, so a UCS-2BE frame records roughly twice
its UTF-8 length — the encoding is evidenced rather than merely asserted.

This exists because every fixture in this repository was written by hand, and
hand-written frames are misleadingly tidy. A live NCS sends identifiers like
`NCS-HOST;P_NEWS\W;C45B2CF1-...`, not `RO-41`, and that difference has
already caught us out once.

> **Capture is off unless a directory is set, and should stay that way.** Frames
> contain message payloads, and `roStorySend` carries the full body of news
> stories. Treat the destination as holding editorial content. Capture is bounded
> at 2000 frames so an enabled run cannot fill a disk.

## Running Tests

```bash
cd src
go test ./...           # all tests
go test -race ./...     # with race detector
go vet ./...            # static analysis
```

## Requirements

- Go 1.24.1 or later
- No external dependencies required for tests (in-memory storage)
- MongoDB 4.4+ required for durable storage (`storage.backend: mongo`)
- Inbound network access on port 10541 for the MOS 2.x transport, and on the
  configured WebSocket port for MOS 4.0

## More Information

- MOS Protocol specification: https://www.mosprotocol.com
- MOS 4.0 documentation: https://mosprotocol.com/wp-content/MOS-Protocol-Documents/MOSProtocolVersion40/index.html

This project is not affiliated with MOS Group.

## Next Steps

Both transports have now been exercised against a live AP ENPS; the evidence and
the outstanding defect list are in [`doc/interop/README.md`](doc/interop/README.md).

The next interoperability steps, in order of value:

1. **Retest after the NOM 9.7.0.85 passive-socket handoff is repaired.** The corrected live
   test left 42 originated messages queued. Exact-build analysis found an unsubscribed
   listener event and no socket replacement for an existing output entry; a vendor repair
   has not been tested (`doc/interop` §§36–37). NOM 9.6's successful result remains valid (§35).
2. **Exercise the file-backed default in live interop.** The outbound `messageID`, deduplication
   receipts, unfinished discovery work and running orders now persist (`doc/interop` §§27, 33),
   but the live NCS proofs used intentionally disposable state.
3. **MOS 3.x WebService** (#15), lowest value and blocked on the WSDL.

## License

See LICENSE file for details.
