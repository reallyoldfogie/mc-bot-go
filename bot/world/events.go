package world

type EventsListener struct {
	LoadChunk   func(pos ChunkPos) error
	UnloadChunk func(pos ChunkPos) error
}
