# mc-bot-go

A Go library for building Minecraft Java Edition client bots. It implements the handshake, login (online/offline mode, encryption, compression), configuration, and play phases of the Minecraft network protocol, and provides a priority-ordered packet event system for building bot behavior on top.

Protocol data — packet IDs, registries, and per-Minecraft-version generated types — lives in the sibling module [`mc-protocol-go`](https://github.com/reallyoldfogie/mc-protocol-go) and is pulled in as a dependency.

## Requirements

- Go 1.24.5+
- A local checkout of [`mc-protocol-go`](https://github.com/reallyoldfogie/mc-protocol-go) and [`protodef-go`](https://github.com/reallyoldfogie/protodef-go), since `go.mod` currently points at them via local `replace` directives:

  ```
  github.com/protodef-go/protodef-go => /home/reallyoldfogie/src/github.com/reallyoldfogie/protodef-go
  github.com/reallyoldfogie/mc-protocol-go => /home/reallyoldfogie/src/github.com/reallyoldfogie/mc-protocol-go
  ```

  Adjust those paths (or replace with versioned requires) if you're building outside that layout.

## Install

```bash
go get github.com/reallyoldfogie/mc-bot-go
```

## Usage

### Ping a server

```go
package main

import (
	"log"

	"github.com/reallyoldfogie/mc-bot-go/bot"
)

func main() {
	resp, delay, err := bot.PingAndList("localhost:25565")
	if err != nil {
		log.Fatalf("ping and list server fail: %v", err)
	}
	log.Println("Status:", string(resp))
	log.Println("Delay:", delay)
}
```

See `cmd/ping/main.go` for the full example.

### Join a server (offline mode)

```go
package main

import (
	"context"
	"encoding/hex"
	"log"

	"github.com/Tnze/go-mc/offline"
	"github.com/reallyoldfogie/mc-bot-go/bot"
	"github.com/reallyoldfogie/mc-protocol-go/data/versions"
)

func main() {
	pktMgr := versions.GetPacketMgrForVersion("1.21.5")
	c := bot.NewClient(pktMgr)

	name := "MyBot"
	id := offline.NameToUUID(name)
	c.SetAuth(bot.Auth{
		Name: name,
		UUID: hex.EncodeToString(id[:]),
	})

	ctx := context.Background()
	if err := c.JoinServer(ctx, "127.0.0.1"); err != nil {
		log.Fatal(err)
	}
	log.Println("Login success")

	// Register event handlers before HandleGame, e.g.:
	// c.Events().AddListener(bot.PacketHandler{...})

	if err := c.HandleGame(ctx); err != nil {
		log.Fatal(err)
	}
}
```

See `cmd/offline/main.go` for the runnable version.

### Join a server (online mode)

Online mode requires a Mojang session (Yggdrasil) login to obtain a name, UUID, and access token before calling `JoinServer`. See `cmd/online/main.go` and `bot/example_test.go` (`ExampleClient_JoinServer_online`) for the full flow, including a note on Microsoft accounts (see [Tnze/go-mc#106](https://github.com/Tnze/go-mc/issues/106)).

## Architecture

`Client.JoinServer` / `JoinServerWithDialer` / `JoinServerWithOptions` drive the connection through four phases:

1. **Handshake** — protocol version, host, port, intent.
2. **Login** — login start, optional encryption via Mojang's session server, compression negotiation, cookie exchange, login plugin messages.
3. **Configuration** — registry data, resource packs, tags, feature flags, cookies, custom report details.
4. **Play** — `Client.HandleGame` is a caller-driven blocking loop that reads packets (including bundle packets) and dispatches them to registered event handlers.

Packet IDs are always resolved dynamically through a `models.PacketMgr` (one per Minecraft version, from `mc-protocol-go`) rather than hardcoded — e.g. `versions.GetPacketMgrForVersion("1.21.5")`.

### Event system

Register handlers on `Client.Events()`:

- `AddGeneric(...)` — runs on every packet, priority-ordered.
- `AddListener(...)` — runs only for a specific clientbound packet ID, priority-ordered.

Generic handlers always run before ID-specific ones for a given packet. A handler returning an error stops `HandleGame`.

### Extension points

- `Client.SetJoinLogin` / `SetJoinConfiguration` — fully replace the default login/configuration functions.
- `Client.SetVersionHandler` — install a `VersionHandler` to override specific login/config sub-steps (login start, login success parsing, keep-alive, ping) for versions whose framing differs from the default.
- `JoinOptions.ReplayRecorder` — capture raw packets during login/config/play for replay-file generation.
- `JoinOptions.MovementMirror` — mirror serverbound movement packets into synthetic clientbound ones.
- `JoinOptions.RegistryDataCallback` — receive a name→ID map per registry after the configuration phase.
- `JoinOptions.PacketLogWriter` — write every packet (both directions) as JSON for debugging.

### Sub-packages

| Package | Purpose |
|---|---|
| `bot/basic` | Player state (position, health, gamemode), keep-alive, settings sync, cookies, tags |
| `bot/msg` | Chat message parsing and dispatch |
| `bot/screen` | Inventory/container tracking (chests, generic containers, horse containers) |
| `bot/world` | Chunk/world state |
| `bot/playerlist` | Online player list tracking |

None of these are wired in automatically — register the handlers you need against `Client.Events()`.

## Development

```bash
go vet ./...   # run before building
go build ./...
go test ./...
```

## License

MIT — see [LICENSE](LICENSE).
