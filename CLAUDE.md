# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`mc-bot-go` is a Go library implementing a Minecraft Java Edition client bot: handshake, login (online/offline mode, encryption, compression), configuration phase, and the in-game packet event loop. It is consumed as a library by other projects (e.g. `cmd/offline`, `cmd/online`, `cmd/ping` are minimal example binaries, not the product).

Protocol data (packet IDs, registries, models, per-version codegen) lives in a **separate sibling module**, `github.com/reallyoldfogie/mc-protocol-go`, imported as `github.com/reallyoldfogie/mc-protocol-go/models` and `.../data/versions`. `go.mod` has local `replace` directives pointing at sibling checkouts:

```
github.com/protodef-go/protodef-go => /home/reallyoldfogie/src/github.com/reallyoldfogie/protodef-go
github.com/reallyoldfogie/mc-protocol-go => /home/reallyoldfogie/src/github.com/reallyoldfogie/mc-protocol-go
```

Both sibling repos must exist locally at those exact paths for this module to build. If you need to change packet IDs, registry parsing, protocol structs, or per-version codegen, that work happens in `mc-protocol-go` (and `protodef-go` beneath it), not here.

**`docs/*.md` in this repo are stale** — they describe a `data/` code-generation package (protodef-go compatibility investigations, packet codegen) that no longer exists in this module; that functionality has moved to `mc-protocol-go`. Don't trust them for current architecture; they're kept for historical reference only.

## Commands

```bash
go build ./...      # build everything
go vet ./...         # run before build, per prior convention (see WARP.md)
go test ./...        # run all tests
go test ./bot/screen/... -run TestName -v   # run a single test
```

Tests currently exist only in `bot/screen/` (`screen_test.go`, `horse_container_test.go`) and `bot/example_test.go` (runnable usage examples, not real Example output tests).

There is no linter/formatter config beyond standard `gofmt`/`go vet`.

## Architecture

### Connection lifecycle (`bot/mcbot.go`, `login.go`, `configuration.go`, `ingame.go`)

`Client.JoinServer` / `JoinServerWithDialer` / `JoinServerWithOptions` drive a fixed sequence:

1. **Handshake** — writes protocol version, host, port, intent (`bot/mcbot.go`).
2. **Login** (`joinLogin` in `login.go`) — login start, optional encryption (Mojang session auth via `sessionserver.mojang.com`), compression threshold, cookie exchange, login plugin messages. Terminates on `LoginSuccess`/`LoginFinished`.
3. **Configuration** (`joinConfiguration` in `configuration.go`) — registry data, resource packs, tags, feature flags, cookies, custom report details, keep-alive/ping. Terminates on `FinishConfiguration`.
4. **Play** — after configuration, `wrapConn` (`bot/client.go`) spins up the connection's read/write goroutines and queues; `Client.HandleGame` (`bot/ingame.go`) is the caller-driven loop that reads packets and dispatches them.

Every phase's packet IDs are resolved dynamically via `c.packetMgr` (a `models.PacketMgr` from `mc-protocol-go`, one instance per Minecraft version) rather than hardcoded constants — e.g. `c.packetMgr.GetClientboundLoginPacketID("ClientboundLoginHello")`. When adding new packet handling, look up IDs the same way instead of hardcoding numbers.

**Version-specific overrides**: `Client.SetJoinLogin` / `SetJoinConfiguration` let a caller fully replace the login/config functions. `Client.SetVersionHandler` (`bot/version_handler.go`) installs a `VersionHandler` that, when present, takes over specific sub-steps (send login start, parse login success, keep-alive, ping) inside the default `joinLogin`/`joinConfiguration` — this is the extension point for versions whose login/config framing differs from the default path.

### Event system (`bot/event.go`, `bot/ingame.go`)

- `Events` holds two kinds of listeners: `generic` (run on every packet, in priority order) and `handlers[]` (indexed by clientbound packet ID, run only for a matching packet, in priority order). Generic listeners always run before ID-specific ones.
- Register with `Client.Events().AddListener(...)` / `AddGeneric(...)`, passing `PacketHandler{ID, Name, Priority, F}`. Higher `Priority` runs first.
- `Client.HandleGame` is a blocking loop the caller must run (typically in its own goroutine); it reads packets off `Conn`, unwraps bundle packets (`ClientboundBundleDelimiter`) recursively via `handleBundlePackets`, and dispatches each to `handlePacket`, which calls generic handlers then ID-specific handlers. A handler returning an error aborts the loop (wrapped in `PacketHandlerError`).
- `Conn` (`bot/client.go`) wraps the underlying `go-mc` connection with separate read/write goroutines and queues (`send`/`recv`), a `sync.Pool` for packet buffers (callers must `Put` buffers back after handling — see the `c.conn.pool.Put(p.Data)` calls in `ingame.go`), and optional packet debug logging (`PacketDebugEnabled` var) and JSON packet logging (`Conn.PacketLogWriter`).

### Sub-packages under `bot/`

- `bot/basic/` — core gameplay state handlers: player state (position/health/gamemode), keep-alive, settings sync, cookies, tags. These are the handlers a real bot registers via `Events().AddListener`.
- `bot/msg/` — chat message parsing/dispatch (system vs. player messages).
- `bot/screen/` — inventory/container (chest, generic container, horse container) tracking; the only sub-package with real unit tests.
- `bot/world/` — chunk/world state (loading, storage, position tracking).
- `bot/playerlist/` — online player list tracking.

None of these are wired in automatically — a consuming application registers the handlers it wants against `Client.Events()`.

### Replay / debugging support (`bot/replay.go`, `Conn.PacketLogWriter`, `JoinOptions.MovementMirror`)

`JoinOptions.ReplayRecorder` captures raw packets during login/config/play for replay-file generation (e.g. for ReplayMod); `recordForReplay` is called at each packet-read site. `JoinOptions.MovementMirror` can mirror serverbound movement packets into synthetic clientbound ones. Both are optional and nil by default — check `bot/replay.go` before touching packet-recording call sites, since recording intentionally skips some protocol-only packets (e.g. `SetCompression`, bundle delimiters).

### Registries and custom data (`bot/client.go`, `bot/configuration.go`)

Registry data received during configuration is parsed manually (not via `go-mc`'s registry system) into `CustomRegistry`/`RegistryEntry` (with optional NBT), stored on `Client` keyed by registry ID, and guarded by `registryDataMu`. `JoinOptions.RegistryDataCallback` is invoked per-registry with a simplified name→ID map for callers that don't need the full entry data.

## Working across the module boundary

Because `mc-protocol-go` and `protodef-go` are pulled in via local `replace` (not versioned releases), a change here that depends on a new field/method in `models.PacketMgr` or `models.*` requires that change to land in the sibling repo first, then `go build` here will pick it up automatically (no `go mod` version bump needed while the replace is local-path).
