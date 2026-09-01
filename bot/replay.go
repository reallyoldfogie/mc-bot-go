package bot

import (
	"fmt"
	"log"
	"os"

	pk "github.com/Tnze/go-mc/net/packet"
)

// PacketRecorder captures raw packets for replay recording.
// It mirrors the minimal API of recorder.Recorder without importing it here.
type PacketRecorder interface {
	RecordNow(id int32, payload []byte) error
	SetSelfID(id int)
}

// MovementMirror is notified of serverbound packets to emit synthetic packets
// (e.g., for replay visibility of the local player).
type MovementMirror interface {
	HandleServerbound(pk.Packet)
	SetEntityMeta(entityID int32, name string, uuid [16]byte)
	SetEntityType(entityType int32)
}

func recordForReplay(rec PacketRecorder, p pk.Packet) {
	if rec == nil {
		return
	}
	payload := make([]byte, len(p.Data))
	copy(payload, p.Data)

	// Debug: trace where recordForReplay is called from
	if os.Getenv("MCPR_DEBUG") != "" {
		fmt.Printf("[REPLAY] recordForReplay called: ID=%d payloadLen=%d\n", p.ID, len(payload))
	}

	if err := rec.RecordNow(int32(p.ID), payload); err != nil {
		log.Printf("[replay] failed to record packet %d: %v", p.ID, err)
	}

	if os.Getenv("MCPR_DEBUG") != "" {
		fmt.Printf("[REPLAY] recordForReplay returned from RecordNow\n")
	}
}
