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

// buildContainerTypesFromRegistryEntries maps "minecraft:menu" registry
// entries (name -> protocol ID) to ContainerTypeInfo using the known
// container shapes in containerMetadata. Names with no known shape are
// returned separately rather than silently dropped, so callers can log a
// warning for forward-compat: a container type a newer Minecraft version
// added that this binary doesn't know the layout of yet.
func buildContainerTypesFromRegistryEntries(entries map[string]int32) (map[int32]ContainerTypeInfo, []string) {
	result := make(map[int32]ContainerTypeInfo, len(entries))
	var unknown []string
	for name, id := range entries {
		metadata, ok := containerMetadata[name]
		if !ok {
			unknown = append(unknown, name)
			continue
		}
		result[id] = ContainerTypeInfo{
			Identifier:      name,
			ContainerSlots:  metadata.ContainerSlots,
			IncludesPlayer:  metadata.IncludesPlayer,
			PlayerSlotCount: metadata.PlayerSlotCount,
		}
	}
	return result, unknown
}

// populateContainerTypesFromLiveRegistry populates m.liveContainerTypes from
// the "minecraft:menu" registry the connected server actually sent during
// configuration (bot/configuration.go). This is the authoritative source
// for the version actually being played, unlike the hardcoded
// containerTypeRegistry fallback (see generic_container.go) which reflects
// a single version's snapshot. It's a no-op if the client has no such
// registry (e.g. offline/replay use, or a server that omits it).
//
// Called lazily (via manager.registrySyncOnce) from onOpenScreen rather than
// from NewManager, since NewManager may run before JoinServer completes
// configuration; onOpenScreen can only fire after configuration has
// finished, so the registry data (if any) is guaranteed to already be
// populated by then.
func (m *manager) populateContainerTypesFromLiveRegistry() {
	if m.c == nil {
		return
	}
	registries := m.c.RegistryData()
	menu, ok := registries["minecraft:menu"]
	if !ok || menu == nil {
		return
	}

	entries := make(map[string]int32, len(menu.Entries))
	for _, entry := range menu.Entries {
		if entry != nil {
			entries[entry.Name] = entry.ID
		}
	}

	local, unknown := buildContainerTypesFromRegistryEntries(entries)
	if len(local) == 0 {
		return
	}

	m.mu.Lock()
	m.liveContainerTypes = local
	m.mu.Unlock()

	fmt.Printf("[registry_loader] Registered %d container types from live server registry data (minecraft:menu)\n", len(local))
	if len(unknown) > 0 {
		fmt.Printf("[registry_loader] Warning: %d unknown container type(s) in server registry (no known shape): %v\n", len(unknown), unknown)
	}
}

// getContainerTypeInfo looks up container-type metadata, preferring the
// live server registry data (see populateContainerTypesFromLiveRegistry)
// over the hardcoded package-level fallback.
func (m *manager) getContainerTypeInfo(typeID int32) (ContainerTypeInfo, bool) {
	m.mu.RLock()
	info, ok := m.liveContainerTypes[typeID]
	m.mu.RUnlock()
	if ok {
		return info, true
	}
	return GetContainerTypeInfo(typeID)
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
