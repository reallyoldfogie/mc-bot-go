package screen

import (
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
}

func NewManager(c bot.Client, e ContainerEventsListener, packetMgr models.PacketMgr) Manager {
	if packetMgr != nil {
		models.SetCurrentNBTVersion(packetMgr.Name())
	}
	m := &manager{
		c:         c,
		screens:   make(map[int]Container),
		inventory: NewInventory(),
		events:    e,
		packetMgr: packetMgr,
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

func (m *manager) ContainerClick(id int, slot int16, button byte, mode int32, slots ChangedSlots, carried *Slot) error {
	packet := serverbound.NewWindowClick()
	packet.SetPacketID(int32(m.packetMgr.GetServerboundPacketID("ServerboundContainerClick")))
	packet.WindowId = basetypes.ContainerID(id)

	m.mu.RLock()
	packet.StateId = pk.VarInt(m.stateID)
	m.mu.RUnlock()

	packet.Slot = pk.Short(slot)
	packet.MouseButton = pk.Byte(button)
	packet.Mode = pk.VarInt(mode)

	changedSlots := make([]serverbound.WindowClickChangedSlotsArrayType, 0, len(slots))
	for location, slotData := range slots {
		itemOpt, err := hashedSlotOption(slotData)
		if err != nil {
			return err
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
		return err
	}
	packet.CursorItem = cursorItem

	m.mu.RLock()
	stateID := m.stateID
	m.mu.RUnlock()

	if debugPath := os.Getenv("MC_AGENT_CLICK_DEBUG_PATH"); debugPath != "" {
		if f, err := os.OpenFile(debugPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
			fmt.Fprintf(f, "[%s] container_click window=%d state=%d slot=%d button=%d mode=%d changed=%d cursorHas=%t\n",
				time.Now().Format(time.RFC3339Nano), id, stateID, slot, button, mode, len(slots), cursorItem.Has)
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

	if err := m.c.Conn().WritePacket(packet.Marshal()); err != nil {
		return err
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

	fmt.Printf("[onOpenScreen] ← Received ClientboundOpenScreen: windowID=%d type=%d title=%q\n",
		ContainerID, Type, Title.String())

	TypeInt32 := int32(Type)

	// Look up container type info from registry
	typeInfo, ok := GetContainerTypeInfo(TypeInt32)
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
	var (
		containerID pk.VarInt
		stateID     pk.VarInt
		slotData    Slots
		carriedItem Slot
	)
	if err := p.Scan(
		&containerID,
		&stateID,
		pk.Array(&slotData),
		&carriedItem,
	); err != nil {
		return Error{err}
	}

	m.mu.Lock()
	m.stateID = int32(stateID)
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
	var (
		containerID pk.VarInt
		stateID     pk.VarInt
		slotID      pk.Short
		slotData    Slot
	)
	if err := p.Scan(&containerID, &stateID, &slotID, &slotData); err != nil {
		return Error{err}
	}

	m.mu.Lock()
	m.stateID = int32(stateID)
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
	var (
		SlotID   pk.VarInt
		SlotData Slot
	)
	if err := p.Scan(&SlotID, &SlotData); err != nil {
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
