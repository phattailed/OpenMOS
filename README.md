# OpenMOS Media Object Server

![MOS Project Official Logo](/res/mosproject-logo.jpg)

A minimal, honest MOS Protocol 4.0 implementation providing WebSocket transport
for newsroom automation interoperability.

> **Current status:** Lab-compatible MOS 4 slice. Not yet verified against a real NCS.

## Implementation Status

| Capability | Implemented | Automated proof | Live NCS proof | Remaining risk |
|---|---|---|---|---|
| WebSocket transport (ws://) | Yes | Loopback tests | No | TLS config untested with real certs |
| Identity/channel validation (mosID, ncsID, channel=ro) | Yes | Unit tests | No | Only `ro` channel supported |
| MOS envelope (`<mos>` with mosID/ncsID/messageID) | Yes | Unit tests | No | Subset of operations handled |
| Profile 0: keepAlive (no response) | Yes | Unit test proves no reply | No | - |
| Profile 0: reqMachInfo/listMachInfo | Yes | Unit test verifies Profile 0 only | No | - |
| Profile 0: heartbeat timeout (bounded) | Yes | Server closes on timeout | No | - |
| roCreate: validate, persist, ack-after-persist | Yes | Integration test | No | In-memory storage only |
| Message deduplication (ncsID+messageID) | Yes | Unit + integration tests | No | In-memory store, not crash-durable |
| Message-ID conflict detection | Yes | Unit test | No | - |
| Persistence across restart | Yes | Test with shared backing store | No | Requires external durable store in production |
| Reconnect without duplication | Yes | Integration test | No | - |
| Passive mode (NCS connects to server) | Yes | All tests use this model | No | - |
| Profile 1-7 operations | No | - | - | Message types parsed but not tested end-to-end |
| TLS/WSS | Configurable | Not tested | No | Needs real certificates |
| MongoDB backing | Code exists | Not tested (no MongoDB in CI) | No | In-memory repos used for all tests |
| Multi-instance HA | No | - | - | Unsupported; single process per identity |

### What this is NOT

- Not a complete MOS 4 implementation (only Profile 0 + roCreate on the `ro` channel)
- Not verified against a real NCS (ENPS or other)
- Not claiming Profiles 1-7 compliance
- Not suitable for production without durable storage (MongoDB) and TLS

## Architecture

OpenMOS implements the MOS 4 protocol using:
- **WebSocket server**: NCS connects to `ws://host:port/mos?mosID=X&ncsID=Y&channel=ro`
- **MOS envelope**: All messages wrapped in `<mos>` with mosID, ncsID, messageID
- **In-memory repositories**: For testing; MongoDB implementations exist but require a running instance
- **Event bus**: Internal pub-sub for running order change notifications
- **Sentry** (optional): Error tracking when DSN is configured

The server operates in **passive mode**: the NCS (protected peer) initiates the WebSocket
connection to this MOS device.

## Connection

```
ws://host:10541/mos?mosID=<configured_mos_id>&ncsID=<your_ncs_id>&channel=ro
```

Query parameters:
- `mosID` - must match the server's configured MOS ID (rejected with 403 otherwise)
- `ncsID` - identifies the connecting NCS (required, rejected with 400 if empty)
- `channel` - must be `ro` (only running-order channel supported)

## Configuration

```yaml
app:
    name: OpenMOS
    version: 1.0.0
    environment: development
server:
    host: 0.0.0.0
    port: 10540
    readtimeout: 5s
    writetimeout: 5s
    shutdowntimeout: 30s
websocket:
    port: 10541
    tlscertfile: ""
    tlskeyfile: ""
mongo:
    uri: "mongodb://localhost:27017"
    database: openmos
    timeout: 10s
mos:
    id: OpenMOS_Server
    ncsid: ""
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
- MongoDB 4.4+ required for production durable storage
- Network access on port 10541 (default WebSocket port)

## More Information

- MOS Protocol specification: https://www.mosprotocol.com
- MOS 4.0 documentation: https://mosprotocol.com/wp-content/MOS-Protocol-Documents/MOSProtocolVersion40/index.html

This project is not affiliated with MOS Group.

## Next Steps

The single best next interoperability action is to test against a real NCS (e.g., AP ENPS)
to validate WebSocket connection establishment, envelope handling, and roCreate/roAck exchange
in a production-like environment.

## License

See LICENSE file for details.
