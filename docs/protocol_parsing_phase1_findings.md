# Phase 1 Diagnostic Findings

## Root Cause Identified

The issue is in `protodef-go/datatypes/type.go` in the `GetType` function.

### The Problem

When a container field references a custom type (e.g., `"SlotComponentType"`), the function flow is:

1. `GetTypeFromJSON("type", "SlotComponentType")` is called
2. `GetType` is called with the string `"SlotComponentType"`
3. `GetType` checks if it's a native type by calling `GetNativeType`
4. `GetNativeType` returns `nil` because `"SlotComponentType"` is NOT a native type (like i8, u8, varint, etc.)
5. `GetType` has no other handler for string type references, so it returns `nil`
6. `GetTypeFromJSON` returns `nil`
7. In gen_packet.go, the field filtering code removes fields with `nil` Type

### Evidence from Debug Logs

```
DEBUG [GetTypeFromJSON]: Returning nil for name='type', option.Type=String, option.Raw='"SlotComponentType"'
DEBUG [container.ReadJSON]: Container '' parsed field 'type' Type=nil Anon=false
```

```
DEBUG [gen_packet.go]: Container 'SlotComponent' has 2 fields before filtering:
  [0] Field='type' Type=nil Anon=false
  [1] Field='data' Type=data (typename=switch) Anon=false
DEBUG [gen_packet.go]: Container 'SlotComponent' FILTERED OUT 1 fields (from 2 to 1)
```

```
DEBUG [gen_packet.go]: Container 'UntrustedSlotComponent' has 2 fields before filtering:
  [0] Field='type' Type=nil Anon=false
  [1] Field='data' Type=nil Anon=false
DEBUG [gen_packet.go]: Container 'UntrustedSlotComponent' FILTERED OUT 2 fields (from 2 to 0)
```

### The Fix Required

In `protodef-go/datatypes/type.go`, the `GetType` function needs to handle string references to custom types:

```go
func GetType(name string, d gjson.Result) *Type {
    var t *Type
    if d.Type == gjson.String {
        t = GetNativeType(d.String())
        if t != nil {
            return t
        }
        // NEW CODE: If not a native type, treat it as a custom type reference
        return &Type{
            Name:     d.String(),
            TypeName: d.String(),
        }
    }
    // ... rest of function
}
```

This will create a Type object for custom type references instead of returning nil, allowing them to be properly preserved in containers.

## Next Steps

Move to Phase 2: Implement the fix in protodef-go/datatypes/type.go
