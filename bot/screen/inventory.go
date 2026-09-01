package screen

import "errors"

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

type inventory struct {
	Slots [46]Slot
}

func (inv *inventory) OnClose() error { return nil }

func (inv *inventory) OnSetSlot(i int, s Slot) error {
	if i < 0 || i >= len(inv.Slots) {
		return errors.New("slot index out of bounds")
	}
	inv.Slots[i] = s
	return nil
}

func (inv *inventory) CraftingOutput() *Slot { return &inv.Slots[0] }
func (inv *inventory) CraftingInput() []Slot { return inv.Slots[CraftingSlotStart:CraftingSlotEnd] }

// Armor returns to the armor section of the inventory.
// The length is 4, which are head, chest, legs and feet.
func (inv *inventory) Armor() []Slot  { return inv.Slots[ArmorSlotStart:ArmorSlotEnd] }
func (inv *inventory) Main() []Slot   { return inv.Slots[MainSlotStart:MainSlotEnd] }
func (inv *inventory) Hotbar() []Slot { return inv.Slots[HotbarSlotStart:HotbarSlotEnd] }
func (inv *inventory) Offhand() *Slot { return &inv.Slots[OffHandSlot] }

func (inv *inventory) GetSlots() []Slot { return inv.Slots[:] }
