package screen

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"math"
	"testing"

	pk "github.com/Tnze/go-mc/net/packet"
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
