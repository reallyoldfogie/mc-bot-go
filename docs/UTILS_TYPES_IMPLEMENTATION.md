# Utils Types Implementation

## Overview

This document describes the implementation of the Field interface from `github.com/Tnze/go-mc/net/packet` for utility types defined in the ProtoDef utils.md specification. These implementations are now part of the code generator and will be automatically generated for all Minecraft versions.

## Changes Made

### Modified Files

1. **`data/templates.go`** - Updated the `baseTypeDefs` template constant

### Changes to `baseTypeDefs` Template

#### 1. Added `fmt` Import
Added `"fmt"` to the imports section to support error formatting in Bitfield methods.

#### 2. Implemented Buffer Type
```go
// Buffer represents raw bytes without length prefix
type Buffer struct {
    Data []byte
}

func (b Buffer) WriteTo(w io.Writer) (int64, error) {
    nn, err := w.Write(b.Data)
    return int64(nn), err
}

func (b *Buffer) ReadFrom(r io.Reader) (int64, error) {
    data, err := io.ReadAll(r)
    b.Data = data
    return int64(len(b.Data)), err
}
```

**Purpose**: Represents raw bytes without any length prefix. Useful for reading/writing unstructured byte data.

#### 3. Implemented PString Type
```go
// PString represents a length-prefixed string
type PString struct {
    Value string
}

func (ps PString) WriteTo(w io.Writer) (int64, error) {
    s := pk.String(ps.Value)
    return s.WriteTo(w)
}

func (ps *PString) ReadFrom(r io.Reader) (int64, error) {
    var s pk.String
    n, err := s.ReadFrom(r)
    ps.Value = string(s)
    return n, err
}
```

**Purpose**: Represents a string with VarInt length prefix, compatible with Minecraft's string encoding.

#### 4. Completed Bitfield Implementation
Added `WriteTo` and `ReadFrom` methods to the existing Bitfield struct:

```go
func (b Bitfield) WriteTo(w io.Writer) (n int64, err error) {
    if b.Value == nil {
        return 0, fmt.Errorf("bitfield value is nil")
    }
    nn, err := w.Write(b.Value)
    return int64(nn), err
}

func (b *Bitfield) ReadFrom(r io.Reader) (n int64, err error) {
    if b.fields == nil {
        return 0, fmt.Errorf("bitfield fields not initialized")
    }

    totalBits := 0
    for _, field := range b.fields {
        totalBits += field.Size
    }

    if totalBits%8 != 0 {
        return 0, fmt.Errorf("bitfield total size %d is not a multiple of 8", totalBits)
    }

    numBytes := totalBits / 8
    b.Value = make([]byte, numBytes)

    nn, err := io.ReadFull(r, b.Value)
    return int64(nn), err
}
```

**Purpose**: Handles bit-packed fields as defined in the ProtoDef specification. Validates that total field sizes are multiples of 8 bits.

## Type Mapping from ProtoDef utils.md

| ProtoDef Type | Generated Go Type | Description |
|---------------|-------------------|-------------|
| `buffer` | `Buffer` | Raw bytes without length prefix |
| `pstring` | `PString` | Length-prefixed string |
| `bitfield` | `Bitfield` | Bit-packed fields |
| `mapper` | `Mapper` | Dynamic type mapping (stub) |

## Field Interface

All implemented types satisfy the `packet.Field` interface:

```go
type Field interface {
    FieldEncoder
    FieldDecoder
}

type FieldEncoder io.WriterTo
type FieldDecoder io.ReaderFrom
```

This means they can be used with:
- `packet.Marshal()` - to create packets
- `packet.Scan()` - to read packets
- Direct `WriteTo()`/`ReadFrom()` methods

## Usage Examples

### Buffer
```go
buf := basetypes.Buffer{
    Data: []byte{0x01, 0x02, 0x03, 0x04},
}
packet := pk.Marshal(0x42, &buf)
```

### PString
```go
str := basetypes.PString{
    Value: "Hello, Minecraft!",
}
packet := pk.Marshal(0x42, &str)
```

### Bitfield
```go
// Bitfield usage requires field definition from protocol
// Typically generated automatically from protocol definitions
```

## Generation Process

To regenerate code with these implementations:

```bash
cd /home/reallyoldfogie/src/github.com/reallyoldfogie/mc-bot-go/data
go generate
```

This will:
1. Process all versions defined in `Versions` variable
2. Generate `basetypes/types.go` for each version
3. Include Buffer, PString, and Bitfield implementations
4. Generate all packet struct types

## Future Enhancements

### Mapper Implementation
The Mapper type currently has stub implementations. Future work should:
- Parse mapper configuration from protocol definitions
- Store mapping data in the Mapper struct
- Implement proper serialization/deserialization based on mappings

### VarIntPrefixedBuffer
Consider adding a specialized type for buffers with VarInt length prefix:
```go
type VarIntPrefixedBuffer struct {
    Data []byte
}
```

### Bitfield Helper Methods
Add convenience methods for bitfield manipulation:
- `GetField(name string) (int64, error)`
- `SetField(name string, value int64) error`
- Constructor: `NewBitfield(fields []Bitfields) *Bitfield`

## Testing

After generation, test the implementations:

```bash
# Run tests for specific version
cd /home/reallyoldfogie/src/github.com/reallyoldfogie/mc-bot-go/data/1.21.5/basetypes
go test -v

# Run all data package tests
cd /home/reallyoldfogie/src/github.com/reallyoldfogie/mc-bot-go/data
go test ./...
```

## Notes

- These implementations are version-agnostic and will be generated identically for all Minecraft versions
- The Buffer type reads all remaining bytes - for length-prefixed buffers, use `pk.ByteArray` or implement VarIntPrefixedBuffer
- Bitfield validation occurs at read-time, ensuring field sizes sum to a multiple of 8 bits
- PString delegates to `pk.String` for proper VarInt length encoding

## References

- ProtoDef utils.md: `/home/reallyoldfogie/src/github.com/reallyoldfogie/protodef-go/ProtoDef/doc/datatypes/utils.md`
- go-mc packet types: `github.com/Tnze/go-mc/net/packet`
- Generator code: `/home/reallyoldfogie/src/github.com/reallyoldfogie/mc-bot-go/data/gen_packet.go`
- Templates: `/home/reallyoldfogie/src/github.com/reallyoldfogie/mc-bot-go/data/templates.go`
