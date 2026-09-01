package screen

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"math"
	"testing"

	pk "github.com/Tnze/go-mc/net/packet"
	"github.com/reallyoldfogie/mc-protocol-go/data/1.21.5/basetypes"
	"github.com/stretchr/testify/require"
)

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
