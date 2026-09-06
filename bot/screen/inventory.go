package screen

import (
	"errors"
	"sync"
)

type Inventory interface {
	CraftingOutput() *Slot
	CraftingInput() []Slot

	// Armor returns to the armor section of the inventory.
	// The length is 4, which are head, chest, legs and feet.
	Armor() []Slot
	Main() []Slot
	Hotbar() []Slot
	Offhand() *Slot

	OnSetSlot(i int, s Slot) error
	GetSlots() []Slot
	OnClose() error
}

const (
	CraftingSlotStart = int16(1)
	CraftingSlotEnd   = CraftingSlotStart + 4

	ArmorSlotStart = int16(5)
	ArmorSlotEnd   = ArmorSlotStart + 4

	MainSlotStart = int16(9)
	MainSlotEnd   = MainSlotStart + 3*9 // 3 rows of 9 slots

	HotbarSlotStart = int16(36)
	HotbarSlotEnd   = HotbarSlotStart + 9

	OffHandSlot = int16(45)
)

func NewInventory() Inventory {
	return &inventory{}
}

// inventory tracks the player's own window-0 slot state. Slots is written
// from the network-receive goroutine (OnSetSlot, on every
// ClientboundContainerSetSlot/ContainerSetContent packet) and read from
// arbitrary caller goroutines (item search, crafting, hotbar selection,
// ...) concurrently. mu protects every access, and every read method below
// returns a defensive copy rather than a slice/pointer into the live
// array, so a caller holding the returned value can never race a later
// write - a caller-side lock can't help here, since (as with
// manager.GetPlayerInventory's own RLock) it would already be released by
// the time the caller inspects the returned data.
//
// A shallow copy of each Slot is sufficient even though Slot embeds slices
// (Components, RemoveComponents): OnSetSlot always replaces inv.Slots[i]
// wholesale with a new Slot value rather than mutating an existing one's
// slice fields in place, so a previously-copied Slot's slices are never
// retroactively changed underneath a reader.
//
// Found live via `go test -race`: an unprotected read in mc-agent's
// crafting code (agent/craft.go) raced this type's OnSetSlot write,
// consistent with a torn read of a slot's Count field making a present
// ingredient look absent (see mc-agent's
// docs/plans/CRAFTING_TABLE_3X3_PLAN.md-adjacent investigation).
type inventory struct {
	mu    sync.RWMutex
	Slots [46]Slot
}

func (inv *inventory) OnClose() error { return nil }

func (inv *inventory) OnSetSlot(i int, s Slot) error {
	inv.mu.Lock()
	defer inv.mu.Unlock()
	if i < 0 || i >= len(inv.Slots) {
		return errors.New("slot index out of bounds")
	}
	inv.Slots[i] = s
	return nil
}

func (inv *inventory) CraftingOutput() *Slot {
	inv.mu.RLock()
	defer inv.mu.RUnlock()
	s := inv.Slots[0]
	return &s
}

func (inv *inventory) CraftingInput() []Slot {
	return inv.copyRange(CraftingSlotStart, CraftingSlotEnd)
}

// Armor returns to the armor section of the inventory.
// The length is 4, which are head, chest, legs and feet.
func (inv *inventory) Armor() []Slot  { return inv.copyRange(ArmorSlotStart, ArmorSlotEnd) }
func (inv *inventory) Main() []Slot   { return inv.copyRange(MainSlotStart, MainSlotEnd) }
func (inv *inventory) Hotbar() []Slot { return inv.copyRange(HotbarSlotStart, HotbarSlotEnd) }

func (inv *inventory) Offhand() *Slot {
	inv.mu.RLock()
	defer inv.mu.RUnlock()
	s := inv.Slots[OffHandSlot]
	return &s
}

func (inv *inventory) GetSlots() []Slot {
	inv.mu.RLock()
	defer inv.mu.RUnlock()
	slots := make([]Slot, len(inv.Slots))
	copy(slots, inv.Slots[:])
	return slots
}

// copyRange returns a defensive copy of Slots[start:end], safe to read
// without racing concurrent OnSetSlot writes - see inventory's doc comment.
func (inv *inventory) copyRange(start, end int16) []Slot {
	inv.mu.RLock()
	defer inv.mu.RUnlock()
	slots := make([]Slot, end-start)
	copy(slots, inv.Slots[start:end])
	return slots
}
