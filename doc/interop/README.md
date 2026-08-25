# MOS Interoperability Evidence — live AP ENPS

**Date:** 2026-08-24
**Target NCS:** AP ENPS with the `NOM` MOS gateway, referred to below as `NCS-HOST`
**OpenMOS under test:** MOS 2.8.4 TCP receive tracer (pre-merge working tree)
**Status:** First live NCS contact for this project. All prior testing was lab-only.

This is a historical record of the first exercise of OpenMOS against a real
Newsroom Computer System. It documents the test rig, what was verified, and the
defects the exercise exposed.

> **Site-specific values are redacted.** This repository is public, so real
> hostnames, SSH aliases, install paths and the host's HTTP service inventory are
> replaced with placeholders. Substitute your own:
>
> | Placeholder | Meaning |
> |---|---|
> | `NCS-HOST` | hostname of the ENPS/NOM machine |
> | `$NCS_SSH_HOST` | SSH alias for that machine |
> | `$NCS_SSH_JUMP` | SSH jump host, if one is needed |
> | `<NOM-DIR>` | NOM installation directory |
> | `<mos-id>` | MOS ID registered for OpenMOS on the NCS |
> | `<ncs-id>` | NCS ID the NCS presents |

---

## 1. Why this exists

Before this exercise, OpenMOS had never spoken to a real NCS. Both the MOS 2.x
and MOS 4 code paths were "lab-compatible" — they passed their own tests against
their own fixtures. Replacing fixtures with a live ENPS overturned several
conclusions that had been drawn from reading the code alone.

Two assumptions proved wrong and are corrected here:

- `Nom384Out.xml` in the NOM directory was read as evidence that ENPS speaks MOS
  2.8.4 on its sockets. It is actually a MOS **3.8.4 WebService** SOAP client
  proxy (`Nom384Out.MOSWebService384.MOSWebService`, with `heartbeatCompleted` /
  `reqMachInfoCompleted` async members). It says nothing about the socket version.
- ENPS was reported as having no MOS 4 WebSocket endpoint. It has one. The initial
  search only covered ports `1054x`; the MOS 4 endpoint is on 80/443 under its own
  path, exactly as the MOS 4.0 spec prescribes.

---

## 2. Test rig

OpenMOS ran on a developer workstation. The NCS could not route inbound to that
workstation, so an SSH reverse tunnel carried the MOS traffic. MOS never
traverses the jump host as MOS — SSH only relays the TCP connection.

```
NCS-HOST (ENPS)                            Developer workstation
┌────────────────────────┐                 ┌─────────────────────────┐
│ MOS client probe       │                 │ OpenMOS                 │
│   -> 127.0.0.1:20541 ──┼── ssh -R ──────►│ 127.0.0.1:10541 (TCP)   │
│                        │                 │   -> MongoDB            │
│ NOM  :10540 :10541     │                 └─────────────────────────┘
│ MOS 3.x WebService     │◄── probes run on the NCS host itself
│ MOS 4 WebSocket :80    │
└────────────────────────┘
```

Remote port `20541` is used because NOM already owns `10540`/`10541`. Both ends
of the tunnel bind loopback only.

> **Security note.** MOS 2.x over TCP has no authentication, encryption, or
> integrity protection of any kind. The listener is therefore bound to
> `127.0.0.1` and is reachable solely through the authenticated SSH tunnel. It
> must never be bound to a routable interface. MOS 4.0 addresses this with
> WSS + HTTP Basic auth; see §7.

### Reproducing

```bash
export NCS_SSH_HOST=<your-ncs-ssh-alias>
export NCS_SSH_JUMP=<your-jump-host>      # optional

bash doc/interop/start-openmos.sh    # build + run OpenMOS on 127.0.0.1:10541
bash doc/interop/open-tunnel.sh      # ssh -R 20541 -> 10541

# MOS 2.x conformance exercise (15 cases), executed on the NCS host:
ssh -J "$NCS_SSH_JUMP" "$NCS_SSH_HOST" "powershell -NoProfile -Command -" \
  < doc/interop/exercise-mos2.ps1

# NCS-side MOS 3.x / MOS 4 exercise:
ssh -J "$NCS_SSH_JUMP" "$NCS_SSH_HOST" "powershell -NoProfile -Command -" \
  < doc/interop/exercise-enps.ps1
```

Teardown: `bash doc/interop/close-rig.sh`.

---

## 3. What the reference ENPS actually supports

All three MOS generations are live on the same host, so each transport has a real
conformance partner available.

| Generation | Endpoint | Verified by |
|---|---|---|
| MOS 2.x socket | TCP `10540` (mom), `10541` (ro) — owned by `NOM` | `netstat` / `Get-NetTCPConnection` → the `nom.exe` PID |
| MOS 3.8.4 WebService | HTTP on a dedicated port, path `/MOS384/` | registered in `http.sys`; HTTP 500 to a bare GET (SOAP expects POST) |
| MOS 4.0 WebSocket | `ws://NCS-HOST/<mos4-path>/` on 80, `wss://` on 443 | **HTTP 101 upgrade accepted**, and a real MOS reply on a binary frame |

`nom.ini` on this host has `[MOS] Version=2.6` — the socket path is configured at
MOS 2.6, not 2.8.x. Note that MOS 2.6 has no `messageID` in its envelope either.

The MOS 4 path is the **NCS-side** endpoint: it is where a MOS device connects
*to* ENPS. Reaching it requires OpenMOS to act as a WebSocket **client**.

---

## 4. MOS 2.x exercise — 15 cases

Driven from the NCS host through the tunnel into OpenMOS. UCS-2 big-endian on the
wire per MOS 2.8 §"Encoding".

| # | Case | Result | Verdict |
|---|---|---|---|
| T01 | Profile 0 `heartbeat` | connection closed, 0 bytes | **DEFECT** |
| T02 | Profile 0 `reqMachInfo` | connection closed, 0 bytes | **DEFECT** |
| T03 | Profile 0 `keepAlive`, no `messageID` | connection closed, 0 bytes | **DEFECT** |
| T04 | `roCreate` with `messageID` | `roAck` `roStatus OK` | PASS |
| T05 | `roReplace` | `roAck` `roStatus OK` | PASS |
| T06 | `roStorySend` | `roAck` `roStatus OK` | PASS |
| T07 | `roDelete` | `roAck` `roStatus OK`; RO removed from MongoDB | PASS |
| T08 | `roCreate` **without** `messageID` | connection closed, 0 bytes | **DEFECT** |
| T09 | `messageID` = `0` | connection closed | PASS (spec requires ≥ 1) |
| T10 | `messageID` = `0x12D` (hex) | `roAck` `roStatus OK` | PASS (spec allows hex) |
| T11 | wrong `mosID` | connection closed | PASS (correctly refused) |
| T12 | duplicate `messageID`, identical content | **ACKed twice**, RO reached `version=2` | **DEFECT** |
| T13 | duplicate `messageID`, different content | **both ACKed**, two ROs persisted | **DEFECT** |
| T14 | two `ro` messages sequentially on one socket | both ACKed | PASS |
| T15 | two frames in a single write (1688 bytes) | both ACKed, correctly demultiplexed | PASS |

Server-side log lines for the failures:

```
[ERROR] parse error: unknown message type                <- T01/T02/T03
[ERROR] handle_message error: invalid MOS envelope identity   <- T08
```

MongoDB state after the run, showing T12/T13:

```
runningOrders count: 7
  _id=EX-DUP         slug=Exercise RO  version=2     <- T12: duplicate was RE-APPLIED
  _id=EX-CONFLICT-A  slug=Exercise RO  version=1     <- T13: both accepted under
  _id=EX-CONFLICT-B  slug=Exercise RO  version=1        the same messageID 302
  _id=EX-HEX, EX-PIPE-1, EX-PIPE-2, and one earlier tracer RO
stories: 7  items: 7
```

**What works well.** The UCS-2BE framer is solid: T15 put two complete `<mos>`
envelopes in one 1688-byte write and both were demultiplexed and acknowledged in
order. Socket reuse (T14) matches the spec's "leave the socket open"
recommendation. All four claimed Profile 2 messages round-trip with a correlated
`messageID` and persist a correct RO/story/item hierarchy — `roEdDur 00:01:00` is
parsed to `duration: 60`.

---

## 5. NCS-side MOS 4 exercise

Run against the NCS's own MOS 4 WebSocket endpoint from the NCS host.

| # | Case | Result |
|---|---|---|
| M01 | `reqMachInfo`, **text** frame (UTF-8), `channel=ro` | 101 accepted, then server closed: `InvalidMessageType` |
| M02 | `reqMachInfo`, **binary** frame (UCS-2BE), `channel=ro` | **468-byte binary MOS reply** (below) |
| M03 | `heartbeat`, text frame | 101 accepted, then closed: `InvalidMessageType` |
| M04 | `reqMachInfo`, text, `channel=mom` | 101 accepted, then closed: `InvalidMessageType` |
| M05 | `reqMachInfo`, text, `channel=aux` | 101 accepted, then closed: `InvalidMessageType` |

The M02 reply, decoded from UCS-2BE — the first real MOS message this project has
received from an NCS:

```xml
<mos>
  <mosID><mos-id></mosID>
  <ncsID><ncs-id></ncsID>
  <mosAck>
    <objID></objID>
    <objRev></objRev>
    <status>NACK</status>
    <statusDescription>MOS ID is not recognized by this NOM</statusDescription>
  </mosAck>
</mos>
```

Three conclusions:

1. **This ENPS requires UCS-2BE binary WebSocket frames and rejects text frames.**
   The close reason is worded confusingly ("Cannot accept binary frame" is emitted
   when a *text* frame arrives), but the behaviour is consistent across
   M01/M03/M04/M05.
2. **The endpoint is functional at the message layer**, not merely at handshake.
   The NACK is a correct, well-formed application response.
3. The only remaining obstacle to a working MOS 4 exchange is device
   registration — our MOS ID is not yet known to this NOM. That is an NCS-side
   configuration change, not a code fix.

All three channels (`ro`, `mom`, `aux`) accept the handshake, and the handshake
also succeeds with no query parameters at all, so this NOM validates identity at
the message layer rather than at connect time.

Incidental observation: our request carried `<messageID>9001</messageID>` and the
`mosAck` came back with **no** `messageID`, despite MOS 4.0 §4.1.6 saying
responses carry the request's ID. Receivers must not require it.

---

## 6. MOS 3.8.4 status

Endpoint registered and live under path `/MOS384/`. All probed paths return HTTP
500 to a GET, consistent with a SOAP endpoint requiring a POST with a
`SOAPAction` header. No WSDL was exposed at the paths tried (`/`,
`MOSWebService.asmx?wsdl`, `mos.asmx?wsdl`, `MOSWebService.svc?wsdl`).

**Not exercised.** Proceeding needs either the NOM WSDL or AP's MOS 3.8.4
WebService documentation. MOS 3.x is the lowest-value target: MOS 4.0 supersedes
it and this ENPS supports both.

---

## 7. Defects and gaps identified

| ID | Area | Finding | Evidence |
|---|---|---|---|
| D1 | TCP / Profile 0 | Profile 0 is entirely unreachable. `xml.Envelope` carries only `roAck`, `roCreate`, `roReplace`, `roDelete`, `roStorySend`, so `heartbeat`/`reqMachInfo`/`listMachInfo`/`keepAlive` cannot be parsed. The `case xml.Heartbeat` / `case xml.ReqMachInfo` branches in `client.go` are dead code. Profile 0 is mandatory for any MOS compliance claim. | T01–T03 |
| D2 | Envelope | `messageID` is required unconditionally (`client.go:185`). The MOS 2.8.4 DTD envelope is `mos (mosID, ncsID, payload)` with no `messageID`, so spec-legal 2.8.x traffic is refused. MOS 4.0 also explicitly exempts `keepAlive`. | T08, T03 |
| D3 | TCP / idempotency | No deduplication on the TCP path. A retried `messageID` re-applies the operation, and a reused `messageID` with different content is accepted. PR #6 built `dedup.go` for the WebSocket path only. | T12, T13 + MongoDB `version=2` |
| D4 | MOS 4 framing | PR #6 requires and sends `websocket.MessageText` (`wsserver.go:266,329,370,387`). This ENPS requires binary UCS-2BE. **Mutually incompatible on frame type alone.** The UCS-2BE codec needed already exists in `xml/wire.go` on the TCP path. | M01–M05 vs M02 |
| D5 | MOS 4 direction | PR #6 provides only a WebSocket *server*. The NCS's MOS 4 endpoint requires OpenMOS to be a *client*. MOS 4 standard mode has both sides connecting. | §3 |
| D6 | MOS 4 addressing | `WS_PORT` defaults to `10541`, double-encoding channel semantics. MOS 4.0 puts transport on 80/443 and carries the legacy port distinction in `channel` (`mom`=10540, `ro`=10541, `aux`=10542). | spec §1 vs `config.go:146` |
| D7 | MOS 4 channels | Only `channel=ro` is accepted; anything else is HTTP 400. This makes Profile 1 (`mom`) and Profile 3 search (`aux`) structurally impossible. This ENPS accepts all three. | `wsserver.go:163`, M04/M05 |
| D8 | Persistence | `runningOrders.mosID` persists as `""` although the envelope carried a MOS ID. | MongoDB dump |
| D9 | MOS 4 passive mode | `wsserver.go:22` claims "operating in passive mode", but it is a server accepting inbound connections — that is *standard* mode. Passive mode requires an outbound client sending `passive=true`, and is the primary reason MOS 4.0 exists. Not implemented. | spec §1, code comment |
| D10 | MOS 4 auth | No `Authorization` handling. MOS 4.0 strongly recommends HTTPS with HTTP Basic. | spec §1 "Authentication" |

---

## 8. Bearing on architecture

The MOS 4.0 specification expects coexistence, not replacement:

> It is expected that an NCS will support MOS v4.0 **in addition to** earlier
> families of the Protocol, i.e.: MOS v2.x (socket) and MOS 3.x (WebService).
> — MOS 4.0 §1

The reference ENPS is exactly that: all three generations on one host. PR #6
deleted the MOS 2.x transport (~1,560 lines across 11 files) on the grounds that
it was "incompatible with MOS 4 WebSocket transport". The message layer is in
fact common to all three generations; only framing and envelope rules differ.

| | Framing | `messageID` | Channel selection |
|---|---|---|---|
| MOS 2.8.x | UCS-2BE over raw TCP | absent from DTD | port: 10540 / 10541 / 10542 |
| MOS 3.8.x | SOAP over HTTP | mandatory | one operation per message |
| MOS 4.0 | UCS-2BE in **binary** WebSocket frames | mandatory, except `keepAlive` | `channel=mom\|ro\|aux` |

D4 makes the shared-core argument concrete: MOS 4 needs precisely the UCS-2BE
codec that already exists in `xml/wire.go` for the TCP path. Deleting the TCP
transport removed code the MOS 4 transport requires.

---

## 9. Scope boundaries observed

- Read-only on the NCS throughout. Only Profile 0 query messages (`heartbeat`,
  `reqMachInfo`) were sent to it; both are pure confidence/discovery messages that
  change no NCS state. All running-order writes went to OpenMOS, never to the NCS.
- No NCS configuration was modified. Registering a MOS ID for OpenMOS is required
  to progress past the M02 `NACK` and is approval-gated.
- No credentials, tokens, certificates or customer data are recorded here. A
  certificate file was observed to exist in the NOM directory and was not read.
- No changes to VPN, exit nodes, routes, firewalls, or SSH configuration.
- The rig was torn down after the exercise and the tunnel port confirmed closed
  from the NCS side.

---

## 10. Verification of fixes

The exercise was re-run against the same NCS after each fix landed. This section
supersedes the "before" state recorded above; §4 and §5 are kept as the original
findings.

### MOS 2.x transport

| Case | Original | After fix | Fixed by |
|---|---|---|---|
| T01 `heartbeat` | closed, 0 bytes | `heartbeat` reply, enveloped, carries `<time>` | Profile 0 (#9) |
| T02 `reqMachInfo` | closed, 0 bytes | `listMachInfo` reply, enveloped | Profile 0 (#9) |
| T03 `keepAlive` | closed, 0 bytes | **no reply**, connection stays open | Profile 0 (#9) |
| T08 `roCreate` without `messageID` | closed, 0 bytes | `roAck` `roStatus OK`, reply omits `messageID` | envelope generation (#8) |

T04–T07 and T10 continue to return `roAck`. T09 (`messageID` 0) and T11 (wrong
`mosID`) continue to be refused, which is correct. T12 and T13 still show
duplicate `messageID`s being re-applied; deduplication on the TCP path is #13 and
remains open.

T03 deserves a note: "no reply" here means the socket stayed open and the read
timed out, which is the correct outcome. MOS 4.0 §4.1.1: "the keepAlive messages
are simply discarded. No reply (ACK, NACK, etc.) is necessary."

### MOS 4.0 transport

§5 concluded that OpenMOS and this ENPS could not exchange a single MOS 4 message
because of the frame-type mismatch. **That is resolved.** The OpenMOS WebSocket
server was exercised from the NCS host using the same .NET `ClientWebSocket`
stack ENPS itself uses:

| Case | Result |
|---|---|
| W1 `reqMachInfo` as **binary** UCS-2BE | **binary** `listMachInfo`, 1612 bytes, decodes cleanly |
| W2 `roCreate` as **binary** UCS-2BE | **binary** `roAck` `roStatus OK` |
| W3 `keepAlive` as binary, no `messageID` | accepted, **no reply** |
| W4 `reqMachInfo` as **text** | tolerated on receipt, reply still **binary** |

W4 is the important one: lenient inbound, strict outbound. That is the right
posture for a protocol with this much vendor variation.

The `listMachInfo` returned on W1 confirms two related fixes: `<mosRev>4.0.0</mosRev>`
on the WebSocket transport where the TCP transport reports `2.8.4`, and
`mosProfile number="0">true` with profiles 1–7 false.

### Still outstanding

- **Deduplication on the TCP path** (#13). T12 and T13 remain as recorded in §4.
- **A MOS 4 exchange initiated by OpenMOS** (#11). Verification so far has the NCS
  host acting as the WebSocket client against our server. Connecting *out* to the
  NCS's own MOS 4 endpoint additionally needs our MOS ID registered on the NOM, or
  it answers `NACK — MOS ID is not recognized by this NOM` as recorded in §5.
- **MOS 3.8.4** (#15). Unchanged; still blocked on the WSDL.
- **`messageID` format consistency** across transports (#20).

### Minor observation

Heartbeat replies carry non-standard `timestamp` and `source` attributes
alongside the spec's `<time>` element. Compatible equipment "will ignore, without
error, any unknown tags", so this is harmless, but the spec's guidance is that
vendor additions belong in `mosExternalMetadata` rather than on predefined
elements. Pre-existing, not introduced by any of the fixes above.
