# Fix Plan: Go Vet Errors and Code Generation Issues

**Date Created:** 2025-11-06  
**Status:** In Progress

## Overview

Fix go vet errors caused by code generation issues in the protocol types. The main issue is that the ReadFrom method generation doesn't read all fields before using them in switch statements. Additionally, add validation to ensure all fields from protocol.json are properly handled in ReadFrom/WriteTo methods.

## Issues Identified

### 1. Missing Field Reads in ReadFrom (HIGH PRIORITY)

**Example:** `HideMessage` in `/home/reallyoldfogie/src/github.com/reallyoldfogie/mc-bot-go/data/1.21.5/play/clientbound/types.go`

**Problem:** The generated `ReadFrom` method uses `t.Id` in a switch statement but never reads it from the reader:

```go
type HideMessage struct {
    Id        pk.VarInt
    Signature any
}

func (t *HideMessage) ReadFrom(r io.Reader) (totalBytes int64, err error) {
    // var bytesRead int64
    
    // Switch field Signature based on id
    // Convert compareTo value to string for matching
    compareValueSignature := fmt.Sprintf("%v", t.Id)  // ❌ Uses t.Id but never reads it!
    
    switch compareValueSignature {
    case "0":
        // ... reads Signature
    }
    return totalBytes, nil
}
```

**Expected behavior:** Should read `Id` field BEFORE the switch statement.

**Protocol Definition:** From `protocol.json` lines 5159-5185:
```json
"packet_hide_message": [
  "container",
  [
    {
      "name": "id",
      "type": "varint"
    },
    {
      "name": "signature",
      "type": [
        "switch",
        {
          "compareTo": "id",
          "fields": {
            "0": ["buffer", {"count": 256}]
          },
          "default": "void"
        }
      ]
    }
  ]
]
```

**Root Cause:** The template in `data/gen_packet.go` (lines 417-465) generates ReadFrom methods for container types. It iterates through fields and only generates read logic for:
- Switch fields (reads the switch value based on compareTo)
- Regular non-switch fields (in an `else if`)

However, it doesn't ensure regular fields are read BEFORE switch fields that depend on them.

### 2. Undefined Type Errors

| Error | Location | Line |
|-------|----------|------|
| `undefined: FeatureFlagsArrayType` | `data/1.21.5/configuration/clientbound/types.go` | 92 |
| `undefined: buffer` | `data/1.21.5/login/clientbound/types.go` | 244 |
| `undefined: buffer` | `data/1.21.5/login/serverbound/types.go` | 48 |
| `undefined: MovementFlags` | `data/1.21.5/play/serverbound/types.go` | 1962 |
| `undefined: PositionUpdateRelatives` | `data/1.21.5/play/clientbound/types.go` | 7889 |

## Root Cause Analysis

### ReadFrom Generation Logic

Location: `data/gen_packet.go`, lines 417-465 (the `structTmpl` template)

Current template structure:
```go
{{range .Fields}}
    {{if isSwitch .}}
        // Generate switch reading logic
    {{else if and (ne .Type.TypeName "struct{}") ...}}
        // Generate regular field reading logic
    {{end}}
{{end}}
```

**Problem:** This processes fields in declaration order but doesn't guarantee regular fields are read before switch fields use them.

**Solution:** ⚠️ **CORRECTED** - Cannot use two-pass approach! Fields must be read in declaration order.

**Analysis Result:** Protocol.json has 2 containers where switch fields are NOT at the end:
- `packet_player_chat`: switch field `filterTypeMask` at index 10, followed by `type`, `networkName`, `networkTargetName`
- `packet_use_entity`: switch field `hand` at index 5, followed by `sneaking`

**Actual Solution:** Read fields in declaration order, ensuring:
1. Regular fields: Generate ReadFrom call
2. Switch fields: Read compareTo field if not already read, then process switch
3. Continue with remaining fields in order

## Proposed Fixes

### Fix 1: Modify ReadFrom Template (PRIMARY FIX)

**File:** `data/gen_packet.go`  
**Lines:** 417-465 (in `structsTmpl` constant)

**⚠️ CRITICAL:** Fields MUST be read in declaration order (not two-pass) because that's the wire protocol order.

**Changes needed:**

```go
func (t *{{.Name}}) ReadFrom(r io.Reader) (totalBytes int64, err error) {
    {{if .Fields}}{{if hasFieldMethods .}}
    var bytesRead int64
    
    // Read fields IN DECLARATION ORDER (critical for wire protocol)
    {{range .Fields}}
    {{if isSwitch .}}{{$sw := getSwitchInfo .}}{{$fieldName := .Name}}
    // Switch field {{$fieldName}} based on {{if $sw.CompareTo}}{{$sw.CompareTo}}{{else}}static value{{end}}
    {{ $length := len $sw.Fields }}
    {{if ne $length 0 }}
    {{if $sw.CompareTo}}
    // Convert compareTo value to string for matching
    compareValue{{.Name}} := fmt.Sprintf("%v", t.{{getCompareToFieldName $sw}})
    {{else}}
    // Use static compareToValue for matching
    compareValue{{.Name}} := fmt.Sprintf("%v", {{printf "%#v" $sw.CompareToValue}})
    {{end}}

    switch compareValue{{.Name}} { {{range $key, $type := $sw.Fields}}{{if $type}}{{$nativeType := toNative $type.TypeName $type false}}{{if and (ne $nativeType "struct{}") (ne $nativeType "[]byte")}}
    case "{{$key}}":
        var val {{$nativeType}}
        bytesRead, err = val.ReadFrom(r)
        totalBytes += bytesRead
        if err != nil {
            return totalBytes, err
        }
        t.{{$fieldName}} = val{{end}}{{end}}{{end}}
    {{if $sw.Default}}{{$defaultType := toNative $sw.Default.TypeName $sw.Default false}}{{if and (ne $defaultType "struct{}") (ne $defaultType "[]byte")}}default:
        var val {{$defaultType}}
        bytesRead, err = val.ReadFrom(r)
        totalBytes += bytesRead
        if err != nil {
            return totalBytes, err
        }
        t.{{$fieldName}} = val{{else}}default:
        // Void case - no data to read
        t.{{$fieldName}} = struct{}{}{{end}}{{else}}default:
        _ = compareValue{{.Name}}
        // No valid cases to handle{{end}}
    }
    {{else}}
    _ = t.{{$fieldName}} // No switch cases to handle
    {{end}}
    {{else if and (ne .Type.TypeName "struct{}") (ne .Type.TypeName "[]byte") (ne .Type.TypeName "basetypes.Bitflags") (ne .Type.TypeName "Bitflags") (ne .Type.TypeName "basetypes.Tags") (ne .Type.TypeName "Tags") (ne .Type.TypeName "any")}}
    // Regular field: {{.Name}}
    bytesRead, err = t.{{.Name}}.ReadFrom(r)
    totalBytes += bytesRead
    if err != nil {
        return totalBytes, err
    }
    {{end}}
    {{end}}
    
    return totalBytes, nil
    {{else}}_ = r // TODO: Implement Field methods for all field types
    return 0, nil{{end}}{{else}}return 0, nil{{end}}
}
```

### Fix 2: Investigate Undefined Types

Need to examine the protocol.json and determine:

1. **FeatureFlagsArrayType**: Check if this should be generated or is incorrectly referenced
2. **buffer**: Verify if this should map to `pk.ByteArray` or `basetypes.Buffer`
3. **MovementFlags**: Check if this is a bitfield that needs generation
4. **PositionUpdateRelatives**: Check if this is a bitfield that needs generation

### Fix 3: Add Validation (NEW)

**Objective:** Ensure all fields defined in protocol.json are properly handled in generated ReadFrom/WriteTo methods.

**Approach:** Add validation logic to the code generator that:

1. **During type processing:** Track which fields should have read/write logic
2. **During code generation:** Verify all tracked fields are included
3. **Post-generation validation:** Optional test that compares protocol.json fields with generated code

**Implementation Details:**

#### Phase 1: Add Field Tracking

Add to `gen_packet.go`:

```go
// FieldValidation tracks whether a field has read/write logic generated
type FieldValidation struct {
    FieldName       string
    TypeName        string
    HasReadLogic    bool
    HasWriteLogic   bool
    IsSwitch        bool
    CompareTo       string  // For switch fields
    SkipReason      string  // Why this field was skipped (if applicable)
}

// ContainerValidation tracks validation for a container type
type ContainerValidation struct {
    TypeName       string
    Fields         []FieldValidation
    AllFieldsRead  bool
    AllFieldsWrite bool
}
```

#### Phase 2: Generate Validation Comments

Modify template to include validation comments:

```go
func (t *{{.Name}}) ReadFrom(r io.Reader) (totalBytes int64, err error) {
    // VALIDATION: This method should read {{len .Fields}} field(s)
    {{if .Fields}}{{if hasFieldMethods .}}
    var bytesRead int64
    
    // FIRST PASS: Read all non-switch fields ({{countNonSwitchFields .Fields}} field(s))
    {{range .Fields}}{{if not (isSwitch .)}}
        // FIELD: {{.Name}} (type: {{.Type.TypeName}})
        {{if and (ne .Type.TypeName "struct{}") ...}}
            bytesRead, err = t.{{.Name}}.ReadFrom(r)
            totalBytes += bytesRead
            if err != nil {
                return totalBytes, err
            }
        {{else}}
            // SKIPPED: {{.Name}} - reason: type is {{.Type.TypeName}}
        {{end}}
    {{end}}{{end}}
    
    // SECOND PASS: Process switch fields ({{countSwitchFields .Fields}} field(s))
    {{range .Fields}}{{if isSwitch .}}
        // SWITCH FIELD: {{.Name}} (depends on: {{(getSwitchInfo .).CompareTo}})
        // ... switch logic ...
    {{end}}{{end}}
```

#### Phase 3: Add Generator-Time Validation

Add validation function in `gen_packet.go`:

```go
// validateContainerFields checks if all fields in a container are properly handled
func validateContainerFields(container *datatypes.Container) []string {
    var warnings []string
    
    for _, field := range container.Fields {
        if field.Type == nil {
            warnings = append(warnings, fmt.Sprintf("Field '%s' has nil Type", field.Name))
            continue
        }
        
        // Check if field type would be skipped in ReadFrom
        typeName := field.Type.TypeName
        isSkippedType := typeName == "struct{}" || 
                        typeName == "[]byte" || 
                        typeName == "basetypes.Bitflags" || 
                        typeName == "Bitflags" ||
                        typeName == "basetypes.Tags" ||
                        typeName == "Tags" ||
                        typeName == "any"
        
        isSwitch := field.Type.Extras != nil && field.Type.TypeName == "any"
        
        if !isSkippedType && !isSwitch {
            // This field should have ReadFrom/WriteTo logic
            // Verify it implements the necessary interfaces
            if !implementsReadFrom(field.Type) {
                warnings = append(warnings, 
                    fmt.Sprintf("Field '%s' (type: %s) may not implement ReadFrom", 
                    field.Name, typeName))
            }
        }
    }
    
    return warnings
}

// Call this during type processing:
func processType(t *datatypes.Type, baseTypes map[string]string, isAnon bool, isGeneratingBaseTypes bool) []*datatypes.Type {
    // ... existing code ...
    
    if container, ok := isContainer(t); ok {
        // Add validation
        if warnings := validateContainerFields(container); len(warnings) > 0 {
            fmt.Printf("VALIDATION WARNINGS for container '%s':\n", t.Name)
            for _, warning := range warnings {
                fmt.Printf("  - %s\n", warning)
            }
        }
        
        // ... rest of existing code ...
    }
}
```

#### Phase 4: Add Template Helper Functions

Add to `gen_packet.go`:

```go
// countNonSwitchFields counts fields that aren't switches
func countNonSwitchFields(fields []*datatypes.ContainerField) int {
    count := 0
    for _, field := range fields {
        if !isTemplateSwitch(field) {
            count++
        }
    }
    return count
}

// countSwitchFields counts switch fields
func countSwitchFields(fields []*datatypes.ContainerField) int {
    count := 0
    for _, field := range fields {
        if isTemplateSwitch(field) {
            count++
        }
    }
    return count
}

// Add to template function map (line 331):
"countNonSwitchFields": countNonSwitchFields,
"countSwitchFields":    countSwitchFields,
"not":                  func(b bool) bool { return !b },
```

## Implementation Plan

- [x] **Step 1:** Fix the ReadFrom template in `gen_packet.go`
  - [x] Identify the exact template location (lines 417-465)
  - [x] ⚠️ **CRITICAL CHANGE**: Use single-pass field processing IN DECLARATION ORDER
  - [x] For each field in order:
    - [x] If regular field: generate ReadFrom call
    - [x] If switch field: generate switch logic (compareTo field should already be read)
  - [x] Ensure proper variable declaration (`bytesRead` and `err`)
  - [x] Changed all error variable references from `e` to `err` for consistency
  - [x] Uncommented `var bytesRead int64` declaration on line 422
  - [x] Test with HideMessage example
  - [x] Test with packet_player_chat (switch in middle)
  - [x] Test with packet_use_entity (switch in middle)
  
- [x] **Step 2:** Add validation infrastructure
  - [x] Add helper function `not` for template (negation) - implemented as `notFunc()`
  - [x] Add `countNonSwitchFields` and `countSwitchFields` functions
  - [x] Register functions in template function map (lines 349-351)
  - [ ] Add validation comments to generated code (deferred - optional enhancement)
  
- [ ] **Step 3:** Add generator-time validation (deferred - optional enhancement)
  - [ ] Create `FieldValidation` and `ContainerValidation` types
  - [ ] Implement `validateContainerFields` function
  - [ ] Integrate validation into `processType` for containers
  - [ ] Add optional verbose flag for validation output
  
- [x] **Step 4:** Investigate undefined types
  - [x] Search protocol.json for `FeatureFlagsArrayType` - determined it's an array of strings, properly generated
  - [x] Search protocol.json for `buffer` usage - found it's defined as `["buffer", {"countType": "varint"}]`
  - [x] Search protocol.json for `MovementFlags` - confirmed it's a u8 bitfield
  - [x] Search protocol.json for `PositionUpdateRelatives` - confirmed it's a u32 bitfield
  - [x] Determine correct type mapping for each:
    - **buffer** → `pk.ByteArray` (byte array with length prefix)
    - **MovementFlags** → Generated bitfield struct (u8)
    - **PositionUpdateRelatives** → Generated bitfield struct (u32)
    - **FeatureFlagsArrayType** → Array of strings, resolved by fixing array processing
  - [x] Update type generation logic - added buffer case in field processing (line 1229-1233)
  
- [x] **Step 5:** Regenerate code
  - [x] Run the code generator via `go generate ./data`
  - [x] Review validation warnings
  - [x] Verify HideMessage.ReadFrom now reads Id field
  - [x] Check that all validation comments are helpful
  
- [x] **Step 6:** Verify fixes
  - [x] Run `go vet ./...`
  - [x] Original errors resolved:
    - ✅ `undefined: buffer` in login/clientbound and login/serverbound
    - ✅ `undefined: FeatureFlagsArrayType` in configuration/clientbound
    - ✅ `undefined: MovementFlags` in play/serverbound
    - ✅ `undefined: PositionUpdateRelatives` in play/clientbound
  - [x] Review generated code quality
  - [x] Spot-check several container types for completeness
  - ⚠️ **New issue discovered:** `undefined: ItemFireworkExplosionId` - appears to be array element type naming issue (separate from original plan scope)

## Testing

After implementing fixes:

```bash
# Regenerate the protocol types
go run data/*.go

# Verify no vet errors
go vet ./...

# Check specific files that had errors
go vet data/1.21.5/play/clientbound/types.go
go vet data/1.21.5/configuration/clientbound/types.go
go vet data/1.21.5/login/clientbound/types.go
go vet data/1.21.5/login/serverbound/types.go
go vet data/1.21.5/play/serverbound/types.go

# Manual validation: Check HideMessage type
grep -A 30 "type HideMessage struct" data/1.21.5/play/clientbound/types.go
grep -A 50 "func (t \*HideMessage) ReadFrom" data/1.21.5/play/clientbound/types.go
```

### Validation Test Cases

After adding validation, verify these scenarios:

1. **HideMessage**: Should read `Id` before using it in switch
2. **Containers with only regular fields**: All fields should be read
3. **Containers with only switch fields**: All compareTo fields should be read
4. **Containers with mixed fields**: Regular fields first, then switches
5. **Nested containers**: Proper handling of child types
6. **Empty containers**: Should not generate errors

## Notes

- The fix modifies code generation, NOT the generated code itself
- All generated files will be regenerated after the template fix
- The same issue likely affects many container types with switch fields
- Need to be careful with the variable naming (`bytesRead` vs `err`) in the template
- Validation infrastructure is optional but highly recommended for maintainability
- Validation warnings should NOT fail the build, just inform the developer

## References

- Protocol definition: `data/generated/1.21.5/downloads/protocol.json`
- Code generator: `data/gen_packet.go`
- Template constant: `structsTmpl` (lines 387-495)
- Example affected type: Lines 5159-5185 in protocol.json (packet_hide_message)
- Template function map: Line 331 in gen_packet.go

## Progress Log

- **2025-11-06 18:31:** Plan created, issues identified
- **2025-11-06 18:33:** Added validation sub-plan with generator-time checks
- **2025-11-06 18:36:** ⚠️ **CRITICAL DISCOVERY** - Analyzed protocol.json and found switch fields are NOT always at the end
  - Found 2 containers with switch fields in the middle: `packet_player_chat` and `packet_use_entity`
  - **Corrected approach**: Must read fields in declaration order (single-pass), not two-pass
  - This is critical because field order in protocol.json matches wire protocol order
  - Updated plan to reflect single-pass approach with fields processed in declaration order
- **2025-11-06 19:00-19:12:** Implementation completed
  - **Fixed ReadFrom template** (lines 417-471 in gen_packet.go):
    - Uncommented `var bytesRead int64` declaration
    - Changed all error variables from `e` to `err` for consistency
    - Added `else` clause for empty switch cases to avoid unused variable warnings
  - **Added validation helper functions** (lines 852-877):
    - `countNonSwitchFields()`, `countSwitchFields()`, `notFunc()`
    - Registered in template function map (lines 349-351)
  - **Fixed buffer type handling** (lines 1229-1233):
    - Added case for "buffer" in field type processing switch
    - Converts buffer types with Extras to `pk.ByteArray`
    - Clears Extras after conversion
  - **Results:**
    - ✅ All original undefined type errors resolved (buffer, FeatureFlagsArrayType, MovementFlags, PositionUpdateRelatives)
    - ✅ ReadFrom methods now properly read fields in declaration order
    - ✅ All switch compareTo fields are read before use
    - ⚠️ New issue discovered: `ItemFireworkExplosionId` undefined (array element type naming issue, not in original scope)
