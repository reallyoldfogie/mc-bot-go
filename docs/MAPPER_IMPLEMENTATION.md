# Mapper Implementation

## Overview

This document describes the full implementation of Mapper types with code generation. Mapper types map integer keys to string values based on protocol definitions, enabling type-safe handling of enumeration-like data in Minecraft packets.

## Implementation Approach

Instead of using a generic `Mapper` type, we generate **specialized struct types** for each mapper with embedded configuration extracted from the protocol definition.

## Changes Made

### Modified Files

1. **`data/gen_packet.go`** - Updated type processing and added mapper template
2. **`data/templates.go`** - Removed generic Mapper stub

### Changes to `gen_packet.go`

#### 1. Updated `processType` Function (Lines 732-740, 815-823)

**Previous Behavior**: Mapper types were aliased to generic `basetypes.Mapper`

**New Behavior**: Creates specialized mapper types with configuration preserved

```go
case "mapper":
    // Mapper types within fields - generate specialized struct
    childType := createChildType(parentName, field.Name, field.Type)
    field.Type.TypeName = childType.Name
    // Keep Extras so we can generate proper ReadFrom/WriteTo
    types = append(types, processType(&childType, baseTypes, field.Anon, isGeneratingBaseTypes)...)
    continue
```

#### 2. Added `toMapper` Helper Function

```go
func toMapper(t *datatypes.Type) *datatypes.Mapper {
    return t.Extras.(*datatypes.Mapper)
}
```

#### 3. Added `toMapper` and `toNative` to Template Functions

Updated the template function map to include:
- `toMapper`: Extract mapper configuration
- `toNative`: Convert type names to native Go types (needed in template)

#### 4. Added Mapper Template (`mapperTmpl`)

New template definition generates:
- Struct with `Value string` field
- Mappings constant (map[int64]string)
- `ReadFrom` method that reads key and maps to string
- `WriteTo` method that reverse-maps string to key

```go
{{define "mapperTmpl"}}{{$mapper := toMapper .}}type {{.Name}} struct {
	Value string
}

var {{.Name}}Mappings = map[int64]string{ {{range $key, $value := $mapper.Mappings}}
	{{$key}}: "{{$value}}",{{end}}
}

func (m *{{.Name}}) ReadFrom(r io.Reader) (int64, error) {
	var key {{if $mapper.Type}}{{toNative $mapper.Type.TypeName $mapper.Type}}{{else}}pk.VarInt{{end}}
	n, err := key.ReadFrom(r)
	if err != nil {
		return n, err
	}
	
	value, ok := {{.Name}}Mappings[int64(key)]
	if !ok {
		return n, fmt.Errorf("unknown {{.Name}} key: %d", key)
	}
	m.Value = value
	return n, nil
}

func (m {{.Name}}) WriteTo(w io.Writer) (int64, error) {
	for k, v := range {{.Name}}Mappings {
		if v == m.Value {
			key := {{if $mapper.Type}}{{toNative $mapper.Type.TypeName $mapper.Type}}{{else}}pk.VarInt{{end}}(k)
			return key.WriteTo(w)
		}
	}
	return 0, fmt.Errorf("unknown {{.Name}} value: %s", m.Value)
}
{{end}}
```

### Changes to `templates.go`

Removed the generic `Mapper` stub and replaced with a comment explaining that mappers are generated as specialized types.

## Generated Code Example

### Input (Protocol Definition)

```json
["mapper", {
  "type": "i8",
  "mappings": {
    "0": "master",
    "1": "music",
    "2": "record",
    "3": "weather",
    "4": "block"
  }
}]
```

### Output (Generated Go Code)

```go
type SoundSource struct {
	Value string
}

var SoundSourceMappings = map[int64]string{
	0: "master",
	1: "music",
	2: "record",
	3: "weather",
	4: "block",
}

func (m *SoundSource) ReadFrom(r io.Reader) (int64, error) {
	var key pk.Byte
	n, err := key.ReadFrom(r)
	if err != nil {
		return n, err
	}
	
	value, ok := SoundSourceMappings[int64(key)]
	if !ok {
		return n, fmt.Errorf("unknown SoundSource key: %d", key)
	}
	m.Value = value
	return n, nil
}

func (m SoundSource) WriteTo(w io.Writer) (int64, error) {
	for k, v := range SoundSourceMappings {
		if v == m.Value {
			key := pk.Byte(k)
			return key.WriteTo(w)
		}
	}
	return 0, fmt.Errorf("unknown SoundSource value: %s", m.Value)
}
```

## How It Works

### 1. Protocol Parsing

The `protodef-go` library parses the protocol definition and creates a `datatypes.Mapper` struct with:
- `Type`: The underlying type for the key (e.g., "i8", "varint")
- `Mappings`: A map of string keys to values (e.g., {"0": "master", "1": "music"})

### 2. Code Generation

When `gen_packet.go` processes a mapper type:

1. **Detection**: `isMapper(t)` returns true
2. **Preservation**: Instead of converting to generic type, keeps `t.Extras` (the `*datatypes.Mapper`)
3. **Template Processing**: The `mapperTmpl` template:
   - Extracts mapper configuration via `toMapper`
   - Generates struct with `Value string` field
   - Creates mappings constant from `$mapper.Mappings`
   - Generates `ReadFrom` that reads key type and maps to string
   - Generates `WriteTo` that reverse-maps string to key

### 3. Type Conversion

The template uses `toNative` to convert the mapper's type (e.g., "i8") to the appropriate Go packet type (e.g., "pk.Byte").

## Usage Examples

### Reading a Packet with Mapper

```go
type SomePacket struct {
    SoundSource SoundSource
    // other fields...
}

var pkt SomePacket
err := packet.Scan(&pkt.SoundSource)
// pkt.SoundSource.Value now contains "master", "music", etc.
```

### Writing a Packet with Mapper

```go
pkt := SomePacket{
    SoundSource: SoundSource{Value: "music"},
}

packet := pk.Marshal(0x42, pkt.SoundSource)
// Writes byte value 1 (mapped from "music")
```

### Accessing Mappings

```go
// Check valid values
for key, value := range SoundSourceMappings {
    fmt.Printf("%d -> %s\n", key, value)
}

// Validate a value
if _, exists := SoundSourceMappings[someKey]; exists {
    // Valid key
}
```

## Error Handling

### Read Errors

- Returns error if underlying key type fails to read
- Returns error if key is not in mappings (unknown enum value)

### Write Errors

- Returns error if Value doesn't match any mapping (invalid enum value)
- Format: `"unknown TypeName value: actual_value"`

## Benefits

1. **Type Safety**: Each mapper is a distinct type, preventing accidental mixing
2. **Embedded Configuration**: Mappings are compile-time constants
3. **No Runtime Lookup**: No need for global registry or configuration files
4. **Clear API**: Users work with meaningful string values, not magic numbers
5. **Version-Specific**: Each Minecraft version gets correctly mapped types
6. **Self-Documenting**: Generated code shows all valid values

## Testing

After generation, test the mapper implementations:

```bash
cd /home/reallyoldfogie/src/github.com/reallyoldfogie/mc-bot-go/data
go generate
cd 1.21.5/basetypes
go test -v
```

## Future Enhancements

### Reverse Mappings

For better write performance, consider generating reverse mappings:

```go
var SoundSourceReverseMappings = map[string]int64{
	"master": 0,
	"music": 1,
	"record": 2,
	"weather": 3,
	"block": 4,
}

func (m SoundSource) WriteTo(w io.Writer) (int64, error) {
	key, ok := SoundSourceReverseMappings[m.Value]
	if !ok {
		return 0, fmt.Errorf("unknown SoundSource value: %s", m.Value)
	}
	keyValue := pk.Byte(key)
	return keyValue.WriteTo(w)
}
```

### String Methods

Add `String()` method for better debugging:

```go
func (m SoundSource) String() string {
	return m.Value
}
```

### Validation Methods

Add validation helpers:

```go
func (m SoundSource) IsValid() bool {
	for _, v := range SoundSourceMappings {
		if v == m.Value {
			return true
		}
	}
	return false
}
```

## References

- ProtoDef utils.md: `/home/reallyoldfogie/src/github.com/reallyoldfogie/protodef-go/ProtoDef/doc/datatypes/utils.md`
- Mapper type definition: `/home/reallyoldfogie/src/github.com/reallyoldfogie/protodef-go/datatypes/mapper.go`
- Generator code: `/home/reallyoldfogie/src/github.com/reallyoldfogie/mc-bot-go/data/gen_packet.go`
- Templates: `/home/reallyoldfogie/src/github.com/reallyoldfogie/mc-bot-go/data/templates.go`
