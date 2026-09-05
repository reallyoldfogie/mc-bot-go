package screen

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"reflect"
	"sort"
	"sync/atomic"
	"unicode/utf16"

	pk "github.com/Tnze/go-mc/net/packet"
	"github.com/google/uuid"
	basetypesPreHash "github.com/reallyoldfogie/mc-protocol-go/data/1.21.1/basetypes"
	"github.com/reallyoldfogie/mc-protocol-go/data/1.21.5/basetypes"
	"github.com/reallyoldfogie/mc-protocol-go/models"
)

const (
	hashTagEmpty          = 1
	hashTagMapStart       = 2
	hashTagMapEnd         = 3
	hashTagListStart      = 4
	hashTagListEnd        = 5
	hashTagByte           = 6
	hashTagShort          = 7
	hashTagInt            = 8
	hashTagLong           = 9
	hashTagFloat          = 10
	hashTagDouble         = 11
	hashTagString         = 12
	hashTagBoolean        = 13
	hashTagByteArrayStart = 14
	hashTagByteArrayEnd   = 15
	hashTagIntArrayStart  = 16
	hashTagIntArrayEnd    = 17
	hashTagLongArrayStart = 18
	hashTagLongArrayEnd   = 19
)

// The slot component name<->ID tables are version-dependent, same as the
// Slot/HashedSlot wire format switch in screen.go: pre-1.21.5 servers use
// data/1.21.1's component numbering, 1.21.5+ servers use data/1.21.5's.
// This mirrors that fix's exact representative-version split (see
// hashedSlotProtocolThreshold's doc comment in screen.go) rather than
// tracking every individual version, since Slot.WriteTo/ReadFrom (screen.go)
// implement pk.Field and have no way to receive a packetMgr through their
// io.Reader/io.Writer-only signatures.
var (
	componentTypeNameToIDPostHash = buildComponentTypeNameToID(basetypes.SlotComponentTypeMappings)
	componentTypeIDToNamePostHash = buildComponentTypeIDToName(basetypes.SlotComponentTypeMappings)
	componentTypeNameToIDPreHash  = buildComponentTypeNameToID(basetypesPreHash.SlotComponentTypeMappings)
	componentTypeIDToNamePreHash  = buildComponentTypeIDToName(basetypesPreHash.SlotComponentTypeMappings)
)

// currentSlotComponentUsesHashedTable selects which table componentTypeID/
// componentTypeName use. It mirrors mc-protocol-go/models.SetCurrentNBTVersion's
// atomic.Pointer + nil-means-default pattern, for the same reason: nil means
// "unset", defaulting to the post-hash (1.21.5+) table to preserve this
// package's original, single-version behavior for callers (including
// existing tests) that never call SetCurrentSlotComponentEncoding.
var currentSlotComponentUsesHashedTable atomic.Pointer[bool]

// SetCurrentSlotComponentEncoding selects the slot-component name<->ID table
// used by componentTypeID/componentTypeName (and therefore Slot.WriteTo/
// ReadFrom). NewManager calls this using the same hashedSlotProtocolThreshold
// check as usesHashedItemSlots, so it tracks whatever version this manager's
// packetMgr reports. Process-wide, like the NBT version it's modeled after:
// see the "Known limitation" note for Phase 2 in
// docs/plans/version-awareness-gaps.md.
func SetCurrentSlotComponentEncoding(usesHashed bool) {
	currentSlotComponentUsesHashedTable.Store(&usesHashed)
}

func slotComponentUsesHashedTable() bool {
	if v := currentSlotComponentUsesHashedTable.Load(); v != nil {
		return *v
	}
	return true // default: preserve historical (1.21.5+) behavior when unset
}

func buildComponentTypeNameToID(mappings map[int64]string) map[string]int32 {
	m := make(map[string]int32, len(mappings))
	for id, name := range mappings {
		m[name] = int32(id)
	}
	return m
}

func buildComponentTypeIDToName(mappings map[int64]string) map[int32]string {
	m := make(map[int32]string, len(mappings))
	for id, name := range mappings {
		m[int32(id)] = name
	}
	return m
}

func componentTypeID(name string) (int32, bool) {
	if slotComponentUsesHashedTable() {
		id, ok := componentTypeNameToIDPostHash[name]
		return id, ok
	}
	id, ok := componentTypeNameToIDPreHash[name]
	return id, ok
}

func componentTypeName(id int32) (string, bool) {
	if slotComponentUsesHashedTable() {
		name, ok := componentTypeIDToNamePostHash[id]
		return name, ok
	}
	name, ok := componentTypeIDToNamePreHash[id]
	return name, ok
}

type hashCode struct {
	value uint32
}

func (h hashCode) bytes() [4]byte {
	var out [4]byte
	binary.BigEndian.PutUint32(out[:], h.value)
	return out
}

func (h hashCode) padToLong() uint64 {
	return uint64(h.value)
}

type hashOps struct {
	table *crc32.Table
}

func newHashOps() hashOps {
	return hashOps{table: crc32.MakeTable(crc32.Castagnoli)}
}

func (h hashOps) checksum(parts ...[]byte) hashCode {
	hasher := crc32.New(h.table)
	for _, part := range parts {
		if len(part) > 0 {
			_, _ = hasher.Write(part)
		}
	}
	return hashCode{value: hasher.Sum32()}
}

func (h hashOps) empty() hashCode {
	return h.checksum([]byte{hashTagEmpty})
}

func (h hashOps) createBoolean(value bool) hashCode {
	if value {
		return h.checksum([]byte{hashTagBoolean, 1})
	}
	return h.checksum([]byte{hashTagBoolean, 0})
}

func (h hashOps) createByte(value int8) hashCode {
	return h.checksum([]byte{hashTagByte, byte(value)})
}

func (h hashOps) createShort(value int16) hashCode {
	buf := []byte{hashTagShort, 0, 0}
	binary.LittleEndian.PutUint16(buf[1:], uint16(value))
	return h.checksum(buf)
}

func (h hashOps) createInt(value int32) hashCode {
	buf := []byte{hashTagInt, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(buf[1:], uint32(value))
	return h.checksum(buf)
}

func (h hashOps) createLong(value int64) hashCode {
	buf := make([]byte, 1+8)
	buf[0] = hashTagLong
	binary.LittleEndian.PutUint64(buf[1:], uint64(value))
	return h.checksum(buf)
}

func (h hashOps) createFloat(value float32) hashCode {
	buf := make([]byte, 1+4)
	buf[0] = hashTagFloat
	binary.LittleEndian.PutUint32(buf[1:], math.Float32bits(value))
	return h.checksum(buf)
}

func (h hashOps) createDouble(value float64) hashCode {
	buf := make([]byte, 1+8)
	buf[0] = hashTagDouble
	binary.LittleEndian.PutUint64(buf[1:], math.Float64bits(value))
	return h.checksum(buf)
}

func (h hashOps) createString(value string) hashCode {
	utf16Vals := utf16.Encode([]rune(value))
	buf := make([]byte, 1+4+2*len(utf16Vals))
	buf[0] = hashTagString
	binary.LittleEndian.PutUint32(buf[1:], uint32(len(utf16Vals)))
	offset := 5
	for _, v := range utf16Vals {
		binary.LittleEndian.PutUint16(buf[offset:], v)
		offset += 2
	}
	return h.checksum(buf)
}

func (h hashOps) createByteList(values []byte) hashCode {
	start := []byte{hashTagByteArrayStart}
	end := []byte{hashTagByteArrayEnd}
	return h.checksum(start, values, end)
}

func (h hashOps) createIntList(values []int32) hashCode {
	buf := make([]byte, 1+len(values)*4+1)
	buf[0] = hashTagIntArrayStart
	offset := 1
	for _, v := range values {
		binary.LittleEndian.PutUint32(buf[offset:], uint32(v))
		offset += 4
	}
	buf[offset] = hashTagIntArrayEnd
	return h.checksum(buf)
}

func (h hashOps) createLongList(values []int64) hashCode {
	buf := make([]byte, 1+len(values)*8+1)
	buf[0] = hashTagLongArrayStart
	offset := 1
	for _, v := range values {
		binary.LittleEndian.PutUint64(buf[offset:], uint64(v))
		offset += 8
	}
	buf[offset] = hashTagLongArrayEnd
	return h.checksum(buf)
}

func (h hashOps) createList(values []hashCode) hashCode {
	buf := make([]byte, 0, 2+len(values)*4)
	buf = append(buf, hashTagListStart)
	for _, val := range values {
		bytes := val.bytes()
		buf = append(buf, bytes[:]...)
	}
	buf = append(buf, hashTagListEnd)
	return h.checksum(buf)
}

func (h hashOps) createMap(values []hashEntry) hashCode {
	sort.Slice(values, func(i, j int) bool {
		if values[i].key.padToLong() == values[j].key.padToLong() {
			return values[i].value.padToLong() < values[j].value.padToLong()
		}
		return values[i].key.padToLong() < values[j].key.padToLong()
	})
	buf := make([]byte, 0, 2+len(values)*8)
	buf = append(buf, hashTagMapStart)
	for _, entry := range values {
		keyBytes := entry.key.bytes()
		valueBytes := entry.value.bytes()
		buf = append(buf, keyBytes[:]...)
		buf = append(buf, valueBytes[:]...)
	}
	buf = append(buf, hashTagMapEnd)
	return h.checksum(buf)
}

type hashEntry struct {
	key   hashCode
	value hashCode
}

func componentHash(value any) (int32, error) {
	ops := newHashOps()
	hash, err := hashValue(ops, value)
	if err != nil {
		return 0, err
	}
	return int32(hash.value), nil
}

func hashValue(ops hashOps, value any) (hashCode, error) {
	if value == nil {
		return ops.empty(), nil
	}

	if hash, ok, err := hashOption(ops, value); ok {
		return hash, err
	}

	switch v := value.(type) {
	case hashCode:
		return v, nil
	case pk.Boolean:
		return ops.createBoolean(bool(v)), nil
	case bool:
		return ops.createBoolean(v), nil
	case pk.Byte:
		return ops.createByte(int8(v)), nil
	case pk.UnsignedByte:
		return ops.createByte(int8(v)), nil
	case pk.Short:
		return ops.createShort(int16(v)), nil
	case pk.UnsignedShort:
		return ops.createShort(int16(v)), nil
	case pk.Int:
		return ops.createInt(int32(v)), nil
	case pk.VarInt:
		return ops.createInt(int32(v)), nil
	case pk.Long:
		return ops.createLong(int64(v)), nil
	case pk.VarLong:
		return ops.createLong(int64(v)), nil
	case pk.Float:
		return ops.createFloat(float32(v)), nil
	case pk.Double:
		return ops.createDouble(float64(v)), nil
	case pk.String:
		return ops.createString(string(v)), nil
	case string:
		return ops.createString(v), nil
	case pk.ByteArray:
		return ops.createByteList([]byte(v)), nil
	case []byte:
		return ops.createByteList(v), nil
	case []int8:
		return ops.createByteList(int8SliceToBytes(v)), nil
	case []int32:
		return ops.createIntList(v), nil
	case []int64:
		return ops.createLongList(v), nil
	case uuid.UUID:
		return ops.createByteList(v[:]), nil
	case models.AnonymousNBT:
		return hashNBTValue(ops, v.Value)
	case *models.AnonymousNBT:
		if v == nil {
			return ops.empty(), nil
		}
		return hashNBTValue(ops, v.Value)
	case models.NBTField:
		return hashNBTValue(ops, v.Value)
	case *models.NBTField:
		if v == nil {
			return ops.empty(), nil
		}
		return hashNBTValue(ops, v.Value)
	case models.NBTValue:
		return hashNBTValue(ops, v)
	}

	if entryHashes, ok := mapHashEntries(ops, value); ok {
		return ops.createMap(entryHashes), nil
	}

	if listHashes, ok := sliceHashValues(ops, value); ok {
		return ops.createList(listHashes), nil
	}

	rv := reflect.ValueOf(value)
	if rv.Kind() == reflect.Pointer && !rv.IsNil() {
		return hashValue(ops, rv.Elem().Interface())
	}

	return ops.empty(), fmt.Errorf("unsupported hash component value: %T", value)
}

func hashOption(ops hashOps, value any) (hashCode, bool, error) {
	rv := reflect.ValueOf(value)
	if !rv.IsValid() {
		return ops.empty(), false, nil
	}
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return ops.empty(), false, nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return ops.empty(), false, nil
	}

	hasField := rv.FieldByName("Has")
	valField := rv.FieldByName("Val")
	if !hasField.IsValid() || !valField.IsValid() {
		return ops.empty(), false, nil
	}

	hasValue := false
	switch hasField.Kind() {
	case reflect.Bool:
		hasValue = hasField.Bool()
	case reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uint:
		hasValue = hasField.Uint() != 0
	case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Int:
		hasValue = hasField.Int() != 0
	default:
		return ops.empty(), false, nil
	}

	if !hasValue {
		return ops.empty(), true, nil
	}

	if valField.Kind() == reflect.Pointer {
		if valField.IsNil() {
			return ops.empty(), true, fmt.Errorf("option value is nil")
		}
		hash, err := hashValue(ops, valField.Elem().Interface())
		return hash, true, err
	}
	hash, err := hashValue(ops, valField.Interface())
	return hash, true, err
}

func mapHashEntries(ops hashOps, value any) ([]hashEntry, bool) {
	rv := reflect.ValueOf(value)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, false
		}
		rv = rv.Elem()
	}
	if rv.Kind() == reflect.Map {
		if rv.Type().Key().Kind() != reflect.String {
			return nil, false
		}
		entries := make([]hashEntry, 0, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			key := iter.Key().String()
			valueHash, err := hashValue(ops, iter.Value().Interface())
			if err != nil {
				return nil, false
			}
			entries = append(entries, hashEntry{
				key:   ops.createString(key),
				value: valueHash,
			})
		}
		return entries, true
	}

	if rv.Kind() != reflect.Struct {
		return nil, false
	}

	entries := make([]hashEntry, 0, rv.NumField())
	rt := rv.Type()
	for i := 0; i < rv.NumField(); i++ {
		field := rt.Field(i)
		if field.PkgPath != "" {
			continue
		}
		key := toLowerSnake(field.Name)
		fieldValue := rv.Field(i)
		if present, unwrapped, ok, err := unwrapOptionValue(fieldValue); ok {
			if err != nil {
				return nil, false
			}
			if !present {
				continue
			}
			fieldValue = unwrapped
		}
		valueHash, err := hashValue(ops, fieldValue.Interface())
		if err != nil {
			return nil, false
		}
		entries = append(entries, hashEntry{
			key:   ops.createString(key),
			value: valueHash,
		})
	}
	if len(entries) == 0 {
		return nil, false
	}
	return entries, true
}

func unwrapOptionValue(value reflect.Value) (present bool, unwrapped reflect.Value, ok bool, err error) {
	rv := value
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return false, reflect.Value{}, false, nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return false, reflect.Value{}, false, nil
	}

	hasField := rv.FieldByName("Has")
	valField := rv.FieldByName("Val")
	if !hasField.IsValid() || !valField.IsValid() {
		return false, reflect.Value{}, false, nil
	}

	hasValue := false
	switch hasField.Kind() {
	case reflect.Bool:
		hasValue = hasField.Bool()
	case reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uint:
		hasValue = hasField.Uint() != 0
	case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Int:
		hasValue = hasField.Int() != 0
	default:
		return false, reflect.Value{}, false, nil
	}

	if !hasValue {
		return false, reflect.Value{}, true, nil
	}

	if valField.Kind() == reflect.Pointer {
		if valField.IsNil() {
			return false, reflect.Value{}, true, fmt.Errorf("option value is nil")
		}
		return true, valField.Elem(), true, nil
	}
	return true, valField, true, nil
}

func sliceHashValues(ops hashOps, value any) ([]hashCode, bool) {
	rv := reflect.ValueOf(value)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, false
		}
		rv = rv.Elem()
	}

	if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
		values := make([]hashCode, 0, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			valueHash, err := hashValue(ops, rv.Index(i).Interface())
			if err != nil {
				return nil, false
			}
			values = append(values, valueHash)
		}
		return values, true
	}

	aryField := rv.FieldByName("Ary")
	if aryField.IsValid() {
		inner := aryField.FieldByName("Ary")
		if inner.IsValid() {
			sliceVal := inner
			if sliceVal.Kind() == reflect.Pointer {
				if sliceVal.IsNil() {
					return []hashCode{}, true
				}
				sliceVal = sliceVal.Elem()
			}
			if sliceVal.Kind() == reflect.Slice {
				values := make([]hashCode, 0, sliceVal.Len())
				for i := 0; i < sliceVal.Len(); i++ {
					valueHash, err := hashValue(ops, sliceVal.Index(i).Interface())
					if err != nil {
						return nil, false
					}
					values = append(values, valueHash)
				}
				return values, true
			}
		}
	}

	return nil, false
}

func hashNBTValue(ops hashOps, value any) (hashCode, error) {
	if value == nil {
		return ops.empty(), nil
	}
	switch v := value.(type) {
	case *models.NBTCompound:
		if v == nil {
			return ops.empty(), nil
		}
		return hashNBTCompound(ops, *v)
	case models.NBTCompound:
		return hashNBTCompound(ops, v)
	case *models.NBTList:
		if v == nil {
			return ops.empty(), nil
		}
		return hashNBTList(ops, *v)
	case models.NBTList:
		return hashNBTList(ops, v)
	case *models.NBTByte:
		if v == nil {
			return ops.empty(), nil
		}
		return ops.createByte(v.Value), nil
	case models.NBTByte:
		return ops.createByte(v.Value), nil
	case *models.NBTShort:
		if v == nil {
			return ops.empty(), nil
		}
		return ops.createShort(v.Value), nil
	case models.NBTShort:
		return ops.createShort(v.Value), nil
	case *models.NBTInt:
		if v == nil {
			return ops.empty(), nil
		}
		return ops.createInt(v.Value), nil
	case models.NBTInt:
		return ops.createInt(v.Value), nil
	case *models.NBTLong:
		if v == nil {
			return ops.empty(), nil
		}
		return ops.createLong(v.Value), nil
	case models.NBTLong:
		return ops.createLong(v.Value), nil
	case *models.NBTFloat:
		if v == nil {
			return ops.empty(), nil
		}
		return ops.createFloat(v.Value), nil
	case models.NBTFloat:
		return ops.createFloat(v.Value), nil
	case *models.NBTDouble:
		if v == nil {
			return ops.empty(), nil
		}
		return ops.createDouble(v.Value), nil
	case models.NBTDouble:
		return ops.createDouble(v.Value), nil
	case *models.NBTString:
		if v == nil {
			return ops.empty(), nil
		}
		return ops.createString(v.Value), nil
	case models.NBTString:
		return ops.createString(v.Value), nil
	case *models.NBTByteArray:
		if v == nil {
			return ops.empty(), nil
		}
		return ops.createByteList(int8SliceToBytes(v.Value)), nil
	case models.NBTByteArray:
		return ops.createByteList(int8SliceToBytes(v.Value)), nil
	case *models.NBTIntArray:
		if v == nil {
			return ops.empty(), nil
		}
		return ops.createIntList(v.Value), nil
	case models.NBTIntArray:
		return ops.createIntList(v.Value), nil
	case *models.NBTLongArray:
		if v == nil {
			return ops.empty(), nil
		}
		return ops.createLongList(v.Value), nil
	case models.NBTLongArray:
		return ops.createLongList(v.Value), nil
	case models.NBTValue:
		return hashNBTValue(ops, any(v))
	}
	return ops.empty(), fmt.Errorf("unsupported NBT value type: %T", value)
}

func hashNBTCompound(ops hashOps, compound models.NBTCompound) (hashCode, error) {
	entries := make([]hashEntry, 0, len(compound.Tags))
	for _, tag := range compound.Tags {
		valueHash, err := hashNBTValue(ops, tag.Value)
		if err != nil {
			return ops.empty(), err
		}
		entries = append(entries, hashEntry{
			key:   ops.createString(tag.Name),
			value: valueHash,
		})
	}
	return ops.createMap(entries), nil
}

func hashNBTList(ops hashOps, list models.NBTList) (hashCode, error) {
	values := make([]hashCode, 0, len(list.Values))
	for _, v := range list.Values {
		valueHash, err := hashNBTValue(ops, v)
		if err != nil {
			return ops.empty(), err
		}
		values = append(values, valueHash)
	}
	return ops.createList(values), nil
}

func int8SliceToBytes(values []int8) []byte {
	out := make([]byte, len(values))
	for i, v := range values {
		out[i] = byte(v)
	}
	return out
}

func toLowerSnake(name string) string {
	var out []rune
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			prev := rune(name[i-1])
			if prev >= 'a' && prev <= 'z' {
				out = append(out, '_')
			}
		}
		if r >= 'A' && r <= 'Z' {
			r = r - 'A' + 'a'
		}
		out = append(out, r)
	}
	return string(out)
}
