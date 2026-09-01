# Container Type Support

## Overview

The screen manager now supports all Minecraft container types through a dynamic registry system.

## Implementation

### Files Modified

1. **`generic_container.go`** (NEW)
   - `GenericContainer` struct for all non-chest container types
   - `ContainerTypeInfo` struct with metadata for each container type
   - `containerTypeRegistry` map with all 25 standard container types (0-24)
   - `RegisterContainerType()` function for dynamic registration of future types

2. **`screen.go`**
   - Updated `onOpenScreen()` to use the container type registry
   - Dynamically allocates correct slot count for each container type
   - Maintains backward compatibility with Chest type for types 0-5

### Supported Container Types

| ID | Identifier | Container Slots | Total Slots | Examples |
|----|-----------|-----------------|-------------|----------|
| 0 | generic_9x1 | 9 | 45 | Small chest |
| 1 | generic_9x2 | 18 | 54 | |
| 2 | generic_9x3 | 27 | 63 | Chest, Barrel, Ender Chest |
| 3 | generic_9x4 | 36 | 72 | |
| 4 | generic_9x5 | 45 | 81 | |
| 5 | generic_9x6 | 54 | 90 | Double Chest |
| 6 | generic_3x3 | 9 | 45 | Dispenser, Dropper |
| 7 | crafter_3x3 | 9 | 45 | Crafter |
| 8 | anvil | 3 | 39 | Anvil |
| 9 | beacon | 1 | 28 | Beacon (special: only 27 player slots) |
| 10 | blast_furnace | 3 | 39 | Blast Furnace |
| 11 | brewing_stand | 5 | 41 | Brewing Stand |
| 12 | crafting | 10 | 46 | Crafting Table |
| 13 | enchantment | 2 | 38 | Enchantment Table |
| 14 | furnace | 3 | 39 | Furnace |
| 15 | grindstone | 3 | 39 | Grindstone |
| 16 | hopper | 5 | 41 | Hopper |
| 17 | lectern | 1 | 1 | Lectern (special: no player inventory) |
| 18 | loom | 4 | 40 | Loom |
| 19 | merchant | 3 | 39 | Villager, Wandering Trader |
| 20 | shulker_box | 27 | 63 | Shulker Box |
| 21 | smithing | 4 | 40 | Smithing Table |
| 22 | smoker | 3 | 39 | Smoker |
| 23 | cartography_table | 3 | 39 | Cartography Table |
| 24 | stonecutter | 2 | 38 | Stonecutter |

### Special Cases

- **Beacon** (Type 9): Only 27 player inventory slots (no hotbar in window)
- **Lectern** (Type 17): No player inventory (1 slot total)
- **Mob Inventories**: Horse/Donkey/Llama/Camel use dedicated packets (not in this registry)

## Usage

### Backward Compatibility

Existing code using `Chest` type for container types 0-5 continues to work unchanged.

### Generic Containers

All other container types (6-24) use `GenericContainer`:

```go
screen, ok := screenMgr.Screens[windowID]
if !ok {
    return errors.New("container not found")
}

switch c := screen.(type) {
case *mcscreen.Chest:
    // Handle chest (types 0-5)
    slots := c.Slots
    containerSlots := c.Container()
    playerSlots := append(c.Main(), c.Hotbar()...)

case *mcscreen.GenericContainer:
    // Handle all other containers (types 6-24)
    slots := c.Slots
    containerSlots := c.Container()
    playerSlots := append(c.Main(), c.Hotbar()...)
}
```

### Dynamic Registration

Support for future Minecraft versions:

```go
// Register a new container type from a future Minecraft version
mcscreen.RegisterContainerType(25, mcscreen.ContainerTypeInfo{
    Identifier:      "future_container",
    ContainerSlots:  10,
    IncludesPlayer:  true,
    PlayerSlotCount: 36,
})
```

## Implementation Notes

### Slot Layout

All containers (except Lectern and Beacon) follow this slot layout:
- **Container slots**: 0 to N-1 (where N = container-specific slot count)
- **Player main inventory**: N to N+26 (27 slots)
- **Player hotbar**: N+27 to N+35 (9 slots)

Total: N + 36 slots

### Forward Compatibility

Unknown container types return an error rather than creating an invalid container. This allows:
1. Clear error messages when connecting to newer server versions
2. Easy addition of new types via `RegisterContainerType()`
3. No silent failures or crashes

## Testing

- ✅ Chest containers (types 0-5) - tested with TestChestInteraction, TestChestWithItems
- ⚠️  Furnace/Hopper containers (types 14, 16) - implementation complete, integration tests pending

## References

- [Minecraft Wiki: Java Edition Protocol/Inventory](https://minecraft.wiki/w/Java_Edition_protocol/Inventory)
- Protocol version: 1.21.5 (770)
