package bot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	// "github.com/Tnze/go-mc/net"
	mcnet "github.com/Tnze/go-mc/net"
	pk "github.com/Tnze/go-mc/net/packet"
	"github.com/Tnze/go-mc/net/queue"

	"github.com/reallyoldfogie/mc-protocol-go/models"
)

// var PacketDebugEnabled = envBool("MC_BOT_PACKET_DEBUG")
var PacketDebugEnabled = true

// Client is used to access Minecraft server
type Client interface {
	ConfigHandler
	Conn() *Conn
	SetAuth(Auth)

	JoinServer(ctx context.Context, addr string) error
	JoinServerWithDialer(ctx context.Context, dialer *net.Dialer, addr string) (err error)
	JoinServerWithOptions(ctx context.Context, addr string, options JoinOptions) (err error)

	HandleGame(ctx context.Context) error

	Name() string
	UUID() uuid.UUID
	Cookies() map[string][]byte
	SetCookies(map[string][]byte)
	RegistryData() map[string]*CustomRegistry // registryID -> Registry
	RegistryTags() map[string]*RegistryTags   // registryID -> tags data
	Events() Events
	LoginPlugin() map[string]CustomPayloadHandler
	CustomReportDetails() map[string]string

	SetJoinLogin(func(conn *mcnet.Conn) error)
	SetJoinConfiguration(func(conn *mcnet.Conn) error)
	SetVersionHandler(VersionHandler)

	MovementMirror() MovementMirror
	RegistryCallback() RegistryDataCallback

	PacketMgr() models.PacketMgr
	Close() error
}

type client struct {
	conn *Conn
	auth Auth

	// These are filled when login process
	name    string
	uuid    uuid.UUID
	cookies map[string][]byte

	// Custom registry storage - version-agnostic, stores complete registry data including NBT
	registryData   map[string]*CustomRegistry // registryID -> Registry
	registryTags   map[string]*RegistryTags   // registryID -> tags data
	registryDataMu sync.RWMutex

	// Ingame packet handlers
	events Events

	// Login plugins
	loginPlugin map[string]CustomPayloadHandler

	// Configuration handler
	ConfigHandler

	// replayRecorder is an optional packet sink used to capture
	// pre-ingame packets (login/configuration) for replay files.
	replayRecorder PacketRecorder

	customReportDetails map[string]string

	// allow customization of login/join process (different versions may need different workflows/IDs)
	joinLoginOverride         func(conn *mcnet.Conn) error
	joinConfigurationOverride func(conn *mcnet.Conn) error

	movementMirror   MovementMirror
	registryCallback RegistryDataCallback

	packetMgr      models.PacketMgr
	versionHandler VersionHandler // optional version-specific packet handler for login/config
}

// RegistryDataCallback is invoked after configuration phase with registry data.
type RegistryDataCallback func(registryID string, entries map[string]int32)

// CustomRegistry stores all entries for a single registry (e.g., minecraft:entity_type)
type CustomRegistry struct {
	ID      string                    // e.g., "minecraft:entity_type"
	Entries []*RegistryEntry          // All entries in order (index = ID)
	ByName  map[string]*RegistryEntry // Fast lookup by name
}

// RegistryEntry represents a single entry in a registry
type RegistryEntry struct {
	ID   int32               // Numeric ID (array index in packet)
	Name string              // e.g., "minecraft:player"
	NBT  *models.NBTCompound // Optional NBT data with entry properties
}

// RegistryTags stores tag data for a registry
type RegistryTags struct {
	RegistryID string             // e.g., "minecraft:block"
	Tags       map[string][]int32 // tag name -> list of entry IDs
}

// CustomPayloadHandler is a function handling custom payload
type CustomPayloadHandler func(data []byte) ([]byte, error)

func (c *client) Close() error {
	return c.conn.Close()
}

// NewClient init and return a new Client.
//
// A new Client has default name "Steve" and zero UUID.
// It is usable for an offline-mode game.
//
// For online-mode, you need login your Mojang account
// and load your Name, UUID and AccessToken to client.
func NewClient(packetMgr models.PacketMgr) Client {
	return &client{
		auth:                Auth{Name: "Steve"},
		registryData:        make(map[string]*CustomRegistry),
		registryTags:        make(map[string]*RegistryTags),
		events:              NewEvents(int32(packetMgr.GetClientboundPacketID("ClientboundPacketIDGuard")), nil),
		loginPlugin:         make(map[string]CustomPayloadHandler),
		ConfigHandler:       NewDefaultConfigHandler(),
		customReportDetails: make(map[string]string),
		cookies:             make(map[string][]byte),
		packetMgr:           packetMgr,
	}
}

func (c *client) Name() string                                 { return c.name }
func (c *client) UUID() uuid.UUID                              { return c.uuid }
func (c *client) Conn() *Conn                                  { return c.conn }
func (c *client) SetAuth(auth Auth)                            { c.auth = auth }
func (c *client) RegistryData() map[string]*CustomRegistry     { return c.registryData }
func (c *client) RegistryTags() map[string]*RegistryTags       { return c.registryTags }
func (c *client) Events() Events                               { return c.events }
func (c *client) LoginPlugin() map[string]CustomPayloadHandler { return c.loginPlugin }
func (c *client) Cookies() map[string][]byte                   { return c.cookies }
func (c *client) SetCookies(cookies map[string][]byte)         { c.cookies = cookies }
func (c *client) CustomReportDetails() map[string]string       { return c.customReportDetails }
func (c *client) SetJoinLogin(f func(conn *mcnet.Conn) error)  { c.joinLoginOverride = f }
func (c *client) SetJoinConfiguration(f func(conn *mcnet.Conn) error) {
	c.joinConfigurationOverride = f
}
func (c *client) MovementMirror() MovementMirror         { return c.movementMirror }
func (c *client) RegistryCallback() RegistryDataCallback { return c.registryCallback }
func (c *client) PacketMgr() models.PacketMgr            { return c.packetMgr }
func (c *client) SetVersionHandler(vh VersionHandler)    { c.versionHandler = vh }

// Conn is a concurrently-safe wrapper of net.Conn with packet queue.
// Note that not all methods are concurrently-safe.
type Conn struct {
	*mcnet.Conn
	send, recv queue.Queue[pk.Packet]
	pool       sync.Pool // pool of recv packet data
	rerr       error
	packetMgr  models.PacketMgr
	replayRec  PacketRecorder
	mirror     MovementMirror
	closed     atomic.Bool
	name       string
	logWriter  io.Writer // optional writer for logging all packets (clientbound + serverbound)
}

func wrapConn(ctx context.Context, c *mcnet.Conn, qr, qw queue.Queue[pk.Packet], packetMgr models.PacketMgr, rec PacketRecorder, mirror MovementMirror, name string, logWriter io.Writer) *Conn {
	wc := Conn{
		Conn:      c,
		send:      qw,
		recv:      qr,
		pool:      sync.Pool{New: func() any { return []byte{} }},
		rerr:      nil,
		packetMgr: packetMgr,
		replayRec: rec,
		mirror:    mirror,
		name:      name,
		logWriter: logWriter,
	}
	go func() {
	ReadLoop:
		for {
			select {
			case <-ctx.Done():
				log.Printf("[conn.ReadLoop %s] received ctx.Done. exiting", wc.name)
				break ReadLoop
			default:
				// take a buffer from pool, after the packet is handled we put it back
				p := pk.Packet{Data: wc.pool.Get().([]byte)}
				if err := c.ReadPacket(&p); err != nil {
					wc.rerr = err
					break ReadLoop
				}
				if ok := wc.recv.Push(p); !ok {
					wc.rerr = errors.New("receive queue is full")
					break ReadLoop
				}
			}
		}
		wc.recv.Close()
	}()
	go func() {
	WriteLoop:
		for {
			select {
			case <-ctx.Done():
				log.Printf("[conn.WriteLoop %s] received ctx.Done. exiting", wc.name)
				break WriteLoop
			default:
				p, ok := wc.send.Pull()
				if !ok {
					break WriteLoop
				}
				if PacketDebugEnabled {
					log.Printf("[conn.WriteLoop %s] %#v", wc.name, p)
				}
				if err := c.WritePacket(p); err != nil {
					log.Printf("[conn.WriteLoop %s][ERROR] WritePacket failed: %#v", wc.name, err)
					break WriteLoop
				}
			}
		}
	}()

	return &wc
}

func (c *Conn) ReadPacket(p *pk.Packet) error {
	packet, ok := c.recv.Pull()
	if !ok {
		return c.rerr
	}
	*p = packet
	if PacketDebugEnabled {
		packetName := c.packetMgr.ClientboundToString(models.ClientboundPacketID(p.ID))
		log.Printf("[ReadPacket %s] %d (%0#2X) %s\n", c.name, p.ID, p.ID, packetName)
		copyLength := min(20, len(p.Data))
		ellipsis := ""
		if copyLength != len(p.Data) {
			ellipsis = "..."
		}
		log.Printf("[ReadPacket %s] %v%s len: %d\n", c.name, p.Data[:copyLength], ellipsis, len(p.Data))
	}
	// Log clientbound packet if writer configured
	if c.logWriter != nil {
		c.logPacket(p, "clientbound")
	}
	return nil
}

func (c *Conn) WritePacket(p pk.Packet) (err error) {
	if c.closed.Load() {
		return errors.New("connection closed")
	}
	if PacketDebugEnabled {
		packetName := c.packetMgr.ServerboundToString(models.ServerboundPacketID(p.ID))
		log.Printf("[WritePacket %s] %d (%0#2X) %s\n", c.name, p.ID, p.ID, packetName)
		log.Printf("[WritePacket %s] %v len: %d \n", c.name, p.Data[:min(20, len(p.Data))], len(p.Data))
	}

	// Log serverbound packet if writer configured
	if c.logWriter != nil {
		c.logPacket(&p, "serverbound")
	}

	if c.mirror != nil {
		c.mirror.HandleServerbound(p)
	}

	defer func() {
		if r := recover(); r != nil {
			log.Printf("[WritePacket %s][ERROR] panic while sending: %v", c.name, r)
			err = errors.New("write packet on closed queue")
		}
	}()
	ok := c.send.Push(p)
	if !ok {
		return errors.New("queue is full")
	}
	return nil
}

// PacketLog is a JSON structure for logging packets with direction and metadata
type PacketLog struct {
	Timestamp      time.Time `json:"timestamp"`
	Direction      string    `json:"direction"` // "serverbound" or "clientbound"
	PacketID       int32     `json:"packet_id"`
	PacketName     string    `json:"packet_name"`
	DataLength     int       `json:"data_length"`
	Data           []byte    `json:"data"`
	ConnectionName string    `json:"connection_name"`
	Source         string    `json:"source"`
}

// logPacket writes a packet to the log writer in JSON format
func (c *Conn) logPacket(p *pk.Packet, direction string) {
	if c.logWriter == nil {
		return
	}

	var packetName string
	if direction == "serverbound" {
		packetName = c.packetMgr.ServerboundToString(models.ServerboundPacketID(p.ID))
	} else {
		packetName = c.packetMgr.ClientboundToString(models.ClientboundPacketID(p.ID))
	}

	pl := PacketLog{
		Timestamp:      time.Now(),
		Direction:      direction,
		PacketID:       p.ID,
		PacketName:     packetName,
		DataLength:     len(p.Data),
		Data:           p.Data,
		ConnectionName: c.name,
		Source:         "connection",
	}

	if err := json.NewEncoder(c.logWriter).Encode(pl); err != nil {
		log.Printf("[logPacket %s][ERROR] failed to write packet log: %v", c.name, err)
	}
}

func (c *Conn) Close() error {
	c.closed.Store(true)
	c.send.Close()
	return c.Conn.Close()
}

// Position is a 3D vector.
type Position struct {
	X, Y, Z int
}

func envBool(key string) bool {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return false
	}
	switch strings.ToLower(val) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
