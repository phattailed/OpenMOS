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

> **Historical result; superseded by §35.** The OpenMOS defects found here were real, but the
> remaining "NCS-side wall" was tested with the wrong NOM topology. Correctly configured
> device-initiated passive delivery is now live-proven on this same NOM 9.6 build.

§24 established from vendor documentation what passive mode means. This is what happened when
it was actually tried, with **no reverse tunnel** — only a forward tunnel, so the NCS could not
reach this machine at all. Anything arriving had to come down the connection we opened.

At the time, passive mode was not yet proven live. The attempt found three defects in our own
implementation and NCS-side stack traces; §35 later separated those valid observations from the
incorrect configuration conclusion.

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

That proposed comparison was later run, but with the same reversed topology. §35 contains the
first controlling live proof.

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

> **Superseded by §35.** This run used `MOSVersion=2.8.4`, `Passive=1` while the MOS device
> initiated the connection. NOM's implementation and the MOS 4 sequence define that row as the
> opposite topology: NOM initiates `passive=true` connections to the device's configured URL.
> The negative result therefore does not establish a NOM 9.7 passive-output defect. The original
> account remains below because it records how the incorrect conclusion was reached.

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

## 31. MOS 4.0 admission was misread, and a response was mistaken for originated output

> **Admission conclusion superseded by §35.** `MOSVersion=4.0` was not itself the cause of the
> 403. The row also had `Passive=1`, which tells NOM to initiate passive connections toward the
> MOS device. NOM consequently rejected a device-initiated connection for that row. With the
> production-realistic device-initiated topology -- `MOSVersion=4.0`, `Passive=0` -- NOM 9.6
> admitted the same `passive=true` client and delivered originated output.

Two findings from a two-connection live test on NOM 9.7, run on the demonstration estate by
another agent with the operator's authorisation. The harness is reusable and stays untracked.

### Original observation: `MOSVersion=4.0` plus `Passive=1` was refused at admission

Setting the device row's `MOSVersion` (field 6) to `4.0` while leaving `Passive=1` made the MOS
4.0 WebSocket upgrade return **HTTP 403**. At the time this was incorrectly attributed to the
version field alone. §35 traces the complete call path and shows why the topology was rejected.

The observation was real; its interpretation was not. A 2.x string happened to bypass NOM's
MOS-4 passive-device list, allowing a connection under a configuration that later fell back to
legacy socket behavior. It was not proof that a 2.x version is required for MOS 4 traffic.

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

### What would actually settle it -- completed in §35

Hold **only** the passive connection, with no second connection at all, and then cause ENPS to
originate work by modifying an approved MOS-controlled rundown in the client.

- Originated `roStorySend`/`roElementAction` arrives: passive mode works on 9.7, and 9.6's
  problem is purely the drain crash.
- It does not arrive: a genuine passive-output failure, no longer explicable as reply routing.

That human-driven test is now complete on NOM 9.6; see §35.

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

## 35. Passive mode works live: the row flag names the initiator, not the accepted socket

The discriminating human-driven test from §31 was completed on the reference NCS running NOM **9.6.2.2026** on
2026-08-29. It overturns three earlier conclusions:

1. Device-initiated passive delivery works on this NOM version.
2. `MOSVersion=4.0` is valid; the earlier 403 came from combining it with the opposite passive
   topology.
3. The `ncsID` key in `MOS4WebSockets.dll` is not the key NOM uses to route its output queue.

### Two valid passive topologies

MOS 4 says the system protected inside the firewall opens the WebSocket client connection to the
externally reachable listener and adds `passive=true`. The receiver then uses that connection for
traffic back through the temporary opening. Either the NCS or the MOS may be the protected
initiator; the configuration must say which one.

NOM's `g_mos` `Passive` field selects **NOM-initiated** passive mode:

| topology | NOM `MOSVersion` | NOM `Passive` | `IP` / `MOSDeviceURL` | connection initiator |
|---|---:|---:|---|---|
| NOM initiates passive connections | `4.0` | `1` | required, full MOS WebSocket URL | NOM |
| MOS initiates a passive connection | `4.0` | `0` | not needed for that connection | MOS device |
| Standard MOS 4 outbound from NOM | `4.0` | `0` | required | NOM |

This is visible in the 9.6 NOM code. `InitializePassiveConnectionsAsync` selects rows whose
`MOSVer` starts with `4`, whose `Passive` value is non-zero, and whose `IP` is non-empty. It then
dials the configured URL twice, once each for `ro` and `mom`, with `_incoming=true`; the DLL emits
that value as `passive=true` in the query string. The row's `IP` property is the DLL's
`MOSDeviceURL`; it is not a separate configuration field.

The same rows populate `PassiveMOSList`. NOM's listener rejects an inbound connection whose
`mosID` is in that list. The error says the device "requested an active connection" without
testing the query's `passive` value, so the wording is misleading, but the rejection itself is
consistent: the row says NOM, not the MOS device, will initiate the passive connections.

The relevant admission logic is the same in the inspected 9.6.2.2026 and 9.7.0.65
`MOS4WebSockets.dll` assemblies. The earlier 9.7 configuration set `MOSVersion=4.0`, `Passive=1`
and also had OpenMOS dial NOM with `passive=true`. Both sides were configured as the passive
initiator, so the resulting HTTP 403 is not evidence of an admission defect.

### The production-realistic live test

The reference NCS's device row kept its normal unique identifier and was configured as:

```text
MOSID=openmos.example.mos
MOSVersion=4.0
Passive=0
IP=<blank>
StorySend=1
```

OpenMOS connected to NOM's `/MOS4NCS/` endpoint with:

```text
mosID=openmos.example.mos
ncsID=NCS-HOST
channel=ro
passive=true
```

NOM admitted the connection without a 403. A human activated a MOS-controlled StorySend rundown,
causing twelve originated items to appear for `openmos.example.mos_ro`. After NOM was restarted,
OpenMOS reconnected at 16:11:31 and immediately received unsolicited `roStorySend`,
`roReadyToAir` and `roCreate` traffic. NOM's UI showed repeated `Sent:` entries and the queue fell
from twelve to two. The final two were waiting for application replies after OpenMOS reported
that it did not yet hold the running order; that is a state-synchronisation issue, not a passive
transport failure.

NOM independently recorded the transmission in:

```text
H:\NOM\LOGS\MOS-openmos.example.mos-20260829.xml
```

The file was created at 16:11:32, last written at 16:13:03, and was 85,078 bytes with SHA-256
`3D95106E06ECA61315491122F3E61BA10E43A5BD806459B1A57781CEDE7250BA`. It contains four
`roCreate`, twenty-two `roStorySend` and two `roReadyToAir` tag occurrences. Those are XML-log
occurrences, not claimed as distinct message counts; NOM may log more than one representation of
an exchange.

### Why the dictionary theory was wrong

Decompiling `MOS4WebSockets.dll` showed that its server stores a received `passive=true` socket in
an internal dictionary under `ncsID_channel`, while its generic send method searches
`mosID_channel`. That looked like the cause of the undelivered queue.

It is not NOM's output route. `MOS4WebSockets` raises `ConnectionEstablished` before returning
from admission. NOM's parent `frmMOS` handler receives the socket and calls:

```text
AddMOSWebSocketOut(e._mosID, e._mos4Channel, e)
```

That attaches it to NOM's own `mcolMOSOut` under `mosID_channel`, which is the collection
`MOSOutput` uses. The live GUI displayed this correct `openmos.example.mos_ro` association, and
the successful delivery proves that path is effective.

An earlier artificial experiment made `mosID` and `ncsID` both `NCS-HOST`. It also delivered
traffic, but equality was coincidence rather than a fix. The experiment is not a valid production
configuration because MOS and NCS identities must remain distinct, and it cannot demonstrate
which dictionary routed the messages. The later unique-ID test is the controlling result.

### Evidence hygiene and teardown

The OpenMOS runtime logged the received operations, and NOM's XML log independently records the
transmissions. The raw-capture manifest written by this OpenMOS client run listed only outbound
frames despite the live receive events. That is an instrumentation gap: do not cite that manifest
alone as proof that no inbound traffic occurred. It needs separate investigation before raw
capture is used as the sole oracle for passive-client tests.

After the test, the client and SSM tunnel were stopped. the reference NCS's `g_mos` was restored
byte-for-byte to SHA-256
`EF76424512CF37E4A8FC909EC01EE7E32585857F51B50654A5A7B741D5EF3CE4`, IIS reset completed, and
Watch restarted `NOM.exe` with a fresh PID. The reference NCS has no buddy node.

### Consequences

- Passive MOS 4 output is **live-proven on NOM 9.6.2.2026** with distinct IDs and no
  `MOSDeviceURL`.
- The earlier NOM 9.7 negative result used the wrong topology and must not be used to claim a 9.7
  passive defect or fix. Repeat it with `MOSVersion=4.0`, `Passive=0`.
- The HTTP 403 from `MOSVersion=4.0`, `Passive=1` is consistent with NOM being configured to dial
  the MOS endpoint; it is not a bulletproof functional bug. Its error text may still merit a small
  diagnostics issue.
- The `ncsID_channel` versus `mosID_channel` internal dictionary mismatch is not the demonstrated
  cause of NOM output failure.
- Any work item asserting that passive delivery is broken on these earlier configurations needs
  its evidence and scope corrected before engineering acts on it.

## 36. NOM 9.7.0.85: originated output queues, but does not reach the passive client

The corrected device-initiated topology was exercised on the development primary on
2026-09-09. This run reaches the output-generation path missing from the earlier 9.7 tests:
a human activated the disposable MOS-controlled StorySend rundown and saved an edit.
No `roReq` or `roReqAll` was used to manufacture the output.

### Verified configuration and runtime

- `NOM.exe`, `NomService.exe`, and `MOS4WebSockets.dll` all report **9.7.0.85**. The active
  process is `NomService`, controlled by `APNOMService`; this differs from the interactive
  `NOM.exe` process used for the successful 9.6 test. That difference is an investigation
  lead, not an established cause.
- The test row has exactly 37 actual-tab-separated fields, UTF-16LE encoding, `MOSVersion=4.0`,
  `Passive=0`, `StorySend=1`, `PreserveExternalMetadata=1`, and a blank endpoint. Its MOS ID
  and the NCS ID are distinct. The row was verified again after restart.
- OpenMOS uses the `ro` channel and initiates the connection to `/MOS4NCS/` with `passive=true`.
  Its listeners are disabled; no reverse tunnel or second requesting MOS session is present.
- OpenMOS runs commit `e2c3811`, with file-backed storage and an isolated state directory.

For exact-build follow-up, the server artifacts were hashed on the host:

| Artifact | SHA-256 |
|---|---|
| `NOM.exe` | `C808CB86238D2857D0CF7011911BB3CEDAC890A6B995D801A8E4BE70A1EA76FC` |
| `NomService.exe` | `D823414C737912AA553881506F52A6F1B61664861BB489DDFB281CFC3566AA6D` |
| `MOS4WebSockets.dll` | `79EFB11FD4F9EAFE9D3B7BFF6A6632BAA9D3EA67B697974BC1041FE5BF0E5563` |

### Setup failures excluded from the controlling observation

The initial configuration-edit script incorrectly used PowerShell `-join "\t"`; that writes
literal backslash-t text, not tab separators. Its read-back compared generated text rather than
parsing the stored columns, so it missed the error. That row was repaired from the verified
backup using `[char]9`, preserving unrelated current rows. Both the serialized result and disk
read-back were then parsed and checked for all 37 fields and the intended values. A PowerShell
check now rejects the original separator mistake. The earlier failed action is invalid evidence
against NOM.

The overnight SSM tunnel also became unresponsive while retaining its local listener and
allowing local keepAlive writes. An ordinary HTTP request through it timed out after six seconds.
A fresh forward to the same primary returned HTTP 200 in approximately 55 ms. The old tunnel
was terminated, and OpenMOS was restarted through the fresh tunnel before the observation below.
A subsequent independent HTTP probe through that fresh tunnel also returned 200. Listener
existence and outgoing keepAlive capture alone are therefore insufficient tunnel-health proof.

### Controlling observation

All times below are UTC. At 14:55:56 the device's output directory contained 41 files, rising to
42 by 14:56:26. Safe XML parsing of those files found **one `roCreate`, 39 `roStorySend`, and two
`roElementAction` messages**, all for the configured device and one running order. There were
no XML parse failures. The queue totaled 138,608 bytes.

OpenMOS was admitted through the fresh tunnel at **14:58:11**. At 14:58:20 all 42 files remained,
no per-device NOM XML log existed, and OpenMOS had no saved running-order snapshot or handler
error. Its raw capture still has the inbound-recording gap described in §35, so absence from
that capture is not used alone as proof of non-delivery.

A single controlled restart of `APNOMService` at **14:59:01–14:59:03** tested the queued-at-startup
sequence without deleting or modifying the queue. The process ID changed, configuration stayed
unchanged, and OpenMOS reconnected at **14:59:06**. At **14:59:36**, all 42 messages remained,
with the same type counts and byte total. No running-order snapshot had appeared locally.

NOM's device-tagged exception entries repeatedly report `System.UriFormatException` while
opening an outbound WebSocket, in `MOS4WebSockets.MOSClientWebSocket.OpenClientWebSocket`,
reported source line 91. The error says the URI format could not be determined. The inspected
1,500-line exception windows contained no `RemoveQueueOut`, `ArgumentOutOfRangeException`, or
`WebSocketException` occurrences. Those bounded observations do not establish that an earlier
defect was fixed.

### Conclusion and follow-up

The delivery test is **red** for this configuration on 9.7.0.85: real originated messages are
queued but are not applied by the passive client, even after a fresh tunnel/connection and a
service restart. Admission succeeds; the failure occurs later. This is a different observed
failure from the old 9.6 queue-removal exception and does not overturn the successful §35 test.

Exact-build tracing in §37 identifies two defects in the accepted-socket handoff that explain
the outbound-URL fallback. A repaired build has not been tested. Neither the old generic-DLL
dictionary theory nor a product-wide passive-mode defect has been established by this run.
No new ADO conclusion was posted. Live persistence/restart recovery remains untested because
no running order reached local storage.

Private process identifiers, full configuration hashes, recovery details and log locations are
kept in the ignored test run directory. No queue files, story bodies, credentials, or raw vendor
logs are included here.

## 37. NOM 9.7.0.85: the accepted passive socket is not handed to the output worker

Read-only analysis on 2026-09-09 traced the §36 failure through the exact assemblies used by
the running service. This is a concrete listener-to-output wiring defect, distinct from the
earlier dictionary-key theory. The live delivery test remains **red**; no vendor binary was
patched and no repaired-build result is claimed.

### Exact-build and symbol checks

The live process's loaded-module list identifies `NomService.dll`, `NOM.dll`, and
`MOS4WebSockets.dll` as 9.7.0.85, at the same paths whose file hashes match the local copies.
`NomService.exe` is a native launcher; `NomService.dll` calls `Nom.Main` in `NOM.dll`, where
the output implementation resides. The service-versus-interactive distinction alone is not
an established cause.

The `MOS4WebSockets.dll` hash is recorded in §36. Additional exact inputs:

| Artifact | SHA-256 |
|---|---|
| `NOM.dll` | `0D73C428286439617BF0070D381E1A730C2088CCD25F1407FA9CC2514C80C7D5` |
| `NomService.dll` | `719515978F5F6B6EC07BCF720536C1CCEC3D1C129EED922B1FDB4033F056947B` |
| `NOM.pdb` | `4A9DB322B58A21105E46C287038BB603B295EABE950E4F551F6703988EBE3078` |
| `MOS4WebSockets.pdb` | `5F7B954BE8512CCB160594E31A15265027DE7DE6B796299C2178A7E7F959B669` |

Both PDBs match their assembly's CodeView GUID and age. The findings were checked in IL as
well as reconstructed C#, with matching-PDB sequence points. The source references below
are **original source filenames and lines from those symbols**, not generated C# line numbers.
Raw assemblies, symbols, decompiled source and build-machine paths remain in ignored scratch.

### Where the handoff stops

`MOSServerWebSocket.ProcessRequest` maps query `passive=true` to `_incoming=false`, accepts
the WebSocket and raises `ConnectionEstablished`. The WebSocket manager relays that event
to NOM's listener wrapper. Admission is therefore not the missing step.

| Source breadcrumb | Compiled behavior |
|---|---|
| `MOSMain.vb:347–349`, `StartProcessing` | Creates a new listener `MOSSocket`, calls `MOS4_Listen`, then `InitializePassiveConnectionsAsync`. It does not subscribe to that instance's `MOS4_ConnectionEstablished` event. |
| `MOSSocket.vb:391–396`, `MOS4WebSockets_ConnectionEstablished` | For an accepted passive socket, it only raises the wrapper's instance event. With no subscriber it returns. The opposite direction has a direct call to the input processor. |
| `MOSOutput.vb:147,155`, constructor | Creates a **different** `MOSSocket` for an output worker and subscribes to that object's event. This is the sole compiled call to `MOSSocket.add_MOS4_ConnectionEstablished` in `NOM.dll`; it does not wire the listener. |
| `MOSMain.vb:3290`, three-argument `AddMOSSocketOut` | If an output entry already exists for `mosID_channel`, it returns without attaching the supplied socket. This would also obstruct queued-before-connect or reconnect handling after the listener wiring is repaired. |

The key IL checks are small and unambiguous: `StartProcessing` creates its listener at
`IL_0475`; the only event subscription is in `MOSOutput`'s constructor at `IL_016b`.
The listener's callback branches from its null-subscriber check at `IL_0010` to `ret` at
`IL_0036`. The existing-output check branches at `IL_0051` directly to `ret` at `IL_0196`.
The wrapper's constructor only calls the base constructor; it installs no hidden subscriber.

### Why the blank-URL exception follows

Originated messages reach `MOSMain.AddQueueOut`, which creates an output entry without a
socket. `MOSOutput.Setup` delegates to `MOSSocket.Setup`; for MOS 4, that constructs an
outbound `MOSClientWebSocket` and takes its base URL from the device's `IP` field
(`MOSSocket.vb:260`). The output loop then tries to connect through that object
(`MOSSocket.vb:299`). The accepted listener socket was never substituted.

With the verified blank endpoint, URL construction in `MOSClientWebSocket.OpenClientWebSocket`
fails. Its matching PDB maps the URL expression and URI construction to
`MOSClientWebSocket.cs:91–92`, consistent with the exception family observed in §36.
This links the missing handoff to the observed outbound-URL fallback; it is not evidence
that a device-initiated passive connection requires a separately reachable device URL.

### Comparison and scope

The working 9.6.2.2026 `NOM.exe` was rechecked in IL, SHA-256
`55CE8C4CA116D9D5331BA92577D7A4361D2CB48ED5D954C2CD1A8F9742863D06`.
Its listener callback directly calls `frmMOS.AddMOSWebSocketOut` and then the output
connection handler. Its registration method also replaces the socket in an existing output
entry. Both behaviors are absent from the traced 9.7.0.85 path.

At 15:29:59 UTC the live queue still held the same 42 messages and 138,608 bytes, and the
configuration hash was unchanged. The local client still had no running-order snapshot;
an independent HTTP probe through its tunnel returned 200. These corroborate §36 but do
not substitute for a repaired-build test.

**Direct evidence:** missing listener subscription and missing existing-entry socket update
in this exact build. **Strong causal explanation:** the accepted passive socket cannot reach
the output worker through this path, leaving the queued messages on an outbound client with
a blank URL. Other defects are not excluded, and other 9.7 builds have not been examined.

For a vendor repair, wire the accepted outgoing socket into registration by distinct
`mosID_channel`, attach or replace it without discarding queued work, and run the output
connection handling. Retest both orders: connect first then have a human activate/edit the
test rundown; and generate queued work first then connect or reconnect. Require actual
delivery, acknowledgment and queue drain, corroborated by the client's stored running order.
No `roReq`-then-disconnect experiment, equal-ID workaround, or URL/configuration change is
needed to test this handoff. No ADO item was changed by this investigation.

## 38. Passive delivery works on 9.6, and standing an appliance up found two of our bugs

A standing appliance was built on a small Linux instance inside the same VPC as the reference NCS
(NOM **9.6.2.2026**), running as an unattended service, MOS 4 passive client only, no listeners and
no inbound firewall rules. Device row per §35: `MOSVersion=4.0`, `Passive=0`, blank endpoint, and
the device dials with `passive=true`.

**§35 is confirmed with production traffic.** A human activated a MOS-controlled rundown in the
ENPS client and ENPS pushed it down the connection the appliance had opened: one `roCreate`, one
`roReadyToAir`, and nine `roStorySend`, carrying real running-order and story identifiers. Nothing
was solicited. Passive output delivery on 9.6 is not in doubt.

Two of our own defects were exposed in the process, and neither was reachable by any existing test.

### `roCreate` was unhandled on the client, and it took the whole rundown with it

The client logged:

```
MOS 4 client received unhandled message type roCreate from ncsID=...
```

`roReadyToAir` and every `roStorySend` routed correctly through the shared dispatcher. `roCreate`
did not, because `roCreate` was never in the shared dispatcher — it was per-transport, on the
reasoning that deduplication scope differs between transports.

That reasoning confused two layers. Deduplication happens in the transport **above** dispatch, using
that transport's own scope; the application step below is identical. Keeping the application
per-transport bought nothing and cost the client completely, because the client has no per-transport
`roCreate` handler of its own.

The consequence is a cascade, and it is exactly what the protocol prescribes: with no running order
created locally, every following `roStorySend` referenced an unknown `roID`. The appliance persisted
a 48-byte snapshot with **zero running orders, zero stories, zero items** — from a rundown that had
arrived intact.

The client's own dispatch comment already claimed a pushed `roCreate` would be applied rather than
dropped. It is now true.

### Inbound frame capture never ran on the passive path

While twelve messages were being received, the capture directory recorded **zero inbound frames**.

Capture lived only in `readMessage`, which is the *handshake's* reader. Passive mode deliberately
skips the handshake (§25), so `readLoop` was the only reader in use — and it decoded and dispatched
without recording anything. §35 had already noticed an "inbound-recording gap" and correctly
declined to treat capture absence as proof of non-delivery; this is that gap, located.

For an appliance whose purpose is producing evidence, silently capturing nothing is worse than the
delivery bug it was concealing: it makes a negative result indistinguishable from a broken
instrument. Recording now happens in the read loop, before parsing, on the same discipline as
elsewhere — a frame that fails to parse is the most valuable one to keep.

### Why no existing test caught either

`TestClaimedSharedMessagesReallyAreShared` verifies what the shared dispatcher recognises, and
`roCreate` was *honestly* classified as per-transport, so it passed. The inventory from §26 asks
"is this message handled?" but not "can the client handle the family it exists to receive?" The
blind spot was one layer over from the one that test closed.

Both are now covered, and both were confirmed by reverting the fix: removing `roCreate` from the
dispatcher reproduces the live failure with the message *"a passive client would log it as
unhandled and drop the whole rundown"*.

### A transient worth recording, because it looks like §37

Mid-test, NOM's own MOS status showed the device as **`Disconnected` with 14 queued** while an
`ESTAB` socket existed at OS level between appliance and NCS. That resembles the 9.7 handoff failure
in §37. It was not: after the client reconnected, NOM attached the socket and the queue drained. So
the queued-before-connect obstacle §37 predicts did **not** bite on 9.6, which narrows §37's scope to
9.7 rather than broadening it.

### Operational notes for rebuilding the appliance

- **SSM Run Command executes without `HOME`**, so Go cannot derive `GOMODCACHE`. Set `GOPATH`,
  `GOMODCACHE` and `GOCACHE` explicitly or the build fails with "module cache not found".
- **`nhooyr.io/websocket`'s vanity domain no longer resolves** — the package moved to
  `github.com/coder/websocket`. `GOPROXY=https://proxy.golang.org` is required so Go does not
  attempt a direct fetch of a dead host. This is a latent fragility in `go.mod` worth migrating off.
- The ingress rule permitting the appliance to reach the NCS was added **by hand to a
  CloudFormation-managed security group**. Template and reality now differ, so a stack update could
  revert it and the appliance would silently stop connecting.

## 39. keepAlive is excluded from capture, because arithmetic

The standing appliance from §38 produced hard numbers within an hour: **92 captured frames in 45
minutes, every one of them an outbound `keepAlive`.**

At one every thirty seconds that is roughly **2,880 a day against a 2,000-frame cap**. Capture would
therefore have stopped some time overnight, and any real running-order delivery after that point
would have left no evidence at all — the precise failure that made a genuine passive delivery look
like a non-event in §38, reintroduced by a different route. The least informative message the
protocol has would have consumed the entire evidence budget.

`keepAlive` is now excluded by default.

Excluding it costs nothing. MOS 4.0 §4.1.1 gives `keepAlive` no `messageID` because it is
unsequenced, it requires no reply, and its only purpose is holding a connection open through
firewalls. Its arrival is already visible in the service log. Nothing is learned from the 2,879th
one on disk.

Three details worth recording:

- **The filter lives in the recorder, not at the call sites.** There are five `Record` call sites
  across three files; filtering in five places is how one gets missed, which is the same mistake
  that produced the transport divergence in §28 and the unhandled `roCreate` in §38. One rule in one
  place applies everywhere by construction.
- **It is a substring test, not a parse.** `Record` deliberately runs *before* parsing, because a
  frame that fails to parse is the most valuable one to keep. Capture must not depend on parsing
  succeeding, so the check cannot either. A MOS envelope carries exactly one operation, so the
  element's presence identifies it.
- **Skipped frames are counted and reported**, at shutdown and via `Recorder.Skipped()`. A quiet
  capture directory must be distinguishable from a broken recorder — otherwise this fix recreates
  the ambiguity it was meant to remove.

Raising the cap was considered and rejected: a larger number moves the cliff without removing it.
`Recorder.KeepAlive` re-enables capture for the rare case of deliberately debugging Profile 0
keep-alive behaviour on a short run.

## 40. Items were never persisted from roStorySend, three ways

One graphics item, added by hand to a story on the reference NCS, exposed three defects in a row.
The item arrived intact — payload and all — and OpenMOS stored the running order and twelve stories
while persisting **zero items**.

That matters more than the earlier gaps. Items are the point of a MOS device: the `objID`, the
`itemChannel` and the graphics payload all live on the item. A running order without items is a
list of headlines. It also means the Profile 2 claim was overstated — running orders and stories
were persisted, items were not.

The captured frame, which is what made each defect visible:

```xml
<storyBody><p> </p>
<p> </p><storyItem><mosItem><itemID>1</itemID>
  <itemSlug>LOWER THIRD: Mayor Jones / Transit Vote</itemSlug>
  <objID>OM-T99124A</objID><mosID>openmos.example.mos</mosID>
  <itemEdDur>150</itemEdDur><itemChannel>A</itemChannel>
  <mosExternalMetadata><mosScope>STORY</mosScope>
    <mosSchema>http://openmos.example/schema/graphics/v1</mosSchema>
    <mosPayload><template>lower_third_2line</template>
      <line1>Mayor Alicia Jones</line1>…</mosPayload>
  </mosExternalMetadata>
</mosItem></storyItem><p> </p>
</storyBody>
```

### `storyItem` is a direct child of `storyBody`, not nested in a paragraph

`StoryParagraph` had `Items []StoryItem` bound to `storyItem`, so only the paragraph-nested form
was modelled. `StoryBody` had no such field. A live ENPS emits `storyItem` as a **sibling of the
paragraphs**, so `encoding/xml` had nowhere to unmarshal it and discarded every item silently.

Both shapes are now accepted. The specification's examples show the nested form and a real NCS
sends the flat one, which is the same "correct by the document, wrong in the field" pattern as the
two `listMachInfo` dialects in §14.

### The fields are one level deeper, inside `mosItem`

`StoryItem`'s fields were declared flat. The real traffic wraps them in `<mosItem>`. A new
`StoryItemFields` models the nested form, and `StoryItem.ItemFields()` returns whichever shape
arrived so callers need not know which. `mosAbstract` is used as a slug fallback: it is an object
field rather than an item field, but ENPS populates both with the same text and some peers send
only the abstract.

### `processStoryBody` built the items and threw them away

```go
// Handle item creation/update (will be implemented in a later step)
// For now, just log what we found
logger.Infof("Found %d items in story %s", len(items), story.ID)
return nil
```

Dead in the same manner as `restoreExternalMetadata` in §32: it compiled, it logged plausibly, and
it did nothing. So even a correctly-shaped item was only counted.

Worse, **`ProcessROStorySend` never called `processStoryBody` at all.** Item extraction existed only
on the `roElementAction` path, through `createNewStory`/`updateStory`. Since `roStorySend` is how
stories actually arrive, items were unreachable in practice by two independent routes at once.

Persistence now delegates to `storeItems`, the routine the `roCreate` path already used, so the two
cannot drift in how they create, update, order or preserve metadata.

### And metadata was dropped on every resend

`storeItems` set `ExternalMetadata` when creating an item and not when updating one. Update is the
**common** path: a live ENPS re-sends the same `roStorySend` repeatedly as an operator edits, so a
graphics payload survived first arrival and was dropped by every message after it. It is now carried
across, and only overwritten when the incoming message actually has blocks, so a peer that omits
them cannot silently erase what is held.

### What did work

`PreserveExternalMetadata=1` on the device row is confirmed: eleven metadata blocks arrived with
payloads intact, including ENPS's own running-order block at `mosScope=PLAYLIST`. The flag gates
whether ENPS sends payloads at all, so this had to be established before drawing any conclusion
about our own handling — an ambiguity that has already cost time once.

The fixture for these tests is the captured frame itself, structure verbatim, rather than one
written by hand. Reverting the `storyBody` binding fails the parse test with *"the element has
nowhere to unmarshal into"*, and reverting persistence fails with *"parsing the item is not
enough"*.

## 41. Writing to the NCS: Profile 7 works, and is gated by a rundown checkbox

Every message OpenMOS had exchanged before this was inbound. `roReqStoryAction` is the only message
in the protocol that lets a MOS device change a running order in the NCS -- Profile 2's whole
running-order family travels the other way -- so this was the first time we asked the newsroom system
to do something rather than told it what we had received.

The reference NCS advertises the profile. Its captured `listMachInfo` reports profiles
0, 1, 2, 3, 4, 6, 7 as `YES` and only 5 as `NO`.

### The round trip works on the first attempt

A `MOVE` sent over a **non-passive** connection was answered in under a second, with our own
`messageID` echoed:

```xml
<roAck>
  <roID>NCS-HOST;P_STORYTELLING\W;2D526A13-…</roID>
  <roStatus>NACK</roStatus>
  <storyID>NCS-HOST;P_STORYTELLING\W;2D526A13-…</storyID>
  <status>External modification not allowed</status>
</roAck>
```

A refusal, but a *conversational* one: the request was parsed, routed and evaluated. That establishes
the transport, the envelope, the operation vocabulary and the element ordering all at once.

The connection had to be non-passive. §25 established that ENPS treats a passive link as its own
output channel and does not service requests arriving on one, so a passive appliance cannot ask for
anything. This is the practical consequence: writing needs a second connection.

### ENPS puts the reason in the wrong element

The specification is explicit that the cause belongs in `roStatus` -- "the NCS sends a NACK message
with `<roStatus>` containing a reason for the error". ENPS instead puts the bare word `NACK` there and
the human-readable cause in the per-element `<status>`.

Our client read only `roStatus`, so the first live attempt reported `NACK` and discarded
*"External modification not allowed"* -- the single piece of information worth having. Both locations
are now consulted. This is the same class of mistake as §14's two `listMachInfo` dialects: the field
that the document says carries the meaning is not the field that carries it.

`storyID` in that `roAck` echoes the **roID**, not a story. NOM refuses before resolving the target,
so that value is not evidence of a lookup failure -- worth knowing before reading it as one.

### The gate is a rundown property, not device configuration

Located in NOM's own strings and confirmed in decompiled IL. `AllowExternalMod` and
`External modification not allowed` sit 34 bytes apart in `NOM.exe`'s UTF-16 literal heap, and the
check appears at five byte-identical sites in `clsMOS.cs`:

```csharp
if (booROReqStoryAction & (Conversion.Val(serverRundownRecord
        .GetProperty("AllowExternalMod")) == 0.0))
{
    list.Add(… + "External modification not allowed");
}
```

in `ProcessROStoryInsert`, `ProcessROStoryReplace`, `ProcessROStoryMoveMultiple`,
`ProcessROStoryDelete` and `ProcessROStorySend`.

`booROReqStoryAction` is an optional parameter defaulting to `false`, set `true` only by
`ProcessROReqStoryAction`. **So the gate fires only on the Profile 7 path.** The same handlers reached
by a legacy `roStoryInsert` or `roStoryMoveMultiple` are not gated at all -- the deprecated messages
MOS 4.0 says never to initiate are *less* restricted than their supported replacement.

`AllowExternalMod` is declared in `g_fielddef` as a checkbox, `"Allow External Modification"`,
scope `RO Property`, group `MOS Properties`, alongside `MOSControl`, `MOSroAllow`, `MOSroBlock` and
`MOSroStorySend`. It is absent from the test rundown's `ENPSObjectProperties`, and
`Conversion.Val("")` is `0`, so it refuses.

Negative results worth recording, because each was a plausible theory:

- **Not device configuration.** `g_mos` has 37 columns and no candidate; the full device property
  vocabulary contains nothing matching Modify, External, Action, Profile, Write, Allow or Lock.
- **Not a system switch.** `nom.ini`'s `[MOS]` section holds only `Version`, `LogIn`, `LogOut`.
  `G_CONFIG` has no such key.
- **Not a licence limitation**, and not hard-coded: it is a runtime property read.
- **`g_mos` has no header row.** Line 0 is a `dummy` template device. Field positions have to be
  established by diffing dated backups, not by reading a header.

### Where this leaves the claim

Originating `roReqStoryAction` is implemented, spec-shaped and answered by a live NCS. Whether ENPS
*applies* a MOVE is unproven, because the rundown property is off. Enabling it is a checkbox in the
ENPS client, per rundown, needing no `g_mos` edit and no NOM restart.

Profile 7 still cannot be claimed regardless: it requires Profiles 0, 1 and 2, and Profile 1 is
object workflow, which OpenMOS does not implement.

## 42. The rundown silently diverged: two identity conventions in one service

Enabling `AllowExternalMod` (§41) made the write succeed. ENPS applied the MOVE, answered
`<roStatus>OK</roStatus>`, and pushed `roElementAction operation="MOVE"` back to the passive appliance
in the same second. The full loop:

```
17:20:54  Sent roReqStoryAction operation=MOVE       -> non-passive connection
17:20:54  ACCEPTED roStatus="OK"                     <- ENPS applied it
17:20:54  Received roElementAction "MOVE"            <- ENPS notified the passive client
```

That is a running order changed in a live newsroom system by OpenMOS, and the change observed coming
back on a second connection.

**Our stored order did not change.** We acknowledged the notification, logged
*"Moved 1 stories"*, and did nothing.

### One root cause, three symptoms

Stories and items are stored under a composite key, `storyPersistenceID(roID, storyID)`, because the
protocol only guarantees a storyID is unique *within* a running order. The `roCreate` and
`roStorySend` family used that composite. **The `roElementAction` family used the bare wire
identifier** -- `element_action.go` referenced `storyPersistenceID` exactly zero times.

So:

1. **MOVE, DELETE and SWAP silently did nothing.** `moveSet` was built from wire IDs and tested
   against `story.ID`, which is the composite. Nothing ever matched, so every story landed in
   "remaining", the sequence was rebuilt identically, and `nil` was returned. An OK ack for work not
   done.
2. **Duplicates accumulated.** Creations minted a second record under the bare ID for a story already
   held under the composite, so the live rundown carried phantom stories -- one with an empty `rawID`
   -- and gapped ordering (`1, 3, 4, … 14`, no 2).
3. **Ordering drifted** as those two populations were renumbered independently.

The spec is at its most emphatic here:

> it is absolutely critical that all messages be applied in the order they are received. If a message
> in a sequence is not applied or "missed" then it is guaranteed that all subsequent messages will
> cause the sequence in the MOS to be even further out of sequence.

And there is a second obligation we were also failing: a device whose sequence no longer matches the
NCS's must send `roElementStat` with status `DISCONNECTED`. Silently diverging while reporting OK is
worse than either applying the change or admitting we cannot.

Translation now happens in one place, `internal/service/identity.go`: `resolveStory`,
`resolveStoryKeys`, `resolveItem`, `resolveItemKeys`, `storyKeyFor`. Each tries the composite key and
falls back to scanning by `RawID`, so records written under the old convention remain addressable
rather than being orphaned by the fix. Unresolvable identifiers are returned as `missing` rather than
skipped, and a MOVE naming a story we do not hold is refused outright -- a partially applied reorder
is the divergence this section is about.

### Two further defects the fix exposed

**MOVE inserted after the target, not before.** `element_target` is "a storyID specifying the story
before which the source stories are moved". The old loop advanced past every story whose order was
`<=` the target's, landing one position late -- so moving a story onto the one immediately following
it was a no-op even once identities matched. Invisible without asserting on the resulting sequence,
which is why the test does.

**REPLACE was not idempotent.** It always called `Create`, which had previously succeeded only
because each replacement invented an unused key. With keys colliding correctly it failed with
"already exists". A live ENPS re-sends a story's replacement throughout an editing session, so
in-place update is the normal path, not the exception.

### Confirmed by reversion

Rebuilding `moveSet` from wire identifiers reproduces the original symptom exactly: *"Moved 1
stories"* in the log, `order = [first, has the graphics item, third]` unchanged, no error. The
fixture is the captured frame from the live loop above.

### An unknown storyID did not trigger recovery

Re-running the loop with the appliance's state deliberately cleared showed the next layer. The write
succeeded again -- `ACCEPTED roStatus="OK"` -- and the notification came back, and this time we
**refused it correctly** instead of silently no-oping:

```
Failed to apply roElementAction: target story not found: story …;593BEF12 is not held
in running order …;2D526A13
```

That is the §42 fix working. But no `roReq` followed, so the divergence stayed. The transport decides
to request a rebuild by matching `UnknownRunningOrderError`, and `resolveStory` was returning a plain
`fmt.Errorf`. We refused honestly and then sat diverged with nothing scheduled to repair it.

The recovery is normative, and it names all three levels:

> if a MOS device receives an `roElementAction` message which references an unknown `roID`, `storyID`
> or `itemID`, the MOS device will send an `roReq` message to the NCS which includes the `roID`.

We had implemented it for an unknown running order only. A missing story or item now reports the same
lost-synchronisation error, from both the target lookup and the source set, so a NACK is always
accompanied by a request for the full `roList`.

## 43. A passive connection cannot carry a request, and trying wedges the NCS

With recovery now firing (§42), the appliance sent an `roReq` down its passive connection to rebuild
cleared state. Nothing came back. **Zero `roList` frames have ever arrived**, across the entire
capture history.

NOM did receive it. Its per-device log records the frame verbatim -- but as a `mosResponse`:

```xml
<mosResponse Command="roReq" Time="…5:34:11 PM" LinkID="70784232-…">
  <mos>…<roReq><roID>NCS-HOST;P_STORYTELLING\W;2D526A13-…</roID></roReq></mos>
</mosResponse>
```

That `LinkID` belongs to NOM's own immediately preceding `nomCommand roElementAction MOVE`. Our
request was filed as **the answer to NOM's outstanding message**. All four `roReq` frames sent that day
were classified `mosResponse`; never once `mosCommand`. The log grammar makes this unambiguous:
`nomCommand`→`mosResponse` pairs carry a `LinkID`, `mosCommand`→`nomResponse` pairs do not.

### Why, from the assemblies

A `passive=true` connection becomes a `MOSSocketOut`, and its arrival handler does no type inspection
at all:

```csharp
MOSWebSocketeOut_MessageArrival(...) {
    int num = MOSSocketOut.IndexOf(sender as MOSSocket);
    mobjSocket[num].ProcessMOS4MessageResponse(e.MOSMessage);
}
ProcessMOS4MessageResponse(string strResponse) {
    mstrResponse = strResponse; ProcessDataArrival(); SendComplete();
}
```

The inbound path, by contrast, calls `AddQueueIn` -- the request queue. So **no frame arriving on a
passive connection can reach request dispatch.** `roReq` is not rejected or special-cased; the code
path does not exist.

### It is not merely futile, it is harmful

`SendComplete()` leads to `RemoveQueueOut`, which threw `ArgumentOutOfRangeException`
**371 times in one day** on the same ~30 second cadence as an endless `roElementAction MOVE` retry. The
sequence: our `roReq` is consumed as the response → send-complete bookkeeping throws → the queue entry
survives → NOM re-sends the same message (messageID 150) thirty seconds later, indefinitely.

So a device that follows the specification's recovery rule on a passive connection puts the NCS into a
permanent retry loop. `canOriginate()` on the responder now makes this a compile-time obligation:
adding a transport forces an answer to whether its lane can carry a request.

### The specification says the opposite

> When the "external" device needs to originate a message sequence, **for example an `roReq` message to
> the "internal" NCS**, it will use this "passive" connection for the specific port that it was
> provided. — MOS 4.0 §1

`roReq` is the document's own example of what a passive connection should carry, and the reference
implementation cannot route it. We follow the implementation, because the implementation is what is on
the other end.

**The consequence for anyone building a MOS 4.0 device:** passive mode alone is not a complete
transport. A passive-only device can receive unsolicited running orders but cannot ask for anything —
no `roReq`, no `roReqAll`, no Profile 7 request — so it has no route to the normative recovery from
lost synchronisation. It needs a second, non-passive connection, which is the same conclusion §41
reached for writing. Two connections is not an optimisation; it is the minimum for a device that must
stay in sync.

### Also observed in NOM, unprompted

- `NotImplementedException` in `frmMOS.MOSWebSocketIn_ConnectionClosed` (`mos.vb:262`) every time our
  socket closes, preceded by "The remote party closed the WebSocket connection without completing the
  close handshake". An inbound-socket lifecycle defect, unrelated to dispatch.
- The string `passive` appears in **no** NOM log written that day. The classification that determines
  all of the above is never recorded.
- Per-device MOS XML logs stamp in **UTC**; `EXCEP.LOG` stamps in **local time**. Comparing them
  without accounting for the four-hour offset will point at the wrong events.

## 44. Two lanes, because neither one is a complete transport

§41 and §43 arrived at the same conclusion from opposite directions, so the client now holds both
connections on the `ro` channel:

| Lane | `passive=true` | Receives unsolicited traffic | Can carry our requests |
|---|---|---|---|
| standard | no | **no** | **yes** |
| passive | yes | **yes** | **no** |

A passive-only device receives rundowns and can never ask for anything, so it has no route to the
normative recovery from lost synchronisation. A standard-only device can ask for everything and will
never be told about a change it did not request. Both statements are observed against NOM 9.6, not
inferred.

The lanes are configured independently rather than one implying the other, because the choice depends
on the deployment:

```yaml
websocket:
    client:
        passive: true       # receive NCS-originated running orders
        requestlane: true   # ALSO open a standard lane to carry our own requests
```

`WS_CLIENT_REQUEST_LANE` is ignored when `passive` is false, since a standard lane already carries
requests. Most deployments are expected to be non-passive, where one lane suffices.

### Recovery has to cross lanes

The consequence for the dispatcher is that the lane which *reports* a divergence is not the lane that
can *repair* it. `roDeps.origin` is an `originator`, consulted when the responding lane returns false
from `canOriginate()`:

- no request lane configured → warn, naming the setting that would fix it
- configured but not connected → **defer**, rather than write to a dead socket or drop the attempt
  silently
- connected → send the `roReq` there, rate-limited by the same guard

In every case the triggering message is still acknowledged. Being unable to recover is not being unable
to answer, and withholding the ack would leave the NCS retrying a message we had in fact received.

### What this does not fix

The two lanes are separate WebSocket connections, so the NCS sees two devices' worth of sockets for one
`mosID`. NOM tolerated that in testing -- the Profile 7 request and the passive delivery were serviced
concurrently on the same second -- but its per-device log records `mosCommand` entries without a
`LinkID`, so from the log alone it is not possible to attribute a request to a specific socket. If a
future defect depends on which socket carried what, that attribution has to come from our side.

## 45. One unparseable item denied a whole running order

With a request lane in place (§44) the client sends `roReqAll` on connect, which is what real devices do
-- in the sampled multi-vendor corpus an automation system's startup is `reqMachInfo` then `roReqAll`
within the same second. The live NCS answered properly, and two firsts arrived together:

```
Sent roReqAll on the request lane to discover the peer's running orders
Received roListAll from ncsID=… with 2 running orders
Received roList from ncsID=… with 13 stories
Failed to apply roList: story 12 item 1: itemID is required
```

Both `roListAll` and `roList` are things this repository had never received from a live NCS. And the
`roList` was **rejected whole** for one item.

### The same nesting, a third time

ENPS wraps item fields one level deeper than the document declares:

```xml
<item><mosItem><itemID>1</itemID><objID>OM-T99124A</objID>…</mosItem></item>
```

against the specification's flat `<!ELEMENT item (itemID, itemSlug?, objID, mosID, …)>`.

This was already known -- it was visible in a captured `roElementAction` frame and noted at the time --
and only the `storyBody`/`storyItem` case was fixed (§40). The `<item>` path was left, so `roList`,
`roCreate`, `roReplace` and `roElementAction` all still read every field as empty and failed validation
on the missing `itemID`.

`ItemInfo.UnmarshalXML` now accepts both shapes and flattens, so every message carrying an item is
covered at once rather than at each call site. Where both levels carry a field the outer one wins, since
that is the shape the document defines; `mosAbstract` stands in for a missing `itemSlug`, because ENPS
populates both with the same text and some peers send only the abstract.

### The amplification is the real lesson

A single malformed item did not degrade the rundown, it denied it entirely: applying a `roList` is
atomic, so thirteen stories were discarded because of one. The device then had no state, refused the
`roStorySend` messages that followed, asked again, and repeated -- a stable loop that looks like a
connectivity problem and is actually one missing element name.

Whether atomicity is right here is a separate question. It is defensible: a partially applied running
order is a sequence that disagrees with the NCS's, which §42 argues against. But the failure mode
deserves recording, because "one bad item, no rundown" is a large blast radius for a parse gap, and the
error surfaced only in a log line nobody was watching.

### The loop closes: our write comes back and converges our state

With the rundown populated, the assertion that had been missing all along:

```
order BEFORE:  1 New Row 8    2 New Row 7    3 shoe    4 hay    5 HAM
   Sent roReqStoryAction operation=MOVE      (3rd story, before the 1st)
   ACCEPTED roStatus="OK"                    <- ENPS applied it
   Received roElementAction "MOVE"           <- ENPS notified the passive lane
   Moved 1 stories … to position 0           <- we applied it
order AFTER:   1 shoe    2 New Row 8    3 New Row 7    4 hay    5 HAM
```

OpenMOS asked a live newsroom system to reorder a rundown, the NCS did it, told us about it on a
different connection, and our stored sequence converged on the NCS's. The rest of the order is
undisturbed, which is the part a reorder gets wrong most easily.

Every earlier run of this test was against an empty rundown, so the inbound half was correctly refused
and never exercised. That is worth stating plainly: the write half was proven hours before the read half,
and a passing `roStatus=OK` said nothing about whether we had applied anything.

## 46. The timing bar: the only message that says *now*

An operator dragging the timing bar in the ENPS client emits `roElementStat`, and this is the message a
device actually needs. Everything else in the running-order family describes what a rundown *contains*;
this describes what is *happening* in it. It is also the most common non-heartbeat message in real
traffic, and OpenMOS had been parsing, logging and acknowledging it while recording nothing.

Each bar move produces a **pair**, roughly two seconds apart:

```xml
<roElementStat element="STORY">
  <roID>NCS-HOST;P_STORYTELLING\W;2D526A13-…</roID>
  <storyID>NCS-HOST;…;DA7C2774-0824-458F-9EF5-3446CFA9C078</storyID>
  <status>PLAY</status>
  <time>2026-09-10T20:20:30</time>
</roElementStat>
```

`STOP` on the story being left, `PLAY` on the story being entered. The on-air position is therefore
fully recoverable from the `PLAY` messages alone, with the transition time carried in the message rather
than inferred from arrival.

The first move of a session also produced a `roMetadataReplace` for the running order — ENPS updating
RO-level metadata, presumably recalculated timings, as the bar moves.

### The ordering trap

`STOP` clears the on-air pointer **only when it names the story currently on air.**

The pair's order is not guaranteed. A `STOP` for the story being left can arrive *after* the `PLAY` for
the story being entered, and clearing unconditionally would blank a pointer that had just been set
correctly — reporting nothing on air while the bar is plainly sitting somewhere. A consumer driving
graphics or automation off that would go dark on every move.

### Where the state lives

`RunningOrder.OnAirStoryID` and `OnAirSince`, not derived by scanning story statuses. The two answer
differently the moment a `STOP` is missed: a scan would report two stories on air, whereas a single
pointer cannot. `OnAirStory()` is a first-class lookup because "what is on air right now" is the
question a consumer asks.

Timestamps go through `ParseMOSTime`, since MOS uses a comma decimal separator that Go's `time.Parse`
rejects. A value that will not parse falls back to arrival time rather than failing the report — the
status is the point.

### It is a notification, not a command

The NCS is reporting where the bar is. Nothing here asks us to play anything. Actual playout control is
Profile 5 (`roCtrl`, `roItemCue`), and the reference NCS advertises **Profile 5: NO**, so those never
arrive from it. A device wanting to *act* on the bar reads this message; a device wanting to *be told to
act* will wait forever on this estate.

### Removed while here

`ReportElementStatus` was the previous handler. It only examined `stat.ItemID` — empty for a STORY-level
report — so the timing bar reached it and did nothing, and it addressed items by bare wire identifier,
which never matches a composite storage key (§42). Now unreferenced and deleted rather than left to look
live, which is the fourth time dead-but-plausible code has cost this project time.

## 47. The heartbeat loop, and an instrument that could not see it

Reported by an operator watching NOM's UI: "constant blast of heartbeat messages". Confirmed in NOM's
own per-device log:

```
<mosCommand  Command="heartbeat" Time="9/10/2026 9:54:21 PM" IP="openmos.example.mos">
<nomResponse Command="heartbeat" Time="9/10/2026 9:54:21 PM" IP="openmos.example.mos">
<mosCommand  Command="heartbeat" Time="9/10/2026 9:54:21 PM" IP="openmos.example.mos">
<nomResponse Command="heartbeat" Time="9/10/2026 9:54:21 PM" IP="openmos.example.mos">
```

Five round trips in one second. **2060 of 6182 lines** in a single rotated log, and **148 rotations of
that log in a day.**

### The specification requires the loop and forbids it, one sentence apart

> An application will respond to a heartbeat message with another heartbeat message. However, care should
> be taken in implementation of this message to avoid an endless looping condition on response.

Answering unconditionally satisfies the first clause and violates the second. We answered every inbound
heartbeat; so does NOM; nothing terminated.

### It was concealed by passive mode

The loop needs a long-lived connection on which both ends heartbeat. A passive lane sends `keepAlive`,
never `heartbeat` — so for as long as the client was passive-only there was no exchange to loop. Adding
the non-passive request lane (§44) created one, and the loop began immediately.

Worth generalising: a feature that removes the *only* thing suppressing a latent fault will appear to
have caused it. The request lane did not introduce the answering behaviour, which had been there all
along; it introduced the conditions under which it mattered.

### Two guards, because one depends on the peer

An inbound heartbeat carrying the `messageID` of one we sent is a **response** and is not answered. That
is precisely what the field is for: *"Messages used as response to a request have the same messageID as
the request."*

Identifier matching is the correct test, but it depends on the other end echoing correctly, so a rate
backstop answers at most one heartbeat per interval. The backstop expires, so a genuine heartbeat is
still answered later and the peer never concludes we are dead. A loop should be impossible rather than
merely unlikely.

### The instrument was blind by construction

Before the operator reported it, the frame rate had been measured through the capture directory and
reported as "2 frames a minute, the normal 30-second cadence". That measurement could not have detected
this: **capture excludes `keepAlive` and `heartbeat`** (§39, extended in §44's commit), an exclusion added
hours earlier in the same session. The count was of everything *except* the messages in question, and it
was used to conclude there was no problem.

The lesson is not "measure more". It is that a filter added for one purpose silently invalidates every
later measurement that passes through it, and the filter is invisible at the point of measuring. Where
a subsystem deliberately discards data, a count taken from it needs to state what it excludes — or the
count needs to come from the other end of the wire, which is what settled this.

## 48. A real customer rundown, and durations that were never seconds

A production rundown from a customer station was activated against the appliance: **45 stories, 93 items,
one `roCreate` followed by 45 `roStorySend`, zero errors, zero unhandled messages.** Everything built to
this point handled it without modification, which is the first time real third-party editorial structure
has passed through OpenMOS end to end.

It also carried three vendors' devices and two vendors' metadata schemas, so the item shapes are not
ENPS's alone:

| Owning `mosID` (device class) | Items | `mosSchema` |
|---|---|---|
| automation | 46 | a switcher vendor's MOS external schema |
| character generator | 28 | *(none — no `mosExternalMetadata` at all)* |
| news production / playout | 19 | a playout vendor's MOS schema |

`mosScope` split PLAYLIST 74 / STORY 19, consistent with the specification. Payload structure was a
generic property bag — `object`, `properties`, `property`, `name`, `value`, `type` — plus audio
configuration (`additionalAudio`, `customLevel`, `overrideState`) and per-vendor keys such as `shotID`
and `dbTemplate`. 85 distinct object identifiers across 93 items, so a few objects are referenced more
than once, exactly as the specification permits.

### Durations are in samples, and we had been storing them as seconds

Every one of the 93 items stored a duration of **zero**, which exposed something larger.

The customer's items carry `objDur` and `objTB` — object duration in samples and the sampling rate —
on **all 93**, while `itemEdDur` appears on only some. We read neither `objDur` nor `objTB`, so that
estate had no timing at all.

Worse, where `itemEdDur` *was* read, it was read wrongly. The specification is consistent and easy to
misread:

> `objTB` — "Describes the sampling rate of the object in samples per second. For PAL Video this would be
> 50. For NTSC it would be 59.94."
>
> `itemUserTimingDur` — "The value is in number of samples."

`itemEdDur`, `itemUserTimingDur` and `objDur` are all **sample counts**. OpenMOS put the raw figure into a
field documented as seconds, so a 150-sample lower third — two and a half seconds of air — was recorded as
150 seconds. Every duration was wrong by the sample rate, roughly sixtyfold for NTSC, and any consumer
computing rundown timing from it would have been nonsensically wrong.

`resolveItemTiming` now prefers `itemEdDur`, falls back to `objDur`, and divides by `objTB`. Three
decisions worth stating:

- **An unknown duration is not a zero duration.** Without a time base the conversion is impossible, and
  guessing a frame rate fabricates a figure that *looks* usable — which in a rundown is worse than an
  absent one. `Samples` is preserved, `Seconds` stays zero, and `Known` reports the difference.
- **The reported rate must be used, not assumed.** The specification makes still stores and character
  generators one sample per second, so for a CG item the sample count already *is* the duration. Assuming
  a video frame rate would divide it by sixty.
- **The exact rate is kept alongside the rounded one**, because 59.94 does not survive rounding and the
  sample count is the only lossless figure.

Two existing tests asserted the old behaviour and were corrected rather than accommodated: they had
encoded the units bug as the expectation.

### A malformed schema URI, carried verbatim

One vendor's schema URI arrives as `http:'vendor.example/…` — an apostrophe where `//` belongs. It is like that
on the wire, not mangled by us.

That is the right outcome. `mosSchema` is "implied to be a pointer or URL", and the payload is to be
carried rather than interpreted, so a device that validated or normalised it would reject or silently
alter production traffic. Worth recording as concrete evidence that these URIs cannot be assumed
well-formed.
