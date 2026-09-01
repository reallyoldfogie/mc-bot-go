package screen

import (
	"errors"
)

// HorseContainer represents a horse/donkey/llama/mule inventory
// Opened via ClientboundOpenHorseScreen packet (0x28)
type HorseContainer struct {
	EntityID        int32 // The entity ID of the horse/donkey/llama
	Slots           []Slot
	ContainerSlots  int // Number of slots in the horse container itself (excluding player inventory)
	PlayerSlotStart int // Index where player inventory slots start
}

func (h *HorseContainer) OnSetSlot(i int, slot Slot) error {
	if i < 0 || i >= len(h.Slots) {
		return errors.New("slot index out of bounds")
	}
	h.Slots[i] = slot
	return nil
}

func (h *HorseContainer) OnClose() error {
	return nil
}

// Container returns the horse-specific slots (saddle, armor, chest contents)
func (h *HorseContainer) Container() []Slot {
	if h.ContainerSlots == 0 {
		return []Slot{}
	}
	return h.Slots[0:h.ContainerSlots]
}

// Main returns the player's main inventory slots (27 slots)
func (h *HorseContainer) Main() []Slot {
	if h.PlayerSlotStart+27 > len(h.Slots) {
		return []Slot{}
	}
	return h.Slots[h.PlayerSlotStart : h.PlayerSlotStart+27]
}

// Hotbar returns the player's hotbar slots (9 slots)
func (h *HorseContainer) Hotbar() []Slot {
	if h.PlayerSlotStart+36 > len(h.Slots) {
		return []Slot{}
	}
	return h.Slots[h.PlayerSlotStart+27 : h.PlayerSlotStart+36]
}
