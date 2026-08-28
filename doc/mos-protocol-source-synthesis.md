# MOS protocol source synthesis for Kiro CLI

**Reviewed:** 2026-08-28

Use this note as a source map, not as a replacement for the protocol documents
or OpenMOS's live interoperability evidence. The sources describe three
different things:

1. the normative MOS 3.8.4 WebService protocol;
2. vendor-specific MOS products and workflows; and
3. a library for replaying stored MOS running-order XML.

When they disagree, implement the normative specification for the applicable
protocol generation, preserve compatibility demonstrated by `doc/interop/`, and
treat vendor behavior as an interoperability example rather than a rule.

## Normative source: MOS 3.8.4

Source: [MOS Protocol 3.8.4 Current](https://mosprotocol.com/wp-content/MOS-Protocol-Documents/MOS-Protocol-3.8.4-Current.htm)

### Do not mix transport generations

MOS 3.x is a WebService protocol: XML methods and types are described by WSDL,
and SOAP is carried over HTTP. It uses UTF-8 and normally one TCP port, `10543`,
with optional `10544` for a second priority lane. Those facts do **not** define
the framing, encoding, or ports for OpenMOS's MOS 2.x raw-TCP or MOS 4 WebSocket
transports. Reuse message semantics only where the relevant generation supports
them.

### Profiles are complete behavior contracts

The specification defines Profiles 0 through 7. A vendor may claim MOS
compatibility only when it fully implements Profile 0 and at least one other
profile, including every required message and the profile workflow. Supporting
a few messages is not equivalent to supporting a profile.

- Profile 0 requires `heartbeat`, `reqMachInfo`, and `listMachInfo`.
  `reqMachInfo` asks for a `listMachInfo`, which identifies the machine, MOS
  version, supported profiles, and device type.
- Profile 2 lets an NCS build and dynamically maintain an ordered content list
  on a MOS device. In 3.8.4 it builds on Profiles 0 and 1 and requires the full
  running-order construction family, not only `roCreate`, `roReplace`,
  `roStorySend`, and `roDelete`.

Therefore OpenMOS should continue describing its implemented messages and live
proof precisely; it should not advertise full Profile 2 conformance until the
entire profile contract is implemented and exercised.

### IDs, ordering, acknowledgements, and recovery

- `mosID` identifies the MOS device and `ncsID` identifies the newsroom system.
  They are configuration identities, not display labels to rewrite in transit.
- A `messageID` is mandatory wherever the schema includes it. A new request uses
  a new ID; its response echoes that ID; a retry of the same request reuses the
  original ID. The sender increments IDs, persists the last value, and wraps to
  `1`. The value is a decimal or hexadecimal signed 32-bit integer of at least
  `1`.
- Message flow is sequential: the sender waits for the current acknowledgement
  before sending the next construction message. Unacknowledged messages are
  buffered and retried. Because list edits are relative to existing elements,
  applying messages in receive order is essential.
- An ACK means the message parsed correctly, required metadata was saved (or did
  not need saving), and referenced metadata entities assumed to exist actually
  exist. ACKing before persistence violates that contract.
- `NACK` should carry the useful error in `statusDescription`.
- If a message references an unknown `roID` or `storyID`, the MOS device should
  treat this as lost synchronization, send `roReq`, and replace its local state
  from the returned full `roList`. This is the normative basis for OpenMOS's
  planned pull-based recovery.
- `roReqAll`/`roListAll` provide running-order discovery. They do not by
  themselves contain every full running order; request individual full lists as
  needed.

### Data-model rules relevant to implementation

- The hierarchy is Running Order → Story → Item → Object. IDs are unique
  within their documented scope; a running-order item is identified by the
  `roID`/`storyID`/`itemID` combination.
- Element order is significant. Items arrive in intended play order, and a MOS
  device must retain the sequence supplied by the NCS even if it executes items
  out of order.
- `objID` is immutable and machine-facing. The pair `mosID` + `objID` uniquely
  identifies an object across multiple media servers. `objSlug` is the mutable,
  human-facing name.
- `mosExternalMetadata` is an opaque payload carried by MOS. `mosScope` controls
  propagation: `OBJECT` stays with object/list/search use, `STORY` may enter an
  item reference in a story, and `PLAYLIST` may also enter running-order
  construction messages. The shared core should preserve unknown payloads and
  enforce scope rather than interpret vendor schemas.
- Tags are case-sensitive; messages must be well-formed and schema-valid for
  the generation being emitted. Be lenient only at intentional inbound
  compatibility seams, never by inventing outbound fields.

## Vendor and library evidence (non-normative)

### RT Software Swift News MOS Gateway

Source: [Swift News: MOS Gateway](https://rtsw.co.uk/document/swift-news-mos-gateway/)

This is useful evidence of a deployed MOS 2.8.4 product, not a definition of
MOS. Its gateway maintains client and server connections on the classic lower
and upper ports, requests machine information at startup, heartbeats idle
sockets, and persists running orders and MOS objects locally. Its default lower
port is `10540` and upper port is `10541`.

Its **Request All** operation is a concrete recovery pattern: send `roReqAll`,
receive `roListAll`, then send `roReq` for each listed running order and persist
each returned `roList`. That pattern supports OpenMOS's recovery direction. The
manual's message-by-message profile matrix, graphics commands, file layout, and
`roCtrl` mappings are Swift behavior only. Its warning that vendors add unique
tags argues for tolerant extension handling on input, not non-standard output.

### BBC `mosromgr`

Sources: [`mosromgr` API](https://mosromgr.readthedocs.io/en/stable/api/index.html),
[MOS types](https://mosromgr.readthedocs.io/en/stable/api/mostypes.html), and
[`MosCollection`](https://mosromgr.readthedocs.io/en/stable/api/moscollection.html)

`mosromgr` parses and classifies stored MOS XML from files, strings, or S3 and
reconstructs a programme by merging an initial `roCreate` with later story/item
mutations. It is a useful precedent for deterministic offline replay and for
keeping source references out of memory, but it is not a transport or
conformance implementation. Do not copy its Python types, sort-by-`message_id`
behavior, or incomplete-message assumptions into the live receive path without
checking the normative generation and captured traffic; live order is receive
order, and retries reuse IDs.

### Zero Density Reality Hub and Octopus

Sources: [Reality Hub MOS configuration for Octopus](https://docs.zerodensity.io/reality-5.5.sp1/integrations/octopus-integration/reality-hub-mos-configuration-for-octopus)
and [Utilizing ZD MOS Plugin](https://docs.zerodensity.io/reality-5.5.sp1/integrations/octopus-integration/utilizing-zd-mos-plugin)

Reality Hub is an interoperability example showing that both peers must agree
on `mosID`, protocol version, encoding, address, and ports. This product supports
MOS 2.6, 2.8, and 2.8.5 and offers several selectable encodings. It labels
`10540` as lower/MOM, `10541` as upper/RO, and `10542` as an optional query port.
Those supported versions and encodings are product capabilities, not permission
for OpenMOS to auto-negotiate or accept every combination.

The plugin guide illustrates the user-facing object lifecycle: author and save
a vendor object, insert its MOS item into an NRCS story, then consume the
running order downstream. Template forms, login, save/drag controls, green UI
status, and ready-to-air presentation are Zero Density/Octopus behavior, not MOS
wire semantics.

### Sofie TV Automation System

The referenced project is **Sofie** (not Sophie). Its reusable
[`sofie-mos-connection` packages](https://github.com/Sofie-Automation/sofie-mos-connection/blob/6348a5303dc0ff154eb162d4978287a69bed6a6a/README.md#L31-L40)
handle classic MOS, while [Sofie Core's separate MOS
gateway](https://github.com/Sofie-Automation/sofie-core/blob/8e31d3469adab54e43ccae6b38d964a770ddca3f/packages/mos-gateway/README.md#L1-L22)
adapts it to the automation application. The following are implementation
lessons, not normative MOS rules.

| Lesson | Pinned Sofie breadcrumb and demonstration | Closest OpenMOS seam | Guidance |
|---|---|---|---|
| Architecture boundary | [`README.md` packages](https://github.com/Sofie-Automation/sofie-mos-connection/blob/6348a5303dc0ff154eb162d4978287a69bed6a6a/README.md#L31-L40) separate connector, XML/model helpers, and test peer; the [gateway README](https://github.com/Sofie-Automation/sofie-core/blob/8e31d3469adab54e43ccae6b38d964a770ddca3f/packages/mos-gateway/README.md#L1-L22) identifies a distinct Core adapter. | `internal/server/` transport/dispatch → `internal/service/` semantics → repositories | **Adapt:** keep the existing Go package seams and shared core. Do not recreate Sofie's npm/package topology. |
| Ports and roles | [`MosConnection` constants and client creation](https://github.com/Sofie-Automation/sofie-mos-connection/blob/6348a5303dc0ff154eb162d4978287a69bed6a6a/packages/connector/src/MosConnection.ts#L22-L25) use lower `10540`, upper `10541`, query `10542`; [`NCSServerConnection.executeCommand`](https://github.com/Sofie-Automation/sofie-mos-connection/blob/6348a5303dc0ff154eb162d4978287a69bed6a6a/packages/connector/src/connection/NCSServerConnection.ts#L161-L190) routes by message port. | `internal/server/channel.go`, `internal/server/server.go`, `internal/server/wsserver.go`, and config | **Adapt by generation:** keep MOS 2.x upper/RO ownership and MOS 4 channel routing explicit. Do not infer MOS 4 ports from Sofie's classic sockets. |
| Stream framing | [`MosMessageParser.parseMessage`](https://github.com/Sofie-Automation/sofie-mos-connection/blob/6348a5303dc0ff154eb162d4978287a69bed6a6a/packages/connector/src/connection/mosMessageParser.ts#L17-L127) accumulates chunks and extracts every complete `<mos>…</mos>` envelope; [tests split frames and fields](https://github.com/Sofie-Automation/sofie-mos-connection/blob/6348a5303dc0ff154eb162d4978287a69bed6a6a/packages/connector/src/__tests__/MessageChunking.spec.ts#L124-L255). | `internal/xml/wire.go`, `internal/xml/parser.go`, `internal/server/mos28_integration_test.go` | **Adopt the cases, not the parser:** retain OpenMOS's UCS-2BE byte discipline and 4 MiB bound; test arbitrary splits, coalesced frames, junk, and partial tags. Do not copy Sofie's unbounded string buffer or automatic junk discard. |
| Serialization, correlation, retry | [`MosSocketClient.queueCommand/processQueue`](https://github.com/Sofie-Automation/sofie-mos-connection/blob/6348a5303dc0ff154eb162d4978287a69bed6a6a/packages/connector/src/connection/mosSocketClient.ts#L144-L227) permits one outstanding command and indexes callbacks by `messageID`; [`executeCommand`](https://github.com/Sofie-Automation/sofie-mos-connection/blob/6348a5303dc0ff154eb162d4978287a69bed6a6a/packages/connector/src/connection/mosSocketClient.ts#L320-L355) retries the same prepared message once on timeout. | `internal/messageid/sequence.go`, `internal/server/dedup.go`, `internal/server/resync.go`, `internal/server/wsclient.go` | **Adapt:** preserve one in-flight request per ordered lane, exact response correlation, and the same ID/body on retry. Do not copy the one-retry/process-memory ceiling; OpenMOS must retain conflict detection and durable-ID safety. |
| Profile/callback validation | [`MosDevice._checkProfileValidness`](https://github.com/Sofie-Automation/sofie-mos-connection/blob/6348a5303dc0ff154eb162d4978287a69bed6a6a/packages/connector/src/MosDevice.ts#L1716-L1764) checks dependencies and required callbacks for enabled profiles. The [library support table](https://github.com/Sofie-Automation/sofie-mos-connection/blob/6348a5303dc0ff154eb162d4978287a69bed6a6a/README.md#L92-L109) is broader than the gateway behavior below. | `internal/xml/parser.go` plus handlers in `internal/server/client_profile*_handler.go` and `dispatch_ro.go` | **Adopt the invariant:** a parsed type is not an implemented workflow. Add a startup/test assertion tying every advertised profile/message to a real handler and response path; do not inherit dependency claims. |
| ACK after application work | Gateway callbacks pass Core promises into [`_getROAck`](https://github.com/Sofie-Automation/sofie-core/blob/8e31d3469adab54e43ccae6b38d964a770ddca3f/packages/mos-gateway/src/mosHandler.ts#L630-L648), which returns `OK` only after Core resolves; [`_coreMosManipulate`](https://github.com/Sofie-Automation/sofie-core/blob/8e31d3469adab54e43ccae6b38d964a770ddca3f/packages/mos-gateway/src/CoreMosDeviceHandler.ts#L470-L508) serializes those operations. | `internal/server/dispatch_ro.go`, `internal/service/`, repository writes, `internal/server/persistence_test.go` | **Adopt:** keep ACK creation downstream of successful persistence and ordered application. Preserve useful NACK context on failure; do not copy Sofie's generic `Error: …` status format blindly. |
| `roReq`/`roReqAll` boundary | The connector implements outbound [`sendRequestAllRunningOrders`](https://github.com/Sofie-Automation/sofie-mos-connection/blob/6348a5303dc0ff154eb162d4978287a69bed6a6a/packages/connector/src/MosDevice.ts#L1503-L1519), but the application gateway's incoming [`onRequestAllRunningOrders`](https://github.com/Sofie-Automation/sofie-core/blob/8e31d3469adab54e43ccae6b38d964a770ddca3f/packages/mos-gateway/src/mosHandler.ts#L431-L441) returns an empty list as unsupported. | `internal/server/resync.go`, `internal/service/resync.go`, `internal/server/dispatch_ro.go`, `internal/server/roreq_integration_test.go` | **Adopt the honesty, not the empty response:** test outbound recovery and inbound list service separately. Advertise only the direction/workflow OpenMOS actually completes. |
| Fixture-driven peer | [Quick-MOS](https://github.com/Sofie-Automation/sofie-mos-connection/blob/6348a5303dc0ff154eb162d4978287a69bed6a6a/packages/quick-mos/README.md#L1-L22) watches fixture files and behaves as an NRCS; connector tests use socket mocks for exact frames. | `internal/xml/live_*_fixtures_test.go`, `internal/xml/real_vendor_frames_test.go`, `internal/server/wsclient_test.go`, `internal/capture/` | **Adapt:** keep tiny in-process peers and sanitized captured fixtures for deterministic failures. They complement but never replace `doc/interop/` live-NCS evidence. |
| Primary/secondary failover | [`MosDevice.executeCommand/switchConnections`](https://github.com/Sofie-Automation/sofie-mos-connection/blob/6348a5303dc0ff154eb162d4978287a69bed6a6a/packages/connector/src/MosDevice.ts#L1573-L1635) switches to a connected buddy and hands over the queue; `MosConnection` also has an OpenMedia-specific heartbeat policy. | No equivalent: OpenMOS documents a single process per identity | **Do not copy yet:** failover changes ordering, dedup scope, ownership, and recovery. Add only with an explicit HA design and cross-node durable state. |

The central warning is the capability boundary: a connector can parse or send a
message that its consuming application does not meaningfully handle. For
OpenMOS, handler wiring, state effects, acknowledgements, and end-to-end tests
remain the authority for support claims.

## Kiro implementation checklist

Before changing protocol code:

1. Identify the generation and transport: MOS 2.x TCP, MOS 3.x SOAP/HTTP, or MOS
   4 WebSocket. Never infer one generation's wire rules from another.
2. Find the exact message definition and profile workflow in the applicable
   normative specification.
3. Trace both transports into the shared message core; semantic fixes belong in
   the common path unless the rule is genuinely framing/envelope-specific.
4. Preserve receive order, correlate responses, deduplicate identical retries,
   and reject a reused `messageID` carrying different content.
5. Persist successfully applied state before ACK. On unknown running-order
   context, NACK/report as appropriate and initiate `roReq` recovery rather than
   fabricating state.
6. Compare against `doc/interop/README.md` and captured fixtures. Vendor docs
   suggest cases to test; live evidence establishes what this repository has
   actually proven.
7. Add the smallest automated check that fails without the behavior. Do not
   broaden a support or conformance claim until the whole claimed workflow is
   implemented and verified.
8. Keep transport capability, message parsing, and application workflow support
   separately visible. A capable dependency does not make an unhandled callback
   or empty response conformant.
9. For each new recovery or framing behavior, add a fixture-driven peer test for
   chunking, retries, and ordering, then retain live-NCS proof as a separate gate.
