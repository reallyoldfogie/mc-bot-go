// Package basic provides some basic packet handler which client needs.
//
// # [player]
//
// The [player] is attached to a [Client] by calling [NewPlayer] before the client joins a server.
//
// There is 4 kinds of clientbound packet is handled by this package.
//   - LoginPacket, for cache player info. The player info will be stored in [player.PlayerInfo].
//   - KeepAlivePacket, for avoid the client to be kicked by the server.
//   - PlayerPosition, is only received when server teleporting the player.
//   - Respawn, for updating player info, which may change when player respawned.
//
// # [EventsListener]
//
// Handles some basic event you probably need.
//   - GameStart
//   - Disconnect
//   - HealthChange
//   - Death
package basic

import (
	"fmt"

	pk "github.com/Tnze/go-mc/net/packet"

	"github.com/reallyoldfogie/mc-bot-go/bot"
	"github.com/reallyoldfogie/mc-protocol-go/models"
)

type Player interface {
	Client() bot.Client
	DimensionType() int32
	AcceptTeleportation(pk.VarInt) error
	Respawn() error
}

type player struct {
	client   bot.Client
	Settings Settings

	packetMgr models.PacketMgr

	PlayerInfo
	WorldInfo
}

// NewPlayer create a new player manager.
func NewPlayer(client bot.Client, settings Settings, events EventsListener, packetMgr models.PacketMgr) Player {
	player := &player{client: client, Settings: settings, packetMgr: packetMgr}

	fmt.Println("[NewPlayer] packetMgr:", packetMgr.Name())
	fmt.Printf("[NewPlayer] Settings: %+v\n", settings)

	client.Events().AddListener(
		bot.PacketHandler{Priority: 0, ID: packetMgr.GetClientboundPacketID("ClientboundLogin"), F: player.handleLoginPacket},
		bot.PacketHandler{Priority: 0, ID: packetMgr.GetClientboundPacketID("ClientboundKeepAlive"), F: player.handleKeepAlivePacket},
		bot.PacketHandler{Priority: 0, ID: packetMgr.GetClientboundPacketID("ClientboundRespawn"), F: player.handleRespawnPacket},
		bot.PacketHandler{Priority: 0, ID: packetMgr.GetClientboundPacketID("ClientboundPing"), F: player.handlePingPacket},
		bot.PacketHandler{Priority: 0, ID: packetMgr.GetClientboundPacketID("ClientboundCommonCookieRequest"), F: player.handleCookieRequestPacket},
		bot.PacketHandler{Priority: 0, ID: packetMgr.GetClientboundPacketID("ClientboundCommonStoreCookie"), F: player.handleStoreCookiePacket},
		bot.PacketHandler{Priority: 0, ID: packetMgr.GetClientboundPacketID("ClientboundUpdateTags"), F: player.handleUpdateTags},
		// bot.PacketHandler{Priority: 0, ID: packetMgr.GetClientboundPacketID("ClientboundTags"), F: player.handleUpdateTags},
	)
	events.attach(player, packetMgr)
	return player
}

func (p *player) Client() bot.Client   { return p.client }
func (p *player) DimensionType() int32 { return p.WorldInfo.DimensionType }

// Respawn is used to send a respawn packet to the server.
// Typically, you should call this method when the player is dead (in the [Death] event handler).
func (p *player) Respawn() error {
	const PerformRespawn = 0

	err := p.client.Conn().WritePacket(pk.Marshal(
		p.packetMgr.GetServerboundPacketID("ServerboundClientCommand"),
		pk.VarInt(PerformRespawn),
	))
	if err != nil {
		return Error{err}
	}

	return nil
}

// AcceptTeleportation is used to send a teleport confirmation packet to the server.
// Typically, you should call this method when received a ClientboundPlayerPosition packet (in the [Teleported] event handler).
func (p *player) AcceptTeleportation(teleportID pk.VarInt) error {
	err := p.client.Conn().WritePacket(pk.Marshal(
		p.packetMgr.GetServerboundPacketID("ServerboundAcceptTeleportation"),
		teleportID,
	))
	if err != nil {
		return Error{err}
	}
	return nil
}

type Error struct {
	Err error
}

func (e Error) Error() string {
	return "bot/basic: " + e.Err.Error()
}

func (e Error) Unwrap() error {
	return e.Err
}
