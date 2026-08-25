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
| MOS envelope, generation-aware `messageID` | Yes | Unit tests | **Yes** | Format strictness differs per transport (#20) |
| Profile 0: `heartbeat` | Yes | Unit tests | **Yes** | — |
| Profile 0: `reqMachInfo`/`listMachInfo` | Yes | Unit tests | **Yes** | — |
| Profile 0: `keepAlive` (no response) | Yes | Unit tests | **Yes** | — |
| Profile 0: heartbeat timeout (bounded) | Yes | Server closes on timeout | No | — |
| `roCreate`: validate, persist, ack after persist | Yes | Integration tests | **Yes** | — |
| `roReplace`, `roStorySend`, `roDelete` | Yes | Integration tests | **Yes** | — |
| Retry deduplication, original ack replayed | Yes | Unit + integration tests | **Yes** | Not durable across restart |
| `messageID` conflict detection | Yes | Unit tests | **Yes** | — |
| Multiple envelopes in one TCP read | Yes | Integration test | **Yes** | — |
| `mosID` persisted on running orders and items | Yes | Integration test | **Yes** | — |
| MongoDB backing | Yes | Not covered in CI | **Yes** | No MongoDB in CI; in-memory used for tests |
| MOS 4 channels `mom` and `aux` | **No** | — | — | Only `ro`; blocks Profiles 1 and 3 (#12) |
| MOS 4 outbound client / passive mode | **No** | — | — | Cannot initiate to an NCS (#11) |
| MOS 4 authentication (HTTP Basic) | **No** | — | — | Spec strongly recommends it (#11) |
| MOS 3.x WebService transport | **No** | — | — | Blocked on WSDL (#15) |
| Profiles 1, 3, 4, 5, 6, 7 | **No** | — | — | Some message types parse, none exercised |
| Multi-instance HA | **No** | — | — | Single process per identity |

### What this is NOT

- Not a complete MOS implementation. Profile 0 and Profile 2 running-order
  construction only, on the `ro` channel.
- Not claiming Profiles 1, 3, 4, 5, 6 or 7. `listMachInfo` advertises Profile 0
  alone, which is deliberate.
- Not able to initiate a connection to an NCS. Both transports are listeners; the
  NCS connects to us.
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
- **Shared message core** — envelope handling, Profile 0, running-order
  construction, deduplication and persistence, used identically by both.
- **Repositories** — in-memory or MongoDB, selected by `storage.backend`.
- **Event bus** — internal pub-sub for running order change notifications.
- **Sentry** (optional) — error tracking when a DSN is configured.

Both servers operate in **standard mode**: the NCS initiates the connection. MOS
4.0 *passive mode*, where a device behind a firewall dials out with
`passive=true`, is not implemented (#11).

## Connection

MOS 2.x, raw TCP:

```
host:10541
```

MOS 4.0, WebSocket:

```
ws://host:8080/mos?mosID=<configured_mos_id>&ncsID=<your_ncs_id>&channel=ro
```

Query parameters:
- `mosID` — must match the server's configured MOS ID (rejected with 403 otherwise)
- `ncsID` — identifies the connecting NCS (required, rejected with 400 if empty)
- `channel` — must be `ro`; `mom` and `aux` are not yet supported (#12)

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
    tlscertfile: ""
    tlskeyfile: ""
storage:
    backend: memory        # "memory" or "mongo"
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

1. **MOS 4 `mom` and `aux` channels** (#12). Only `ro` is accepted today, which
   makes Profile 1 and Profile 3 search structurally impossible. The reference NCS
   accepts all three.
2. **An outbound MOS 4 client with passive mode and HTTP Basic auth** (#11).
   OpenMOS is listener-only, so it cannot initiate to an NCS. Passive mode is the
   main reason MOS 4.0 exists, and reaching a real NCS endpoint additionally needs
   our MOS ID registered on that NCS.
3. **Agree `messageID` format handling across transports** (#20), against observed
   NCS behaviour rather than assumption.
4. **MOS 3.x WebService** (#15), lowest value and blocked on the WSDL.

## License

See LICENSE file for details.
