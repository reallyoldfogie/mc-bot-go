package screen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Registry represents the structure of Minecraft's registries.json file
type Registry struct {
	Entries map[string]RegistryEntry `json:"entries"`
}

// RegistryEntry represents a single entry in a registry
type RegistryEntry struct {
	ProtocolID int32 `json:"protocol_id"`
}

// Registries represents the top-level structure of registries.json
type Registries struct {
	Menu Registry `json:"minecraft:menu"`
}

// ContainerMetadata holds the hardcoded metadata for each container type
// This maps the registry identifier to slot counts and player inventory inclusion
var containerMetadata = map[string]struct {
	ContainerSlots  int
	IncludesPlayer  bool
	PlayerSlotCount int
}{
	"minecraft:generic_9x1":       {9, true, 36},
	"minecraft:generic_9x2":       {18, true, 36},
	"minecraft:generic_9x3":       {27, true, 36},
	"minecraft:generic_9x4":       {36, true, 36},
	"minecraft:generic_9x5":       {45, true, 36},
	"minecraft:generic_9x6":       {54, true, 36},
	"minecraft:generic_3x3":       {9, true, 36},
	"minecraft:crafter_3x3":       {9, true, 36},
	"minecraft:anvil":             {3, true, 36},
	"minecraft:beacon":            {1, false, 27}, // Special: only 27 player slots
	"minecraft:blast_furnace":     {3, true, 36},
	"minecraft:brewing_stand":     {5, true, 36},
	"minecraft:crafting":          {10, true, 36},
	"minecraft:enchantment":       {2, true, 36},
	"minecraft:furnace":           {3, true, 36},
	"minecraft:grindstone":        {3, true, 36},
	"minecraft:hopper":            {5, true, 36},
	"minecraft:lectern":           {1, false, 0}, // Special: no player inventory
	"minecraft:loom":              {4, true, 36},
	"minecraft:merchant":          {3, true, 36},
	"minecraft:shulker_box":       {27, true, 36},
	"minecraft:smithing":          {4, true, 36},
	"minecraft:smoker":            {3, true, 36},
	"minecraft:cartography_table": {3, true, 36},
	"minecraft:stonecutter":       {2, true, 36},
}

// LoadContainerTypesFromRegistry loads container type IDs from a Minecraft registries.json file
// and updates the containerTypeRegistry with the correct protocol IDs for the current version.
//
// This function expects the registries.json file to be at:
// {dataPath}/data_generator/reports/registries.json
//
// Example usage:
//
//	if err := screen.LoadContainerTypesFromRegistry("/path/to/minecraft/data/1.21.5"); err != nil {
//	    log.Printf("Warning: failed to load container registry: %v (using hardcoded values)", err)
//	}
func LoadContainerTypesFromRegistry(dataPath string) error {
	registryPath := filepath.Join(dataPath, "data_generator", "reports", "registries.json")

	// Read the registries file
	data, err := os.ReadFile(registryPath)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", registryPath, err)
	}

	// Parse the JSON
	var registries Registries
	if err := json.Unmarshal(data, &registries); err != nil {
		return fmt.Errorf("failed to parse %s: %w", registryPath, err)
	}

	// Build the container type registry from the minecraft:menu entries
	loadedCount := 0
	for identifier, entry := range registries.Menu.Entries {
		// Look up the metadata for this container type
		metadata, ok := containerMetadata[identifier]
		if !ok {
			// Unknown container type - skip it but log a warning
			fmt.Printf("[registry_loader] Warning: unknown container type %q (ID %d) - skipping\n",
				identifier, entry.ProtocolID)
			continue
		}

		// Create the ContainerTypeInfo
		info := ContainerTypeInfo{
			Identifier:      identifier,
			ContainerSlots:  metadata.ContainerSlots,
			IncludesPlayer:  metadata.IncludesPlayer,
			PlayerSlotCount: metadata.PlayerSlotCount,
		}

		// Register it with the protocol ID from the registry
		RegisterContainerType(entry.ProtocolID, info)
		loadedCount++
	}

	fmt.Printf("[registry_loader] ✓ Loaded %d container types from %s\n", loadedCount, registryPath)
	return nil
}
