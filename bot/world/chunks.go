package world

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"math"
	"reflect"
	"sync"

	pk "github.com/Tnze/go-mc/net/packet"

	"github.com/reallyoldfogie/mc-bot-go/bot"
	"github.com/reallyoldfogie/mc-bot-go/bot/basic"
	"github.com/reallyoldfogie/mc-protocol-go/models"
)

// ChunkPos represents a chunk position (X, Z coordinates)
type ChunkPos struct {
	X, Z int32
}

// skipHeightmapsArray skips over the Heightmaps field in a MapChunk packet
// Heightmaps is Array[VarInt, MapChunkArrayType] where each MapChunkArrayType is:
//   - Type: VarInt (heightmap type enum)
//   - Data: Array[VarInt, Long]
func skipHeightmapsArray(r io.Reader) error {
	// Read array length (number of heightmap types)
	var arrayLen pk.VarInt
	if _, err := arrayLen.ReadFrom(r); err != nil {
		return fmt.Errorf("failed to read heightmaps array length: %w", err)
	}

	// Skip each heightmap
	for i := 0; i < int(arrayLen); i++ {
		// Skip Type (VarInt)
		var heightmapType pk.VarInt
		if _, err := heightmapType.ReadFrom(r); err != nil {
			return fmt.Errorf("failed to read heightmap type %d: %w", i, err)
		}

		// Skip Data array (Array[VarInt, Long])
		var dataLen pk.VarInt
		if _, err := dataLen.ReadFrom(r); err != nil {
			return fmt.Errorf("failed to read heightmap data length %d: %w", i, err)
		}
		for j := 0; j < int(dataLen); j++ {
			var dataEntry pk.Long
			if _, err := dataEntry.ReadFrom(r); err != nil {
				return fmt.Errorf("failed to skip heightmap data entry %d.%d: %w", i, j, err)
			}
		}
	}

	return nil
}

type World struct {
	c         bot.Client
	p         basic.Player
	events    EventsListener
	packetMgr models.PacketMgr

	mu      sync.RWMutex // Protects Columns map from concurrent access
	Columns map[ChunkPos]*ChunkData

	overridesMu    sync.RWMutex
	blockOverrides map[blockPos]uint32

	// Chunk batching (1.20.2+): track number of batches received for acknowledgement
	chunkBatchCount float32

	// World time tracking
	worldTimeMu sync.RWMutex
	worldAge    int64 // Server world age in ticks (-1 = not initialized)
	timeOfDay   int64 // Time of day in ticks (-1 = not initialized)
}

type blockPos struct {
	X, Y, Z int32
}

func NewWorld(c bot.Client, p basic.Player, events EventsListener, packetMgr models.PacketMgr) *World {
	w := &World{
		c:              c,
		p:              p,
		packetMgr:      packetMgr,
		events:         events,
		Columns:        make(map[ChunkPos]*ChunkData),
		blockOverrides: make(map[blockPos]uint32),
		worldAge:       -1, // Not initialized
		timeOfDay:      -1, // Not initialized
	}
	c.Events().AddListener(
		bot.PacketHandler{Priority: 64, ID: packetMgr.GetClientboundPacketID("ClientboundLogin"), F: w.onPlayerSpawn},
		bot.PacketHandler{Priority: 64, ID: packetMgr.GetClientboundPacketID("ClientboundRespawn"), F: w.onPlayerSpawn},
		bot.PacketHandler{Priority: 0, ID: packetMgr.GetClientboundPacketID("ClientboundLevelChunkWithLight"), F: w.handleLevelChunkWithLightPacket},
		bot.PacketHandler{Priority: 0, ID: packetMgr.GetClientboundPacketID("ClientboundForgetLevelChunk"), F: w.handleForgetLevelChunkPacket},
		bot.PacketHandler{Priority: 0, ID: packetMgr.GetClientboundPacketID("ClientboundChunkBatchFinished"), F: w.handleChunkBatchFinished},
	)
	blockChangeID := packetMgr.GetClientboundPacketID("ClientboundBlockChange")
	if blockChangeID >= 0 {
		c.Events().AddListener(bot.PacketHandler{Priority: 0, ID: blockChangeID, F: w.handleBlockChangePacket})
	}
	multiBlockChangeID := packetMgr.GetClientboundPacketID("ClientboundMultiBlockChange")
	if multiBlockChangeID >= 0 {
		c.Events().AddListener(bot.PacketHandler{Priority: 0, ID: multiBlockChangeID, F: w.handleMultiBlockChangePacket})
	}
	return w
}

func (w *World) onPlayerSpawn(pk.Packet) error {
	// unload all chunks
	w.mu.Lock()
	w.Columns = make(map[ChunkPos]*ChunkData)
	w.mu.Unlock()
	w.clearAllOverrides()
	// Reset chunk batch counter for new world/respawn
	w.chunkBatchCount = 0
	return nil
}

func (w *World) handleLevelChunkWithLightPacket(packet pk.Packet) error {
	// TODO: Get dimension type from w.c.RegistryData instead of tnze registry
	// For now, use default dimension type
	_ = w.p.DimensionType          // Use variable to avoid unused warning
	var currentDimType interface{} // Placeholder

	// Use mc-protocol-go's MapChunk packet for proper version-aware parsing
	mapChunkPkt, err := w.packetMgr.GetClientboundPacketByID(models.ClientboundPacketID(packet.ID))
	if err != nil {
		log.Printf("[World] Error getting MapChunk packet instance: %v", err)
		return err
	}

	// Set NBT version for version-aware parsing (1.20.5+ removed name field from NBT)
	models.SetCurrentNBTVersion(w.packetMgr.Name())

	// Parse packet using mc-protocol-go's version-aware parsing
	var x, z pk.Int
	var chunkDataBytes pk.ByteArray

	// Try full packet scan first
	if err := mapChunkPkt.Scan(packet); err != nil {
		// If Scan fails (likely on BlockEntities NBT), fall back to manual parsing
		log.Printf("[World] Warning: MapChunk Scan failed, using fallback parsing: %v", err)

		// Heightmaps is Array[VarInt, MapChunkArrayType], not NBT
		// We need to skip it to get to ChunkData since we don't use heightmaps
		reader := bytes.NewReader(packet.Data)

		// Parse X and Z
		if _, err := x.ReadFrom(reader); err != nil {
			log.Printf("[World] Error reading X in fallback: %v", err)
			return err
		}
		if _, err := z.ReadFrom(reader); err != nil {
			log.Printf("[World] Error reading Z in fallback: %v", err)
			return err
		}

		// Skip Heightmaps array (Array[VarInt, MapChunkArrayType])
		if err := skipHeightmapsArray(reader); err != nil {
			log.Printf("[World] Error skipping heightmaps in fallback: %v", err)
			return err
		}

		// Read ChunkData
		if _, err := chunkDataBytes.ReadFrom(reader); err != nil {
			log.Printf("[World] Error reading ChunkData in fallback: %v", err)
			return err
		}

		// Note: We stop here and don't parse BlockEntities or lighting data
		// since the primary Scan() already failed on those fields
	} else {
		// Extract fields from successfully parsed packet
		var ok bool
		x, ok = models.GetPacketFieldAs[pk.Int](mapChunkPkt, "X")
		if !ok {
			return fmt.Errorf("failed to extract X from MapChunk")
		}
		z, ok = models.GetPacketFieldAs[pk.Int](mapChunkPkt, "Z")
		if !ok {
			return fmt.Errorf("failed to extract Z from MapChunk")
		}
		chunkDataBytes, ok = models.GetPacketFieldAs[pk.ByteArray](mapChunkPkt, "ChunkData")
		if !ok {
			return fmt.Errorf("failed to extract ChunkData from MapChunk")
		}
	}

	// Create version-agnostic chunk data storage
	pos := ChunkPos{X: int32(x), Z: int32(z)}
	// TODO: Get dimension height from registry data instead of using hardcoded value
	_ = currentDimType        // Suppress unused warning
	const defaultHeight = 384 // Default for overworld in 1.18+
	sectionCount := defaultHeight / 16
	chunk := &ChunkData{
		X:            int32(x),
		Z:            int32(z),
		SectionCount: sectionCount,
		RawData:      chunkDataBytes,
		Sections:     make([]*Section, sectionCount), // Initialize section cache (lazy populated)
	}

	// Store chunk (with mutex protection to prevent concurrent map access)
	w.mu.Lock()
	w.Columns[pos] = chunk
	chunksLoaded := len(w.Columns) // Cache value while holding lock
	w.mu.Unlock()
	w.clearOverridesForChunk(pos.X, pos.Z)

	// Log progress periodically (every 50 chunks) instead of every chunk
	if chunksLoaded%50 == 0 || chunksLoaded <= 10 {
		log.Printf("[World] Loaded %d chunks (latest: %d, %d)", chunksLoaded, pos.X, pos.Z)
	}

	// Trigger load chunk event
	if w.events.LoadChunk != nil {
		if err := w.events.LoadChunk(pos); err != nil {
			return err
		}
	}
	return nil
}

func (w *World) handleForgetLevelChunkPacket(packet pk.Packet) error {
	var x, z pk.Int
	if err := packet.Scan(&x, &z); err != nil {
		return err
	}

	pos := ChunkPos{X: int32(x), Z: int32(z)}

	var err error
	if w.events.UnloadChunk != nil {
		err = w.events.UnloadChunk(pos)
	}
	w.mu.Lock()
	delete(w.Columns, pos)
	w.mu.Unlock()
	w.clearOverridesForChunk(pos.X, pos.Z)
	return err
}

// handleChunkBatchFinished handles the ClientboundChunkBatchFinished packet.
// This packet is sent after a batch of chunks, and the client must acknowledge
// receipt to allow the server to send more chunks (1.20.2+).
func (w *World) handleChunkBatchFinished(packet pk.Packet) error {
	// Increment batch counter
	w.chunkBatchCount++

	// Send ServerboundChunkBatchReceived acknowledgement immediately
	// Packet structure: Float (batch count)
	ackPacket := pk.Marshal(
		int32(w.packetMgr.GetServerboundPacketID("ServerboundChunkBatchReceived")),
		pk.Float(w.chunkBatchCount),
	)

	if err := w.c.Conn().WritePacket(ackPacket); err != nil {
		log.Printf("[World] Error sending chunk batch acknowledgement: %v", err)
		return err
	}

	// Log progress periodically (every 10 batches) instead of every batch
	if int(w.chunkBatchCount)%10 == 0 || int(w.chunkBatchCount) <= 3 {
		log.Printf("[World] Acknowledged %d chunk batches (total chunks: %d)", int(w.chunkBatchCount), len(w.Columns))
	}

	return nil
}

// GetBlockAt returns the block state ID at the given world coordinates.
// The second return value indicates whether the chunk is loaded:
//   - true: chunk is loaded (stateID is valid, may be 0 for air)
//   - false: chunk is not loaded (stateID should be ignored)
func (w *World) GetBlockAt(x, y, z float64) (uint32, bool) {
	bx, by, bz := toBlockCoords(x, y, z)
	if stateID, ok := w.getBlockOverride(bx, by, bz); ok {
		return stateID, true // Override blocks are always "loaded"
	}
	// Calculate chunk position
	chunkX := int(bx) >> 4 // x / 16
	chunkZ := int(bz) >> 4 // z / 16
	pos := ChunkPos{X: int32(chunkX), Z: int32(chunkZ)}

	if verbose {
		log.Printf("[DEBUG world.GetBlockAt] x=%.02f y=%.02f z=%.02f (chunkX=%d,chunkZ=%d)\n", x, y, z, chunkX, chunkZ)
	}
	// Get chunk (with read lock to prevent concurrent modification)
	w.mu.RLock()
	chunk, exists := w.Columns[pos]
	w.mu.RUnlock()

	if !exists || chunk == nil {
		log.Printf("[ERROR world.GetBlockAt] chunk=%v || exists=%t x=%.02f y=%.02f z=%.02f (chunkX=%d,chunkZ=%d)\n", chunk, exists, x, y, z, chunkX, chunkZ)
		return 0, false // Chunk not loaded, return false
	}

	// Calculate chunk-relative coordinates using floored block coords to handle negatives.
	relX := int(bx) & 15 // x % 16
	relZ := int(bz) & 15 // z % 16
	if verbose {
		log.Printf("[DEBUG world.GetBlockAt] x=%f y=%f z=%f (chunkX=%d, chunkZ=%d) (relX=%d, relZ=%d)\n", x, y, z, chunkX, chunkZ, relX, relZ)
	}
	// Use ChunkData's GetBlockAt method (handles version-agnostic parsing)
	return chunk.GetBlockAt(relX, int(y), relZ), true // Chunk loaded, return true
}

func (w *World) handleBlockChangePacket(packet pk.Packet) error {
	blockPkt, err := w.packetMgr.GetClientboundPacketByID(models.ClientboundPacketID(packet.ID))
	if err != nil {
		return err
	}
	if err := blockPkt.Scan(packet); err != nil {
		return err
	}
	posVal, ok := models.GetPacketFieldValue(blockPkt, "Location")
	if !ok {
		posVal, ok = models.GetPacketFieldValue(blockPkt, "Position")
	}
	x, y, z, ok := decodeXYZ(posVal)
	if !ok {
		return nil
	}
	stateID, ok := models.GetPacketFieldAs[int32](blockPkt, "Type")
	if !ok {
		stateID, ok = models.GetPacketFieldAs[int32](blockPkt, "BlockState")
	}
	if !ok {
		return nil
	}
	log.Printf("[World] Block change at (%d, %d, %d) to state ID %d\n", x, y, z, stateID)
	w.setBlockOverride(x, y, z, uint32(stateID))
	return nil
}

func (w *World) handleMultiBlockChangePacket(packet pk.Packet) error {
	multiPkt, err := w.packetMgr.GetClientboundPacketByID(models.ClientboundPacketID(packet.ID))
	if err != nil {
		return err
	}
	if err := multiPkt.Scan(packet); err != nil {
		return err
	}
	chunkVal, ok := models.GetPacketFieldValue(multiPkt, "ChunkCoordinates")
	if !ok {
		return nil
	}
	chunkX, sectionY, chunkZ, ok := decodeXYZ(chunkVal)
	if !ok {
		return nil
	}
	recordsVal, ok := models.GetPacketFieldValue(multiPkt, "Records")
	if !ok {
		return nil
	}
	records, ok := recordsVal.(models.Array[pk.VarInt, pk.VarInt])
	if !ok {
		return nil
	}
	recordList := records.Get()
	if recordList == nil {
		return nil
	}
	baseX := chunkX * 16
	baseY := sectionY * 16
	baseZ := chunkZ * 16
	for i, rec := range recordList {
		stateID, offX, offY, offZ := decodeMultiBlockRecord(int32(rec))
		log.Printf("[World] Multi block change %d at (%d, %d, %d) to state ID %d\n", i, baseX+offX, baseY+offY, baseZ+offZ, stateID)
		w.setBlockOverride(baseX+offX, baseY+offY, baseZ+offZ, stateID)
	}
	return nil
}

func (w *World) getBlockOverride(x, y, z int32) (uint32, bool) {
	w.overridesMu.RLock()
	state, ok := w.blockOverrides[blockPos{X: x, Y: y, Z: z}]
	w.overridesMu.RUnlock()
	return state, ok
}

func (w *World) setBlockOverride(x, y, z int32, state uint32) {
	w.overridesMu.Lock()
	w.blockOverrides[blockPos{X: x, Y: y, Z: z}] = state
	w.overridesMu.Unlock()
}

func (w *World) clearOverridesForChunk(chunkX, chunkZ int32) {
	w.overridesMu.Lock()
	for pos := range w.blockOverrides {
		if pos.X>>4 == chunkX && pos.Z>>4 == chunkZ {
			delete(w.blockOverrides, pos)
		}
	}
	w.overridesMu.Unlock()
}

func (w *World) clearAllOverrides() {
	w.overridesMu.Lock()
	w.blockOverrides = make(map[blockPos]uint32)
	w.overridesMu.Unlock()
}

func decodeXYZ(val any) (int32, int32, int32, bool) {
	rv := reflect.ValueOf(val)
	if !rv.IsValid() {
		return 0, 0, 0, false
	}
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return 0, 0, 0, false
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return 0, 0, 0, false
	}
	fx := rv.FieldByName("X")
	fy := rv.FieldByName("Y")
	fz := rv.FieldByName("Z")
	if !fx.IsValid() || !fy.IsValid() || !fz.IsValid() {
		return 0, 0, 0, false
	}
	x, ok := intFieldToInt32(fx)
	if !ok {
		return 0, 0, 0, false
	}
	y, ok := intFieldToInt32(fy)
	if !ok {
		return 0, 0, 0, false
	}
	z, ok := intFieldToInt32(fz)
	if !ok {
		return 0, 0, 0, false
	}
	return x, y, z, true
}

func intFieldToInt32(v reflect.Value) (int32, bool) {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int32(v.Int()), true
	default:
		return 0, false
	}
}

func decodeMultiBlockRecord(record int32) (uint32, int32, int32, int32) {
	raw := uint32(record)
	pos := raw & 0xFFF
	stateID := raw >> 12
	offX := int32((pos >> 8) & 0xF)
	offZ := int32((pos >> 4) & 0xF)
	offY := int32(pos & 0xF)
	return stateID, offX, offY, offZ
}

func toBlockCoords(x, y, z float64) (int32, int32, int32) {
	return int32(math.Floor(x)), int32(math.Floor(y)), int32(math.Floor(z))
}

// GetWorldAge returns the current server world age in ticks and whether it's been initialized.
// Returns (0, false) if no Update Time packet has been received yet.
// Returns (worldAge, true) when the value is valid from the server.
func (w *World) GetWorldAge() (int64, bool) {
	w.worldTimeMu.RLock()
	defer w.worldTimeMu.RUnlock()
	if w.worldAge < 0 {
		return 0, false
	}
	return w.worldAge, true
}

// GetTimeOfDay returns the current time of day in ticks and whether it's been initialized.
// Time of day ranges from 0-23999 ticks per day.
// Returns (0, false) if no Update Time packet has been received yet.
// Returns (timeOfDay, true) when the value is valid from the server.
func (w *World) GetTimeOfDay() (int64, bool) {
	w.worldTimeMu.RLock()
	defer w.worldTimeMu.RUnlock()
	if w.timeOfDay < 0 {
		return 0, false
	}
	return w.timeOfDay, true
}

// SetWorldTime updates both world age and time of day from the server's Update Time packet.
// This should be called whenever a ClientboundUpdateTime packet is received.
func (w *World) SetWorldTime(worldAge, timeOfDay int64) {
	w.worldTimeMu.Lock()
	defer w.worldTimeMu.Unlock()
	w.worldAge = worldAge
	w.timeOfDay = timeOfDay
}
