package bot

import (
	"bytes"
	"fmt"
	"io"
	"log"

	"github.com/Tnze/go-mc/chat"
	"github.com/Tnze/go-mc/net"
	pk "github.com/Tnze/go-mc/net/packet"

	"github.com/reallyoldfogie/mc-protocol-go/models"
)

type ConfigHandler interface {
	EnableFeature(features []pk.Identifier)

	PushResourcePack(res ResourcePack)
	PopResourcePack(id pk.UUID)
	PopAllResourcePack()

	SelectDataPacks(packs []DataPack) []DataPack
}

type ResourcePack struct {
	ID            pk.UUID
	URL           string
	Hash          string
	Forced        bool
	PromptMessage *chat.Message // Optional
}

type ConfigErr struct {
	Stage string
	Err   error
}

func (l ConfigErr) Error() string {
	return "bot: configuration error: [" + l.Stage + "] " + l.Err.Error()
}

func (l ConfigErr) Unwrap() error {
	return l.Err
}

func (c *client) joinConfiguration(conn *net.Conn) error {
	log.Printf("[CONFIG] Starting configuration phase\n")
	packetCount := 0
	for {
		var p pk.Packet
		if err := conn.ReadPacket(&p); err != nil {
			return ConfigErr{"config custom payload", err}
		}

		packetCount++
		packetNameStr := c.packetMgr.ClientboundConfigToString(models.ClientboundPacketID(p.ID))
		log.Printf("[CONFIG #%d] Packet %s (ID=%d), PayloadLen=%d bytes\n", packetCount, packetNameStr, p.ID, len(p.Data))
		recordForReplay(c.replayRecorder, p)

		switch models.ClientboundPacketID(p.ID) {
		case c.packetMgr.GetClientboundConfigPacketID("ClientboundConfigCookieRequest"):
			var key pk.Identifier
			err := p.Scan(&key)
			if err != nil {
				return ConfigErr{"cookie request", err}
			}
			cookieContent := c.cookies[string(key)]
			err = conn.WritePacket(pk.Marshal(
				c.packetMgr.GetServerboundConfigPacketID("ServerboundConfigCookieResponse"),
				key, pk.OptionEncoder[pk.ByteArray]{
					Has: cookieContent != nil,
					Val: pk.ByteArray(cookieContent),
				},
			))
			if err != nil {
				return ConfigErr{"cookie response", err}
			}

		case c.packetMgr.GetClientboundConfigPacketID("ClientboundConfigCustomPayload"):
			var channel pk.Identifier
			var data pk.PluginMessageData
			err := p.Scan(&channel, &data)
			if err != nil {
				return ConfigErr{"custom payload", err}
			}
			// TODO: Provide configuration custom data handling interface
			//
			// There are two types of Custom packet.
			// One for Login stage, the other for config and play stage.
			// The first one called "Custom Query", and the second one called "Custom Payload".
			// We can know the different by their name, the "query" is one request to one response, paired.
			// But the second one can be sent in any order.
			//
			// And the custome payload packet seems to be same in config stage and play stage.
			// How do we provide API for that?

		case c.packetMgr.GetClientboundConfigPacketID("ClientboundConfigDisconnect"):
			const ErrStage = "disconnect"
			var reason chat.Message
			err := p.Scan(&reason)
			if err != nil {
				return ConfigErr{ErrStage, err}
			}
			return ConfigErr{ErrStage, DisconnectErr(reason)}

		case c.packetMgr.GetClientboundConfigPacketID("ClientboundConfigFinishConfiguration"):
			var err error
			if c.versionHandler != nil {
				err = c.versionHandler.Configuration().SendFinishConfiguration(conn)
			} else {
				err = conn.WritePacket(pk.Marshal(
					c.packetMgr.GetServerboundConfigPacketID("ServerboundConfigFinishConfiguration"),
				))
			}
			if err != nil {
				return ConfigErr{"finish config", err}
			}
			return nil

		case c.packetMgr.GetClientboundConfigPacketID("ClientboundConfigKeepAlive"):
			const ErrStage = "keep alive"
			var keepAliveID int64
			var err error
			if c.versionHandler != nil {
				keepAliveID, err = c.versionHandler.Configuration().ParseKeepAlive(p)
				if err != nil {
					return ConfigErr{ErrStage, err}
				}
				err = c.versionHandler.Configuration().SendKeepAlive(conn, keepAliveID)
			} else {
				var keepAliveIDPk pk.Long
				err = p.Scan(&keepAliveIDPk)
				if err != nil {
					return ConfigErr{ErrStage, err}
				}
				err = conn.WritePacket(pk.Marshal(
					c.packetMgr.GetServerboundConfigPacketID("ServerboundConfigKeepAlive"),
					keepAliveIDPk,
				))
			}
			if err != nil {
				return ConfigErr{ErrStage, err}
			}

		case c.packetMgr.GetClientboundConfigPacketID("ClientboundConfigPing"):
			var pingID int32
			var err error
			if c.versionHandler != nil {
				pingID, err = c.versionHandler.Configuration().ParsePing(p)
				if err != nil {
					return ConfigErr{"ping", err}
				}
				err = c.versionHandler.Configuration().SendPong(conn, pingID)
			} else {
				var pingIDPk pk.Int
				err = p.Scan(&pingIDPk)
				if err != nil {
					return ConfigErr{"ping", err}
				}
				err = conn.WritePacket(pk.Marshal(
					c.packetMgr.GetServerboundConfigPacketID("ServerboundConfigPong"),
					pingIDPk,
				))
			}
			if err != nil {
				return ConfigErr{"pong", err}
			}

		case c.packetMgr.GetClientboundConfigPacketID("ClientboundConfigResetChat"):
			// TODO

		case c.packetMgr.GetClientboundConfigPacketID("ClientboundConfigRegistryData"):
			const ErrStage = "registry"
			var registryID pk.Identifier

			r := bytes.NewReader(p.Data)
			_, err := registryID.ReadFrom(r)
			if err != nil {
				return ConfigErr{ErrStage, err}
			}

			// Parse registry entries - custom implementation
			var arrayLength pk.VarInt
			_, err = arrayLength.ReadFrom(r)
			if err != nil {
				return ConfigErr{ErrStage, fmt.Errorf("failed to read registry array length: %w", err)}
			}

			// Build complete registry with all entry data
			registry := &CustomRegistry{
				ID:      string(registryID),
				Entries: make([]*RegistryEntry, 0, int(arrayLength)),
				ByName:  make(map[string]*RegistryEntry, int(arrayLength)),
			}

			// For callback compatibility (just name→ID mappings)
			nameToID := make(map[string]int32, int(arrayLength))

			// Each entry's index is its ID
			for id := int32(0); id < int32(arrayLength); id++ {
				// Read entry identifier (name)
				var entryID pk.Identifier
				_, err := entryID.ReadFrom(r)
				if err != nil {
					return ConfigErr{ErrStage, fmt.Errorf("failed to read registry entry %d name: %w", id, err)}
				}

				// Read optional NBT data flag
				var hasData pk.Boolean
				_, err = hasData.ReadFrom(r)
				if err != nil {
					return ConfigErr{ErrStage, fmt.Errorf("failed to read registry entry %d has-data flag: %w", id, err)}
				}

				// Create registry entry
				entry := &RegistryEntry{
					ID:   id,
					Name: string(entryID),
					NBT:  nil,
				}

				// Read NBT data if present (using mc-protocol-go models.NBTField)
				if hasData {
					nbtData := &models.NBTField{
						Version: c.packetMgr.Name(),
					}
					_, err = nbtData.ReadFrom(r)
					if err != nil {
						return ConfigErr{ErrStage, fmt.Errorf("failed to read registry entry %d NBT data: %w", id, err)}
					}
					// Store NBT compound for future processing
					entry.NBT = nbtData.Value
				}

				// Add to registry structures
				registry.Entries = append(registry.Entries, entry)
				registry.ByName[entry.Name] = entry
				nameToID[entry.Name] = id
			}

			log.Printf("Parsed %d entries for registry %s\n", len(registry.Entries), string(registryID))
			for _, entry := range registry.Entries {
				log.Printf("\t- ID %d: %s\n", entry.ID, entry.Name)
			}

			// Store complete registry data in client for future access
			c.registryDataMu.Lock()
			c.registryData[string(registryID)] = registry
			c.registryDataMu.Unlock()

			// Invoke callback with name→ID mappings for compatibility
			if c.registryCallback != nil {
				c.registryCallback(string(registryID), nameToID)
			}

		case c.packetMgr.GetClientboundConfigPacketID("ClientboundConfigResourcePackPop"):
			var id pk.Option[pk.UUID, *pk.UUID]
			err := p.Scan(&id)
			if err != nil {
				return ConfigErr{"resource pack pop", err}
			}

		case c.packetMgr.GetClientboundConfigPacketID("ClientboundConfigResourcePackPush"):
			var id pk.UUID
			var Url, Hash pk.String
			var Forced pk.Boolean
			var PromptMessage pk.Option[chat.Message, *chat.Message]
			err := p.Scan(
				&id,
				&Url,
				&Hash,
				&Forced,
				&PromptMessage,
			)
			if err != nil {
				return ConfigErr{"resource pack", err}
			}
			res := ResourcePack{
				ID:     id,
				URL:    string(Url),
				Hash:   string(Hash),
				Forced: bool(Forced),
			}
			if PromptMessage.Has {
				res.PromptMessage = &PromptMessage.Val
			}
			c.ConfigHandler.PushResourcePack(res)

		case c.packetMgr.GetClientboundConfigPacketID("ClientboundConfigStoreCookie"):
			var key pk.Identifier
			var payload pk.ByteArray
			err := p.Scan(&key, &payload)
			if err != nil {
				return ConfigErr{"store cookie", err}
			}
			c.cookies[string(key)] = []byte(payload)

		case c.packetMgr.GetClientboundConfigPacketID("ClientboundConfigTransfer"):
			var host pk.String
			var port pk.VarInt
			err := p.Scan(&host, &port)
			if err != nil {
				return ConfigErr{"transfer", err}
			}
			// TODO: trnasfer to the specific server
			// How does it work? Just connect the new server, and re-start at handshake?

		case c.packetMgr.GetClientboundConfigPacketID("ClientboundConfigUpdateEnabledFeatures"):
			features := []pk.Identifier{}
			err := p.Scan(pk.Array(&features))
			if err != nil {
				return ConfigErr{"update enabled features", err}
			}
			c.ConfigHandler.EnableFeature(features)

		case c.packetMgr.GetClientboundConfigPacketID("ClientboundConfigUpdateTags"):
			const ErrStage = "update tags"
			r := bytes.NewReader(p.Data)

			var length pk.VarInt
			_, err := length.ReadFrom(r)
			if err != nil {
				return ConfigErr{ErrStage, err}
			}

			var registryID pk.Identifier
			for i := 0; i < int(length); i++ {
				_, err = registryID.ReadFrom(r)
				if err != nil {
					return ConfigErr{ErrStage, err}
				}

				// Read and skip tag data for now - not using tnze registry system
				// TODO: Parse and store tags in c.RegistryTags for future use
				_, err = idleTagsDecoder{}.ReadFrom(r)
				if err != nil {
					return ConfigErr{ErrStage, err}
				}
			}

		case c.packetMgr.GetClientboundConfigPacketID("ClientboundConfigSelectKnownPacks"):
			const ErrStage = "select known packs"
			packs := []DataPack{}
			err := p.Scan(pk.Array(&packs))
			if err != nil {
				return ConfigErr{ErrStage, err}
			}
			knwonPacks := c.ConfigHandler.SelectDataPacks(packs)
			err = conn.WritePacket(pk.Marshal(
				c.packetMgr.GetServerboundConfigPacketID("ServerboundConfigSelectKnownPacks"),
				pk.Array(knwonPacks),
			))
			if err != nil {
				return ConfigErr{ErrStage, err}
			}

		case c.packetMgr.GetClientboundConfigPacketID("ClientboundConfigCustomReportDetails"):
			const ErrStage = "custom report details"
			var length pk.VarInt
			var title, description pk.String
			r := bytes.NewReader(p.Data)
			_, err := length.ReadFrom(r)
			if err != nil {
				return ConfigErr{ErrStage, err}
			}
			for i := 0; i < int(length); i++ {
				_, err = title.ReadFrom(r)
				if err != nil {
					return ConfigErr{ErrStage, err}
				}
				_, err = description.ReadFrom(r)
				if err != nil {
					return ConfigErr{ErrStage, err}
				}
				c.customReportDetails[string(title)] = string(description)
			}

		case c.packetMgr.GetClientboundConfigPacketID("ClientboundConfigServerLinks"):
			// TODO
		}
	}
}

type DataPack struct {
	Namespace string
	ID        string
	Version   string
}

func (d DataPack) WriteTo(w io.Writer) (n int64, err error) {
	n, err = pk.String(d.Namespace).WriteTo(w)
	if err != nil {
		return n, err
	}
	n1, err := pk.String(d.ID).WriteTo(w)
	if err != nil {
		return n + n1, err
	}
	n2, err := pk.String(d.Version).WriteTo(w)
	return n + n1 + n2, err
}

func (d *DataPack) ReadFrom(r io.Reader) (n int64, err error) {
	n, err = (*pk.String)(&d.Namespace).ReadFrom(r)
	if err != nil {
		return n, err
	}
	n1, err := (*pk.String)(&d.ID).ReadFrom(r)
	if err != nil {
		return n + n1, err
	}
	n2, err := (*pk.String)(&d.Version).ReadFrom(r)
	return n + n1 + n2, err
}

type DefaultConfigHandler struct {
	resourcesPack []ResourcePack
}

func NewDefaultConfigHandler() *DefaultConfigHandler {
	return &DefaultConfigHandler{
		resourcesPack: make([]ResourcePack, 0),
	}
}

func (d *DefaultConfigHandler) EnableFeature(features []pk.Identifier) {}

func (d *DefaultConfigHandler) PushResourcePack(res ResourcePack) {
	d.resourcesPack = append(d.resourcesPack, res)
}

func (d *DefaultConfigHandler) PopResourcePack(id pk.UUID) {
	for i, v := range d.resourcesPack {
		if id == v.ID {
			d.resourcesPack = append(d.resourcesPack[:i], d.resourcesPack[i+1:]...)
			break
		}
	}
}

func (d *DefaultConfigHandler) PopAllResourcePack() {
	d.resourcesPack = d.resourcesPack[:0]
}

func (d *DefaultConfigHandler) SelectDataPacks(packs []DataPack) []DataPack {
	return []DataPack{}
}

type idleTagsDecoder struct{}

func (idleTagsDecoder) ReadFrom(r io.Reader) (int64, error) {
	var count pk.VarInt
	var tag pk.Identifier
	var length pk.VarInt
	n, err := count.ReadFrom(r)
	if err != nil {
		return n, err
	}
	for i := 0; i < int(count); i++ {
		var n1, n2, n3 int64
		n1, err = tag.ReadFrom(r)
		if err != nil {
			return n + n1, err
		}
		n2, err = length.ReadFrom(r)
		if err != nil {
			return n + n1 + n2, err
		}
		n += n1 + n2

		var id pk.VarInt
		for i := 0; i < int(length); i++ {
			n3, err = id.ReadFrom(r)
			if err != nil {
				return n + n3, err
			}
			n += n3
		}
	}
	return n, nil
}
