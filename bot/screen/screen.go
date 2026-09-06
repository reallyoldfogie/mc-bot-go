package screen

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Tnze/go-mc/chat"
	pk "github.com/Tnze/go-mc/net/packet"
	"github.com/reallyoldfogie/mc-bot-go/bot"
	basetypesPreHash "github.com/reallyoldfogie/mc-protocol-go/data/1.21.1/basetypes"
	serverboundPreHash "github.com/reallyoldfogie/mc-protocol-go/data/1.21.1/play/serverbound"
	"github.com/reallyoldfogie/mc-protocol-go/data/1.21.5/basetypes"
	"github.com/reallyoldfogie/mc-protocol-go/data/1.21.5/play/serverbound"
	"github.com/reallyoldfogie/mc-protocol-go/models"
)

type Manager interface {
	ContainerClick(id int, slot int16, button byte, mode int32, slots ChangedSlots, carried *Slot) error
	ForceCloseScreen(windowID int) error
	GetCursorSlot() Slot
	GetPlayerInventory() Inventory
	GetScreenByID(windowID int) Container
	ServerUpdateVersion() int64

	Screens() map[int]Container
	SetScreens(map[int]Container)

	Cursor() Slot
	SetCursor(slot Slot)

	Inventory() Inventory
	SetInventory(Inventory)
}

type manager struct {
	// Synchronization: protects screens, cursor, inventory, stateID, serverUpdateVersion
	mu sync.RWMutex

	c bot.Client

	screens   map[int]Container
	inventory Inventory
	cursor    Slot
	events    ContainerEventsListener
	// The last received State ID from server
	stateID int32
	// Incremented each time the server sends an inventory update.
	serverUpdateVersion int64

	packetMgr models.PacketMgr

	// registrySyncOnce guards the one-time population of liveContainerTypes
	// from the connected server's "minecraft:menu" registry data (see
	// populateContainerTypesFromLiveRegistry in registry_loader.go).
	registrySyncOnce sync.Once
	// liveContainerTypes holds container-type metadata derived from the
	// server this manager is actually connected to, keyed by protocol ID.
	// Consulted before the hardcoded package-level containerTypeRegistry
	// fallback; protected by mu like the other manager fields.
	liveContainerTypes map[int32]ContainerTypeInfo

	// slotCodec, when set, provides exact per-version Slot (de)serialization
	// in place of the built-in pre/post-1.21.5 heuristic (see SlotCodec's
	// doc comment). Set once in NewManager from c.VersionHandler(); never
	// mutated afterward, so no lock is needed to read it.
	slotCodec SlotCodec
}

func NewManager(c bot.Client, e ContainerEventsListener, packetMgr models.PacketMgr) Manager {
	if packetMgr != nil {
		models.SetCurrentNBTVersion(packetMgr.Name())
		SetCurrentSlotComponentEncoding(packetMgr.VersionProtocol() >= hashedSlotProtocolThreshold)
	}
	m := &manager{
		c:         c,
		screens:   make(map[int]Container),
		inventory: NewInventory(),
		events:    e,
		packetMgr: packetMgr,
	}
	if vh := c.VersionHandler(); vh != nil {
		if sc, ok := vh.(SlotCodec); ok {
			m.slotCodec = sc
		}
	}
	m.screens[0] = m.inventory

	// Register packet handlers - only register if packet ID is valid
	var handlers []bot.PacketHandler

	if id := packetMgr.GetClientboundPacketID("ClientboundOpenScreen"); id > 0 {
		handlers = append(handlers, bot.PacketHandler{Priority: 0, ID: id, F: m.onOpenScreen})
	}
	if id := packetMgr.GetClientboundPacketID("ClientboundContainerSetContent"); id > 0 {
		handlers = append(handlers, bot.PacketHandler{Priority: 0, ID: id, F: m.onSetContentPacket})
	}
	if id := packetMgr.GetClientboundPacketID("ClientboundContainerClose"); id > 0 {
		handlers = append(handlers, bot.PacketHandler{Priority: 0, ID: id, F: m.onCloseScreen})
	}
	if id := packetMgr.GetClientboundPacketID("ClientboundContainerSetSlot"); id > 0 {
		handlers = append(handlers, bot.PacketHandler{Priority: 0, ID: id, F: m.OnSetSlot})
	}
	if id := packetMgr.GetClientboundPacketID("ClientboundSetPlayerInventory"); id > 0 {
		handlers = append(handlers, bot.PacketHandler{Priority: 0, ID: id, F: m.onSetPlayerInventory})
	}
	// Add horse screen handler if the packet exists (version-specific)
	// Try different packet names across versions
	horseScreenID := packetMgr.GetClientboundPacketID("ClientboundHorseScreenOpen")
	if horseScreenID <= 0 {
		horseScreenID = packetMgr.GetClientboundPacketID("ClientboundOpenHorseWindow")
	}
	if horseScreenID > 0 {
		handlers = append(handlers, bot.PacketHandler{Priority: 0, ID: horseScreenID, F: m.onOpenHorseScreen})
	}

	if len(handlers) > 0 {
		c.Events().AddListener(handlers...)
	}
	return m
}

func (m *manager) Screens() map[int]Container {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// Return a shallow copy to avoid races on the map itself
	result := make(map[int]Container, len(m.screens))
	for k, v := range m.screens {
		result[k] = v
	}
	return result
}

func (m *manager) SetScreens(in map[int]Container) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.screens = in
}

func (m *manager) Cursor() Slot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cursor
}

func (m *manager) SetCursor(slot Slot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cursor = slot
}

func (m *manager) Inventory() Inventory {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.inventory
}

func (m *manager) SetInventory(in Inventory) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inventory = in
	m.screens[0] = in
}

type ChangedSlots map[int]*Slot

// hashedSlotProtocolThreshold is 1.21.5's protocol version number
// (models.PacketMgr.VersionProtocol()). ServerboundContainerClick's item
// slot encoding changed at exactly this version: pre-1.21.5 clients send a
// full item Slot (a plain item-count/id/component switch, matching
// data/1.21.1/basetypes.Slot); 1.21.5 and later send an
// Option[HashedSlot] — a presence flag plus a component-hash-only summary
// (data/1.21.5/basetypes.HashedSlot) — a real Mojang protocol rework, not
// an artifact of this codebase. Verified directly against
// data/1.21.1..1.21.4's generated WindowClick struct (all four use the
// plain-Slot shape; 1.21.1's protocol number 767 is used below as the
// representative "old" wire format for that whole range) versus
// data/1.21.5 and later (all HashedSlot). Numeric protocol version is used
// rather than the human version string specifically to avoid a semver/
// lexicographic-comparison bug ("1.21.10" < "1.21.2" as plain strings).
//
// Found live: before this fix, ContainerClick unconditionally built and
// sent the 1.21.5+ HashedSlot-shaped packet for every version, including
// pre-1.21.5 servers — which fails to decode it
// ("DecoderException: Failed to decode packet
// 'serverbound/minecraft:container_click'") and disconnects the client.
// Discovered via mc-agent's testing/craft_test.go (a new inventory-click
// live test, the first in that codebase to exercise a full pickup/place
// sequence against every supported version) failing specifically and only
// against 1.21.1.
const hashedSlotProtocolThreshold = 770

func (m *manager) usesHashedItemSlots() bool {
	if m.packetMgr == nil {
		return true // preserve prior (1.21.5+) behavior if no version info is available
	}
	return m.packetMgr.VersionProtocol() >= hashedSlotProtocolThreshold
}

func (m *manager) ContainerClick(id int, slot int16, button byte, mode int32, slots ChangedSlots, carried *Slot) error {
	m.mu.RLock()
	stateID := m.stateID
	m.mu.RUnlock()

	cursorHas := carried != nil && carried.Count > 0
	if debugPath := os.Getenv("MC_AGENT_CLICK_DEBUG_PATH"); debugPath != "" {
		if f, err := os.OpenFile(debugPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
			fmt.Fprintf(f, "[%s] container_click window=%d state=%d slot=%d button=%d mode=%d changed=%d cursorHas=%t\n",
				time.Now().Format(time.RFC3339Nano), id, stateID, slot, button, mode, len(slots), cursorHas)
			for location, slotData := range slots {
				fmt.Fprintf(f, "\tchanged slot=%d id=%d count=%d components=%d removes=%d\n",
					location, slotData.ID, slotData.Count, len(slotData.Components), len(slotData.RemoveComponents))
			}
			if carried != nil {
				fmt.Fprintf(f, "\tcursor id=%d count=%d components=%d removes=%d\n",
					carried.ID, carried.Count, len(carried.Components), len(carried.RemoveComponents))
			}
			_, _ = f.WriteString("\n")
			_ = f.Close()
		}
	}

	if m.slotCodec != nil {
		if err := m.slotCodec.SendContainerClick(m.c.Conn(), id, stateID, slot, button, mode, slots, carried); err != nil {
			return err
		}
	} else {
		var (
			packet pk.Packet
			err    error
		)
		if m.usesHashedItemSlots() {
			packet, _, err = m.buildHashedContainerClick(id, slot, button, mode, slots, carried, stateID)
		} else {
			packet, _, err = m.buildPlainContainerClick(id, slot, button, mode, slots, carried, stateID)
		}
		if err != nil {
			return err
		}
		if err := m.c.Conn().WritePacket(packet); err != nil {
			return err
		}
	}

	// CRITICAL: Increment state ID locally after sending the packet
	// This is necessary for rapid successive clicks that happen before the server responds.
	// The Minecraft server increments its state ID when processing each click, so we must
	// mirror that behavior on the client side to keep state IDs in sync.
	// When the server eventually responds with a set_slot packet, it will overwrite this
	// with the authoritative state ID from the server.
	m.mu.Lock()
	m.stateID++
	m.mu.Unlock()

	return nil
}

// buildHashedContainerClick builds a ServerboundContainerClick packet using
// the 1.21.5+ HashedSlot item-slot encoding. This is the packet shape that
// was, prior to this fix, used unconditionally for every version.
func (m *manager) buildHashedContainerClick(id int, slot int16, button byte, mode int32, slots ChangedSlots, carried *Slot, stateID int32) (pk.Packet, bool, error) {
	packet := serverbound.NewWindowClick()
	packet.SetPacketID(int32(m.packetMgr.GetServerboundPacketID("ServerboundContainerClick")))
	packet.WindowId = basetypes.ContainerID(id)
	packet.StateId = pk.VarInt(stateID)
	packet.Slot = pk.Short(slot)
	packet.MouseButton = pk.Byte(button)
	packet.Mode = pk.VarInt(mode)

	changedSlots := make([]serverbound.WindowClickChangedSlotsArrayType, 0, len(slots))
	for location, slotData := range slots {
		itemOpt, err := hashedSlotOption(slotData)
		if err != nil {
			return pk.Packet{}, false, err
		}
		changedSlots = append(changedSlots, serverbound.WindowClickChangedSlotsArrayType{
			Location: pk.Short(location),
			Item:     itemOpt,
		})
	}
	packet.ChangedSlots = models.Array[pk.VarInt, serverbound.WindowClickChangedSlotsArrayType]{
		Ary: models.Ary[pk.VarInt]{Ary: &changedSlots},
	}

	cursorItem, err := hashedSlotOption(carried)
	if err != nil {
		return pk.Packet{}, false, err
	}
	packet.CursorItem = cursorItem

	return packet.Marshal(), bool(cursorItem.Has), nil
}

// buildPlainContainerClick builds a ServerboundContainerClick packet using
// the pre-1.21.5 plain-Slot item-slot encoding (see
// hashedSlotProtocolThreshold's doc comment). Uses data/1.21.1's generated
// WindowClick struct as the representative "old" wire format for the whole
// 1.21.1-1.21.4 range (all four versions share this exact shape for this
// packet, verified directly).
func (m *manager) buildPlainContainerClick(id int, slot int16, button byte, mode int32, slots ChangedSlots, carried *Slot, stateID int32) (pk.Packet, bool, error) {
	packet := serverboundPreHash.NewWindowClick()
	packet.SetPacketID(int32(m.packetMgr.GetServerboundPacketID("ServerboundContainerClick")))
	packet.WindowId = basetypesPreHash.ContainerID(id)
	packet.StateId = pk.VarInt(stateID)
	packet.Slot = pk.Short(slot)
	packet.MouseButton = pk.Byte(button)
	packet.Mode = pk.VarInt(mode)

	changedSlots := make([]serverboundPreHash.WindowClickChangedSlotsArrayType, 0, len(slots))
	for location, slotData := range slots {
		item, err := plainSlotFromSlot(slotData)
		if err != nil {
			return pk.Packet{}, false, err
		}
		changedSlots = append(changedSlots, serverboundPreHash.WindowClickChangedSlotsArrayType{
			Location: pk.Short(location),
			Item:     item,
		})
	}
	packet.ChangedSlots = models.Array[pk.VarInt, serverboundPreHash.WindowClickChangedSlotsArrayType]{
		Ary: models.Ary[pk.VarInt]{Ary: &changedSlots},
	}

	cursorItem, err := plainSlotFromSlot(carried)
	if err != nil {
		return pk.Packet{}, false, err
	}
	packet.CursorItem = cursorItem

	cursorHas := carried != nil && carried.Count > 0
	return packet.Marshal(), cursorHas, nil
}

func hashedSlotOption(slot *Slot) (models.Option[basetypes.HashedSlot], error) {
	if slot == nil || slot.Count <= 0 {
		return models.Option[basetypes.HashedSlot]{Has: false}, nil
	}
	hashed, err := hashedSlotFromSlot(slot)
	if err != nil {
		return models.Option[basetypes.HashedSlot]{Has: false}, err
	}
	return models.Option[basetypes.HashedSlot]{Has: true, Val: &hashed}, nil
}

// plainSlotFromSlot converts this package's local Slot into the pre-1.21.5
// wire-format Slot (data/1.21.1/basetypes.Slot, representative of the whole
// 1.21.1-1.21.4 range — see hashedSlotProtocolThreshold). An empty/nil slot
// encodes as ItemCount=0 with a Void payload, matching how the older
// protocol represents "no item" (there's no separate presence flag the way
// HashedSlot's Option wrapper has — emptiness is intrinsic to Slot itself).
//
// Known gap, not solved here: items carrying components (enchantments,
// custom names, written-book contents, etc.) are rejected with an error
// rather than sent incorrectly. The pre-1.21.5 wire format encodes each
// component's actual data with a per-component-type switch (varint, bool,
// anonymous NBT, ...), unlike 1.21.5+'s uniform hash-only representation —
// correctly reproducing that would need a real per-component encoder, a
// meaningfully larger undertaking than fixing plain item movement (the vast
// majority of inventory interactions — ordinary stackable items with no
// attached data). Revisit if a live test needs to move a
// component-bearing item on a pre-1.21.5 server.
func plainSlotFromSlot(slot *Slot) (basetypesPreHash.Slot, error) {
	if slot == nil || slot.Count <= 0 {
		return basetypesPreHash.Slot{
			ItemCount:       0,
			UnnamedType0001: &models.Void{},
		}, nil
	}
	if len(slot.Components) > 0 || len(slot.RemoveComponents) > 0 {
		return basetypesPreHash.Slot{}, fmt.Errorf(
			"plainSlotFromSlot: item components on pre-1.21.5 container clicks are not supported yet (item id %d carries %d component(s), %d removal(s))",
			slot.ID, len(slot.Components), len(slot.RemoveComponents))
	}
	return basetypesPreHash.Slot{
		ItemCount: pk.VarInt(slot.Count),
		UnnamedType0001: &basetypesPreHash.SlotUnnamedType0001Default{
			ItemId:                pk.VarInt(slot.ID),
			AddedComponentCount:   0,
			RemovedComponentCount: 0,
		},
	}, nil
}

func hashedSlotFromSlot(slot *Slot) (basetypes.HashedSlot, error) {
	hashed := basetypes.HashedSlot{
		ItemId:    pk.VarInt(slot.ID),
		ItemCount: pk.VarInt(slot.Count),
	}

	components := make([]basetypes.HashedSlotComponentsArrayType, 0, len(slot.Components))
	for _, component := range slot.Components {
		name, ok := componentTypeName(int32(component.Type))
		if !ok {
			return hashed, fmt.Errorf("unknown slot component type ID %d", component.Type)
		}
		hashValue, err := componentHash(component.Data)
		if err != nil {
			return hashed, err
		}
		components = append(components, basetypes.HashedSlotComponentsArrayType{
			Type: basetypes.SlotComponentType{Value: name},
			Hash: pk.Int(hashValue),
		})
	}
	hashed.Components = models.Array[pk.VarInt, basetypes.HashedSlotComponentsArrayType]{
		Ary: models.Ary[pk.VarInt]{Ary: &components},
	}

	removeComponents := make([]basetypes.HashedSlotRemoveComponentsArrayType, 0, len(slot.RemoveComponents))
	for _, componentType := range slot.RemoveComponents {
		name, ok := componentTypeName(int32(componentType))
		if !ok {
			return hashed, fmt.Errorf("unknown slot component type ID %d", componentType)
		}
		removeComponents = append(removeComponents, basetypes.HashedSlotRemoveComponentsArrayType{
			Type: basetypes.SlotComponentType{Value: name},
		})
	}
	hashed.RemoveComponents = models.Array[pk.VarInt, basetypes.HashedSlotRemoveComponentsArrayType]{
		Ary: models.Ary[pk.VarInt]{Ary: &removeComponents},
	}

	return hashed, nil
}

func (c ChangedSlots) WriteTo(w io.Writer) (n int64, err error) {
	n, err = pk.VarInt(len(c)).WriteTo(w)
	if err != nil {
		return
	}
	for i, v := range c {
		n1, err := pk.Short(i).WriteTo(w)
		if err != nil {
			return n + n1, err
		}
		n2, err := v.WriteTo(w)
		if err != nil {
			return n + n1 + n2, err
		}
		n += n1 + n2
	}
	return
}

func (m *manager) onOpenScreen(p pk.Packet) error {
	var (
		ContainerID pk.VarInt
		Type        pk.VarInt
		Title       chat.Message
	)
	if err := p.Scan(&ContainerID, &Type, &Title); err != nil {
		return Error{err}
	}

	m.registrySyncOnce.Do(m.populateContainerTypesFromLiveRegistry)

	fmt.Printf("[onOpenScreen] ← Received ClientboundOpenScreen: windowID=%d type=%d title=%q\n",
		ContainerID, Type, Title.String())

	TypeInt32 := int32(Type)

	// Look up container type info: the live server registry first, falling
	// back to the hardcoded package-level defaults.
	typeInfo, ok := m.getContainerTypeInfo(TypeInt32)
	if !ok {
		// Unknown container type - log warning but don't fail
		// This allows forward compatibility with future Minecraft versions
		fmt.Printf("[onOpenScreen] ✗ Unknown container type %d for windowID=%d\n", TypeInt32, ContainerID)
		return Error{fmt.Errorf("unknown container type %d - cannot create container", TypeInt32)}
	}

	m.mu.Lock()
	// Check if screen already exists
	if _, ok := m.screens[int(ContainerID)]; ok {
		m.mu.Unlock()
		fmt.Printf("[onOpenScreen] ✗ Duplicate container ID %d (screens: %v)\n", ContainerID, getScreenIDs(m.screens))
		return errors.New("container id already exists in screens")
	}

	// Create appropriate container based on type
	if TypeInt32 < 6 {
		// Types 0-5 are chest variants - use the specialized Chest type for backward compatibility
		Rows := TypeInt32 + 1
		chest := Chest{
			Type:  TypeInt32,
			Slots: make([]Slot, typeInfo.TotalSlots()),
			Rows:  int(Rows),
			Title: Title,
		}
		m.screens[int(ContainerID)] = &chest
	} else {
		// All other container types use GenericContainer
		container := GenericContainer{
			Type:            TypeInt32,
			Title:           Title,
			Slots:           make([]Slot, typeInfo.TotalSlots()),
			ContainerSlots:  typeInfo.ContainerSlots,
			PlayerSlotStart: typeInfo.ContainerSlots,
		}
		m.screens[int(ContainerID)] = &container
	}
	m.mu.Unlock()

	if m.events != nil {
		if err := m.events.Open(int(ContainerID), int32(Type), Title); err != nil {
			return Error{err}
		}
	}
	return nil
}

func (m *manager) onOpenHorseScreen(p pk.Packet) error {
	var (
		WindowID  pk.Byte
		SlotCount pk.VarInt
		EntityID  pk.Int
	)
	if err := p.Scan(&WindowID, &SlotCount, &EntityID); err != nil {
		return Error{err}
	}

	// SlotCount is the number of chest columns (3 slots each), not the total
	// container slot count. Every AbstractHorseScreenHandler-family window
	// (horse, donkey, mule, llama, camel, skeleton/zombie horse) additionally
	// reserves a fixed 2-slot base at indices 0-1 regardless of species —
	// saddle+armor for horse-type mounts, saddle+invisible-armor for
	// donkey/mule, decoration+invisible-reserved for llama (which has no
	// saddle at all). Chest slots, if any, start at index 2.
	//
	// Verified against real ClientboundOpenHorseWindow/ClientboundWindowItems
	// packet pairs: camel (SlotCount=0) -> 2 total container slots; donkey
	// and llama (SlotCount=5) -> 17 total container slots = 2 + 5*3. Also
	// matches https://minecraft.wiki/w/Java_Edition_protocol/Inventory's
	// llama formula (2 + 3*strength) and its base-slot description for horse
	// and donkey/mule.
	const horseContainerBaseSlots = 2
	const slotsPerChestColumn = 3

	// Horse containers include player inventory (36 slots)
	// Total slots = container slots + player inventory
	containerSlots := horseContainerBaseSlots + int(SlotCount)*slotsPerChestColumn
	playerSlots := 36
	totalSlots := containerSlots + playerSlots

	horseContainer := HorseContainer{
		EntityID:        int32(EntityID),
		Slots:           make([]Slot, totalSlots),
		ContainerSlots:  containerSlots,
		PlayerSlotStart: containerSlots,
	}

	m.mu.Lock()
	if _, ok := m.screens[int(WindowID)]; ok {
		m.mu.Unlock()
		return errors.New("container id already exists in screens")
	}
	m.screens[int(WindowID)] = &horseContainer
	m.mu.Unlock()

	// Horse screens don't have a type or title, so we use -1 and empty message
	// The events.Open callback can check for type -1 to identify horse containers
	if m.events != nil {
		emptyTitle := chat.Message{}
		if err := m.events.Open(int(WindowID), -1, emptyTitle); err != nil {
			return Error{err}
		}
	}
	return nil
}

func (m *manager) onSetContentPacket(p pk.Packet) error {
	// Read containerID/stateID/slotCount the same way p.Scan would (a
	// single bytes.Reader over p.Data, fields read in order -- see
	// go-mc's Packet.Scan), then take over reading the Slot-typed fields
	// ourselves via m.decodeSlot so a SlotCodec (if any) actually gets
	// used for them.
	r := bytes.NewReader(p.Data)

	var containerID, stateID, slotCount pk.VarInt
	if _, err := containerID.ReadFrom(r); err != nil {
		return Error{err}
	}
	if _, err := stateID.ReadFrom(r); err != nil {
		return Error{err}
	}
	if _, err := slotCount.ReadFrom(r); err != nil {
		return Error{err}
	}
	if slotCount < 0 {
		return Error{errors.New("negative slot count in ClientboundContainerSetContent")}
	}
	slotData := make(Slots, slotCount)
	for i := range slotData {
		s, err := m.decodeSlot(r)
		if err != nil {
			return Error{err}
		}
		slotData[i] = s
	}
	carriedItem, err := m.decodeSlot(r)
	if err != nil {
		return Error{err}
	}

	m.mu.Lock()
	m.applyServerStateID(int32(stateID))
	m.recordServerUpdate()
	m.cursor = carriedItem

	if debugPath := os.Getenv("MC_AGENT_CLICK_DEBUG_PATH"); debugPath != "" {
		if f, err := os.OpenFile(debugPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
			fmt.Fprintf(f, "[%s] set_content container=%d state=%d \n\tslots= (%d) %s\n", time.Now().Format(time.RFC3339Nano), containerID, m.stateID, len(slotData), slotData)
			_ = f.Close()
		}
	}

	// copy the slot data to container
	if containerID == 0 {
		for i, v := range slotData {
			if err := m.inventory.OnSetSlot(i, v); err != nil {
				m.mu.Unlock()
				return Error{err}
			}
		}
		m.mu.Unlock()
		// Call events after unlock
		for i := range slotData {
			if m.events != nil {
				if err := m.events.SetSlot(-2, int16(i)); err != nil {
					return Error{err}
				}
			}
		}
		return nil
	}

	container, ok := m.screens[int(containerID)]
	if !ok {
		m.mu.Unlock()
		return Error{errors.New("setting content of non-exist container")}
	}

	for i, v := range slotData {
		err := container.OnSetSlot(i, v)
		if err != nil {
			m.mu.Unlock()
			return Error{err}
		}
	}
	m.mu.Unlock()

	// Call events after unlock
	for i := range slotData {
		if m.events != nil {
			if err := m.events.SetSlot(int(containerID), int16(i)); err != nil {
				return Error{err}
			}
		}
	}
	return nil
}

// ForceCloseScreen manually closes a screen on the client side.
// This should be called after sending ServerboundContainerClose to the server,
// since the server does NOT send ClientboundContainerClose for player-initiated closes.
func (m *manager) ForceCloseScreen(windowID int) error {
	if windowID == 0 {
		return nil // Cannot close player inventory
	}

	m.mu.Lock()
	c, ok := m.screens[windowID]
	if ok {
		delete(m.screens, windowID)
	}
	m.mu.Unlock()

	if ok {
		if err := c.OnClose(); err != nil {
			return Error{err}
		}
		if m.events != nil {
			if err := m.events.Close(windowID); err != nil {
				return Error{err}
			}
		}
	}
	return nil
}

func (m *manager) onCloseScreen(p pk.Packet) error {
	var ContainerID pk.VarInt
	if err := p.Scan(&ContainerID); err != nil {
		return Error{err}
	}

	m.mu.Lock()
	// DEBUG: Log when close screen is called
	if debugPath := os.Getenv("MC_AGENT_CLICK_DEBUG_PATH"); debugPath != "" {
		if f, err := os.OpenFile(debugPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
			fmt.Fprintf(f, "[%s] onCloseScreen called: ContainerID=%d, Screens before delete=%v\n",
				time.Now().Format(time.RFC3339Nano), ContainerID, getScreenIDs(m.screens))
			_ = f.Close()
		}
	}

	c, ok := m.screens[int(ContainerID)]
	if ok {
		delete(m.screens, int(ContainerID))

		// DEBUG: Log after deletion
		if debugPath := os.Getenv("MC_AGENT_CLICK_DEBUG_PATH"); debugPath != "" {
			if f, err := os.OpenFile(debugPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
				fmt.Fprintf(f, "[%s] onCloseScreen deleted window %d, Screens after delete=%v\n",
					time.Now().Format(time.RFC3339Nano), ContainerID, getScreenIDs(m.screens))
				_ = f.Close()
			}
		}
	} else {
		// DEBUG: Log when screen not found
		if debugPath := os.Getenv("MC_AGENT_CLICK_DEBUG_PATH"); debugPath != "" {
			if f, err := os.OpenFile(debugPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
				fmt.Fprintf(f, "[%s] onCloseScreen: window %d NOT FOUND in Screens map (have: %v)\n",
					time.Now().Format(time.RFC3339Nano), ContainerID, getScreenIDs(m.screens))
				_ = f.Close()
			}
		}
	}
	m.mu.Unlock()

	// Call OnClose and events after releasing lock
	if ok {
		if err := c.OnClose(); err != nil {
			return Error{err}
		}
		if m.events != nil {
			if err := m.events.Close(int(ContainerID)); err != nil {
				return Error{err}
			}
		}
	}
	return nil
}

// Helper function for debug logging
func getScreenIDs(screens map[int]Container) []int {
	ids := make([]int, 0, len(screens))
	for id := range screens {
		ids = append(ids, id)
	}
	return ids
}

func (m *manager) OnSetSlot(p pk.Packet) (err error) {
	r := bytes.NewReader(p.Data)

	var containerID, stateID pk.VarInt
	var slotID pk.Short
	if _, err := containerID.ReadFrom(r); err != nil {
		return Error{err}
	}
	if _, err := stateID.ReadFrom(r); err != nil {
		return Error{err}
	}
	if _, err := slotID.ReadFrom(r); err != nil {
		return Error{err}
	}
	slotData, err := m.decodeSlot(r)
	if err != nil {
		return Error{err}
	}

	m.mu.Lock()
	m.applyServerStateID(int32(stateID))
	m.recordServerUpdate()
	if debugPath := os.Getenv("MC_AGENT_CLICK_DEBUG_PATH"); debugPath != "" {
		if f, err := os.OpenFile(debugPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
			fmt.Fprintf(f, "[%s] set_slot container=%d state=%d slot=%d id=%d count=%d components=%d removes=%d\n",
				time.Now().Format(time.RFC3339Nano), containerID, m.stateID, slotID, slotData.ID, slotData.Count, len(slotData.Components), len(slotData.RemoveComponents))
			_ = f.Close()
		}
	}
	if containerID == -1 && slotID == -1 {
		m.cursor = slotData
	} else if containerID == 0 {
		err = m.inventory.OnSetSlot(int(slotID), slotData)
	} else if containerID == -2 {
		err = m.inventory.OnSetSlot(int(slotID), slotData)
	} else if c, ok := m.screens[int(containerID)]; ok {
		err = c.OnSetSlot(int(slotID), slotData)
	}
	m.mu.Unlock()

	// Call events after releasing lock
	if m.events != nil {
		if err := m.events.SetSlot(int(containerID), int16(slotID)); err != nil {
			return Error{err}
		}
	}
	if err != nil {
		return Error{err}
	}
	return nil
}

func (m *manager) onSetPlayerInventory(p pk.Packet) (err error) {
	r := bytes.NewReader(p.Data)

	var SlotID pk.VarInt
	if _, err := SlotID.ReadFrom(r); err != nil {
		return Error{err}
	}
	SlotData, err := m.decodeSlot(r)
	if err != nil {
		return Error{err}
	}

	m.mu.Lock()
	m.recordServerUpdate()
	var shouldSetSlot bool
	if SlotID >= 0 && int(SlotID) < len(m.inventory.GetSlots()) {
		shouldSetSlot = true
		if err := m.inventory.OnSetSlot(int(SlotID), SlotData); err != nil {
			m.mu.Unlock()
			return Error{err}
		}
	}
	m.mu.Unlock()

	// Call events after releasing lock
	if shouldSetSlot {
		if m.events != nil {
			if err := m.events.SetSlot(-2, int16(SlotID)); err != nil {
				return Error{err}
			}
		}
	}
	return nil
}

func (m *manager) recordServerUpdate() {
	atomic.AddInt64(&m.serverUpdateVersion, 1)
}

// applyServerStateID updates the tracked container stateID from an incoming
// server packet, but only ever advances it, never regresses it.
//
// ContainerClick optimistically bumps m.stateID locally immediately after
// sending each click (see its comment) so back-to-back clicks fired before
// the server acknowledges the first one still declare distinct, correct
// stateIDs. But an incoming set_slot/set_content packet's own stateID only
// reflects what the server had processed as of *that* packet -- it can lag
// behind a local counter that's already been advanced by a later click sent
// (but not yet acknowledged) in the meantime. Overwriting m.stateID
// unconditionally with that lagging value re-exposes a stateID already
// declared by the later click, so the server rejects that click's state
// change and replies with a full resync (ClientboundContainerSetContent) --
// observed live as a multi-click sequence's last click silently losing its
// effect. Must be called with m.mu held.
func (m *manager) applyServerStateID(stateID int32) {
	if stateID > m.stateID {
		m.stateID = stateID
	}
}

func (m *manager) ServerUpdateVersion() int64 {
	return atomic.LoadInt64(&m.serverUpdateVersion)
}

type Slot struct {
	ID               pk.VarInt
	Count            pk.VarInt
	Components       []SlotComponent
	RemoveComponents []pk.VarInt
}

func (s Slot) String() string {
	return fmt.Sprintf("{ID:%d Count:%d Components:%d Removes:%d}", s.ID, s.Count, len(s.Components), len(s.RemoveComponents))
}

type Slots []Slot

func (s Slots) String() string {
	var result strings.Builder
	result.WriteString("[")
	for i, slot := range s {
		if i > 0 {
			result.WriteString(", ")
		}
		result.WriteString(strconv.FormatInt(int64(i), 10) + ":" + slot.String())
	}
	result.WriteString("]")
	return result.String()
}

type SlotComponent struct {
	Type pk.VarInt
	Data any
}

func (s *Slot) WriteTo(w io.Writer) (n int64, err error) {
	itemCount := pk.VarInt(0)
	if s != nil && s.Count > 0 {
		itemCount = s.Count
	}
	bytesWritten, err := itemCount.WriteTo(w)
	n += bytesWritten
	if err != nil {
		return n, err
	}
	if itemCount <= 0 {
		return n, nil
	}

	bytesWritten, err = s.ID.WriteTo(w)
	n += bytesWritten
	if err != nil {
		return n, err
	}

	bytesWritten, err = pk.VarInt(len(s.Components)).WriteTo(w)
	n += bytesWritten
	if err != nil {
		return n, err
	}

	bytesWritten, err = pk.VarInt(len(s.RemoveComponents)).WriteTo(w)
	n += bytesWritten
	if err != nil {
		return n, err
	}

	for _, component := range s.Components {
		name, ok := componentTypeName(int32(component.Type))
		if !ok {
			return n, fmt.Errorf("unknown slot component type ID %d", component.Type)
		}
		field, ok := component.Data.(pk.Field)
		if !ok {
			return n, fmt.Errorf("slot component data does not implement pk.Field: %T", component.Data)
		}
		slotComponent := basetypes.SlotComponent{
			Type: basetypes.SlotComponentType{Value: name},
			Data: field,
		}
		bytesWritten, err = slotComponent.WriteTo(w)
		n += bytesWritten
		if err != nil {
			return n, err
		}
	}

	for _, componentType := range s.RemoveComponents {
		name, ok := componentTypeName(int32(componentType))
		if !ok {
			return n, fmt.Errorf("unknown slot component type ID %d", componentType)
		}
		slotComponentType := basetypes.SlotComponentType{Value: name}
		bytesWritten, err = slotComponentType.WriteTo(w)
		n += bytesWritten
		if err != nil {
			return n, err
		}
	}

	return n, nil
}

func (s *Slot) ReadFrom(r io.Reader) (n int64, err error) {
	var (
		bytesRead        int64
		itemCount        pk.VarInt
		componentsAdd    pk.VarInt
		componentsRemove pk.VarInt
	)

	s.Components = nil
	s.RemoveComponents = nil

	bytesRead, err = itemCount.ReadFrom(r)
	n += bytesRead
	if err != nil {
		return n, err
	}

	if itemCount <= 0 {
		s.ID = 0
		s.Count = 0
		return n, nil
	}

	bytesRead, err = s.ID.ReadFrom(r)
	n += bytesRead
	if err != nil {
		return n, err
	}

	s.Count = itemCount

	bytesRead, err = componentsAdd.ReadFrom(r)
	n += bytesRead
	if err != nil {
		return n, err
	}

	bytesRead, err = componentsRemove.ReadFrom(r)
	n += bytesRead
	if err != nil {
		return n, err
	}

	for i := 0; i < int(componentsAdd); i++ {
		var component basetypes.SlotComponent
		bytesRead, err = component.ReadFrom(r)
		n += bytesRead
		if err != nil {
			return n, err
		}

		typeID, ok := componentTypeID(component.Type.Value)
		if !ok {
			return n, errors.New("unknown slot component type: " + component.Type.Value)
		}

		s.Components = append(s.Components, SlotComponent{
			Type: pk.VarInt(typeID),
			Data: component.Data,
		})
	}

	for i := 0; i < int(componentsRemove); i++ {
		var componentType basetypes.SlotComponentType
		bytesRead, err = componentType.ReadFrom(r)
		n += bytesRead
		if err != nil {
			return n, err
		}
		typeID, ok := componentTypeID(componentType.Value)
		if !ok {
			return n, errors.New("unknown slot component type: " + componentType.Value)
		}
		s.RemoveComponents = append(s.RemoveComponents, pk.VarInt(typeID))
	}

	return n, nil
}

type Container interface {
	OnSetSlot(i int, s Slot) error
	OnClose() error
}

type Error struct {
	Err error
}

func (e Error) Error() string {
	return "bot/screen: " + e.Err.Error()
}

func (e Error) Unwrap() error {
	return e.Err
}

// Getter methods for interface-based access (added for mc-agent)

// GetScreenByID returns the screen/container for a given window ID.
// Returns nil if the window doesn't exist.
func (m *manager) GetScreenByID(windowID int) Container {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if screen, ok := m.screens[windowID]; ok {
		return screen
	}
	return nil
}

// GetPlayerInventory returns a pointer to the player's main inventory.
func (m *manager) GetPlayerInventory() Inventory {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.inventory
}

// GetCursorSlot returns the current cursor slot.
func (m *manager) GetCursorSlot() Slot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cursor
}
