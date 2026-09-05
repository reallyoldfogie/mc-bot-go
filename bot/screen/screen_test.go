package screen

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"io"
	"math"
	"testing"

	pk "github.com/Tnze/go-mc/net/packet"
	"github.com/reallyoldfogie/mc-bot-go/bot"
	v1_21_1 "github.com/reallyoldfogie/mc-protocol-go/data/1.21.1"
	basetypesPreHash "github.com/reallyoldfogie/mc-protocol-go/data/1.21.1/basetypes"
	v1_21_5 "github.com/reallyoldfogie/mc-protocol-go/data/1.21.5"
	"github.com/reallyoldfogie/mc-protocol-go/data/1.21.5/basetypes"
	"github.com/reallyoldfogie/mc-protocol-go/models"
	"github.com/stretchr/testify/require"
)

// TestUsesHashedItemSlots and the plainSlotFromSlot/buildPlainContainerClick
// tests below cover the pre-1.21.5 container_click fix: before it,
// ContainerClick unconditionally sent the 1.21.5+ HashedSlot wire format to
// every version, which a pre-1.21.5 server can't decode and disconnects
// the client for — found live via mc-agent's testing/craft_test.go failing
// specifically against 1.21.1.
func TestUsesHashedItemSlots(t *testing.T) {
	oldMgr := &manager{packetMgr: v1_21_1.Packets{}}
	require.False(t, oldMgr.usesHashedItemSlots(), "1.21.1 (protocol 767) should use the pre-1.21.5 plain-Slot format")

	newMgr := &manager{packetMgr: v1_21_5.Packets{}}
	require.True(t, newMgr.usesHashedItemSlots(), "1.21.5 (protocol 770) should use the HashedSlot format")

	nilMgr := &manager{}
	require.True(t, nilMgr.usesHashedItemSlots(), "a nil packetMgr should preserve the prior (hashed) behavior")
}

func TestPlainSlotFromSlot_EmptySlot(t *testing.T) {
	for _, slot := range []*Slot{nil, {Count: 0}} {
		got, err := plainSlotFromSlot(slot)
		require.NoError(t, err)
		require.Equal(t, pk.VarInt(0), got.ItemCount)
		_, isVoid := got.UnnamedType0001.(*models.Void)
		require.True(t, isVoid, "empty slot should encode as ItemCount=0 with a Void payload")
	}
}

func TestPlainSlotFromSlot_SimpleItem(t *testing.T) {
	got, err := plainSlotFromSlot(&Slot{ID: 36, Count: 2})
	require.NoError(t, err)
	require.Equal(t, pk.VarInt(2), got.ItemCount)
	def, ok := got.UnnamedType0001.(*basetypesPreHash.SlotUnnamedType0001Default)
	require.True(t, ok, "non-empty slot should encode as SlotUnnamedType0001Default, got %T", got.UnnamedType0001)
	require.Equal(t, pk.VarInt(36), def.ItemId)
	require.Equal(t, pk.VarInt(0), def.AddedComponentCount)
	require.Equal(t, pk.VarInt(0), def.RemovedComponentCount)
}

func TestPlainSlotFromSlot_RejectsComponentsRatherThanSendingThemWrong(t *testing.T) {
	_, err := plainSlotFromSlot(&Slot{ID: 5, Count: 1, Components: []SlotComponent{{Type: 1, Data: pk.VarInt(1)}}})
	require.Error(t, err, "an item carrying components must error, not be silently sent as if it had none")
}

func TestSlotReadFromComponents(t *testing.T) {
	var damageTypeID int64 = -1
	var removeTypeID int64 = -1
	removeTypeName := "max_damage"
	for id, name := range basetypes.SlotComponentTypeMappings {
		if name == "damage" {
			damageTypeID = id
		}
		if name == removeTypeName {
			removeTypeID = id
		}
	}
	if removeTypeID == -1 {
		removeTypeName = "damage"
		removeTypeID = damageTypeID
	}
	require.NotEqual(t, int64(-1), damageTypeID)
	require.NotEqual(t, int64(-1), removeTypeID)

	buf := &bytes.Buffer{}
	_, _ = pk.VarInt(2).WriteTo(buf) // item count
	_, _ = pk.VarInt(5).WriteTo(buf) // item ID
	_, _ = pk.VarInt(1).WriteTo(buf) // added components
	_, _ = pk.VarInt(1).WriteTo(buf) // removed components
	component := basetypes.SlotComponent{
		Type: basetypes.SlotComponentType{Value: "damage"},
		Data: func() pk.Field {
			val := pk.VarInt(5)
			return &val
		}(),
	}
	_, _ = component.WriteTo(buf)
	removeType := basetypes.SlotComponentType{Value: removeTypeName}
	_, _ = removeType.WriteTo(buf)

	var slot Slot
	_, err := slot.ReadFrom(buf)
	require.NoError(t, err)
	require.Equal(t, pk.VarInt(2), slot.Count)
	require.Equal(t, pk.VarInt(5), slot.ID)
	require.Len(t, slot.Components, 1)
	require.Equal(t, pk.VarInt(damageTypeID), slot.Components[0].Type)
	value, ok := slot.Components[0].Data.(*pk.VarInt)
	require.True(t, ok)
	require.Equal(t, pk.VarInt(5), *value)
	require.Len(t, slot.RemoveComponents, 1)
	require.Equal(t, pk.VarInt(removeTypeID), slot.RemoveComponents[0])
}

func TestSlotWriteToRoundTrip(t *testing.T) {
	var damageTypeID int64 = -1
	var removeTypeID int64 = -1
	removeTypeName := "max_damage"
	for id, name := range basetypes.SlotComponentTypeMappings {
		if name == "damage" {
			damageTypeID = id
		}
		if name == removeTypeName {
			removeTypeID = id
		}
	}
	if removeTypeID == -1 {
		removeTypeName = "damage"
		removeTypeID = damageTypeID
	}
	require.NotEqual(t, int64(-1), damageTypeID)
	require.NotEqual(t, int64(-1), removeTypeID)

	slot := Slot{
		ID:    pk.VarInt(7),
		Count: pk.VarInt(3),
		Components: []SlotComponent{
			{
				Type: pk.VarInt(damageTypeID),
				Data: func() pk.Field {
					val := pk.VarInt(9)
					return &val
				}(),
			},
		},
		RemoveComponents: []pk.VarInt{pk.VarInt(removeTypeID)},
	}

	buf := &bytes.Buffer{}
	_, err := slot.WriteTo(buf)
	require.NoError(t, err)

	reader := bytes.NewReader(buf.Bytes())
	var decoded Slot
	_, err = decoded.ReadFrom(reader)
	require.NoError(t, err)
	require.Equal(t, slot.ID, decoded.ID)
	require.Equal(t, slot.Count, decoded.Count)
	require.Len(t, decoded.Components, 1)
	require.Equal(t, slot.Components[0].Type, decoded.Components[0].Type)
	value, ok := decoded.Components[0].Data.(*pk.VarInt)
	require.True(t, ok)
	require.Equal(t, pk.VarInt(9), *value)
	require.Equal(t, slot.RemoveComponents, decoded.RemoveComponents)
}

func TestComponentHashCRC32C(t *testing.T) {
	hashValue, err := componentHash(pk.String("hello"))
	require.NoError(t, err)
	require.Equal(t, hashForString("hello"), hashValue)
}

func TestHashOpsStringEncoding(t *testing.T) {
	hashValue, err := componentHash(pk.String("abc"))
	require.NoError(t, err)
	require.Equal(t, hashForString("abc"), hashValue)
}

func TestHashOpsFloatEncoding(t *testing.T) {
	hashValue, err := componentHash(pk.Float(1.25))
	require.NoError(t, err)
	require.Equal(t, hashForFloat(1.25), hashValue)
}

func hashForString(value string) int32 {
	utf16Vals := []uint16{}
	for _, r := range value {
		if r > 0xFFFF {
			hi, lo := utf16EncodeRune(r)
			utf16Vals = append(utf16Vals, hi, lo)
		} else {
			utf16Vals = append(utf16Vals, uint16(r))
		}
	}
	buf := make([]byte, 1+4+2*len(utf16Vals))
	buf[0] = hashTagString
	binary.LittleEndian.PutUint32(buf[1:], uint32(len(utf16Vals)))
	offset := 5
	for _, v := range utf16Vals {
		binary.LittleEndian.PutUint16(buf[offset:], v)
		offset += 2
	}
	table := crc32.MakeTable(crc32.Castagnoli)
	return int32(crc32.Checksum(buf, table))
}

func hashForFloat(value float32) int32 {
	buf := make([]byte, 1+4)
	buf[0] = hashTagFloat
	binary.LittleEndian.PutUint32(buf[1:], math.Float32bits(value))
	table := crc32.MakeTable(crc32.Castagnoli)
	return int32(crc32.Checksum(buf, table))
}

func utf16EncodeRune(r rune) (uint16, uint16) {
	r -= 0x10000
	hi := uint16(0xD800 + (r >> 10))
	lo := uint16(0xDC00 + (r & 0x3FF))
	return hi, lo
}

// fakeSlotCodec is a minimal SlotCodec for tests. Embedding it in a fake
// bot.VersionHandler (see fakeVersionHandlerWithCodec) is enough to make
// that handler satisfy screen.SlotCodec too.
type fakeSlotCodec struct {
	decodeCalls int
	decodeSlot  Slot
	decodeErr   error

	sendContainerClickCalls int
	sendContainerClickErr   error
	lastWindowID            int
	lastStateID             int32
	lastSlot                int16
	lastButton              byte
	lastMode                int32
	lastChangedSlots        ChangedSlots
	lastCursor              *Slot
}

func (f *fakeSlotCodec) DecodeSlot(r io.Reader) (Slot, int64, error) {
	f.decodeCalls++
	return f.decodeSlot, 0, f.decodeErr
}

func (f *fakeSlotCodec) SendContainerClick(conn bot.PacketWriter, windowID int, stateID int32, slot int16, button byte, mode int32, changedSlots ChangedSlots, cursor *Slot) error {
	f.sendContainerClickCalls++
	f.lastWindowID = windowID
	f.lastStateID = stateID
	f.lastSlot = slot
	f.lastButton = button
	f.lastMode = mode
	f.lastChangedSlots = changedSlots
	f.lastCursor = cursor
	return f.sendContainerClickErr
}

// fakeVersionHandlerWithCodec satisfies bot.VersionHandler (the minimum
// needed to be set via Client.SetVersionHandler) and, via the embedded
// fakeSlotCodec, screen.SlotCodec too -- covering the optional-interface
// pattern NewManager uses to pick one up.
type fakeVersionHandlerWithCodec struct {
	fakeSlotCodec
}

func (f *fakeVersionHandlerWithCodec) Version() string                        { return "test-version" }
func (f *fakeVersionHandlerWithCodec) Login() bot.LoginHandler                { return nil }
func (f *fakeVersionHandlerWithCodec) Configuration() bot.ConfigurationHandler { return nil }

// fakeVersionHandlerWithoutCodec satisfies bot.VersionHandler but not
// SlotCodec, covering the case where a caller sets a VersionHandler for
// login/configuration only.
type fakeVersionHandlerWithoutCodec struct{}

func (f *fakeVersionHandlerWithoutCodec) Version() string                        { return "test-version" }
func (f *fakeVersionHandlerWithoutCodec) Login() bot.LoginHandler                { return nil }
func (f *fakeVersionHandlerWithoutCodec) Configuration() bot.ConfigurationHandler { return nil }

// TestNewManager_PicksUpSlotCodecFromVersionHandler covers NewManager's
// optional-interface detection: a VersionHandler set via
// Client.SetVersionHandler that also implements SlotCodec should end up on
// manager.slotCodec.
func TestNewManager_PicksUpSlotCodecFromVersionHandler(t *testing.T) {
	packetMgr := v1_21_1.Packets{}
	client := bot.NewClient(packetMgr)
	fakeVH := &fakeVersionHandlerWithCodec{}
	client.SetVersionHandler(fakeVH)

	mgr := NewManager(client, nil, packetMgr)
	m, ok := mgr.(*manager)
	require.True(t, ok)
	require.Equal(t, SlotCodec(fakeVH), m.slotCodec)
}

// TestNewManager_NoSlotCodecWhenNotProvided covers the two cases where
// manager.slotCodec must stay nil: no VersionHandler set at all, and one
// set that doesn't implement SlotCodec.
func TestNewManager_NoSlotCodecWhenNotProvided(t *testing.T) {
	packetMgr := v1_21_1.Packets{}

	t.Run("no VersionHandler set", func(t *testing.T) {
		client := bot.NewClient(packetMgr)
		mgr := NewManager(client, nil, packetMgr)
		m, ok := mgr.(*manager)
		require.True(t, ok)
		require.Nil(t, m.slotCodec)
	})

	t.Run("VersionHandler without SlotCodec", func(t *testing.T) {
		client := bot.NewClient(packetMgr)
		client.SetVersionHandler(&fakeVersionHandlerWithoutCodec{})
		mgr := NewManager(client, nil, packetMgr)
		m, ok := mgr.(*manager)
		require.True(t, ok)
		require.Nil(t, m.slotCodec)
	})
}

// TestContainerClick_UsesSlotCodecWhenSet covers ContainerClick delegating
// whole-packet construction to SlotCodec.SendContainerClick when one is
// set, instead of the built-in buildHashedContainerClick/
// buildPlainContainerClick -- needed because more than just the item slot
// encoding differs by version (e.g. the window ID field is a plain byte
// pre-1.21.5 and a VarInt from 1.21.5 on), so a codec must own the entire
// packet, not just per-item encoding.
func TestContainerClick_UsesSlotCodecWhenSet(t *testing.T) {
	fake := &fakeSlotCodec{}
	client := bot.NewClient(v1_21_1.Packets{})
	m := &manager{c: client, slotCodec: fake}

	carried := &Slot{ID: 7, Count: 1}
	changed := ChangedSlots{2: {ID: 5, Count: 3}}
	err := m.ContainerClick(9, 4, 1, 2, changed, carried)
	require.NoError(t, err)

	require.Equal(t, 1, fake.sendContainerClickCalls)
	require.Equal(t, 9, fake.lastWindowID)
	require.Equal(t, int32(0), fake.lastStateID)
	require.Equal(t, int16(4), fake.lastSlot)
	require.Equal(t, byte(1), fake.lastButton)
	require.Equal(t, int32(2), fake.lastMode)
	require.Equal(t, changed, fake.lastChangedSlots)
	require.Same(t, carried, fake.lastCursor)
	require.Equal(t, int32(1), m.stateID, "stateID must still advance on the codec path")
}

// TestOnSetContentPacket_DecodesEmptySlotsWithoutCodec is a regression test
// for rewriting onSetContentPacket from p.Scan(..., pk.Array(&slotData),
// &carriedItem) to manual bytes.Reader + per-slot decodeSlot calls (needed
// so a SlotCodec, when present, actually gets used) -- confirms the manual
// version still parses containerID/stateID/slot-count/slots/carried
// identically to before for the no-codec (built-in Slot.ReadFrom) path.
func TestOnSetContentPacket_DecodesEmptySlotsWithoutCodec(t *testing.T) {
	chest := &Chest{Slots: make([]Slot, 2)}
	m := &manager{
		screens:   map[int]Container{5: chest},
		inventory: NewInventory(),
	}

	// containerID=5, stateID=5, slotCount=2, two empty slots (itemCount=0,
	// encoded as a single zero varint byte each), empty carried item.
	raw := []byte{0x05, 0x05, 0x02, 0x00, 0x00, 0x00}
	err := m.onSetContentPacket(pk.Packet{Data: raw})
	require.NoError(t, err)
	require.Equal(t, int32(5), m.stateID)
	require.Equal(t, pk.VarInt(0), chest.Slots[0].Count)
	require.Equal(t, pk.VarInt(0), chest.Slots[1].Count)
	require.Equal(t, pk.VarInt(0), m.cursor.Count)
}

// TestOnSetContentPacket_UsesSlotCodecWhenSet covers the codec dispatch
// itself: every Slot-shaped field (each array element plus the carried
// item) must go through SlotCodec.DecodeSlot when one is set.
func TestOnSetContentPacket_UsesSlotCodecWhenSet(t *testing.T) {
	fake := &fakeSlotCodec{decodeSlot: Slot{ID: 99, Count: 3}}
	chest := &Chest{Slots: make([]Slot, 2)}
	m := &manager{
		screens:   map[int]Container{5: chest},
		inventory: NewInventory(),
		slotCodec: fake,
	}

	raw := []byte{0x05, 0x05, 0x02, 0x00, 0x00, 0x00} // trailing zero bytes are unused: fake.DecodeSlot ignores r
	err := m.onSetContentPacket(pk.Packet{Data: raw})
	require.NoError(t, err)
	require.Equal(t, 3, fake.decodeCalls, "2 array slots + 1 carried item")
	require.Equal(t, pk.VarInt(99), chest.Slots[0].ID)
	require.Equal(t, pk.VarInt(99), chest.Slots[1].ID)
	require.Equal(t, pk.VarInt(99), m.cursor.ID)
}

// TestOnSetSlot_UsesSlotCodecWhenSet covers the same dispatch for OnSetSlot
// (ClientboundContainerSetSlot).
func TestOnSetSlot_UsesSlotCodecWhenSet(t *testing.T) {
	fake := &fakeSlotCodec{decodeSlot: Slot{ID: 42, Count: 1}}
	chest := &Chest{Slots: make([]Slot, 2)}
	m := &manager{
		screens:   map[int]Container{5: chest},
		inventory: NewInventory(),
		slotCodec: fake,
	}

	// containerID=5 (varint), stateID=1 (varint), slotID=0 (pk.Short, 2
	// bytes big-endian), then the (ignored by fake) slot bytes.
	raw := []byte{0x05, 0x01, 0x00, 0x00, 0x00}
	err := m.OnSetSlot(pk.Packet{Data: raw})
	require.NoError(t, err)
	require.Equal(t, 1, fake.decodeCalls)
	require.Equal(t, pk.VarInt(42), chest.Slots[0].ID)
}

// TestOnSetPlayerInventory_UsesSlotCodecWhenSet covers the same dispatch
// for onSetPlayerInventory (ClientboundSetPlayerInventory).
func TestOnSetPlayerInventory_UsesSlotCodecWhenSet(t *testing.T) {
	fake := &fakeSlotCodec{decodeSlot: Slot{ID: 7, Count: 1}}
	m := &manager{inventory: NewInventory()}
	m.slotCodec = fake

	// slotID=0 (varint), then the (ignored by fake) slot bytes.
	raw := []byte{0x00, 0x00}
	err := m.onSetPlayerInventory(pk.Packet{Data: raw})
	require.NoError(t, err)
	require.Equal(t, 1, fake.decodeCalls)
	require.Equal(t, pk.VarInt(7), m.inventory.GetSlots()[0].ID)
}

// TestComponentTypeMapping_VersionSplit covers the pre/post-1.21.5 slot
// component mapping fix: type ID 10 means "can_place_on" pre-1.21.5 but
// "enchantments" post-1.21.5 (component IDs were renumbered when the
// HashedSlot rework landed), so using the wrong table silently produces the
// wrong component.
func TestComponentTypeMapping_VersionSplit(t *testing.T) {
	prev := currentSlotComponentUsesHashedTable.Load()
	defer func() { currentSlotComponentUsesHashedTable.Store(prev) }()

	SetCurrentSlotComponentEncoding(false) // pre-1.21.5
	name, ok := componentTypeName(10)
	require.True(t, ok)
	require.Equal(t, "can_place_on", name)
	id, ok := componentTypeID("can_place_on")
	require.True(t, ok)
	require.Equal(t, int32(10), id)

	SetCurrentSlotComponentEncoding(true) // 1.21.5+
	name, ok = componentTypeName(10)
	require.True(t, ok)
	require.Equal(t, "enchantments", name)
	id, ok = componentTypeID("enchantments")
	require.True(t, ok)
	require.Equal(t, int32(10), id)
}

// TestSlotComponentUsesHashedTable_DefaultsToHashed covers the
// atomic.Pointer nil-means-default behavior (mirroring
// mc-protocol-go/models.SetCurrentNBTVersion): callers who never call
// SetCurrentSlotComponentEncoding (e.g. existing tests that construct a
// Slot directly with no manager involved) must keep getting the original
// 1.21.5+ table.
func TestSlotComponentUsesHashedTable_DefaultsToHashed(t *testing.T) {
	prev := currentSlotComponentUsesHashedTable.Load()
	currentSlotComponentUsesHashedTable.Store(nil)
	defer func() { currentSlotComponentUsesHashedTable.Store(prev) }()

	require.True(t, slotComponentUsesHashedTable())
}

// TestBuildContainerTypesFromRegistryEntries covers the live-registry
// container-type fix: a server's "minecraft:menu" registry is the
// authoritative source for container-type protocol IDs (they can and do
// differ across versions), not the hardcoded containerTypeRegistry
// snapshot.
func TestBuildContainerTypesFromRegistryEntries(t *testing.T) {
	entries := map[string]int32{
		"minecraft:furnace":       99,  // deliberately not 14, as it is in the hardcoded snapshot
		"minecraft:some_new_menu": 100, // no known shape yet
	}
	got, unknown := buildContainerTypesFromRegistryEntries(entries)

	require.Len(t, got, 1)
	info, ok := got[99]
	require.True(t, ok)
	require.Equal(t, "minecraft:furnace", info.Identifier)
	require.Equal(t, 3, info.ContainerSlots)
	require.True(t, info.IncludesPlayer)

	require.Equal(t, []string{"minecraft:some_new_menu"}, unknown)
}

// TestManagerGetContainerTypeInfo_PrefersLiveRegistry covers
// manager.getContainerTypeInfo preferring per-connection live registry data
// over the hardcoded package-level fallback, and falling back to it when
// the live data doesn't have an entry.
func TestManagerGetContainerTypeInfo_PrefersLiveRegistry(t *testing.T) {
	m := &manager{
		liveContainerTypes: map[int32]ContainerTypeInfo{
			14: {Identifier: "minecraft:furnace_but_remapped", ContainerSlots: 3, IncludesPlayer: true, PlayerSlotCount: 36},
		},
	}

	info, ok := m.getContainerTypeInfo(14)
	require.True(t, ok)
	require.Equal(t, "minecraft:furnace_but_remapped", info.Identifier,
		"live registry data must take priority over the hardcoded fallback")

	// Type 8 (anvil in the hardcoded snapshot) isn't in this manager's live
	// data, so it must fall back to the package-level default.
	info, ok = m.getContainerTypeInfo(8)
	require.True(t, ok)
	require.Equal(t, "anvil", info.Identifier)
}
