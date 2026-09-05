package screen

import (
	"io"

	"github.com/reallyoldfogie/mc-bot-go/bot"
)

// SlotCodec lets a caller supply real per-version Slot wire-format handling,
// in place of this package's built-in pre/post-1.21.5 heuristic
// (hashedSlotProtocolThreshold, SetCurrentSlotComponentEncoding). A
// bot.VersionHandler set via Client.SetVersionHandler that also implements
// SlotCodec is picked up automatically by NewManager (see manager.slotCodec)
// -- this is the same optional-interface pattern already used elsewhere for
// VersionHandler (e.g. bot/version_handler.go's split between the base
// VersionHandler and its Login/Configuration sub-interfaces).
//
// This exists because the built-in heuristic only distinguishes two
// buckets (pre-1.21.5 / 1.21.5+), using data/1.21.1 and data/1.21.5 as
// representative versions for each -- see the "Known limitation" note for
// Phase 2 in docs/plans/version-awareness-gaps.md. A caller that already
// maintains real per-version protocol code (e.g. mc-agent's
// handler_versions, which does this today for login/configuration) can
// implement SlotCodec to get exact behavior for every version it supports,
// instead of the two-bucket approximation.
//
// The one existing consumer, mc-agent, already has the shape this expects:
// mc-agent/agent/version_handler_adapter.go bridges its own
// models.VersionHandler into bot.VersionHandler the same way; adding
// SlotCodec methods to that adapter (or a sibling type) is the natural way
// to supply one.
type SlotCodec interface {
	// DecodeSlot reads one full (non-hashed) Slot in this version's real
	// wire format from r -- the format used by
	// ClientboundContainerSetContent, ClientboundContainerSetSlot, and
	// ClientboundSetPlayerInventory in every version.
	DecodeSlot(r io.Reader) (Slot, int64, error)

	// SendContainerClick builds and writes a complete
	// ServerboundContainerClick packet in this version's real wire format.
	// This covers more than just the item slot encoding: the window ID
	// field's own wire type differs too (a plain byte pre-1.21.5, a VarInt
	// from 1.21.5 on -- verified directly against
	// mc-protocol-go/data/1.21.1/basetypes.ContainerID vs
	// data/1.21.5/basetypes.ContainerID), so the whole packet, not just
	// each item, needs to come from real version-specific code rather than
	// a generic byte-level composition in this package.
	SendContainerClick(conn bot.PacketWriter, windowID int, stateID int32, slot int16, button byte, mode int32, changedSlots ChangedSlots, cursor *Slot) error
}

// decodeSlot reads one Slot from r, using m.slotCodec when NewManager found
// one, or this package's built-in Slot.ReadFrom otherwise. Callers that
// currently use p.Scan(..., &someSlot, ...) for a packet containing one or
// more Slots should instead read their non-Slot fields from
// bytes.NewReader(p.Data) directly (p.Scan does exactly this internally --
// see go-mc's Packet.Scan) and call this for each Slot, so the codec (if
// any) actually gets used.
func (m *manager) decodeSlot(r io.Reader) (Slot, error) {
	if m.slotCodec != nil {
		s, _, err := m.slotCodec.DecodeSlot(r)
		return s, err
	}
	var s Slot
	_, err := s.ReadFrom(r)
	return s, err
}
