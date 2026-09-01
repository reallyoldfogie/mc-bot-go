package screen

import (
	"errors"

	"github.com/Tnze/go-mc/chat"
	gomc_inventory "github.com/Tnze/go-mc/data/inventory"
)

// GenericContainer represents any container type (furnace, hopper, brewing stand, etc.)
type GenericContainer struct {
	Type            gomc_inventory.InventoryID
	Title           chat.Message
	Slots           []Slot
	ContainerSlots  int // Number of slots in the container itself (excluding player inventory)
	PlayerSlotStart int // Index where player inventory slots start
}

func (c *GenericContainer) OnSetSlot(i int, slot Slot) error {
	if i < 0 || i >= len(c.Slots) {
		return errors.New("slot index out of bounds")
	}
	c.Slots[i] = slot
	return nil
}

func (c *GenericContainer) OnClose() error {
	return nil
}

// Container returns just the container-specific slots (e.g., 3 slots for furnace, 5 for hopper)
func (c *GenericContainer) Container() []Slot {
	return c.Slots[0:c.ContainerSlots]
}

// Main returns the player's main inventory slots (27 slots)
func (c *GenericContainer) Main() []Slot {
	if c.PlayerSlotStart+27 > len(c.Slots) {
		return nil
	}
	return c.Slots[c.PlayerSlotStart : c.PlayerSlotStart+27]
}

// Hotbar returns the player's hotbar slots (9 slots)
func (c *GenericContainer) Hotbar() []Slot {
	if c.PlayerSlotStart+36 > len(c.Slots) {
		return nil
	}
	return c.Slots[c.PlayerSlotStart+27 : c.PlayerSlotStart+36]
}

// ContainerTypeInfo holds metadata about a container type
type ContainerTypeInfo struct {
	Identifier      string // e.g., "generic_9x3", "furnace", "hopper"
	ContainerSlots  int    // Number of container-specific slots
	IncludesPlayer  bool   // Whether this container includes player inventory
	PlayerSlotCount int    // Number of player inventory slots (usually 36 = 27 main + 9 hotbar)
}

// TotalSlots returns the total number of slots in this container type
func (c ContainerTypeInfo) TotalSlots() int {
	if c.IncludesPlayer {
		return c.ContainerSlots + c.PlayerSlotCount
	}
	return c.ContainerSlots
}

// containerTypeRegistry maps container type IDs to their metadata
// Based on https://minecraft.wiki/w/Java_Edition_protocol/Inventory
var containerTypeRegistry = map[int32]ContainerTypeInfo{
	// Chest variants (9xN)
	0: {"generic_9x1", 9, true, 36},
	1: {"generic_9x2", 18, true, 36},
	2: {"generic_9x3", 27, true, 36},
	3: {"generic_9x4", 36, true, 36},
	4: {"generic_9x5", 45, true, 36},
	5: {"generic_9x6", 54, true, 36},

	// Other containers
	6:  {"generic_3x3", 9, true, 36},       // Dispenser, dropper
	7:  {"crafter_3x3", 9, true, 36},       // Crafter (has 10 total slots but 9 are for crafting, 1 is for output)
	8:  {"anvil", 3, true, 36},             // Anvil
	9:  {"beacon", 1, true, 36},            // Beacon includes full player inventory
	10: {"blast_furnace", 3, true, 36},     // Blast furnace
	11: {"brewing_stand", 5, true, 36},     // Brewing stand
	12: {"crafting", 10, true, 36},         // Crafting table (9 crafting grid + 1 output)
	13: {"enchantment", 2, true, 36},       // Enchantment table
	14: {"furnace", 3, true, 36},           // Furnace
	15: {"grindstone", 3, true, 36},        // Grindstone
	16: {"hopper", 5, true, 36},            // Hopper
	17: {"lectern", 1, false, 0},           // Lectern (special: no player inventory)
	18: {"loom", 4, true, 36},              // Loom
	19: {"merchant", 3, true, 36},          // Villager/wandering trader
	20: {"shulker_box", 27, true, 36},      // Shulker box
	21: {"smithing", 4, true, 36},          // Smithing table
	22: {"smoker", 3, true, 36},            // Smoker
	23: {"cartography_table", 3, true, 36}, // Cartography table
	24: {"stonecutter", 2, true, 36},       // Stonecutter
}

// GetContainerTypeInfo returns metadata for a container type ID
// Returns ok=false if the type is unknown
func GetContainerTypeInfo(typeID int32) (ContainerTypeInfo, bool) {
	info, ok := containerTypeRegistry[typeID]
	return info, ok
}

// RegisterContainerType allows dynamic registration of new container types
// This enables support for future Minecraft versions without code changes
func RegisterContainerType(typeID int32, info ContainerTypeInfo) {
	containerTypeRegistry[typeID] = info
}

// GetContainerTypeIDByIdentifier returns the protocol ID for a container by its identifier
// (e.g., "minecraft:loom" or just "loom"). Returns -1 if not found.
func GetContainerTypeIDByIdentifier(identifier string) int32 {
	// Strip "minecraft:" prefix if present
	if len(identifier) > 10 && identifier[:10] == "minecraft:" {
		identifier = identifier[10:]
	}

	for typeID, info := range containerTypeRegistry {
		// Strip "minecraft:" prefix from registry identifier if present
		registryID := info.Identifier
		if len(registryID) > 10 && registryID[:10] == "minecraft:" {
			registryID = registryID[10:]
		}

		if registryID == identifier {
			return typeID
		}
	}
	return -1
}
