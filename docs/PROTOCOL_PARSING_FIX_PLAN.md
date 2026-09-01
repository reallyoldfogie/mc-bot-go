# Protocol Parsing Fix Plan

## Investigation Summary

### Issue 1: Missing Fields in Generated Containers

**Problem**: Containers like `SlotComponent` and `UntrustedSlotComponent` are missing fields that switches reference.

**Root Cause**: The issue is in `data/gen_packet.go` at lines 773-780 in the `processType` function. When processing containers, the code filters out fields with `nil Type`:

```go
// Filter out fields with nil Type
filteredFields := []*datatypes.ContainerField{}
for _, field := range container.Fields {
    if field.Type != nil {
        filteredFields = append(filteredFields, field)
    }
}
container.Fields = filteredFields
```

**Investigation Findings**:

1. **Protocol Definition** (from protocol.json):
   ```json
   "SlotComponent": [
     "container",
     [
       {"name": "type", "type": "SlotComponentType"},
       {"name": "data", "type": ["switch", {"compareTo": "type", "fields": {...}}]}
     ]
   ]
   ```

2. **Generated Code** (in basetypes/types.go):
   ```go
   type UnnamedType0026 struct {
       Data any  // Missing: Type SlotComponentType
   }
   ```

3. **The Problem**: 
   - The `type` field (SlotComponentType) is being parsed correctly from protocol.json by protodef-go
   - However, it's being filtered out by the `if field.Type != nil` check at line 776
   - This happens because protodef-go might be setting `field.Type` to `nil` for certain field types, OR
   - The field is being processed in a way that makes it appear later but gets overwritten

**Diagnosis**: The actual issue is NOT that fields have nil Type, but rather:
- When protodef-go parses the container, it correctly creates both fields
- However, there may be an issue where the compareTo field is not being preserved properly
- Need to add debug logging to see what fields exist before filtering

### Issue 2: Unreachable Code Warnings

**Problem**: Some containers with only void switch cases generate harmless unreachable code warnings.

**Examples**:
- `UntrustedSlotComponent` - Actually has TWO fields in protocol.json but BOTH are missing in generated code
- Other containers that end up with empty struct definitions

**Root Cause**: This is a SECONDARY issue caused by Issue 1. When fields are missing:
1. Containers end up with no fields or only void-typed switch fields
2. The generated ReadFrom/WriteTo methods have no actual field reading/writing code
3. Compiler warns about unreachable code after `return 0, nil`

## Proposed Fix Plan

### Phase 1: Diagnostic Enhancement (Priority: High)

**Goal**: Understand exactly when and why fields are being set to nil or filtered out.

**Tasks**:
1. Add debug logging in `processType` before and after the field filtering:
   ```go
   // BEFORE filtering
   fmt.Printf("DEBUG: Container %s has %d fields before filtering:\n", t.Name, len(container.Fields))
   for i, field := range container.Fields {
       typeInfo := "nil"
       if field.Type != nil {
           typeInfo = field.Type.Name + " (" + field.Type.TypeName + ")"
       }
       fmt.Printf("  [%d] %s: %s\n", i, field.Name, typeInfo)
   }
   ```

2. Add similar logging in protodef-go's container.ReadJSON to see what's being parsed:
   ```go
   // In /home/reallyoldfogie/src/github.com/reallyoldfogie/protodef-go/datatypes/container.go
   // After line 49 (field.Type = GetTypeFromJSON(...))
   ```

3. Run generation and capture output:
   ```bash
   cd data
   go run gen_data.go templates.go gen_packet.go getMCVersionData.go parsePackets.go > debug.log 2>&1
   ```

### Phase 2: Root Cause Fix (Priority: High)

**Option A: Fix Field Filtering Logic** (if fields are actually nil)
- Investigate WHY fields are nil in the first place
- Fix protodef-go to ensure all fields have proper Type set
- The filtering at line 773-780 is actually correct safety code

**Option B: Fix Field Preservation** (if fields are not nil but lost)
- The issue might be that fields are being processed correctly but:
  - Overwritten during type processing
  - Lost during container field iteration
  - Not properly cloned when creating child types

**Recommended Investigation Steps**:
1. Check if `GetTypeFromJSON` in protodef-go is returning nil for certain type references
2. Check if `SlotComponentType` is being properly resolved as a base type
3. Verify that when a switch references a compareTo field, that field is in the container

### Phase 3: Template Fix for Unreachable Code (Priority: Medium)

**Goal**: Fix templates to not generate unreachable code warnings.

**Approach**:
1. In the struct template (lines 392-434 of gen_packet.go), detect if container has no readable fields
2. If no fields need ReadFrom calls, skip the `var totalBytes` and related setup:
   ```go
   func (t *{{.Name}}) ReadFrom(r io.Reader) (int64, error) {
       {{if .Fields}}{{if hasFieldMethods .}}
       // ... existing code with totalBytes ...
       {{else}}
       // No fields to read
       return 0, nil
       {{end}}{{else}}
       return 0, nil
       {{end}}
   }
   ```

3. Similar fix for WriteTo method (lines 437-465)

### Phase 4: Comprehensive Testing (Priority: High)

**Test Cases**:
1. Verify `SlotComponent` has both `Type` and `Data` fields
2. Verify `UntrustedSlotComponent` has proper fields
3. Run a full generation and check for:
   - Missing fields in other containers
   - Unreachable code warnings
4. Build test to ensure switches can access their compareTo fields at runtime

## Implementation Order

1. **Day 1**: Add diagnostic logging (Phase 1)
2. **Day 1-2**: Analyze logs and identify exact root cause
3. **Day 2-3**: Implement root cause fix (Phase 2)
4. **Day 3**: Fix template warnings (Phase 3)  
5. **Day 4**: Run comprehensive tests (Phase 4)
6. **Day 4**: Clean up debug logging and finalize

## Key Files to Modify

1. `/home/reallyoldfogie/src/github.com/reallyoldfogie/mc-bot-go/data/gen_packet.go`
   - Lines 773-780: Field filtering logic
   - Lines 392-465: Struct template

2. `/home/reallyoldfogie/src/github.com/reallyoldfogie/protodef-go/datatypes/container.go`
   - Lines 21-54: Container JSON parsing
   - May need to investigate GetTypeFromJSON

3. `/home/reallyoldfogie/src/github.com/reallyoldfogie/protodef-go/datatypes/switch.go`
   - Verify switch parsing preserves compareTo field reference

## Expected Outcome

After fixes:
- `SlotComponent` should generate:
  ```go
  type UnnamedType0026 struct {
      Type SlotComponentType
      Data any
  }
  ```
- No unreachable code warnings
- All switch statements can properly access their compareTo fields
- All containers have complete field definitions matching protocol.json

## Risk Assessment

**Low Risk**:
- Template fixes (Phase 3)
- Diagnostic logging (Phase 1)

**Medium Risk**:
- Field filtering changes (Phase 2, Option A)
- Could affect other containers if not careful

**High Risk**:
- Changes to protodef-go library (Phase 2, depending on root cause)
- Would affect all type parsing, needs thorough testing

## Notes

- The protocol.json IS correct - both SlotComponent and UntrustedSlotComponent have proper field definitions
- The bug is in the Go code generation pipeline, NOT in protodef-go parsing
- This affects multiple container types, not just SlotComponent
- The unreachable code issue is a SYMPTOM, not the root cause
