package bot

import (
	pk "github.com/Tnze/go-mc/net/packet"
)

// PacketWriter is a minimal interface for writing packets during login/config.
type PacketWriter interface {
	WritePacket(pk.Packet) error
}

// VersionHandler provides version-specific packet construction and parsing for login/config phases.
// This is a minimal interface that allows mc-agent to inject version-specific handlers.
type VersionHandler interface {
	// Version returns the Minecraft version string
	Version() string

	// Login returns the login phase handler
	Login() LoginHandler

	// Configuration returns the configuration phase handler
	Configuration() ConfigurationHandler
}

// LoginHandler handles login phase packet construction and parsing
type LoginHandler interface {
	// SendLoginStart sends the login start packet
	SendLoginStart(conn PacketWriter, username string, uuid [16]byte) error

	// SendEncryptionResponse sends the encryption response packet
	SendEncryptionResponse(conn PacketWriter, sharedSecret, verifyToken []byte) error

	// SendLoginAcknowledged sends the login acknowledged packet
	SendLoginAcknowledged(conn PacketWriter) error

	// ParseLoginSuccess parses a login success packet
	ParseLoginSuccess(p pk.Packet) (username string, uuid [16]byte, err error)

	// ParseEncryptionRequest parses an encryption request packet
	ParseEncryptionRequest(p pk.Packet) (serverID string, publicKey, verifyToken []byte, err error)
}

// ConfigurationHandler handles configuration phase packet construction and parsing
type ConfigurationHandler interface {
	// SendFinishConfiguration sends the finish configuration packet
	SendFinishConfiguration(conn PacketWriter) error

	// SendKeepAlive sends a keepalive packet during configuration
	SendKeepAlive(conn PacketWriter, id int64) error

	// SendPong sends a pong packet in response to a ping
	SendPong(conn PacketWriter, pingID int32) error

	// ParseKeepAlive parses a keepalive packet
	ParseKeepAlive(p pk.Packet) (id int64, err error)

	// ParsePing parses a ping packet
	ParsePing(p pk.Packet) (pingID int32, err error)
}
