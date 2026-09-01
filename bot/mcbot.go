// Package bot implements a simple Minecraft client that can join a server
// or just ping it for getting information.
//
// Runnable example could be found at examples/ .
package bot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"

	"github.com/Tnze/go-mc/chat"
	mcnet "github.com/Tnze/go-mc/net"
	pk "github.com/Tnze/go-mc/net/packet"
	"github.com/Tnze/go-mc/net/queue"
	"github.com/Tnze/go-mc/yggdrasil/user"
)

// ProtocolVersion is the protocol version number of minecraft net protocol
const (
	ProtocolVersion = 767
	DefaultPort     = mcnet.DefaultPort
)

type JoinOptions struct {
	MCDialer mcnet.MCDialer

	// Indicate not to fetch and sending player's PubKey
	NoPublicKey bool

	// Specify the player PubKey to use.
	// If nil, it will be obtained from Mojang when joining
	KeyPair *user.KeyPairResp

	QueueRead  queue.Queue[pk.Packet]
	QueueWrite queue.Queue[pk.Packet]

	// ReplayRecorder is an optional sink used to capture raw packets during
	// the login/configuration phases before the ingame event loop starts.
	ReplayRecorder PacketRecorder
	// MovementMirror can be used to mirror certain serverbound packets (e.g. movement)
	// into synthetic clientbound packets for replays.
	MovementMirror MovementMirror

	// RegistryDataCallback is called after configuration phase completes,
	// allowing the caller to process registry data (e.g., extract entity types).
	RegistryDataCallback RegistryDataCallback

	// PacketLogWriter is an optional writer for logging all packets (serverbound + clientbound).
	// Packets are written as JSON with direction, ID, and raw data.
	PacketLogWriter io.Writer

	ProtocolVersion uint
}

// JoinServer connect a Minecraft server for playing the game.
// Using roughly the same way to parse address as minecraft.
func (c *client) JoinServer(ctx context.Context, addr string) (err error) {
	return c.JoinServerWithOptions(ctx, addr, JoinOptions{})
}

// JoinServerWithDialer is similar to JoinServer but using a net.Dialer.
func (c *client) JoinServerWithDialer(ctx context.Context, dialer *net.Dialer, addr string) (err error) {
	return c.JoinServerWithOptions(ctx, addr, JoinOptions{
		MCDialer: (*mcnet.Dialer)(dialer),
	})
}

func (c *client) JoinServerWithOptions(ctx context.Context, addr string, options JoinOptions) (err error) {
	if options.MCDialer == nil {
		options.MCDialer = &mcnet.DefaultDialer
	}

	if options.QueueRead == nil {
		options.QueueRead = queue.NewLinkedQueue[pk.Packet]()
	}
	if options.QueueWrite == nil {
		options.QueueWrite = queue.NewLinkedQueue[pk.Packet]()
	}

	if options.ProtocolVersion == 0 {
		options.ProtocolVersion = uint(c.packetMgr.VersionProtocol())
	}

	c.replayRecorder = options.ReplayRecorder
	c.movementMirror = options.MovementMirror
	c.registryCallback = options.RegistryDataCallback

	return c.join(ctx, addr, options)
}

func (c *client) join(ctx context.Context, addr string, options JoinOptions) error {
	const Handshake = 0x00

	// Split Host and Port. The DialMCContext will do this once,
	// but we need the result for sending handshake packet here.
	host, portStr, err := net.SplitHostPort(addr)
	var port uint64
	if err != nil {
		var addrErr *net.AddrError
		const missingPort = "missing port in address"
		if errors.As(err, &addrErr) && addrErr.Err == missingPort {
			host = addr
			port = 25565
		} else {
			return LoginErr{"split address", err}
		}
	} else {
		port, err = strconv.ParseUint(portStr, 0, 16)
		if err != nil {
			return LoginErr{"parse port", err}
		}
	}

	// Dial connection
	conn, err := options.MCDialer.DialMCContext(ctx, addr)
	if err != nil {
		return LoginErr{"connect server", err}
	}

	fmt.Println("Writing Handshake packet")
	// Handshake
	err = conn.WritePacket(pk.Marshal(
		Handshake,
		pk.VarInt(options.ProtocolVersion), // Protocol version
		pk.String(host),                    // Host
		pk.UnsignedShort(port),             // Port
		pk.VarInt(2),
	))
	if err != nil {
		return LoginErr{"handshake", err}
	}

	// Login Start
	joinLogin := c.joinLogin
	if c.joinLoginOverride != nil {
		joinLogin = c.joinLoginOverride
	}
	if err := joinLogin(conn); err != nil {
		return err
	}

	joinConfiguration := c.joinConfiguration
	if c.joinConfigurationOverride != nil {
		joinConfiguration = c.joinConfigurationOverride
	}

	// Configuration
	if err := joinConfiguration(conn); err != nil {
		return err
	}

	c.conn = wrapConn(ctx, conn, options.QueueRead, options.QueueWrite, c.packetMgr, c.replayRecorder, c.movementMirror, c.name, options.PacketLogWriter)
	return nil
}

type DisconnectErr chat.Message

func (d DisconnectErr) Error() string {
	return "disconnect because: " + chat.Message(d).String()
}
