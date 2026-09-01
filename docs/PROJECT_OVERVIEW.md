# MC-Bot-Go Project Overview & Architecture

## Project Summary

`mc-bot-go` is a Minecraft client bot implementation written in Go that can connect to Minecraft Java Edition servers. It implements the Minecraft network protocol, handles authentication, manages world state, and provides a framework for bot behaviors through event handlers and packet listeners.

---

## Core Components

### 1. **Bot Package** (`bot/`)

The main package containing the Minecraft client implementation.

#### Key Components:

- **`Client`** (`client.go`)
  - Central bot instance managing connection lifecycle
  - Holds authentication info, UUID, name, registries
  - Manages events, login plugins, configuration handlers, and cookies
  - Integrates `PacketMgr` for version-specific packet handling
  - Provides customizable `JoinLogin` and `JoinConfiguration` functions

- **`Conn`** (`client.go`)
  - Thread-safe wrapper around `go-mc` network connection
  - Implements packet queuing (send/recv queues)
  - Uses sync.Pool for efficient packet buffer management
  - Integrates with `PacketMgr` for packet ID translation/debugging

- **`Events`** (`event.go`)
  - Event system with priority-based packet handlers
  - Supports generic handlers (all packets) and ID-specific handlers
  - Dynamic event registration with priority ordering
  - Version-agnostic through `models.ClientboundPacketID`

#### Sub-packages:

- **`bot/basic/`** - Core gameplay handlers
  - Player state management (position, health, gamemode)
  - Keep-alive packet handling
  - Settings synchronization
  - Respawn logic
  - Cookie management
  - Tags updates
  
- **`bot/msg/`** - Chat message handling
  - Chat message parsing and event dispatching
  - System messages vs player messages

- **`bot/screen/`** - Inventory/container management
  - Chest interactions
  - Inventory tracking
  - Screen/container events

- **`bot/world/`** - World/chunk management
  - Chunk loading/unloading
  - Level data handling
  - Chunk position tracking

- **`bot/playerlist/`** - Player list management
  - Tracks online players
  - Player info updates

#### Network Flow:

1. **Connection** (`mcbot.go`):
   - `JoinServerWithOptions()` → Handshake → Login → Configuration → Play
   - Supports custom dialers, contexts, packet queues
   - Protocol version negotiation

2. **Login Phase** (`login.go`):
   - Sends login start packet
   - Handles encryption (online mode)
   - Compression negotiation
   - Cookie exchange
   - Custom login plugins

3. **Configuration Phase** (`configuration.go`):
   - Registry data synchronization
   - Resource pack handling
   - Known packs selection
   - Cookie storage
   - Tag updates
   - Feature flags

4. **Play Phase** (`ingame.go`):
   - Main game loop in `HandleGame()`
   - Packet dispatching to registered event handlers
   - Bundle packet support
   - Packet buffer recycling

---

### 2. **Data Package** (`data/`)

Code generation and Minecraft version-specific data management.

#### Structure:

- **`data/models/`** - Core data models
  - `packets.go` - Packet ID type definitions
  - `blocks.go` - Block structures
  - `sounds.go` - Sound structures
  - `registry.go` - Registry parsing from Minecraft data
  - `asset_index_file.go` - Asset index parsing
  - `versionMetadata.go` - Version info structures

- **`data/versions/`** - Generated version managers
  - `packetMgr.go` - Version-specific packet ID mapping interface
  - `blockMgr.go` - Block ID management
  - `soundMgr.go` - Sound ID management
  - `versionProtocol.go` - Version to protocol mapping

- **`data/<version>/`** - Per-version generated code (e.g., `1.21.5/`)
  - `packetid.go` - Packet ID constants and lookup functions
  - `blockid.go` - Block data structures
  - `soundid.go` - Sound data structures
  - `<namespace>/<direction>/` - Protocol struct definitions (e.g., `play/clientbound/`)

#### Code Generation Pipeline:

1. **`gen_data.go`** - Main data generation orchestrator
   - Downloads Minecraft client/server JARs
   - Extracts assets (blocks, sounds, lang files)
   - Generates server reports (blocks, registries, packets)
   - Caches downloaded data
   - Generates version-specific managers

2. **`gen_packet.go`** - Packet code generation
   - Reads packets.json from server reports
   - Generates packet ID constants
   - Creates lookup functions (name → ID, ID → name)
   - **Uses protodef-go to parse protocol definitions**
   - Generates Go struct definitions from protocol specs

3. **`parsePackets.go`** - Protocol definition fetching
   - Downloads protocol.json from minecraft-data GitHub repo
   - **Uses `protodef.ReadProtocolFile()` to parse JSON**
   - Caches protocol definitions

4. **`getMCVersionData.go`** - Minecraft version data fetcher
   - Downloads version manifests from Mojang
   - Retrieves JARs and assets
   - Runs data generators from server JAR

---

### 3. **Utils Package** (`utils/`)

Utility functions for server version checking.

- **`checkServerVersion.go`** - Server ping and version validation

---

## Component Interactions

### Data Flow:

```
User Application
    ↓
bot.Client.JoinServer()
    ↓
[Handshake] → [Login] → [Configuration] → [Play]
    ↓
bot.Client.HandleGame() ← reads packets
    ↓
Events.handlers[] ← dispatches by packet ID
    ↓
User Event Handlers (basic.Player, world.World, msg.Chat, etc.)
```

### Packet Management:

```
Incoming Network Packets
    ↓
Conn.ReadPacket() ← thread-safe queue
    ↓
PacketMgr.ClientboundToString() ← version-specific translation
    ↓
Events system ← dispatches to handlers
    ↓
Handler functions ← process packet data
```

### Version Management:

```
Application startup
    ↓
versions.GetPacketMgrForVersion("1.21.5")
    ↓
Version-specific generated code (data/1.21.5/)
    ↓
Used throughout bot for packet IDs, blocks, sounds
```

---

## Deployment Architecture

### Build Process:

1. **Data Generation** (one-time per version):
   ```bash
   cd data
   go generate
   ```
   - Downloads Minecraft data
   - Generates version-specific code
   - Creates manager interfaces

2. **Application Build**:
   ```bash
   go build
   ```
   - Standard Go compilation
   - Links generated version code
   - Produces single binary

### Dependencies:

#### External Libraries:
- **github.com/Tnze/go-mc** - Minecraft protocol primitives
  - Network layer (`net` package)
  - Packet types (`net/packet`)
  - Chat, NBT, registry support
  
- **github.com/protodef-go/protodef-go** - Protocol definition parsing
  - `datatypes` package - Protocol type definitions
  - `protocol` package - Protocol structure
  - `namespace` package - Namespace handling
  - `protodef` package - JSON parsing

- **github.com/google/uuid** - UUID handling
- **github.com/aquasecurity/go-version** - Version parsing/comparison
- **golang.org/x/text** - Text transformations (title casing)

#### Development Dependencies:
- **github.com/davecgh/go-spew** - Debugging output
- **github.com/stretchr/testify** - Testing framework

### Runtime Environment:

- **Go Version**: 1.23.3+
- **OS**: Cross-platform (Linux, Windows, macOS)
- **Network**: Requires internet access for:
  - Minecraft server connections
  - Authentication (online mode)
  - Data generation (development only)

---

## Runtime Behavior

### Initialization:

1. Create `PacketMgr` for target Minecraft version
2. Create `bot.Client` with `PacketMgr`
3. Register event handlers (basic.Player, world.World, etc.)
4. Configure authentication (online/offline mode)

### Connection Lifecycle:

1. **Handshake**:
   - Send protocol version
   - Send server address/port
   - Indicate login intent

2. **Login**:
   - Send player name/UUID
   - Handle encryption (online mode)
   - Process compression settings
   - Exchange cookies
   - Await login success

3. **Configuration**:
   - Receive and store registries
   - Process resource packs
   - Update tags
   - Handle feature flags
   - Acknowledge configuration complete

4. **Play**:
   - Enter main game loop
   - Process incoming packets
   - Dispatch to event handlers
   - Send response packets (keep-alive, teleport confirms, etc.)

### Packet Handling:

- **Generic Handlers**: Called for every packet (priority-ordered)
- **Specific Handlers**: Called only for matching packet IDs (priority-ordered)
- **Error Handling**: Errors bubble up wrapped with context
- **Bundle Support**: Multiple packets processed atomically

### State Management:

- **Player State**: Position, health, gamemode, dimension (basic.Player)
- **World State**: Loaded chunks, block data (world.World)
- **Registry State**: Dimension types, biomes, etc. (Client.Registries)
- **Network State**: Cookies, custom report details (Client)

---

## Protodef-Go Integration Analysis

### Current Usage in mc-bot-go:

The project uses `protodef-go` in the **data generation phase** to parse Minecraft protocol definitions and generate Go code.

#### Import Locations:

1. **`data/gen_packet.go`**:
   ```go
   import (
       "github.com/protodef-go/protodef-go/datatypes"
       "github.com/protodef-go/protodef-go/namespace"
       "github.com/protodef-go/protodef-go/protocol"
   )
   ```

2. **`data/parsePackets.go`**:
   ```go
   import (
       "github.com/protodef-go/protodef-go/protocol"
       protodef "github.com/protodef-go/protodef-go/protodef"
   )
   ```

#### API Usage Patterns:

##### parsePackets.go (Line 228):
```go
data, err := protodef.ReadProtocolFile(tmpFile.Name())
```
**Status**: ✅ **COMPATIBLE** - This function exists in current protodef-go

##### gen_packet.go - Type System:
```go
// Creating protocol structures
func generateProtocolStructs(version string, protocolDefinitions *protocol.Protocol) error {
    for _, t := range protocolDefinitions.Types {
        // Process types
    }
    for name, namespace := range protocolDefinitions.Namespaces {
        // Process namespaces
    }
}
```
**Status**: ✅ **COMPATIBLE** - `protocol.Protocol` structure matches

##### gen_packet.go - Datatype Handling:
```go
// Type assertions and conversions
func toContainer(t *datatypes.Type) *datatypes.Container {
    return t.Extras.(*datatypes.Container)
}

func toArray(t *datatypes.Type) *datatypes.Array {
    return t.Extras.(*datatypes.Array)
}

func toBitfield(t *datatypes.Type) *datatypes.Bitfield {
    return t.Extras.(*datatypes.Bitfield)
}

func toOption(t *datatypes.Type) *datatypes.Option {
    return t.Extras.(*datatypes.Option)
}

func toSwitch(t *datatypes.Type) *datatypes.Switch {
    return t.Extras.(*datatypes.Switch)
}
```
**Status**: ✅ **COMPATIBLE** - All these types exist in current protodef-go

##### gen_packet.go - Type Processing:
```go
func processType(t *datatypes.Type, baseTypes map[string]string, isAnon bool) []*datatypes.Type {
    // Accesses: t.Name, t.TypeName, t.Extras
    if container, ok := isContainer(t); ok {
        for _, field := range container.Fields {
            // Accesses: field.Name, field.Type, field.Anon
        }
    }
}
```
**Status**: ✅ **COMPATIBLE** - All field accesses match current API

##### gen_packet.go - Namespace Processing:
```go
func processNamespace(version, nsName string, namespace *namespace.Namespace, baseTypes map[string]string) error {
    for boundName, boundNamespace := range namespace.Namespaces {
        for _, theType := range boundNamespace.Types {
            // Process types
            if t.Extras != nil {
                t.Extras.SetName(t.Name)
                t.Extras.UpdateContainedNames(updatedNames)
            }
        }
    }
}
```
**Status**: ⚠️ **POTENTIALLY INCOMPATIBLE** - Methods on `Extras` interface

### Missing/Incompatible Features:

#### 1. **Extras Interface Methods**:

**Issue**: The code calls methods on `t.Extras`:
- `t.Extras.SetName(name)` (lines 466, 547, 587, 588 in gen_packet.go)
- `t.Extras.UpdateContainedNames(map)` (line 614 in gen_packet.go)

**Current protodef-go**: The `Extras` field is `any` type. The specific types like `Container`, `Array`, etc. exist but may not have these interface methods.

**Investigation Needed**: Check if current protodef-go datatypes implement:
- `SetName(string)` method
- `UpdateContainedNames(map[string]string)` method
- `GetName() string` method (referenced via `t.Extras.GetName()` on line 465)

**Impact**: These are used during code generation to properly name nested/anonymous types.

**Recommendation**: 
- If methods missing: Add them to protodef-go datatypes
- Or: Update mc-bot-go to use a different approach for naming

---

### Compatibility Summary:

| Component | Status | Notes |
|-----------|--------|-------|
| `protodef.ReadProtocolFile()` | ✅ Compatible | Core parsing function works |
| `protocol.Protocol` structure | ✅ Compatible | Types and Namespaces fields present |
| `datatypes.Type` structure | ✅ Compatible | Name, TypeName, Extras fields present |
| `datatypes.Container` | ✅ Compatible | Name and Fields present |
| `datatypes.Array` | ✅ Compatible | Structure exists |
| `datatypes.Bitfield` | ✅ Compatible | Structure exists |
| `datatypes.Option` | ✅ Compatible | Structure exists |
| `datatypes.Switch` | ✅ Compatible | Structure exists |
| `datatypes.ContainerField` | ✅ Compatible | Name, Type, Anon fields present |
| `namespace.Namespace` | ⚠️ Needs verification | Used but structure not examined |
| `Extras.SetName()` | ⚠️ Potentially missing | Method called but not confirmed to exist |
| `Extras.GetName()` | ⚠️ Potentially missing | Method called but not confirmed to exist |
| `Extras.UpdateContainedNames()` | ⚠️ Potentially missing | Method called but not confirmed to exist |

---

## Required Actions for Full Compatibility:

### 1. **Verify Extras Interface**:

Check if protodef-go datatypes implement a common interface with these methods:
```go
type ExtrasInterface interface {
    SetName(string)
    GetName() string
    UpdateContainedNames(map[string]string)
}
```

### 2. **If Methods Missing - Option A**: Add to protodef-go

Add methods to datatypes in protodef-go:
- `Container.SetName(string)`
- `Container.GetName() string`
- `Container.UpdateContainedNames(map[string]string)`
- Same for `Array`, `Bitfield`, `Option`, `Switch`

### 3. **If Methods Missing - Option B**: Update mc-bot-go

Refactor `gen_packet.go` to not rely on these methods:
- Track naming separately in generation code
- Use direct field access instead of methods

### 4. **Test Code Generation**:

Run the data generation:
```bash
cd data
go generate
```

Check for:
- Compilation errors in generated code
- Correct packet struct definitions
- Proper type conversions

### 5. **Validate Generated Code**:

After generation, verify:
- `data/versions/packetMgr.go` compiles
- Version-specific packet files work correctly
- Protocol struct definitions are usable

---

## Future Considerations:

### Version Support:

The code generation system is designed to support multiple Minecraft versions:
- Each version gets its own subdirectory (`data/1.21.5/`, etc.)
- Version managers provide abstraction over version differences
- New versions can be added by updating `Versions` array in `gen_data.go`

### Protocol Evolution:

As Minecraft protocol changes:
- Packet IDs may shift (handled by PacketMgr)
- New packet types may be added (requires event handler updates)
- Protocol structures change (handled by regenerating from protocol.json)

### Performance Optimization:

Current architecture is optimized for:
- Minimal allocations (packet buffer pooling)
- Concurrent packet processing (goroutine-based send/recv)
- Event dispatch efficiency (priority-sorted handlers)

Potential improvements:
- Packet caching for repeated sends
- More granular event categories
- Lazy chunk loading/unloading

---

## Conclusion:

The mc-bot-go project is **largely compatible** with the current protodef-go implementation. The main areas requiring attention are:

1. **Verification of Extras interface methods** (`SetName`, `GetName`, `UpdateContainedNames`)
2. **Namespace structure validation** (not thoroughly examined in this analysis)

If these methods are missing, the project has two clear paths forward:
- **Extend protodef-go** with the missing interface methods
- **Refactor mc-bot-go** to remove dependency on these methods

Both approaches are straightforward and the codebase is well-structured to accommodate either solution.
