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
