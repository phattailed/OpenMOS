# MOS Interoperability Evidence — live AP ENPS

**Date:** 2026-08-24
**Target NCS:** AP ENPS with the `NOM` MOS gateway, referred to below as `NCS-HOST`
**OpenMOS under test:** MOS 2.8.4 TCP receive tracer (pre-merge working tree)
**Status:** NCS-initiated interop achieved — a live ENPS delivered real running
orders to OpenMOS with zero errors. See §10.

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

## 10. NCS-initiated interop: real running orders delivered

**This is the result the project was working toward.** Everything in §4 and §5 had
OpenMOS or a probe script as the initiator. Here the NCS itself opened the socket
and pushed real running-order state from a live rundown, unprompted.

### Outcome

A rundown was created in ENPS referencing our MOS ID. NOM connected and delivered
its full queue with **zero errors**:

| Message | Count |
|---|---|
| `roCreate` | 1 |
| `roStorySend` | 10 |
| errors | **0** |

NOM then held the socket open, which is what the spec asks for: "It is a good
idea to establish and maintain the socket connection continuously as this gives
the other application the opportunity to monitor continuity."

Persisted state, from real ENPS content:

```
runningOrders  1     slug "morning-news", duration 5400 (a 90-minute rundown)
stories       10     slugs: gat, test, mop, map, chu, shoe, chew, hat, oosh, hay
items          0     see "why zero items" below
mosID                openmos.example.mos  -- populated, not empty
```

### What this exercised that synthetic frames never did

**Composite identifiers containing `;` and `\`.** Real ENPS IDs are not simple
tokens:

```
roID    NCS-HOST;P_NEWS\W;C45B2CF1-D7C9-4E3D-AEF9-C60DAEC93538
storyID NCS-HOST;P_NEWS\W\R_C45B2CF1-...;B7C56B36-890D-4A04-9A3A-...
```

Every hand-written fixture in this repo used IDs like `RO-41` and `STORY-1`. The
`roID` round-trips intact as the MongoDB `_id`, and the composite story key is
percent-escaped before use, so `;` becomes `%3B` and `\` becomes `%5C`. This
worked, but it worked untested — a regression test using realistic IDs is worth
adding.

**A 90-minute `roEdDur`** parsed correctly to 5400 seconds.

**Ten `roStorySend` messages in sequence** on one held-open socket.

### Why zero items

Expected, not a defect. With `StorySend` enabled ENPS performs forced playlist
construction and sends *every* story in the rundown, not only those containing
items belonging to this device — the workflow the spec describes for prompters
and publishing devices. Items are only included for objects owned by the
receiving MOS, and OpenMOS serves no media objects (Profile 1 is not
implemented). So ten stories with no items is exactly right for this
configuration.

### messageID: the evidence #20 was waiting for

Real ENPS `messageID` values are **plain incrementing integers**. The counter NOM
keeps for this device at `H:\NOM\MOS\MESSAGEID\<mosID>` read `25` when the queue
was first observed and `35` after delivery.

That resolves the open question in #20 empirically: tightening `messageID` format
validation toward the spec's "32-bit signed integer >= 1" is safe against this
NCS rather than speculative. Note this is one vendor's behaviour, so leniency on
receipt is still the right posture.

#### Decision taken

**Strict outbound, lenient inbound** — option 3 of the three in #20.

Everything OpenMOS originates satisfies §4.1.6, funnelled through
`xml.FormatMessageID` so the rule lives in one place, and guarded by
`xml.ValidateOutboundMessageID`. Inbound, both transports now share
`xml.AcceptInboundMessageID`, which requires presence where the generation
requires it and does not police format.

The evidence supports enforcing on both sides, and this NCS would pass either way.
It was not the deciding factor. Two things were:

1. **The failure modes are not symmetric.** A spurious rejection discards a running
   order — real editorial content — because a correlation token is spelled
   unexpectedly. Accepting an odd-looking identifier costs nothing, since we only
   echo it. One vendor's conformance is thin evidence about the next vendor's.
2. **This same NCS already deviates on this very element.** It answered a request
   carrying `messageID` `9001` with a `mosAck` bearing no `messageID` at all,
   though §4.1.6 says responses carry the request's ID (§5 above). A vendor loose
   about presence is not a safe bet to be strict about format.

One consequence worth stating plainly: **echoing is not origination.** When a
request arrives with a non-numeric `messageID`, the reply reproduces it verbatim
rather than substituting a conformant value. Correlation belongs to the peer that
chose the identifier; "correcting" it would leave that peer unable to match the
reply to its request. So an outbound frame may legitimately carry a
non-conformant `messageID` — but only ever one the peer chose itself.

Inbound validation was therefore loosened rather than tightened, so #20's
acceptance criterion about NACKing rejections applies to the structural faults that
remain. Those now name the fault in `statusDescription` instead of returning a bare
`invalid envelope`, bounded so a NACK cannot reflect an unbounded amount of
peer-supplied text.

### Getting NOM to connect at all

Three findings, each of which cost real time and none of which is documented
anywhere obvious.

**The device IP field does not accept `host:port`.** The web UI labels the column
`IP/Endpoint`, and the value `127.0.0.1:20541` is stored happily. NOM then passes
the entire string to a DNS resolve and fails every 30 seconds:

```
RemoteHost 127.0.0.1:20541 Authoritative answer: Host not found [11001]
  [openmos.example.mos WinsockIn_Error]
```

It is a hostname or IP only. The port is fixed at the MOS Upper Port, 10541.

**`H:\NOM\LOGS\EXCEP.LOG` is where connection failures surface.** Nothing in the
MOS monitor UI explained the blank IP column; that log said it in one line.

**NOM's own listener does not block the port.** NOM binds `0.0.0.0:10541`, but it
binds IPv4 `INADDR_ANY` without exclusive use, so on Windows a *more specific*
bind to `127.0.0.1:10541` both succeeds and wins for loopback traffic. That makes
an SSH reverse tunnel on the standard port viable with no change to NOM, no
`netsh portproxy` (its `iphlpsvc` was disabled anyway), and no sshd
reconfiguration:

```
ssh -R 10541:127.0.0.1:10541 <ncs-host>
```

Only loopback traffic diverts; connections to the host's routable address still
reach NOM. Reverting is closing the tunnel.

A caution learned the hard way: the reverse forward can die while the SSH master
still reports healthy, and NOM's 30-second retries then all fail. Verify the
remote listener, not just the control socket.

### Still outstanding

- **Raw frame capture.** NOM creates `H:\NOM\MOS\OUT\<mosID>\` but wrote no files
  even with `LogOut=1`, so no authentic `roCreate` fixture could be archived from
  the NCS side. OpenMOS deliberately keeps raw XML out of its logs. An opt-in
  capture flag would let real fixtures replace the hand-written ones.
- **A MOS 4 exchange initiated by OpenMOS** (#11).
- **MOS 3.8.4** (#15), still blocked on the WSDL.
- **`messageID` format alignment** (#20), now unblocked by the evidence above.

---

## 11. Verification of fixes

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

| T12 duplicate `messageID`, identical content | ACKed twice, RO reached `version=2` | original ack replayed, RO stays at `version=1` | dedup (#13) |
| T13 duplicate `messageID`, different content | both ACKed and persisted | second NACKed, never persisted | dedup (#13) |

T04–T07 and T10 continue to return `roAck`. T09 (`messageID` 0) and T11 (wrong
`mosID`) continue to be refused, which is correct.

T12 is worth stating precisely: the retry is answered with the *same* ack, and the
running order is not touched a second time. Answering a retry with silence is the
one option that cannot work, since the spec has the sender retrying "at intervals
until a response is received".

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

Separately, `mosID` was being persisted as an empty string on running orders, and
item-level `mosID` was dropped entirely. Both are now stored, and stored
distinctly: a `roCreate` whose envelope names one MOS and whose item names another
keeps both, which is what makes Profile 6 redirection possible (#14).

### Still outstanding

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

## 12. MOS 4.0 initiated by OpenMOS: first outbound exchange

Every exchange before this one was initiated by the NCS. This is the first in which
OpenMOS dialled *out*, on the MOS 4.0 WebSocket transport, and completed Profile 0
against a live AP ENPS.

### The endpoint

Not IIS. NOM self-hosts an HTTP listener; `<NOM-DIR>\LOGS\MOS4STARTUP.LOG`
records the prefixes it registers:

```
http://*/MOS4NCS/
https://*/MOS4NCS/
```

No port in the prefix means the .NET `HttpListener` defaults, 80 and 443, which
`netsh http show servicestate` confirms as `HTTP://*:80/MOS4NCS/`. The
implementation is `<NOM-DIR>\MOS4WebSockets.dll` alongside
`Microsoft.Owin.Host.HttpListener.dll`. The MOS 3.8.4 WebService is a separate
product on `:10543/MOS384/` under IIS proper — do not confuse them.

The `*` wildcard prefix ignores the `Host` header, which is why the endpoint is
reachable through an SSH forward tunnel at all: a request arriving with
`Host: 127.0.0.1:8090` still matches.

TLS on 443 negotiates TLS 1.2 with a certificate for an unrelated wildcard domain,
so it will not validate against the host's own name. Plain `ws://` on 80 is the
usable path until that is addressed.

### Result: Profile 0 completed

```
MOS 4 client completed Profile 0 exchange with ncsID=NCS-HOST
```

Four frames, all UCS-2BE binary, captured with `capture.dir`:

| # | Direction | Message | Wire bytes |
|---|---|---|---|
| 1 | out | `reqMachInfo` | 324 |
| 2 | in | `listMachInfo` | 1680 |
| 3 | out | `heartbeat` | 394 |
| 4 | in | `heartbeat` | 332 |

The old `NACK — MOS ID is not recognized by this NOM` is gone: registering the
device in `g_mos` resolved it.

### Two defects found in the first seconds

Both had existed since the beginning. Neither was caught by any hand-written
fixture, because our encoder and our parser agreed with each other and were both
wrong. This is the case for capturing real traffic, made concrete.

**1. MOS booleans are `YES`/`NO`, not `true`/`false`.** `listMachInfo` could not be
parsed at all:

```
parse envelope: strconv.ParseBool: parsing "YES": invalid syntax
```

Go's `encoding/xml` maps a `bool` to the XML Schema spelling. MOS does not use it.
A `YesNo` type now handles both directions, strictly emitting `YES`/`NO` and
leniently accepting `true`/`false`/`1`/`0` on receipt. Note it implements
`encoding.TextMarshaler`, not `xml.Marshaler`: a field tagged `,chardata` never
consults the XML interfaces, so implementing those leaves the broken default
quietly in place.

**2. Our heartbeat carried three invented attributes.** The NCS rejected it and
quoted the offending element back:

```
<mos>Invalid command: heartbeat requestID="2" timestamp="..." source="..."</mos>
```

The spec is `<!ELEMENT heartbeat (time)>` with no attributes at all. OpenMOS was
emitting `requestID`, `timestamp` and `source`. They are still tolerated inbound,
and a peer-supplied `requestID` is echoed for correlation, but we no longer
originate any of them. This affected the MOS 2.x transport equally, since both
share the generator.

### Observations worth recording

**The NCS reports `mosRev 2.8.4` on the MOS 4.0 WebSocket transport.** Not 4.0.
The natural assumption that the MOS 4 transport implies `mosRev 4.0` does not hold
for this NCS, and there is now a test asserting the observed value so that
assumption cannot quietly return.

**It advertises Profiles 0, 1, 2, 3, 4, 6 and 7 as `YES`, and Profile 5 as `NO`,**
with `deviceType="NCS"`.

**`messageID` is echoed verbatim** on both replies — `1` then `2` — consistent with
§4.1.6 and with the socket transport's behaviour.

**Its `time` carries no UTC offset** (`2026-08-26T03:52:26`, which is UTC) while
OpenMOS emits an offset (`2026-08-25T23:52:26-04:00`). The NCS accepted ours, so
this is a note rather than a defect.

**`DOM` is US-locale text**, `4/15/2026 2:21:26 PM`, not ISO 8601.

**The handshake does not validate `channel` or `mosID`.** A bogus value in either
still returns `101 Switching Protocols`; authorization happens at the message
layer.

### Reproduction

The Mac cannot route to the NCS, so the outbound leg needs a forward tunnel. Add it
to an existing SSH master rather than opening a new one, which avoids the
`ssh -f` hazard noted below:

```sh
ssh -O forward -L 8090:127.0.0.1:80 -S <control-socket> $NCS_SSH_HOST
```

Then point the client at it:

```sh
WS_CLIENT_ENABLED=true \
WS_CLIENT_PEER_URL=ws://127.0.0.1:8090/MOS4NCS/ \
WS_CLIENT_CHANNEL=ro \
MOS_NCS_ID=<NCS-ID> \
CAPTURE_DIR=./capture ./openmos --config=config.yaml
```

### Operational note: NOM only dials when it has queued work

Worth stating because it looks like a connectivity fault and is not. After
re-enabling the device and restarting NOM, no connection arrived. The cause was an
empty queue, proven three ways: no `SYN_SENT` or `ESTABLISHED` to the MOS port at
all; `MOS\OUT\<mosID>` empty with an mtime matching the moment the previous
session's queue drained; and a bare TCP probe from the NCS host reaching OpenMOS
immediately, confirming the path was fine.

The earlier NCS-initiated session succeeded because a queue had accumulated during
a period when the device's endpoint was misconfigured, and NOM drained all eleven
messages within two seconds of restarting. Retries are logged every 30 seconds when
work exists, so total silence in `EXCEP.LOG` means no work rather than failing work.

Consequence for testing: an NCS-initiated exercise needs a fresh MOS event
generated on the NCS side. And because OpenMOS defaults to in-memory storage, a
restart leaves it with no record of a previously delivered running order, so a bare
`roStorySend` would be NACKed for an unknown `roID` — a new `roCreate` is required,
or durable storage.

### Tooling hazard: `ssh -f` hangs an agent shell

`ssh -f` forks into the background but leaves stdout attached to the caller's pipe,
so a wrapper waiting on that pipe never returns even though the tunnel is up. Two
practical rules: add forwards to an existing master with `ssh -O forward` instead of
spawning new backgrounded clients, and verify the *listener* rather than the exit
status.

## 13. Authentic Profile 2 frames, and how to get them on demand

§12 recorded that the NCS only dials a device when it has queued work, which made
capturing genuine running-order traffic look like it needed someone editing a
rundown in the ENPS client.

It does not. The NCS answers a **device-initiated** `roReqAll` immediately:

```xml
<roListAll>
<ro>
<roID>NCS-HOST;P_NEWS\W;C45B2CF1-...</roID>
<roSlug>morning-news</roSlug>
<roChannel></roChannel>
<roEdStart>2026-08-25T19:00:00</roEdStart>
<roEdDur>01:30:00</roEdDur>
...
<mosExternalMetadata>
<mosScope>PLAYLIST</mosScope>
<mosSchema>http://NCS-HOST:10505/schema/enpsro.dtd</mosSchema>
<mosPayload><roMOSIDList>openmos.example.mos</roMOSIDList></mosPayload>
</mosExternalMetadata>
</ro>
</roListAll>
```

Better still, asking for the running order queued work on the NCS side, and NOM then
dialled the MOS 2.x socket and delivered **ten real `roStorySend` messages**, all
acknowledged. So a device can provoke authentic Profile 2 traffic whenever it wants,
with no NCS-side interaction at all. That is now the reproduction path for fixtures.

### What real frames contain that hand-written ones did not

```xml
<roStorySend>
<roID>NCS-HOST;P_NEWS\W;C45B2CF1-...</roID>
<storyID>NCS-HOST;P_NEWS\W\R_C45B2CF1-...;7F9AA3AB-...</storyID>
<storySlug>hat</storySlug>
<storyNum></storyNum>
<storyBody><p>overture</p>
<p> </p></storyBody>
<mosExternalMetadata>
  <mosScope>PLAYLIST</mosScope>
  <mosSchema>http://NCS-HOST:10505/schema/enps.dtd</mosSchema>
  <mosPayload>
    <MediaTime>0</MediaTime><RevisionNumber>5</RevisionNumber>
    <Creator>OPERATOR</Creator><CreatedDateTime>20260717T163525Z</CreatedDateTime>
    <TextTime>0</TextTime><pubApproved>0</pubApproved>
    <SourceTextTime>0</SourceTextTime><Actual>0</Actual>
    <SourceMediaTime>0</SourceMediaTime><ModTime>20260717T163525Z</ModTime>
    <Owner>OPERATOR</Owner><ModBy>OPERATOR</ModBy>
    <ENPSItemType>3</ENPSItemType>
  </mosPayload>
</mosExternalMetadata>
</roStorySend>
```

- **`storyBody` contains markup**, not text. `<p>` elements, including an empty one.
- **`mosPayload` is a vendor blob of arbitrary elements.** The spec says a payload is
  opaque to the device; this is what opaque looks like in practice.
- **`mosSchema` points at a DTD on an ENPS port** (10505), a service distinct from
  both MOS transports.
- **`roEdDur` is `HH:MM:SS`**, not seconds, on this path.
- **`messageID` was 36**, continuing the counter that stood at 35 after the previous
  session. Plain incrementing integers, NCS-originated, spanning sessions.

Sanitized versions are now fixtures in `internal/xml/live_profile2_fixtures_test.go`.
Raw captures are never committed: story bodies are editorial content.

### The defect this exposed: roStorySend fabricated running orders

The ten stories arrived with **no `roCreate` before them**. From the NCS's side the
device already held the running order, from the earlier session; OpenMOS had
restarted with in-memory storage and lost it.

OpenMOS accepted all ten and answered `roStatus=OK`. It created each story with a
`RunningOrderID` pointing at a running order that did not exist, and the Profile 6
path separately fabricated one with the invented slug `"Auto-created RO"`.

Both were wrong. `roStorySend` adds a story to a running order; it is not a way to
bring one into being. The old behaviour invented state nobody asked for and, worse,
concealed the single condition most worth reporting — that the two sides disagree
about what the device holds. An `roAck` with `roStatus=ERROR` is the only signal that
can prompt the NCS to resynchronise with a `roCreate`.

Now fixed on both paths, with the unknown `roID` named in the error. Note that no
test covered the old behaviour, which is how it survived; and one existing test was
quietly depending on it, having created a story without ever creating its running
order.

### Reproduction

```sh
# 1. tunnel (add to an existing master; never spawn `ssh -f`)
ssh -O forward -L 8090:127.0.0.1:80 -S <control-socket> $NCS_SSH_HOST
ssh -O forward -R 10541:127.0.0.1:10541 -S <control-socket> $NCS_SSH_HOST

# 2. run with capture on
CAPTURE_DIR=./capture ./openmos --config=config.yaml

# 3. ask for the running orders over MOS 4, which also queues MOS 2.x work
#    payload: <roReqAll></roReqAll>, UCS-2BE in a binary frame
```

## 14. Four other vendors: what one NCS could not teach us

Everything above was learned against a single ENPS version on transports we chose.
Agreeing with one peer is not interoperability. This section comes from roughly 55 MB
of real MOS traffic between an AP ENPS estate and four independent vendors'
devices — a prompter, a graphics system, two automation systems and a gateway —
about 90,000 messages, none of it produced by us.

### The corpus

| Device class | Messages | Ports used | `messageID` |
|---|---|---|---|
| Prompter | 18,735 | 10541 | always |
| Graphics | 12,717 | 10540 **and** 10541 | **never** |
| Automation ×2 | ~16,200 | 10540 and 10541 | always |
| Gateway | 551 | n/a (`Port="0"`, `LinkID` attribute) | always |

### The same NCS speaks two different `listMachInfo` dialects

This is the finding with the sharpest teeth. ENPS 8.2 answers `reqMachInfo` on the
socket transport with **flat** profile elements:

```xml
<mosRev>2.8.3</mosRev>
<mosProfile0>YES</mosProfile0>
<mosProfile1>YES</mosProfile1>
...
```

ENPS 9.6 on the MOS 4.0 WebSocket answers the same request with a **container**:

```xml
<supportedProfiles deviceType="NCS">
<mosProfile number="0">YES</mosProfile>
```

Same vendor, same message, two encodings. OpenMOS understood only the container form,
so it would have silently read *no supported profiles at all* from most of the
installed estate — no error, just a peer that appears to support nothing.

Both are now parsed, and `ListMachInfo.Profiles()` returns a single merged view. The
flat fields are pointers so that "not mentioned" stays distinguishable from
"explicitly NO"; collapsing those would turn silence into a false claim.

### A device that has never sent a `messageID`

The graphics system sent 12,717 messages across 27 log files without one, and NOM's
replies carried none either. On the socket transport that is legal — the element is a
MOS 3.x/4.0 requirement — but it settles #20 empirically rather than by argument. Had
we enforced presence uniformly, this vendor would have been unreachable.

It also shows why the rule belongs to the *transport*: the identical frame is
legitimate on the socket and invalid on MOS 4.0, where §4.1.1 requires the element.
There is now a test asserting exactly that asymmetry.

### Every `roAck` in the corpus is a refusal

All 2,820 of them:

```xml
<roAck>
<roID></roID>
<roStatus>Buddy server cannot respond because main server is available</roStatus>
</roAck>
```

and the object equivalent:

```xml
<mosAck>
<status>NACK</status>
<statusDescription>Buddy server cannot respond because main server is available</statusDescription>
</mosAck>
```

These logs are from an ENPS **buddy** (standby) server. Devices connect to both
members of the pair and the standby refuses everything until it takes over. Two
consequences for any device implementation:

- **`roStatus` is free prose, not an enumeration.** We emit `OK` and `ERROR`; a real
  NCS emits an English sentence. Anything switching on this value will misbehave.
- **`roID` can be empty in an ack.** A device correlating acks by `roID` alone will
  drop every one of these.

### Devices pull; they do not wait to be told

The automation system's startup, all within the same second:

```
reqMachInfo  ->  listMachInfo      (twice: once per port)
roReqAll     ->  roListAll
```

and the prompter sends `roReq` twelve times over three days, at varied hours.

This is the answer to the divergence recorded in §13. When OpenMOS restarted and lost
its running orders, the NCS carried on sending `roStorySend` because from its side
nothing had changed. Refusing those is correct, but it is only half the protocol's
answer: **the device is expected to recover by asking.** The NCS is not obliged to
notice our amnesia, so a device that only complains stays broken.

That reframes `roReqAll` from the trick discovered in §13 into ordinary, expected
device behaviour. Implementing the pull is the clear next step; the error text now at
least names it.

### Smaller things that would each have cost an afternoon

- **`roElementStat` was the most common non-heartbeat message**, 2,802 occurrences,
  and the socket transport could not parse it at all — it was reachable on MOS 4.0
  but missing from the socket envelope, so our two transports understood different
  vocabularies over one supposedly shared core. `roReq`, `roList`, `roReqAll` and
  `roListAll` had the same gap. All now present on both.
- **Timestamps use a comma decimal separator**: `2022-03-29T20:05:07,453Z`. This was
  first recorded here as a vendor quirk. It is not — see §15.
- **Operations arrive self-closing**: `<roReqAll/>`, `<reqMachInfo/>`, `<itemChannel/>`.
- **An empty `<roListAll></roListAll>` is a valid answer**, not a failure.
- **`messageID` values run large**: 1,127,213 on one automation system. Still within
  a signed 32-bit integer, which is what §4.1.6 requires, but nowhere near a small
  counter.
- **`objID` carries embedded semicolons**: `PACKAGE;SOT VO CLIP`, the same composite
  habit as `roID` and `storyID`.
- **The gateway logs `Port="0"` with a `LinkID` GUID attribute** rather than a socket
  port, so not every MOS peer is reached over the two socket ports at all.

### Why this section exists

Three defects in §12 and §13 were found by pointing at one live NCS for an hour.
Seven more came from reading somebody else's logs. Neither exercise required writing
a fixture, and neither could have been replaced by re-reading the specification: every
finding here is a place where real implementations and a reasonable reading of the
document differ.

## 15. What the specification says, and where it disagrees with itself

§14 was written from traffic alone. Reading MOS 3.8.4 afterwards confirmed most of
it, corrected one item, and settled two questions that observation could only leave
open.

### Corrected: the comma timestamp is normative, not a vendor quirk

§14 filed `2022-03-29T20:05:07,453Z` under vendor oddities. The specification defines
exactly that:

> Format is `YYYY-MM-DD'T'hh:mm:ss[,ddd]['Z']`, e.g. `1999-04-11T14:22:07,125Z` or
> `1999-04-11T14:22:07,125-05:00`. [...] `[,ddd]` represents fractional time in which
> all three digits must be present.

So the automation system is right and Go is the awkward one: `time.Parse` accepts only
a period, which means **no stdlib layout can read a conformant MOS timestamp carrying a
fraction.** There is now a `ParseMOSTime` that accepts the comma form the spec defines
and the period form everything else uses, and a `FormatMOSTime` that emits the comma
with the required three digits.

Because everything in brackets is optional, all of these are conformant, and all four
appear in real traffic:

```
2026-08-26T03:52:26            no fraction, no zone   (live ENPS)
2022-03-29T20:05:07,453Z       comma fraction, UTC    (automation system)
1999-04-11T14:22:07,125-05:00  comma fraction, offset (spec example)
2026-08-25T23:52:26-04:00      no fraction, offset    (OpenMOS)
```

That last line matters: OpenMOS's own output was already conformant, since the spec
permits the zone to be "an offset from UTC in hours and minutes". No change was needed
there, only the ability to read what others send.

### Settled: the spec contains BOTH profile encodings

§14 recorded that ENPS 8.2 sends flat `<mosProfile0>` on the socket while 9.6 sends a
`<supportedProfiles>` container on MOS 4.0, and treated that as vendor divergence.

It is not. **The specification defines both, in the same document.** Its structural
outline shows the container:

```
supportedProfiles (deviceType = (MOS, NCS))
  mosProfile (number = (0))
```

while its own WSDL schema for the same message declares the flat form:

```xml
<s:element minOccurs="0" maxOccurs="1" name="supportedProfiles" type="s:string"/>
<s:element minOccurs="0" maxOccurs="1" name="mosProfile0" type="s:string"/>
<s:element minOccurs="0" maxOccurs="1" name="mosProfile1" type="s:string"/>
```

Note the WSDL even types `supportedProfiles` as a plain string, which cannot hold the
nested `mosProfile` elements the outline describes. Both ENPS versions are reading the
same specification and implementing different halves of it.

That makes parsing both the only defensible behaviour, and it is now justified by the
document rather than by vendor sympathy. It also means neither encoding can be called
the wrong one.

### Confirmed by the spec

- **`roStatus` is free text.** "Options are: `"OK"` or error description. 128 chars
  max." The buddy-server sentence in §14 is conformant, and anything treating this as
  an enumeration is not. Note the 128-character limit, which OpenMOS does not enforce
  on output.
- **MOS booleans are `YES`/`NO`.** "A `"YES"` or `"NO"` value is required for each
  profile" — the defect fixed in §12 was a genuine conformance failure, not a
  tolerance gap.
- **`heartbeat` carries only `time`.** The structural outline is `heartbeat` then
  `time`, with no attributes, confirming the §12 fix. The spec also warns to "avoid an
  endless looping condition on response", which is the reflection guard OpenMOS
  already has.
- **Devices recover by pulling.** "If a message references an unknown `roID` or
  `storyID`, the MOS device should treat this as lost synchronization, send `roReq`,
  and replace its local state from the returned full `roList`." §13's fix — refusing
  `roStorySend` for an unknown `roID` — is the first half of a normative requirement,
  and the pull is the second half. `roReq` may be answered with `roList` **or** a
  NACK-bearing `roAck` when the running order is unknown or unavailable, so the
  recovery path must handle both.
- **The ACK contract is explicit.** An ACK means the message parsed, required metadata
  was saved, and "referenced metadata entities assumed to exist actually exist".
  Acking a `roStorySend` for a running order we do not hold breaks the third clause,
  which is precisely the §13 defect.
- **`roElementStat` belongs on the upper port.** "Port: MOS Upper Port (10541) -
  Running Order". It and the enquiry family are now classified so channel routing
  accepts them on `ro` and refuses them on `mom`; they parsed but were unclassified,
  so MOS 4.0 would have refused them as unknown.
- **Element order is significant.** Items "arrive in intended play order" and a device
  "must retain the sequence supplied by the NCS even if it executes items out of
  order". The in-memory backend returned stories in Go map order — see §16.

### Still outstanding against the spec

- **`messageID` should persist across restarts.** The sender "increments IDs, persists
  the last value, and wraps to `1`". Wrapping is now implemented; persistence is not,
  so a restarted OpenMOS reissues identifiers a peer may still associate with earlier
  requests — and a peer implementing retry deduplication could answer from its cache
  instead of processing.
- **`mosExternalMetadata` is not preserved.** The spec calls the payload opaque and
  requires it to be carried; our model holds `map[string]string`, which cannot
  represent the nested vendor XML that real traffic carries in `<mosPayload>`.
  `mosScope` propagation rules are likewise unenforced.
- **Profile 2 is not fully implemented**, so the README deliberately claims
  "running-order construction" rather than the profile.

## 16. Story and item order: the default backend reordered rundowns

Found while checking the ordering requirement above.

`Order` was populated correctly on ingest, and the MongoDB backend sorted by it. The
in-memory backend did not: it iterated a map and returned whatever order Go produced,
which is deliberately randomised and varies between calls on the same data.

In-memory is the **default** backend and the one every test uses, so the default
configuration silently reordered rundowns and no test could have noticed. For a
broadcast device that is the worst class of defect: order is meaning, not
presentation.

Both list methods now sort explicitly. The regression tests use twenty elements
inserted in a jumbled sequence, because map iteration can coincidentally match
insertion order for very small maps, and one of them reads repeatedly to catch the
specific failure mode that a single passing read proves nothing.

## 17. The resynchronisation mechanism was inverted

Found while starting on the pull-based recovery that §15 established is normative. The
mechanism recovery depends on did not work in either direction.

MOS 3.8.4 defines a two-stage pull:

| Message | Carries | Answered with |
|---|---|---|
| `roReq` | a `roID` | `roList` — a full build of that ONE running order, or a NACK-bearing `roAck` |
| `roReqAll` | nothing | `roListAll` — summary descriptions of ALL running orders |

OpenMOS had the two bound the wrong way round, and the Go type names are how it
happened: `ReqRunningOrderList` was `<roReq>` and `ReqRunningOrder` was `<roReqAll>` —
the inverse of what the names suggest. PR #34 noted the trap in a comment; it turned
out the trap had already been fallen into.

The consequences:

- **`<roReq>` silently discarded the requested `roID`.** The type had no `roID` field
  at all, so a peer asking for one specific running order was answered with summaries
  of every running order it knew.
- **`<roReqAll/>` failed.** The type declared a `roID`, so a conformant self-closing
  request parsed to an empty identifier and the handler then errored looking it up.
- **`roList` was modelled as `roListAll`.** It carried a list of summaries in nested
  `<ro>` elements, whereas §3.5.2 carries the running-order fields directly followed by
  `story*`, exactly like `roCreate` and `roReplace`. So a conformant `roList` parsed to
  nothing, and the one we emitted could not be read by a conformant peer.
- **`roList` also carried three invented attributes** — `requestID`, `timestamp`,
  `source` — the same defect class as the heartbeat in §12.
- **A `roReq` was answered with `roCreate`.** Wrong message: `roCreate` is an NCS
  telling a device about a new running order, not the answer to a request for one.
- **Neither reply could be sent at all.** `GenerateEnvelope` had no case for `roList`
  or `roListAll`, so even a correct message layer failed at the envelope stage and the
  connection was dropped. That was masked because no test exercised either path.

All fixed, and the types renamed to `ROReq`, `ROReqAll` and `ROList` so the names match
the wire and the inversion cannot recur. `roListAll` was already correct.

Worth stating plainly: **nothing tested any of this.** Two broken messages, a wrong
payload shape, a wrong reply type and a missing envelope case survived together because
the whole path was untested. The devices in §14 sent `roReq` 18 times across three
days; against this implementation every one of those would have received the wrong
answer.

The recovery behaviour itself — reacting to an unknown `roID` by pulling — is still
outstanding. It now has a mechanism that works underneath it.

## 18. Pull recovery implemented

The normative behaviour from §15, now built on the mechanism §17 repaired.

> "If a message references an unknown `roID` or `storyID`, the MOS device should treat
> this as lost synchronization, send `roReq`, and replace its local state from the
> returned full `roList`."

The sequence, exercised end to end on the wire:

1. The NCS sends `roStorySend` for a running order we do not hold.
2. We answer `roAck` with a NACK that says why — the peer is waiting for an answer to
   *that* message and must know it was not applied.
3. We send `roReq` for the missing `roID`, on the same connection, because this peer is
   the one holding the stale belief.
4. The NCS answers `roList`.
5. We rebuild local state from it, and the previously refused `roStorySend` then applies
   cleanly.

### The loop guard is the load-bearing part

A live ENPS sent **ten** `roStorySend` messages in a row for a running order we had
lost. If every refusal produced a `roReq`, and the NCS answered each with a NACK because
the running order is gone on its side too, the pair would trade messages indefinitely.
The specification warns about precisely this shape for `heartbeat`: "care should be taken
in implementation of this message to avoid an endless looping condition on response."

So a `roID` may be requested at most once per interval, the tracked set is bounded, and a
successful `roList` clears the record so a genuinely new divergence is actionable at once.
When the bound is reached the guard **refuses rather than evicting**: declining to ask is
safe, whereas forgetting that we asked is what re-opens the loop.

Every message is still answered. Silence would be worse than a NACK, because the spec has
senders retrying until they get a response.

### Two test-quality problems this exposed

Neither was in production code, and both mattered.

**The test frame reader could not read two frames.** It filled a buffer until it saw a
closing `</mos>`, then returned everything it had read — including bytes belonging to the
next frame — and kept no leftover. Every previous exchange was one request and one reply,
so it never showed. The moment the server answered with a NACK *and* a `roReq`, the second
read began mid-element. It now splits at the first closing tag and buffers the surplus,
which is what the production framer already did.

**The in-test repository doubles returned map order.** The production repositories were
fixed for exactly this in §16, but the integration tests use their own doubles, and those
still iterated a map. The recovery test failed roughly one run in twelve with
`story order not preserved: got S-2 then S-1` — the assertion it was written to protect,
defeated by the harness rather than the code. The doubles now sort as the real
repositories do.

A double that does not reproduce the behaviour under test cannot test it. Both were found
by running the suite repeatedly rather than once.

### Scope

Implemented on the MOS 2.x transport only. The MOS 4 WebSocket path implements
`keepAlive`, `reqMachInfo` and `roCreate` and NACKs everything else as unimplemented, so
it has no `roStorySend` to recover from yet. That asymmetry is now the top item in the
README rather than an unstated gap.

Also outstanding: applying a `roList` converges on what the NCS sent but does not delete
stories absent from it. "Replace its local state" arguably requires that, and a partial
convergence is recorded here rather than presented as a full replace.

## 19. Reading MOS 4.0 properly

§15 read MOS 3.8.4. The MOS 4.0 document had not been read in full, despite MOS 4.0
being one of the two transports implemented and despite §-references to it appearing
throughout this artifact. Those references were carried from notes rather than checked.
This corrects that.

Most of what was asserted holds. Four things did not.

### Corrected: `roStorySend` is Profile 4, not Profile 6

MOS 4.0 §2 is specific about which messages belong to which profile:

- **Profile 4** (Advanced RO/Content List Workflow, §2.5) requires `roReqAll`,
  `roListAll` and **`roStorySend`**.
- **Profile 6** (MOS Redirection, §2.7) "does not include any additional MOS
  messages". It is a naming convention for fully qualified mosIDs —
  `<family>.<machine>.<location>.<enterprise>.mos` — and nothing else.
- **Profile 7** (§2.8) has one message, `roReqStoryAction`.

OpenMOS had `roStorySend` and `roReqStoryAction` in a file called
`client_profile6_handler.go`, both attributed to Profile 6, which owns neither. Renamed
and corrected. This matters because precise profile attribution is what the README's
status table is for; a file asserting the wrong profile quietly undermines it.

### Confirmed clean: none of MOS 4.0's deprecated messages exist here

MOS 4.0 removes thirteen messages outright and states that an implementation "should
NEVER initiate" them: `roStoryAppend`, `roStoryInsert`, `roStoryReplace`, `roStoryMove`,
`roStoryMoveMultiple`, `roStorySwap`, `roStoryDelete`, `roItemInsert`, `roItemReplace`,
`roItemMoveMultiple`, `roItemDelete`, `roStat`, `roItemStat` — all superseded by
`roElementAction`.

Checked: not one of them exists in this codebase, so there is nothing to accidentally
initiate. Receipt of legacy messages is explicitly permitted, so nothing needs adding
either.

### Sharpened: `messageID` persistence is a MUST

§15 recorded persistence as something the spec "expects". §4.1.7 is firmer: "the last
used messageID **must be persistent**". Since the section also describes retry
deduplication as the field's whole purpose, a restarted OpenMOS reissuing 1, 2, 3 could
have those answered from a peer's dedup cache rather than processed. Still outstanding,
now stated at the right strength.

### Not implemented: the DISCONNECTED signal

§2.3 requires that if a device's running-order sequence is "intentionally changed such
that it no longer represents the sequence as transmitted from the NCS", the device
"will immediately send a series of `roElementStat` messages to the NCS with a `status`
of `DISCONNECTED` and ACK all subsequent 'ro' messages with a `status` of
`DISCONNECTED`", recovering afterwards via `roReq`.

OpenMOS never reorders on its own, so the trigger cannot currently fire. That makes
this inapplicable rather than broken — but it would become required the moment any
local reordering were added, and it is not implemented.

### Confirmed by MOS 4.0, having previously been taken on trust

- **Encoding is UCS-2 big-endian**: "All MOS message contents are transmitted in
  Unicode, high-order byte first, also known as 'big endian.'"
- **Channel-to-port mapping**: `mom` = 10540, `ro` = 10541, `aux` = 10542, exactly as
  implemented, with `aux` carrying the `mosReqObjList` query family.
- **`keepAlive` needs no `messageID`**: "Since a reply is not required and therefore not
  sequenced, the messageID field is not required for this message." Its example carries
  none.
- **Passive mode** is `passive=true` on the URL, one connection per channel, and the
  inner device must re-establish "as quickly as possible" if it drops.
- **Authentication** is HTTP Basic in the `Authorization` header, explicitly to avoid
  credentials "in the URL", and devices "are also expected to accept self-signed
  certificates, or provide an option to do so".
- **Pull recovery** is stated twice, once in §2.3 in more operational language than
  3.8.4 uses, confirming §18's implementation.
- **Profile 2 requires Profiles 0 AND 1.** Profile 1 is object workflow — `mosObj`,
  `mosReqObj`, `mosReqAll`, `mosListAll`. OpenMOS implements no object workflow, so it
  could not claim Profile 2 even if the running-order family were complete. The
  README's "Profile 2 running-order construction" phrasing is right for a second
  reason.

### More spec self-contradictions

§15 found the `listMachInfo` profile encoding defined two ways. There are more, and each
argues for lenient parsing:

- **The XSD types `mosProfile` as `xsd:boolean`**, which accepts `true`/`false`/`1`/`0`
  and specifically **not** `YES`/`NO` — while the prose says "A `YES` or `NO` value is
  required for each profile" and every example uses `YES`. The `YesNo` type accepts all
  of them, which is now justified by the document disagreeing with itself rather than by
  vendor sympathy.
- **The DTD allows exactly one profile**: `<!ELEMENT supportedProfiles (mosProfile)>`,
  no repetition operator, while every example lists eight and the XSD permits
  `maxOccurs='8'`.
- **`roElementStat` requires `itemID` in the DTD** but marks it optional in the
  structural outline. Real traffic with `element="RO"` omits it, so the outline wins in
  practice.
- The `keepAlive` example is malformed, closing a tag it never opens.

### Sequential flow, per port

§2.3 states a constraint worth recording: a sender "will not send another message to the
target device on the same port until it receives an acknowledgement", and acknowledgement
on one port is independent of the other. Unacknowledged messages are buffered and
retried, and the spec recommends buffering them across a restart.

OpenMOS answers requests rather than driving long outbound sequences, so this mostly
constrains the peer. Where it touches us is pull recovery: the `roReq` in §18 is a new
request, and the rate limit that keeps recovery from looping also happens to keep only
one outstanding. That is a coincidence of design rather than an implementation of the
rule, and is worth knowing if outbound traffic ever grows.

## 20. One shared message core, actually

The README has claimed since the beginning that OpenMOS is "one message core behind two
transports", with the transports owning framing and envelope rules and nothing else. For
the running-order family that was not true.

The MOS 2.x socket path had fifteen running-order handlers. The MOS 4.0 WebSocket path had
one — `roCreate` — and answered everything else with `message type X is not implemented`.
So a rundown could be built and maintained over the socket, and only created over
WebSocket.

### Why not simply add fifteen more handlers

Because that is how the divergence happened in the first place. §14 found `roElementStat`
parseable on one transport and not the other; §17 found the `roReq`/`roReqAll` pair bound
inversely, with `roList` shaped as `roListAll`. Each was a case of the same message being
implemented twice and drifting.

Fifteen more methods on the WebSocket server would have doubled the surface and guaranteed
the next drift. So the running-order handling moved out of both transports into
`dispatch_ro.go`, and each transport supplies a `peerResponder` — an interface with two
methods, "who is this peer" and "send this message back to them". Everything above that
line is shared.

The handlers are now identical by construction rather than by discipline. A test asserts
the seam directly: every message in the family must be recognised by the shared dispatcher,
which means it either works on both transports or on neither.

### What this closed

On the MOS 4.0 transport, these went from `not implemented` to working, exercised end to
end over real UCS-2BE binary frames:

`roReplace`, `roDelete`, `roMetadataReplace`, `roStorySend`, `roReadyToAir`,
`roElementAction`, `roElementStat`, `roReq`, `roReqAll`, `roList`

Pull recovery (§18) came with them, because it lives in the shared `roStorySend` handler
rather than in a transport. There is a loopback test proving a MOS 4 peer that sends a
story for an unknown running order gets a NACK and then a `roReq`, exactly as the socket
peer does.

### Two defects found while doing it

**`roElementStat` was discarding its `element` attribute.** The struct had no field for
it. MOS 4.0 declares it `<!ATTLIST roElementStat element CDATA #REQUIRED>` and §3.7.1 gives
the values `RO`, `STORY`, `ITEM` — it is the one field distinguishing a running-order
status from a story or item status. All three appear in real traffic, and §14 recorded
`roElementStat` as the most common non-heartbeat message in the sampled corpus. We were
parsing the message and throwing away its subject.

**`roStatus` was the bare word `ERROR`.** MOS 4.0 §6 defines the field as `"OK"` or an
error description, 128 characters. `ERROR` describes nothing, and a peer that cannot see
why its `roReplace` was refused cannot correct it — which is presumably why a real ENPS
puts whole sentences here, as §14 found. Failures now name their cause, trimmed to fit the
128-character limit, with a test asserting the bound.

### What is deliberately still per-transport

`roCreate` keeps its own handler on each side. Both already do deduplication with
ack-after-persist, but the dedup scoping differs — the socket scopes by connection, MOS 4.0
scopes by channel because each channel carries an independent `messageID` sequence.
Unifying that is a separate change with its own reasoning, and folding it in here would
have mixed two arguments.

Profile 0 also stays per-transport, correctly: `messageID` requirements and frame encoding
are exactly the things that legitimately differ between generations.

## 21. A second NCS estate, reached over AWS SSM

Everything above came from one ENPS host. A second estate turned out to be reachable
without SSH at all, through AWS Systems Manager Run Command, which matters because it
needs no inbound access and no tunnel.

Access is per-profile and narrow: of four local AWS profiles only one could call
`ssm:DescribeInstanceInformation`, and only in one region. The others were denied in all
nineteen. Worth checking every profile before concluding a path is unavailable.

The estate holds several NOM pairs, distinguishable by customer tag rather than hostname:
one is the host used throughout this artifact, another is a **multi-vendor demonstration
rig** carrying a prompter-style spread of real integrations — a graphics/asset system, a
cloud media service, an automation product and several in-house devices.

### The endpoint field takes a URL, which corrects an earlier note

§10 recorded that the MOS device endpoint field is "hostname-or-IP only", because NOM
DNS-resolved the whole string and `127.0.0.1:20541` failed with `Host not found [11001]`.

That is true for a **MOS 2.x socket device**, and it is not the whole rule. The
demonstration rig has a device configured with a full HTTPS URL in the same field:

```
id=[<vendor>.mos]  endpoint=[https://<cloud-host>/]  mosver=[2.8]
```

So the field is interpreted according to the device's transport: dialled as a hostname for a
socket device, used as a URL for a web/cloud one. The earlier note was a correct observation
generalised too far.

**Several devices have an entirely empty endpoint** and are perfectly normal — they are
inbound-only or plugin-based, and NOM never dials them. That is worth knowing because
blanking the endpoint is how this project disables its own test device: it stops NOM dialling
out, which is the intent, but "blank" is a legitimate steady state rather than a disabled
marker.

### NOM 9.7 exists, and registers MOS 4.0 the same way

The demonstration rig runs **NOM 9.7.0.65**, a version ahead of the 9.6 all the MOS 4.0
evidence in §12 came from. Its `MOS4STARTUP.LOG` registers the same prefixes:

```
http://*/MOS4NCS/
https://*/MOS4NCS/
```

and `netsh http show servicestate` confirms `HTTP://*:80/MOS4NCS/`, `HTTPS://*:443/MOS4NCS/`
and the separate MOS 3.8.4 WebService on `:10543/MOS384/`. So the MOS 4.0 endpoint shape is
stable across at least two NOM releases, which is worth knowing before treating it as a
9.6 quirk.

The process is `NomService` rather than `nom.exe` on this build — a detail that made a
naive "is NOM running" check report false while the service was plainly running and writing
logs.

### An observation, not a diagnosis

The rig's `EXCEP.LOG` was being written continuously, and of the last 400 entries **398 were
the same line**:

```
[10054] An existing connection was forcibly closed by the remote host. [ReadCallback]
```

with no `ESTABLISHED` connections on either MOS socket port at the time — so something is
connecting and being reset repeatedly rather than holding a session. That is somebody else's
rig and not diagnosed here; it is recorded because a MOS implementation should expect to see
this pattern and because it is a reminder that `ReadCallback` resets are what a half-open
peer looks like from the NCS side.

### Not done: testing OpenMOS against this NCS

Reaching it read-only needed no permission. Actually exercising it would need a device row
added to a shared demonstration rig, which is a change to someone else's environment, so it
has not been made. It is the obvious next piece of live evidence: a second NOM release, and
a device list that includes transports we have never spoken to.

## 22. The messageID counter is now durable

§19 recorded persistence as an outstanding MUST. MOS 4.0 §4.1.7: "the last used messageID
must be persistent."

The reason is in the same section, and it is not tidiness. The field exists so a receiver can
tell a retry from a new request — "it can see from the messageID whether or not it processed
this message already". A process that restarts and reissues 1, 2, 3 may therefore have those
messages answered from a peer's deduplication cache rather than processed, and **the sender
cannot tell the difference between "done" and "mistaken for a retry"**.

The reference NCS solves this with a file per mosID under `MOS\MESSAGEID\`, which is both a
precedent and a hint that a file is sufficient.

### Reserved in blocks, because the failure modes are not symmetric

The sequence records a high-water mark rather than every value, handing out identifiers from
memory until the block is exhausted. A crash therefore loses the unused remainder.

That direction is deliberate. **Skipping identifiers is harmless** — a peer only cares whether
it has seen a value before. **Reusing one is the exact hazard the field exists to prevent.**
So the design is biased towards skipping, and there is a test asserting that a restarted
sequence resumes *above* the previous block rather than inside it.

Writes are temp-file-and-rename, so a crash mid-write leaves the previous mark rather than a
truncated one. A truncated mark would read low and reissue, which is the single outcome worth
engineering against.

### Availability over strictness, said out loud

If the counter cannot be written — no directory configured, or an unwritable path — the
sequence keeps working from memory and reports itself degraded, which is logged once at
startup. A device that will not talk is worse than one that risks a repeated identifier after
a crash.

But it is logged, because silently not meeting a MUST is the kind of gap this artifact exists
to prevent. `state.dir` defaults to `state` and is honoured from `STATE_DIR`; empty disables
persistence explicitly rather than by accident.

Still outstanding: the socket transport does not originate requests, so it has no counter to
persist. If it ever does, it should share this sequence rather than grow its own.

## 23. mosExternalMetadata was being discarded

The payload MOS exists to carry was being thrown away, in three places at once.

MOS 4.0 §4.1.5 describes `mosExternalMetadata` as "a mechanism for transporting additional
metadata, independent of schema or DTD", and the DTD types the payload as
`<!ELEMENT mosPayload ANY>`. It is explicitly opaque: a device carries it without
interpreting it, which is how MOS supports vendor schemas it has never heard of.

### Three simultaneous losses

**1. The payload was typed as a string.** `MosPayload string` with an element tag means
`encoding/xml` collects only the element's *character data* — and a payload made of child
elements has none. So every real payload parsed to `""` and vanished with no error. Proven
before fixing:

```
mosScope="PLAYLIST"   mosSchema="http://example/schema"   mosPayload=""  (len 0)
```

It is now captured with `,innerxml`, which preserves the raw XML on read and writes it back
literally, because that is what carrying an opaque payload requires.

**2. Story and item had no field for it at all.** The spec places `mosExternalMetadata*` at
running-order, story *and* item level. `StoryInfo` and `ItemInfo` declared none, so those two
levels were dropped structurally regardless of the payload typing.

**3. `roCreate` had no running-order-level field either.** So all three levels were lost.

### The model could not hold it

These types already carried `Metadata map[string]string`, which cannot represent nested XML.
Real payloads are documents: ENPS sends a dozen production fields, and an automation vendor
sends entire template definitions several levels deep with attributes. Flattening those to
key/value pairs destroys exactly the structure the spec requires be preserved.

There is now an `ExternalMetadata` type holding scope, schema and the raw payload, on running
orders, stories, items and objects. A regression test confirms the loss is detected: with
preservation removed, all three levels report "metadata was not stored at all".

### Two non-conformant shapes noticed, not changed

Recorded rather than fixed, because both reach beyond this change:

- **`storyDur` is emitted on a story.** The spec's story outline is
  `(storyID, storySlug?, storyNum?, mosExternalMetadata*, item*)` — there is no story
  duration element. A conformant peer ignores unknown tags, so it is tolerated, but it is
  invented output.
- **`objPath` is emitted bare on an item.** The spec nests paths in an `objPaths` structure
  containing `objPath*`, `objProxyPath*` and `objMetadataPath*`. Changing it touches the
  object family too.

### And a lever on the NCS side

An ENPS device row carries a **`PreserveExternalMetadata`** boolean, and it is `false` by
default on every device inspected — including this project's test device. So whether ENPS
sends the payload at all is configurable per device, which means an empty payload in captured
traffic does not by itself prove a parsing fault. Worth setting before concluding anything
from a capture.

## 24. What ENPS actually means by "passive"

Vendor documentation for ENPS MOS support settles this, and it inverts a working assumption.

### Passive is about which side dials, and it changes what a connection is FOR

ENPS describes three arrangements:

| ENPS term | Who connects | What the connection carries |
|---|---|---|
| **Active** | the MOS device connects to ENPS | messages **from** the device |
| **Passive** (primary sense) | **ENPS connects to the MOS device** | messages **from** the device, over a link ENPS opened |
| **Passive** (inbound variant) | the device connects to ENPS with `passive=true` | messages **to** the device — ENPS creates a `MOSOutput` for it |

The third row is the operationally decisive one. In ENPS's words, when an inbound connection
is passive "the NOM creates a new instance MOSSocket for the WebSocket connection, and an
associated instance of **MOSOutput** [...] ENPS (NOM) will use this connection for messages
to the MOS device." Whereas if the connection is **not** passive, "ENPS (NOM) creates a new
instance MOSSocket (**for incoming messages**)".

So a plain inbound MOS 4.0 connection is read-only from ENPS's point of view. It will accept
what we send and answer it, but it will never push a running order down it.

**That explains §12 exactly.** OpenMOS dialled in without the flag, completed Profile 0, and
received nothing further — not because Profile 2 was unavailable, but because ENPS had
classified the link as input-only. And it means passive mode is not merely a firewall
convenience: **it is the mechanism by which an outbound-dialling device receives NCS-initiated
traffic at all.** For this project that removes the need for a reverse tunnel on the MOS 4
transport entirely.

### Basic authentication applies only to wss, not ws

"Per protocol only the WSS (WebSocket Secure), and not WS (Web Socket) connection will include
Basic Authentication", and where it is used "the username and password provided in the Basic
Authentication header must match a valid username and password on the ENPS NOM Server".

Two consequences. The credential is a **server account**, not a per-device secret. And over
plain `ws://` it is neither sent nor checked — so the client's HTTP Basic support, which is
implemented and unit-tested, cannot be exercised at all without a TLS endpoint whose
certificate validates. The reference host's certificate is for an unrelated domain, which is
why that row still reads "No" for live proof.

### Message flow is queued and tick-driven

Input is processed on a `tmrInput` tick and output on a `tmrOutput` tick, with both queues
persisted. Notably: "if a WebSocket connection exists for outgoing messages for the MOS
device, then the next queued output message is sent". If no such connection exists, output
simply accumulates.

That is the same behaviour §12 recorded from the outside — NOM only appears to dial when it
has queued work — now explained from the inside. It also means a passive connection that drops
does not lose queued messages; they wait.

### Configuration is per device

The MOS table in System Maintenance holds the `Passive` flag, the URL of the MOS endpoint, and
the credentials for `wss://`. The device row exposed through the global tables endpoint
confirms it as a boolean field alongside `PreserveExternalMetadata`, both `false` by default.

## 25. Passive mode: three of our bugs fixed, and an NCS-side wall

§24 established from vendor documentation what passive mode means. This is what happened when
it was actually tried, with **no reverse tunnel** — only a forward tunnel, so the NCS could not
reach this machine at all. Anything arriving had to come down the connection we opened.

The honest summary: **passive mode is still not proven live.** But the attempt found three
defects in our own implementation, and the remaining obstacle is on the NCS side with its own
stack traces.

### Our bug 1: the client drove a handshake the peer will never answer

The client always performed Profile 0 — `reqMachInfo` then `heartbeat` — before entering its
read loop. On a passive connection ENPS answers neither, because it treats the link as an
output channel. Observed exactly:

```
out: <mos>...<messageID>1</messageID><reqMachInfo></reqMachInfo></mos>
in:  (nothing, ever)
```

Passive mode now connects and listens. The peer initiates; we answer.

### Our bug 2: the handshake had no timeout, so the client wedged

Thirty-three seconds in: one frame out, zero in, no reconnect, no error. The client was blocked
in `readMessage` on the long-lived context with nothing to bound it. A peer that accepts a
connection and then says nothing could hang the client indefinitely.

The client-driven handshake is now bounded, and reconnects rather than waiting forever.

### Our bug 3: we never sent keepAlive at all

The client held connections with periodic `heartbeat`, which is a liveness *question* the peer
is expected to answer. On a passive connection nobody answers it.

`keepAlive` is the correct mechanism and we had never implemented sending it. MOS 4.0 §2.1 is
explicit: "Firewalls often close connections after short periods without traffic. The keepAlive
message is utilized as a mechanism to keep the connection active, **especially when MOS passive
mode is in use**." It expects no reply and carries no `messageID`, being unsequenced.

Passive connections are now held with `keepAlive`, active ones still with `heartbeat`. Confirmed
on the wire.

### A test that encoded the wrong assumption

The existing client tests set `Passive: true` and asserted a completed Profile 0 handshake —
which only made sense while passive was a cosmetic URL parameter. That is precisely the
assumption the live run disproved. The default is now active mode, with a separate test
asserting that passive mode does **not** initiate.

### The NCS-side wall

With the device row's `Passive` and `PreserveExternalMetadata` flags both set, NOM restarted,
and the client connected and holding with `keepAlive`, **nothing was delivered**. The NCS has
work to deliver and cannot:

- `H:\NOM\MOS\OUT\<mosID>` holds **10 queued messages**, unchanged throughout.
- `MOSOutput.RemoveQueueOut` throws **24 times** in a 300-line window:
  `System.ArgumentOutOfRangeException: InvalidArgument=Value of '0' is not valid for 'index'`,
  raised from `System.Windows.Forms.ListBox.ObjectCollection.get_Item`. The output queue is
  backed by a WinForms list control, and the drain path is indexing a row that is not there.
- `MOS4WebSockets.MOSWebSocket.ReceiveMessages` throws a `WebSocketException`.
- NOM continues to DNS-resolve the mosID as a hostname every 30 seconds
  (`Host not found [11001]`), which is what it does when it has queued output and no endpoint
  to dial.

So on this NOM 9.6 build the passive inbound path does not drain the output queue. That is
consistent with the vendor documentation hedging on this exact variant — it calls the
device-does-not-accept-connections arrangement "less likely but can work".

None of it was repaired: those are stack traces in the NCS's own code, on a host this project
only borrows.

### What would settle it

The NAB estate runs **NOM 9.7.0.65**, a release ahead of this one, reachable read-only over
SSM (§21). If 9.7 drains a passive queue where 9.6 does not, that is the answer — and it needs
a device row on a shared demonstration rig, which is a change to someone else's environment
and has not been made.

Until then the README records passive mode as implemented, loopback-tested, and **not** proven
live, with the reason stated rather than left as "untested".

## 26. Acting on the Sofie breadcrumbs: two gaps, seven already closed

`doc/mos-protocol-source-synthesis.md` now pins nine Sofie-to-OpenMOS breadcrumbs, each with a
source reference, the behaviour it demonstrates, the corresponding OpenMOS seam, and
adopt/adapt/do-not-copy guidance. Working through them: seven describe things this repository
already does, and two named real gaps. Both are now closed.

### Breadcrumb 5, the invariant worth having mechanically

"A parsed type is not an implemented workflow. Add a startup/test assertion tying every
advertised profile/message to a real handler and response path."

This is the single most useful item in the table, because it is the failure this project keeps
having. It has happened three separate ways: `roElementStat` parsed on one transport and not the
other (§14); `roReq` and `roReqAll` parsed but bound to each other's handlers (§17); and the
whole Profile 2 family parsed on MOS 4.0 while the transport answered "not implemented" (§20).
Every one was a message the parser accepted and the application did nothing with.

`internal/server/message_inventory_test.go` now records, for all 32 elements the parser accepts,
whether it is handled by the shared dispatcher, handled per-transport, or parsed with nothing
acting on it — and *why*, for the last category. Three checks enforce it:

- **The inventory must cover the parser.** The test reads the parser's own `case` list, so
  adding a message type fails the build until somebody classifies it. Verified by adding a
  fake `roFakeNewMessage` case: `the parser accepts these messages and nothing records what
  happens to them: [roFakeNewMessage]`.
- **Claims must be truthful.** Anything marked shared must actually be recognised by
  `dispatchRunningOrder`, and anything else must not be — so the classification cannot drift
  into aspiration.
- **Every gap must state a reason.** A parsed-but-unimplemented entry with no explanation is
  indistinguishable from an oversight.

It found one on its first run: **`roListAll`**. It has a handler, so it looked handled. The
handler only logs. `roListAll` is the discovery *answer*, and its only real use is driving a
follow-up `roReq` per running order — MOS 4.0 §2.5: "For a full listing of the contents of the
RO the MOS device must issue a subsequent roReq". That two-stage walk is not implemented, so an
inbound `roListAll` changes nothing. It is now classified by **effect** rather than by whether a
function exists, and the missing walk is recorded in the README.

### Breadcrumb 3, framing cases without Sofie's parser

"Adopt the cases, not the parser: retain OpenMOS's UCS-2BE byte discipline and 4 MiB bound;
test arbitrary splits, coalesced frames, junk, and partial tags. Do not copy Sofie's unbounded
string buffer or automatic junk discard."

The framer was already correct, and the 4 MiB bound already enforced in `Append`. But two subtle
protections carried no tests and either could be "simplified" away by someone who did not know
why they were there:

- `searchFrom` retains a trailing window rather than jumping to the buffer end, so a `</mos>`
  split across two reads is still found.
- `index%2 != 0` rejects a terminator match at an odd byte offset. In UCS-2BE a frame boundary
  is only real at an even offset, and a payload can contain byte runs that look exactly like
  `</mos>` one byte out of alignment. This is a hazard a string-based parser such as Sofie's
  cannot have, so the case had to be constructed rather than borrowed: the characters
  U+3C00 U+2F00 U+6D00 U+6F00 U+7300 U+3E00 preceded by one whose low byte is zero encode to
  exactly `</mos>` in UCS-2BE, starting at an odd offset. The test asserts its own setup, and
  fails if the decoy does not land misaligned.

`internal/xml/framing_robustness_test.go` adds seven tests: every split point of a two-frame
stream, byte-at-a-time delivery, five coalesced frames in one read, the odd-offset decoy, a
withheld half-code-unit terminator, non-MOS roots refused rather than discarded, and the 4 MiB
ceiling on an unterminated frame.

Both protections were then removed deliberately to confirm the tests earn their place. Dropping
the alignment check fails exactly `TestFramerIgnoresCloseTagAtOddByteOffset`; dropping the
window retention fails the three split-related tests and nothing else.

### The seven already satisfied

Architecture boundary, port/role separation by generation, ACK-after-persistence, the
`roReq`/`roReqAll` direction split, and fixture-driven in-process peers all describe existing
structure. Serialization and retry are partly satisfied — the durable sequence, dedup and
conflict detection are in place, but OpenMOS does not yet enforce one in-flight request per
ordered lane on its own outbound traffic, which matters more once it originates more than
Profile 0. Primary/secondary failover is marked do-not-copy-yet and has correctly not been
copied.

The central warning in that section is the one to keep: a connector can parse or send a message
its consuming application does not meaningfully handle. Handler wiring, state effects,
acknowledgements and end-to-end tests remain the authority for what this repository claims.

## 27. The discovery walk, and durability kept narrow

Two related gaps, both about state that cannot be rebuilt by asking.

### The walk: roListAll was a summary nobody followed up

§26 recorded that `roListAll` had a handler which only logged, and that the message inventory
test classified it parsed-only for that reason. That is now implemented, and the classification
changed with it -- which is the inventory mechanism working rather than a coincidence.

Recovery is two-stage by design. `roReqAll` returns a `roListAll`: identifiers, slugs and
timings, with no stories or items. MOS 4.0 §2.5 is explicit that it does not end there -- "for a
full listing of the contents of the RO the MOS device must issue a subsequent roReq". Until now
OpenMOS stopped at the summary, so startup and reconnect recovery was half a mechanism.

The walk is **sequential, not a burst**, and that is the part worth defending. Sending one
`roReq` per advertised running order at once would be simpler and is wrong twice: MOS 4.0 §4.1
requires that a sender "must not send another message on the same port until the previous message
is acknowledged", and a real NCS can advertise far more running orders than it is reasonable to
demand at once. So one request is outstanding at a time and the next is sent when the previous
`roList` is applied. A deliberately reverted burst implementation is caught by three tests,
including one whose failure message quotes the rule.

Three details that are not obvious:

- **A deadline is required, not optional.** `roReq` is not guaranteed a `roList`; the
  specification allows a NACK-bearing `roAck` instead, and a real ENPS buddy server NACKs
  everything (§16). Without a timeout one refusal would stall the walk permanently, leaving every
  later running order unrequested and the divergence silent -- worse than the gap being closed.
- **The walk bypasses `resyncGuard` deliberately.** That guard is a loop-breaker, keyed per
  running order with a thirty-second interval, and it exists because a live ENPS sent ten
  `roStorySend` in a row for a running order we did not hold. A discovery walk is the opposite
  situation: each identifier is requested once, in sequence, because the NCS has just said it
  holds them. Passing the walk through the loop-breaker would make a legitimate first-time walk
  suppress itself whenever it followed a recent divergence on the same running order.
- **An unsolicited `roList` must not advance the walk.** Unsolicited lists are legal, and
  treating one as an answer would skip a running order without ever requesting it.

Advancement is opportunistic rather than timer-driven: any inbound message is a chance to unstick
a walk whose answer never came. The honest consequence is that a peer falling completely silent
mid-walk pauses it until traffic resumes. In practice Profile 0 flows at least every thirty
seconds, so this is a pause and not a deadlock, and a peer sending nothing at all has a larger
problem than an unfinished walk.

### Durability, and what was deliberately left out

Three pieces of protocol state cannot be rebuilt by asking the NCS, and only those three now
persist:

1. **The outbound `messageID`** (§22). Reserved in blocks, so a crash skips rather than repeats.
2. **Deduplication receipts.** The retry rule is that a sender repeats a message with the same
   `messageID` until answered, and the receiver must replay its original response rather than
   apply the message twice. In memory that works; across a restart the history was lost, the
   retry looked new, and the message was applied again. For a `roStorySend` that is a duplicated
   story; for a `roElementAction`, an operation performed twice.
3. **Unfinished discovery work.** The NCS states what it holds once. If the process stops halfway
   through fetching it, nothing repeats that statement, so the remainder stays divergent --
   present on the NCS, absent locally, with no error anywhere.

Everything else is recoverable by asking, so it is not persisted.

**No pluggable storage abstraction was added.** An earlier plan was a `StateStore` interface with
room to bolt on S3 or Cassandra; that was dropped as premature. `DedupStore` was already an
interface for a real reason -- bounded memory versus durable -- so `FileDedupStore` is a second
implementation of something that existed, not a new layer. A single-process deployment does not
need more, and inventing the seam before a second backend exists means guessing at its shape.

Implementation choices worth recording:

- **Dedup uses an append-only log, not a rewritten snapshot**, because it is written on every
  inbound message and rewriting the whole set each time would make the hot path proportional to
  history. The log is compacted from live state once it passes a multiple of the capacity, so it
  stays proportional to the bounded entry set. `FileDedupStore` composes `MemoryDedupStore`
  rather than reimplementing eviction, so the LRU bound and the conflict rules stay in one place.
- **The walk uses a snapshot**, because its state changes once per running order rather than once
  per message. Temp-file-and-rename, as with the `messageID` mark.
- **The in-flight identifier is persisted as part of the pending queue.** After a restart no
  answer is coming for it, so it must be requested again rather than waited on.
- **Each transport gets its own state subdirectory.** Dedup scopes are already separate -- the
  transports run concurrently and MOS 4 multiplexes three channels with independent `messageID`
  sequences -- so mixing their durable state would invite exactly the cross-talk the scoping
  prevents.
- **There is no fsync per record.** A hard power loss can lose the last few appends, and those
  messages would be re-applied on retry. That is the same failure this removes, reduced from
  "every message since startup" to "the last few before the crash". Buying the remainder costs an
  fsync per message and is not worth it here. The README says so rather than implying the
  guarantee is total.
- **Unusable storage degrades loudly to memory rather than refusing to start.** A storage problem
  should cost durability, not availability; non-durable dedup is precisely what shipped before.
  The same choice as the `messageID` sequence, for the same reason.

Every one of these was verified by removing the persistence and watching the tests fail: dropping
the dedup append fails three tests, dropping the walk snapshot fails the resume test.

An empty `state.dir` still disables all of it, and a test pins that `""` means disabled rather
than the current directory -- the config-zero-value trap that has caught this project three times.

## 28. A second NCS version, and four defects that only a real peer could find

The question was whether the passive-mode failure in §25 is specific to NOM 9.6. Answering it
needed a second estate, which is reachable only through AWS Systems Manager -- no SSH, no
tunnel. That turned out to be solvable, and the attempt found four defects in OpenMOS before
passive mode was even reached.

### Getting a socket to an SSM-only host

Run Command executes commands *on* an instance but provides no socket *to* it, and passive mode
needs OpenMOS to dial the NCS. SSM port forwarding closes that gap:

```sh
aws ssm start-session --target <instance> \
  --document-name AWS-StartPortForwardingSession \
  --parameters '{"portNumber":["80"],"localPortNumber":["18080"]}'
```

Verified end to end: a WebSocket upgrade completed from a laptop to the rig, `HTTP/1.1 101
Switching Protocols`, `Server: Microsoft-HTTPAPI/2.0`.

Three operational facts worth keeping:

- **Dial `localhost`, never `127.0.0.1`.** The endpoint is served by `http.sys` under a wildcard
  prefix, which rejects an IP-literal `Host` header with `400 Bad Request - Invalid Hostname`.
  A hostname-form `Host` passes. No hosts-file entry or header override is needed, only the URL
  form.
- **Target the MAIN server, not its buddy.** The rig is a main/buddy pair. A buddy refuses MOS
  traffic while main is available, answering everything with "Buddy server cannot respond because
  main server is available" -- the same behaviour seen in the multi-vendor corpus (§16).
- **The Windows service is `APNOMService`,** though the process is `NomService`. Checking the
  service by the process name reports "not running" while it plainly is.

### What NOM 9.7 said, and what OpenMOS did with it

The exchange, verbatim from capture:

```xml
out: <mos><mosID>openmos.probe.mos</mosID><ncsID>DEMO-NCS</ncsID>
     <messageID>1</messageID><reqMachInfo></reqMachInfo></mos>
in:  <mos><mosID>openmos.probe.mos</mosID><ncsID>DEMO-NCS</ncsID>
     <mosAck><objID></objID><objRev></objRev><status>NACK</status>
     <statusDescription>MOS ID is not recognized by this NOM</statusDescription></mosAck></mos>
```

That is the correct answer for an unconfigured device, and it proves the MOS 4 client's framing,
UCS-2BE encoding and envelope all work against 9.7 as well as 9.6. It also proves NOM completes
the WebSocket upgrade before it knows whether it recognises the device.

OpenMOS reported it as **`unknown message type`**, then reconnected. Roughly once a second.
Twenty-six times in twenty seconds, into another team's exception log.

### Defect 1: OpenMOS could not run as a purely outbound client

`main` refused to start unless a listener was enabled. A device that only dials out is
legitimate, and for MOS 4.0 it is the *point*: passive mode exists so a device behind a firewall
opens the connection itself and needs no inbound exposure. Such a device may be unable to listen
at all. The check now counts the outbound client as a transport.

### Defect 2: `Envelope` kept a second, shorter vocabulary than the parser

This is the structural one. There are two envelope implementations. `MosEnvelope`, used by the
MOS 4.0 server, captures the inner operation generically and hands it to `ParseMessage` -- so it
understood all 31 message types the parser knows. `Envelope`, used by the MOS 2.x socket path
**and** the MOS 4 client, had an explicit field per message and listed only 15.

So sixteen messages were unreachable on the socket transport while working on the WebSocket one,
including `roElementAction`, `roMetadataReplace` and `roReadyToAir` -- three the README claimed
worked on both. The loopback tests passed because they call `dispatchRunningOrder` directly with
Go structs, never crossing the parse layer.

That is exactly the divergence the shared dispatcher was built to eliminate, and it survived
because **the dispatcher sits below the parse layer**. Unifying the handlers did not unify the
vocabulary.

`Envelope.Message()` now falls back to `ParseMessage` on a generically captured body, so the two
paths share one vocabulary and the typed list cannot silently fall behind again. The inventory
test from §26 gained a check that every classified message is reachable *through an envelope*,
which is where traffic actually arrives -- and that check is what found all sixteen.

### Defect 3: `mosAck` was mis-shaped and unreadable

`mosAck` had no field in `Envelope` at all, so a refusal could not be carried. Two types both
claimed `xml:"mosAck"`: one XSD-shaped with `objID`/`objRev`, and one with invented
`requestID`/`timestamp`/`source` attributes -- and the parser used the second, silently dropping
`objID` and `objRev`.

Those three attributes are the *same invention* that made a live ENPS reject our `heartbeat` with
`Invalid command: heartbeat requestID="2"` (§12). Heartbeat was fixed without auditing the rest
of the generator, so `mosAck` kept emitting them for months.

Then, with the shape corrected, the refusal was still rejected -- this time as `envelope is
missing messageID`, because **NOM sends a NACK with no messageID**. The validator's own comment
already recorded that behaviour, observed on 9.6, and enforced presence anyway. Acknowledgements
are now exempt inbound: nothing correlates against them, real servers omit the field, and
refusing them means being unable to read the one message that says *why* the peer said no.

OpenMOS now reports:

```
profile 0 handshake failed: peer refused reqMachInfo with NACK:
MOS ID is not recognized by this NOM
```

### Defect 4: a refused handshake was treated as a healthy session

The worst of the four, because its blast radius is somebody else's server. The client reset its
reconnect backoff whenever the *socket* connected, not when the *session became usable*. Against
a peer that accepts the upgrade and then refuses the handshake -- precisely what NOM does for an
unconfigured mosID -- the backoff reset on every attempt and the client reconnected indefinitely
at the initial interval.

The specification's "re-establish as quickly as possible" describes a healthy session dropping.
A peer saying no deserves progressively more patience, not less.

Measured on the live rig: **26 connections in 20 seconds before, 6 in 30 seconds after**, with
the delay growing 500ms, 1s, 2s, 4s, 8s, 16s. Reverting the fix in the test harness produces 28
connections against 6, so the test earns its place.

### Passive mode on 9.7: still unanswered, and honestly so

`MOSOutput.RemoveQueueOut` appears **zero** times in 9.7's entire exception log, against 24
occurrences in a 300-line window on 9.6. That is tempting and it is not evidence. The rig has no
MOS 4 device configured, no passive device, and an empty output queue, so the code path has
almost certainly never run. Absence is consistent with a fix and does not demonstrate one.

Settling it needs a device row with `Passive=1` on a shared demonstration rig belonging to
another team. Three rows there are fully empty placeholders, so amending one would be a smaller
change than adding one, but either way it is their environment and their decision. Nothing was
added.

### Footprint

Connecting at all appends to the rig's exception log; that is inherent and unavoidable. The first
run's reconnect loop wrote about twenty-six entries, which is more than it should have been and
is the reason defect 4 is recorded as the most serious of the four. Subsequent runs wrote six.
No configuration was changed, nothing was restarted, and every port-forwarding session was
confirmed closed afterwards -- including the `session-manager-plugin` child, which survives its
parent and silently held the tunnel open the first time.

## 29. Passive mode on NOM 9.7: not reproduced, and not fixed either

A properly conducted 9.7 test was run on the demonstration estate, going considerably further
than §28 could: matching device rows added to both nodes of the main/buddy pair with verified
identical SHA-256, IIS reset on both, the JSON web-services application pool cycled, both NOM
services restarted (the extra step required when `g_mos` is edited directly), and an approved,
MOS-controlled rundown created and then updated.

OpenMOS reconnected successfully in passive mode.

**No `MOSOutput.RemoveQueueOut` and no `ArgumentOutOfRangeException` appeared** — and that is not
the result it looks like. NOM produced **no per-device MOS log and no observable outbound work at
all**. The 9.6 defect was not reproduced because the code path that throws it never ran.

So the comparison remains open, and the two versions failed in *different* places:

| | NOM 9.6 (§25) | NOM 9.7 |
|---|---|---|
| Device row `Passive=1` | yes | yes |
| Our end connects and holds | yes, with `keepAlive` | yes |
| NCS queued outbound work | **yes, 10 items** | **none** |
| `RemoveQueueOut` throws | 24 times per 300 log lines | never called |

9.6 queued work and could not drain it. 9.7 never queued anything. Only the first is a defect in
the drain path; the second is a question about what makes NOM consider a MOS 4 device an output
target at all.

### The most likely reason nothing was queued

Recorded as hypotheses, not findings, because they were not tested:

- **`StorySend` (field 15).** On the demonstration rig 13 of 14 device rows have this at `0`; the
  reference rig's OpenMOS row has it at `1`. A row copied from a neighbour would inherit `0`, and
  running-order content is exactly what that field gates. This is the leading candidate.
- **`MOSVersion` (field 6).** No row on that rig was MOS 4.x. A device declared `2.8.4` may be
  treated as a socket device, which NOM tries to *dial*, never using the inbound WebSocket link.
- **Provoking the work.** On the reference rig the queue filled because `roReqAll` was issued,
  which queues NCS-side work (§16). A passive connection cannot ask, since ENPS does not read
  requests on it — but a *second, non-passive* connection can, and that two-connection trick is
  how authentic Profile 2 traffic was obtained without GUI access (§25). Creating a rundown may
  simply not be enough on its own.

Either way, teardown was verified: both `g_mos` files restored to their baseline digest, fourteen
rows each, test device absent, disposable rundown removed, all services running.

## 30. A panic on every shutdown, and a race the test for it uncovered

Reported from the 9.7 run: `TCPServer.Shutdown panics with close of closed channel`.

Not an edge case, and not intermittent in cause. `TCPServer.Start` ends with:

```go
<-ctx.Done()
return s.Shutdown(context.Background())
```

and `main` does:

```go
cancel()                                  // Start's <-ctx.Done() fires: Shutdown, call one
tcpServer.Shutdown(context.Background())  // Shutdown, call two
```

Both paths run on **every ordinary shutdown** and race to close the same channel. Whichever
arrives second panics. `close(s.shutdownCh)` had no guard at all.

`WSServer` had the same two-caller shape with a guard that looks correct and is not:

```go
select {
case <-s.shutdownCh:
	return
default:
}
close(s.shutdownCh)
```

Two callers can both observe the channel open and both proceed to close it. That narrows the
window without closing it. Both now use `sync.Once`.

Writing the concurrency test then exposed a **second, pre-existing race**: `s.httpServer` is
assigned by `Start` and read by `Shutdown` with no synchronisation, which the detector reports as
soon as `Shutdown` is called concurrently. `Serve` now runs on a local reference and the field is
guarded by a mutex.

Both faults were confirmed by reverting the fixes: the unguarded close reproduces
`panic: close of closed channel`, and the select-and-default guard reproduces `WARNING: DATA RACE`
under `-race`. The suite is now race-clean across three consecutive runs, where a single run had
been passing before.

## 31. MOS 4.0 admission requires a 2.x version string, and a distinction that changed a result

Two findings from a two-connection live test on NOM 9.7, run on the demonstration estate by
another agent with the operator's authorisation. The harness is reusable and stays untracked.

### `MOSVersion=4.0` is refused at admission

Setting the device row's `MOSVersion` (field 6) to `4.0` makes the MOS 4.0 WebSocket upgrade
return **HTTP 403**. `2.8.4` is required for admission on this build.

That is worth stating plainly because it is counterintuitive and because this document
previously suggested the opposite. §29 offered, as a hypothesis for why the rig produced no
outbound work, that a device declared `2.8.4` might be treated as a socket device and therefore
dialled rather than reached over the held WebSocket. The reverse is true: `4.0` is rejected
outright, and the version string that *works* for MOS 4.0 traffic is a 2.x one. The hypothesis
is withdrawn.

It also means the `MOSVersion` column does not select the transport generation in the way the
field name suggests, at least not on this build. Transport is determined by how the device
connects; this field gates something else.

### A `roList` is a response, not NCS-originated output

The test held a passive connection, opened a second standard connection for the same device,
completed Profile 0, sent `roReqAll`, received `roListAll` (1296 bytes) proving one running order
existed, sent `roReq` for it, **dropped the standard connection immediately**, then waited twenty
seconds for the running order on the passive connection. It did not arrive.

That is a clean, deterministic, reproducible observation. It is probably not a defect.

Responses in MOS return on the connection the request arrived on. Two pieces of evidence, one
from each version:

- On 9.7, in this very run, `roListAll` came back on the **standard** connection, not the passive
  one — the same request-response pattern.
- On 9.6 (§25), a `roReq` sent on a non-passive connection while a passive connection was held
  produced a `roList` of 5,320 bytes **on the requesting connection**. The passive capture
  directory contained only our own outbound frame.

So dropping the requesting connection immediately after `roReq` most likely means NOM built a
reply for a socket that had gone, and the reply died with it. Nothing in the specification or the
vendor documentation claims a reply is re-routed to a different connection.

**The distinction that matters:** passive mode is about `MOSOutput`, and `MOSOutput` carries
NCS-**originated** traffic — the `roCreate`, `roStorySend` and `roElementAction` generated when a
rundown changes. The vendor documentation says ENPS "creates a MOSOutput and will use this
connection for messages to the MOS device". A `roList` answering our own `roReq` is not
originated output; it is a reply within an exchange we started.

That is also what 9.6 demonstrated. The ten items in `H:\NOM\MOS\OUT\<device>` were originated
running-order content, and `RemoveQueueOut` was failing to **drain** them. The 9.6 defect lives in
the originated-output path, which this test never reaches — which explains the zero
`RemoveQueueOut` occurrences without implying a fix.

### What would actually settle it

Hold **only** the passive connection, with no second connection at all, and then cause ENPS to
originate work by modifying an approved MOS-controlled rundown in the client.

- Originated `roStorySend`/`roElementAction` arrives: passive mode works on 9.7, and 9.6's
  problem is purely the drain crash.
- It does not arrive: a genuine passive-output failure, no longer explicable as reply routing.

That requires a rundown edit in the ENPS client, which is a human action. Until it is run, passive
mode remains unproven on both versions and the README says so.

## 32. mosScope enforced, and the emission it turned out to be missing

The task was to act on `mosScope` rather than merely carry it. Implementing it exposed a larger
defect underneath: there was nothing to filter, because the metadata was never emitted at all.

### The dead half of §23

§23 fixed `mosExternalMetadata` being discarded three ways on ingest, and the README gained a row
saying the payload was preserved verbatim. That was true of *storage* and untrue of the *wire*.

`restoreExternalMetadata` -- the conversion from stored blocks back to wire form -- was **dead
code**. Defined, never called, not referenced by a single test. Go does not complain about an
unused function, so it compiled cleanly for as long as it existed. `storyInfosFor` built
`StoryInfo` and `ItemInfo` values with no metadata field set, and `CreateROList` had no
running-order-level field to set.

So every `roList` OpenMOS produced dropped all vendor metadata. That matters most in the one place
it is least visible: `roList` is the payload of pull recovery, where a peer rebuilds its state from
ours. The loss was silent and nothing compared what went in against what came out.

The round-trip test now does exactly that comparison -- ingest a `roCreate` carrying blocks at all
three levels, ask for it back with `roReq`, and inspect the `roList` produced. Reverting the
emission fails it with "the wire emission is missing, not merely misfiltered".

This is the same lesson as §26, one layer down: a function existing is not a workflow. The message
inventory test catches an unhandled *message*; nothing was watching for an unreachable *field*.

### The scope rule is a hierarchy, not a switch

The README previously described the next step as stripping `STORY`-scoped blocks from
running-order construction messages and keeping `PLAYLIST` ones. That is too coarse, because a
running-order construction message has three levels and the rule differs per level.

Per `doc/mos-protocol-source-synthesis.md`: `OBJECT` "stays with object/list/search use", `STORY`
"may enter an item reference in a story", `PLAYLIST` "may also enter running-order construction
messages". Each scope permits everywhere the narrower one does, plus one more context:

| level | `OBJECT` | `STORY` | `PLAYLIST` |
|---|---|---|---|
| running order | no | no | **yes** |
| story | no | **yes** | **yes** |
| item | no | **yes** | **yes** |

`OBJECT` is excluded from all three: a running order is not object, list or search use. An
`OBJECT`-scoped block belongs with `mosObj` traffic, which OpenMOS does not implement.

### Enforced on emission, never on storage

Scope is applied to what OpenMOS **emits**. Inbound blocks are stored verbatim whatever their
scope, because discarding metadata a peer sent us would break the project's lenient-inbound rule
and lose data we may have to hand back. A test pins both halves: storage keeps all three scopes,
the wire carries only what each level permits.

Two deliberate leniencies, both recorded in the code:

- **An absent `mosScope` is kept.** The element is optional, and omitting it is not a request to
  be discarded.
- **An unrecognised scope is kept**, and comparison is case-insensitive after trimming. The
  payload is opaque to us either way, so silently dropping an unlabelled block is the worse
  failure. Rejecting `Playlist` would be pedantry that costs real data.

Filtering happens at the call site *and* inside `CreateROList`, so a caller that forgets cannot
emit a `STORY`-scoped block at running-order level.

Both behaviours were verified by reverting them: removing the emission fails the round-trip test,
and making the filter permit everything fails the hierarchy test on `OBJECT`, `STORY` and `Story`
plus the round trip.

## 33. Running orders now persist by default

Protocol state has survived a restart since §27 -- the outbound `messageID`, deduplication
receipts and unfinished discovery work. The rundown itself did not, and that is the piece whose
loss is hardest to notice.

A restart left OpenMOS silently disagreeing with the NCS about what it holds. The NCS has no
reason to say again, so nothing surfaces the divergence until something breaks. That is exactly
how the `roStorySend` defect in §13 stayed hidden: the running order was gone from our side, and
fabricating one looked like success.

`storage.backend` now defaults to **`file`**. `memory` and `mongo` remain available.

### A decorator, not a third backend

`OpenDurable` wraps the in-memory repositories and writes a snapshot after every successful
mutation. It does not reimplement storage.

That choice is deliberate. The in-memory repositories already implement all 22 interface methods
correctly, including the element ordering that was itself a defect once: the in-memory backend
used to return Go map order while Mongo sorted properly, so the *default* backend silently
reordered rundowns. Reimplementing that query logic a third time would invite the same divergence
back. This composes the existing implementation, exactly as `FileDedupStore` composes
`MemoryDedupStore`.

Snapshot rather than append log, for the opposite reason to dedup: a rundown changes far less often
than a message arrives, and interop-scale rundowns are tens of stories. Dedup needed a log because
it writes on every inbound message; this does not. Temp-file-and-rename, as with the `messageID`
mark.

### Details that matter

- **The persisted shape uses slices, not maps.** A map would discard order, reintroducing the
  §13-era defect through the back door. The restart test sets each story's `Order` field
  deliberately *against* alphabetical ID order, so an implementation that sorted by ID rather than
  honouring the NCS-supplied sequence fails instead of passing by coincidence.
- **Snapshots are taken by walking the public list methods**, not by reaching into the memory
  repositories. Whatever the repositories would actually return to a peer is what gets saved.
- **Deletions persist.** A snapshot that only grew would resurrect deleted state, which is worse
  than not persisting at all: the NCS would be told we hold a running order it has removed. Tested.
- **A corrupt snapshot is skipped, not fatal.** Local state starts empty and pull recovery rebuilds
  it by asking the NCS. Persistence stays enabled for the rest of the run, and a write afterwards
  reloads correctly -- both tested.
- **Objects are not persisted.** OpenMOS implements no object workflow, so there is nothing durable
  to keep and pretending otherwise would imply support that does not exist.
- **An unusable state directory degrades loudly to memory** rather than refusing to start, matching
  `internal/messageid` and `FileDedupStore`.

### The tests still use memory

`memory` remains the backend the test suite selects, because a test that writes to disk is a test
that leaks between runs. The durable path has its own tests using `t.TempDir`, and the suite was
checked for stray `runningorders.json`, `dedup.log` and `discovery.json` files afterwards.

The config default was changed in **both** places it is set, before the YAML load as well as in the
environment fallback. Setting only the fallback would leave "YAML loaded but key missing" resolving
to the zero value -- the trap recorded in this project's steering that has caused three separate
failures.

## 34. One `roReq` outstanding per lane

MOS 4.0 §4.1: a sender "must not send another message on the same port until the previous message
is acknowledged; the two ports are independent". The discovery walk in §27 honours this within
itself, but there were two independent senders of `roReq`:

- `sendDiscoveryReq`, serialised by the walk
- `requestResync`, rate-limited by `resyncGuard` but not serialised against the walk

So a divergence arriving mid-walk produced **two concurrent requests on the same lane**. Both were
individually well behaved; together they broke the rule.

The walk is now the sole owner of outbound `roReq`. Recovery enqueues instead of sending, so there
is exactly one outstanding by construction rather than by discipline.

**Recovery jumps the queue.** It goes to the front, not the back: the peer is actively sending us
messages about a running order we do not hold, while the walk is catching up on state nobody is
asking for yet. Tested — after the in-flight request completes, the recovery identifier is next,
ahead of the remaining discovery work.

**Why serialising is safe here:** the walk already has a deadline (§27). A gate with no timeout
would turn one lost acknowledgement into a permanently stuck lane, which is worse than the overlap
it prevents — the same failure shape as the wedged client in §25 and the stalled walk in §27. The
in-flight request either completes, which drains the queue, or times out, which also drains it.

Verified by reverting: sending recovery directly again fails the test with
`recovery sent a second concurrent roReq: [RO-1 RO-DIVERGED]`.

### What is deliberately not gated

`resyncGuard` still applies first, as a loop-breaker. It answers "should we ask at all", the walk
answers "may we ask now"; they are different questions and both are needed.

Periodic `heartbeat` still fires on its timer regardless of an outstanding `roReq`. Heartbeat is
Profile 0 liveness machinery, and gating it behind running-order work would defeat its purpose:
a peer that stopped answering `roReq` is exactly when liveness detection matters most. The README
claims the rule for the request family OpenMOS originates in volume, not universally.
