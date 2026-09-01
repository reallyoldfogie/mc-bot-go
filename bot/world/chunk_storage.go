package world

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"sync"

	pk "github.com/Tnze/go-mc/net/packet"
)

const verbose = false

// Section size constants for PaletteContainer calculations
const (
	// BLOCK_SECTION_SIZE is the number of blocks in a chunk section (16×16×16)
	BLOCK_SECTION_SIZE = 4096
	// BIOME_SECTION_SIZE is the number of biome entries in a chunk section (4×4×4)
	// Biomes are stored at a coarser granularity than blocks
	BIOME_SECTION_SIZE = 64
)

// Section represents a decoded chunk section with cached palette data
// This eliminates redundant parsing when accessing multiple blocks in the same section
type Section struct {
	BlockCount   int16    // Number of non-air blocks in section
	BitsPerEntry uint8    // 0 = single-valued, >0 = palette-based
	Palette      []uint32 // Block state IDs (empty if BitsPerEntry == 0)
	SingleValue  uint32   // Single state ID if BitsPerEntry == 0
	DataArray    []uint64 // Packed block indices (empty if BitsPerEntry == 0)
}

// ChunkData represents a chunk's data in a version-agnostic way
// It stores the raw section data from the MapChunk packet
type ChunkData struct {
	X, Z         int32
	SectionCount int
	RawData      []byte // Raw ChunkData from MapChunk packet

	// Cached decoded sections (lazy populated on first access)
	Sections     []*Section
	sectionsLock sync.RWMutex // Protects concurrent access to Sections
}

// GetBlockAt returns the block state ID at the given chunk-relative coordinates
// x, z are chunk-relative (0-15), y is world absolute
func (c *ChunkData) GetBlockAt(x, y, z int) uint32 {
	if verbose {
		log.Printf("[DEBUG ChunkData.GetBlockAt] x=%d y=%d z=%d sectionCount=%d\n", x, y, z, c.SectionCount)
	}
	// Calculate section index
	sectionIdx := y >> 4           // y / 16
	const minSectionIdx = -64 / 16 // -4 => min y / 16

	sectionSlotIdx := sectionIdx - minSectionIdx

	// Validate section index
	if sectionSlotIdx < 0 || sectionSlotIdx >= c.SectionCount {
		log.Printf("[ERROR ChunkData.GetBlockAt] sectionIdx=%d sectionSlotIdx=%d (sectionSlotIdx >= c.SectionCount)=%t x=%d y=%d z=%d \n", sectionIdx, sectionSlotIdx, sectionIdx >= c.SectionCount, x, y, z)
		return 0 // Outside world bounds
	}

	if verbose {
		log.Printf("[ChunkData.GetBlockAt] (%d, %d, %d) sectionIdx %d sectionSlotIdx %d", x, y, z, sectionIdx, sectionSlotIdx)
	}

	// Get section from cache (or load if not cached)
	section := c.getOrLoadSection(sectionSlotIdx)
	if section == nil {
		log.Printf("[ERROR ChunkData.GetBlockAt] Failed to load section %d for block at (%d, %d, %d)", sectionSlotIdx, x, y, z)
		return 0
	}

	// Extract block state from cached section
	// Calculate section-relative Y (handles negative Y correctly)
	sectionRelY := y - (sectionIdx << 4)
	return c.getBlockFromSection(section, x, sectionRelY, z)
}

// skipSection skips over a section in the ChunkData ByteArray
func skipSection(r io.Reader) error {
	// Skip BlockCount (Short)
	var blockCount pk.Short
	if _, err := blockCount.ReadFrom(r); err != nil {
		return err
	}

	// Skip BlockStates PaletteContainer
	if err := skipPaletteContainer(r, BLOCK_SECTION_SIZE); err != nil {
		return err
	}

	// Skip Biomes PaletteContainer
	if err := skipPaletteContainer(r, BIOME_SECTION_SIZE); err != nil {
		return err
	}

	return nil
}

// skipPaletteContainer skips over a PaletteContainer in the stream
// sectionSize is the number of entries: BLOCK_SECTION_SIZE (4096) or BIOME_SECTION_SIZE (64)
func skipPaletteContainer(r io.Reader, sectionSize int) error {
	// Read bits per entry
	var bitsPerEntry pk.UnsignedByte
	if _, err := bitsPerEntry.ReadFrom(r); err != nil {
		return err
	}

	bits := int(bitsPerEntry)

	// Handle single-valued palette (bits == 0)
	if bits == 0 {
		// Single value palette: just read the single VarInt value
		var singleValue pk.VarInt
		if _, err := singleValue.ReadFrom(r); err != nil {
			return err
		}
		// No data array for single-valued palette
		return nil
	}

	// Read palette (array of VarInt)
	var paletteLength pk.VarInt
	if _, err := paletteLength.ReadFrom(r); err != nil {
		return err
	}

	for i := 0; i < int(paletteLength); i++ {
		var paletteEntry pk.VarInt
		if _, err := paletteEntry.ReadFrom(r); err != nil {
			return err
		}
	}

	// Calculate data array length (Minecraft 1.21.5+ no longer sends this as VarInt)
	// Formula: (sectionSize + entriesPerLong - 1) / entriesPerLong
	entriesPerLong := 64 / bits
	dataLength := (sectionSize + entriesPerLong - 1) / entriesPerLong

	// Read data array (array of Long)
	for i := 0; i < dataLength; i++ {
		var dataEntry pk.Long
		if _, err := dataEntry.ReadFrom(r); err != nil {
			return err
		}
	}

	return nil
}

// readBlockStateFromSection reads a specific block's state ID from a section
func readBlockStateFromSection(r io.Reader, x, y, z int) (stateID uint32, err error) {
	if verbose {
		defer log.Printf("[DEBUG readBlockStateFromSection] x=%d y=%d z=%d => stateID=%d", x, y, z, stateID)
	}
	// Read BlockCount (we don't use it, but need to read it)
	var blockCount pk.Short
	if _, err := blockCount.ReadFrom(r); err != nil {
		log.Printf("[ERROR readBlockStateFromSection (blockCount.ReadFrom)] x=%d y=%d z=%d: err:%s\n", x, y, z, err.Error())
		return 0, err
	}

	if verbose {
		log.Printf("[DEBUG readBlockStateFromSection] blockCount=%d\n", int16(blockCount))
	}

	// Read BlockStates PaletteContainer
	stateID, err = readBlockFromPaletteContainer(r, x, y, z)
	if err != nil {
		log.Printf("[ERROR readBlockStateFromSection (readBlockFromPaletteContainer)] x=%d y=%d z=%d: err:%s\n", x, y, z, err.Error())
		return 0, err
	}

	return stateID, nil
}

// readBlockFromPaletteContainer reads a specific block's value from a PaletteContainer
func readBlockFromPaletteContainer(r io.Reader, x, y, z int) (uint32, error) {
	// Read bits per entry
	var bitsPerEntry pk.UnsignedByte
	if _, err := bitsPerEntry.ReadFrom(r); err != nil {
		log.Printf("[ERROR readBlockFromPaletteContainer - bitsPerEntry.ReadFrom] x=%d y=%d z=%d ", x, y, z)
		return 0, err
	}

	if verbose {
		log.Printf("[DEBUG readBlockStateFromSection] bitsPerEntry=%d\n", uint8(bitsPerEntry))
	}

	bits := int(bitsPerEntry)

	// Handle single-valued palette (bits == 0)
	if bits == 0 {
		// Single value palette: all blocks have the same value
		var singleValue pk.VarInt
		if _, err := singleValue.ReadFrom(r); err != nil {
			log.Printf("[ERROR readBlockFromPaletteContainer - singleValue.ReadFrom] x=%d y=%d z=%d ", x, y, z)

			return 0, err
		}

		if verbose {
			log.Printf("[DEBUG readBlockFromPaletteContainer] x=%d y=%d z=%d singleValue=%d", x, y, z, int32(singleValue))
		}
		return uint32(singleValue), nil
	}

	// Read palette
	var paletteLength pk.VarInt
	if _, err := paletteLength.ReadFrom(r); err != nil {
		log.Printf("[ERROR readBlockFromPaletteContainer - paletteLength.ReadFrom] x=%d y=%d z=%d err=%s", x, y, z, err.Error())
		return 0, err
	}

	palette := make([]uint32, paletteLength)
	for i := 0; i < int(paletteLength); i++ {
		var paletteEntry pk.VarInt
		if _, err := paletteEntry.ReadFrom(r); err != nil {
			log.Printf("[ERROR readBlockFromPaletteContainer - paletteEntry.ReadFrom] x=%d y=%d z=%d err=%s", x, y, z, err.Error())
			return 0, err
		}
		palette[i] = uint32(paletteEntry)
	}

	if verbose {
		log.Printf("[DEBUG readBlockFromPaletteContainer] paletteLength=%d palette=%v", paletteLength, palette)
	}
	// Calculate data array length (Minecraft 1.21.5+ no longer sends this as VarInt)
	// Formula: (size + entries_per_long - 1) / entries_per_long
	// Where size = BLOCK_SECTION_SIZE (blocks in section), entries_per_long = 64 / bits
	var dataLength int

	if bits == 0 {
		// Single-valued palette has no data array
		dataLength = 0
	} else {
		entriesPerLong := 64 / bits
		dataLength = (BLOCK_SECTION_SIZE + entriesPerLong - 1) / entriesPerLong
	}

	if verbose {
		log.Printf("[DEBUG readBlockFromPaletteContainer] Calculated dataLength=%d (bitsPerEntry=%d)", dataLength, bits)
	}

	// Handle empty data array (single-valued palette)
	if dataLength == 0 {
		if len(palette) > 0 {
			if verbose {
				log.Printf("[DEBUG readBlockFromPaletteContainer] Empty data array, returning palette[0]=%d", palette[0])
			}
			return palette[0], nil
		}
		if verbose {
			log.Printf("[DEBUG readBlockFromPaletteContainer] Empty data array and empty palette, returning 0")
		}
		return 0, nil
	}

	dataArray := make([]uint64, dataLength)
	for i := 0; i < int(dataLength); i++ {
		var dataEntry pk.Long
		if _, err := dataEntry.ReadFrom(r); err != nil {
			return 0, err
		}
		dataArray[i] = uint64(dataEntry)
	}

	// Calculate block index within section
	// Minecraft convention: (y << 8) | (z << 4) | x
	blockIdx := (y << 8) | (z << 4) | x

	// Extract value from packed data array
	valuesPerLong := 64 / bits
	longIndex := blockIdx / valuesPerLong
	indexInLong := blockIdx % valuesPerLong

	if longIndex >= len(dataArray) {
		if verbose {
			log.Printf("[DEBUG readBlockFromPaletteContainer] longIndex=%d >= len(dataArray)=%d, returning 0", longIndex, len(dataArray))
		}
		return 0, nil // Out of bounds
	}

	// Extract the value
	shift := indexInLong * bits
	mask := uint64((1 << bits) - 1)
	paletteIndex := (dataArray[longIndex] >> shift) & mask

	if verbose {
		log.Printf("[DEBUG readBlockFromPaletteContainer] x=%d y=%d z=%d blockIdx=%d longIndex=%d indexInLong=%d shift=%d dataArray[%d]=0x%x paletteIndex=%d",
			x, y, z, blockIdx, longIndex, indexInLong, shift, longIndex, dataArray[longIndex], paletteIndex)
	}

	if int(paletteIndex) >= len(palette) {
		if verbose {
			log.Printf("[DEBUG readBlockFromPaletteContainer - (paletteIndex >= len(palette)=%t)] x=%d y=%d z=%d", (int(paletteIndex) >= len(palette)), x, y, z)
		}
		return 0, nil // Invalid palette index
	}

	stateID := palette[paletteIndex]
	if verbose {
		log.Printf("[DEBUG readBlockFromPaletteContainer] x=%d y=%d z=%d paletteIndex=%d => stateID=%d", x, y, z, paletteIndex, stateID)
	}
	return stateID, nil
}

// ============================================================================
// Section Caching Implementation
// ============================================================================

// getOrLoadSection returns the cached section at the given index, loading it
// from RawData if not yet cached. Thread-safe with double-checked locking.
func (c *ChunkData) getOrLoadSection(sectionSlotIdx int) *Section {
	// Fast path: check cache with read lock
	c.sectionsLock.RLock()
	if c.Sections[sectionSlotIdx] != nil {
		section := c.Sections[sectionSlotIdx]
		c.sectionsLock.RUnlock()
		if verbose {
			log.Printf("[DEBUG getOrLoadSection] Cache HIT for section %d", sectionSlotIdx)
		}
		return section
	}
	c.sectionsLock.RUnlock()

	// Slow path: acquire write lock and load section
	c.sectionsLock.Lock()
	defer c.sectionsLock.Unlock()

	// Double-check: another goroutine may have loaded it
	if c.Sections[sectionSlotIdx] != nil {
		if verbose {
			log.Printf("[DEBUG getOrLoadSection] Cache HIT (double-check) for section %d", sectionSlotIdx)
		}
		return c.Sections[sectionSlotIdx]
	}

	// Load section from RawData
	if verbose {
		log.Printf("[DEBUG getOrLoadSection] Cache MISS for section %d, loading from RawData", sectionSlotIdx)
	}
	section, err := c.loadSection(sectionSlotIdx)
	if err != nil {
		log.Printf("[ERROR getOrLoadSection] Failed to load section %d: %v", sectionSlotIdx, err)
		return nil
	}

	// Cache the loaded section
	c.Sections[sectionSlotIdx] = section
	if verbose {
		log.Printf("[DEBUG getOrLoadSection] Cached section %d (BitsPerEntry=%d, PaletteSize=%d)",
			sectionSlotIdx, section.BitsPerEntry, len(section.Palette))
	}
	return section
}

// loadSection parses a section from RawData and returns a cached Section struct
func (c *ChunkData) loadSection(sectionSlotIdx int) (*Section, error) {
	reader := bytes.NewReader(c.RawData)

	// Skip to target section
	for i := 0; i < sectionSlotIdx; i++ {
		if err := skipSection(reader); err != nil {
			return nil, fmt.Errorf("failed to skip section %d: %w", i, err)
		}
	}

	// Read BlockCount (Short)
	var blockCount pk.Short
	if _, err := blockCount.ReadFrom(reader); err != nil {
		return nil, fmt.Errorf("failed to read block count: %w", err)
	}

	// Parse BlockStates PaletteContainer into Section
	section, err := c.loadPaletteContainer(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to load palette container: %w", err)
	}
	section.BlockCount = int16(blockCount)

	// Skip Biomes PaletteContainer (not needed for block lookups)
	if err := skipPaletteContainer(reader, BIOME_SECTION_SIZE); err != nil {
		return nil, fmt.Errorf("failed to skip biomes: %w", err)
	}

	return section, nil
}

// loadPaletteContainer parses a PaletteContainer from the reader into a Section
func (c *ChunkData) loadPaletteContainer(r io.Reader) (*Section, error) {
	section := &Section{}

	// Read bits per entry
	var bitsPerEntry pk.UnsignedByte
	if _, err := bitsPerEntry.ReadFrom(r); err != nil {
		return nil, fmt.Errorf("failed to read bitsPerEntry: %w", err)
	}
	section.BitsPerEntry = uint8(bitsPerEntry)

	bits := int(bitsPerEntry)

	// Handle single-valued palette (bits == 0)
	if bits == 0 {
		var singleValue pk.VarInt
		if _, err := singleValue.ReadFrom(r); err != nil {
			return nil, fmt.Errorf("failed to read single value: %w", err)
		}
		section.SingleValue = uint32(singleValue)
		section.Palette = nil
		section.DataArray = nil
		return section, nil
	}

	// Read palette
	var paletteLength pk.VarInt
	if _, err := paletteLength.ReadFrom(r); err != nil {
		return nil, fmt.Errorf("failed to read palette length: %w", err)
	}

	section.Palette = make([]uint32, paletteLength)
	for i := 0; i < int(paletteLength); i++ {
		var paletteEntry pk.VarInt
		if _, err := paletteEntry.ReadFrom(r); err != nil {
			return nil, fmt.Errorf("failed to read palette entry %d: %w", i, err)
		}
		section.Palette[i] = uint32(paletteEntry)
	}

	// Read data array
	entriesPerLong := 64 / bits
	if entriesPerLong > 0 {
		dataLength := (BLOCK_SECTION_SIZE + entriesPerLong - 1) / entriesPerLong

		section.DataArray = make([]uint64, dataLength)
		for i := 0; i < dataLength; i++ {
			var dataEntry pk.Long
			if _, err := dataEntry.ReadFrom(r); err != nil {
				return nil, fmt.Errorf("failed to read data entry %d: %w", i, err)
			}
			section.DataArray[i] = uint64(dataEntry)
		}
	}
	return section, nil
}

// getBlockFromSection extracts a block state ID from a cached Section
func (c *ChunkData) getBlockFromSection(section *Section, x, y, z int) uint32 {
	// Handle single-valued section
	if section.BitsPerEntry == 0 {
		return section.SingleValue
	}

	// Calculate block index within section (y is section-relative 0-15)
	blockIdx := (y << 8) | (z << 4) | x

	// Extract value from packed data array
	bits := int(section.BitsPerEntry)
	valuesPerLong := 64 / bits
	if valuesPerLong == 0 {
		return 0 // Avoid division by zero
	}
	longIndex := blockIdx / valuesPerLong
	indexInLong := blockIdx % valuesPerLong

	if longIndex >= len(section.DataArray) {
		if verbose {
			log.Printf("[DEBUG getBlockFromSection] longIndex=%d >= len(DataArray)=%d, returning 0",
				longIndex, len(section.DataArray))
		}
		return 0 // Out of bounds
	}

	// Extract the palette index
	shift := indexInLong * bits
	mask := uint64((1 << bits) - 1)
	paletteIndex := (section.DataArray[longIndex] >> shift) & mask

	if int(paletteIndex) >= len(section.Palette) {
		if verbose {
			log.Printf("[DEBUG getBlockFromSection] paletteIndex=%d >= len(Palette)=%d, returning 0",
				paletteIndex, len(section.Palette))
		}
		return 0 // Invalid palette index
	}

	return section.Palette[paletteIndex]
}
