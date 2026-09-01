package screen

import (
	"testing"

	pk "github.com/Tnze/go-mc/net/packet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOnOpenHorseScreen(t *testing.T) {
	// slotCount here is the wire value from ClientboundOpenHorseWindow, which
	// is the number of chest columns (3 slots each) — NOT the total container
	// slot count. Every horse-family window additionally reserves a fixed
	// 2-slot base (indices 0-1) regardless of species; see the comment on
	// horseContainerBaseSlots in screen.go for how this was verified against
	// real captured packets (camel, donkey, llama).
	tests := []struct {
		name              string
		windowID          byte
		slotCount         int32
		entityID          int32
		expectedTotal     int
		expectedContainer int
		expectedPlayer    int
	}{
		{
			name:              "Regular horse - unchested, 0 chest columns",
			windowID:          1,
			slotCount:         0,
			entityID:          100,
			expectedTotal:     38, // 2 (saddle+armor) + 36 (player)
			expectedContainer: 2,
			expectedPlayer:    2,
		},
		{
			name:              "Donkey with full chest - 5 chest columns",
			windowID:          2,
			slotCount:         5,
			entityID:          200,
			expectedTotal:     53, // 2 (saddle+invisible armor) + 5*3 (chest) + 36 (player)
			expectedContainer: 17,
			expectedPlayer:    17,
		},
		{
			name:              "Llama with full chest (strength 5) - 5 chest columns",
			windowID:          3,
			slotCount:         5,
			entityID:          300,
			expectedTotal:     53, // 2 (decoration+reserved) + 5*3 (chest) + 36 (player)
			expectedContainer: 17,
			expectedPlayer:    17,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create minimal manager for testing
			mgr := &manager{
				screens: make(map[int]Container),
				events:  nil,
			}

			// Create packet
			packet := pk.Marshal(
				0x28, // Packet ID for ClientboundOpenHorseScreen
				pk.Byte(tt.windowID),
				pk.VarInt(tt.slotCount),
				pk.Int(tt.entityID),
			)

			// Call handler
			err := mgr.onOpenHorseScreen(packet)
			require.NoError(t, err)

			// Verify container was created
			container, ok := mgr.screens[int(tt.windowID)]
			require.True(t, ok, "horse container should exist")

			// Verify it's a HorseContainer
			horseContainer, ok := container.(*HorseContainer)
			require.True(t, ok, "container should be a HorseContainer")

			// Verify fields
			assert.Equal(t, tt.entityID, horseContainer.EntityID, "entity ID should match")
			assert.Equal(t, tt.expectedTotal, len(horseContainer.Slots), "total slots should match")
			assert.Equal(t, tt.expectedContainer, horseContainer.ContainerSlots, "container slots should match")
			assert.Equal(t, tt.expectedPlayer, horseContainer.PlayerSlotStart, "player slot start should match")
		})
	}
}

func TestOnOpenHorseScreen_DuplicateWindowID(t *testing.T) {
	mgr := &manager{
		screens: make(map[int]Container),
		events:  nil,
	}

	// Create first horse screen
	packet1 := pk.Marshal(
		0x28,
		pk.Byte(1),
		pk.VarInt(2),
		pk.Int(100),
	)
	err := mgr.onOpenHorseScreen(packet1)
	require.NoError(t, err)

	// Try to create second horse screen with same window ID
	packet2 := pk.Marshal(
		0x28,
		pk.Byte(1), // Same window ID
		pk.VarInt(17),
		pk.Int(200),
	)
	err = mgr.onOpenHorseScreen(packet2)
	require.Error(t, err, "should error on duplicate window ID")
	assert.Contains(t, err.Error(), "already exists", "error should mention duplicate")
}

func TestHorseContainer_SetSlot(t *testing.T) {
	horse := &HorseContainer{
		EntityID:        100,
		Slots:           make([]Slot, 38), // 2 + 36
		ContainerSlots:  2,
		PlayerSlotStart: 2,
	}

	// Test setting a valid slot
	slot := Slot{ID: 1, Count: 1}
	err := horse.OnSetSlot(5, slot)
	require.NoError(t, err)
	assert.Equal(t, slot, horse.Slots[5])

	// Test setting out of bounds slot
	err = horse.OnSetSlot(100, slot)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "out of bounds")
}

func TestHorseContainer_SlotAccess(t *testing.T) {
	horse := &HorseContainer{
		EntityID:        100,
		Slots:           make([]Slot, 38), // 2 + 36
		ContainerSlots:  2,
		PlayerSlotStart: 2,
	}

	// Fill slots with test data
	for i := range horse.Slots {
		horse.Slots[i] = Slot{ID: pk.VarInt(i), Count: 1}
	}

	// Test Container() - should return first 2 slots
	containerSlots := horse.Container()
	require.Len(t, containerSlots, 2)
	assert.Equal(t, pk.VarInt(0), containerSlots[0].ID)
	assert.Equal(t, pk.VarInt(1), containerSlots[1].ID)

	// Test Main() - should return 27 slots starting at index 2
	mainSlots := horse.Main()
	require.Len(t, mainSlots, 27)
	assert.Equal(t, pk.VarInt(2), mainSlots[0].ID)
	assert.Equal(t, pk.VarInt(28), mainSlots[26].ID)

	// Test Hotbar() - should return 9 slots starting at index 29
	hotbarSlots := horse.Hotbar()
	require.Len(t, hotbarSlots, 9)
	assert.Equal(t, pk.VarInt(29), hotbarSlots[0].ID)
	assert.Equal(t, pk.VarInt(37), hotbarSlots[8].ID)
}

func TestHorseContainer_Close(t *testing.T) {
	horse := &HorseContainer{
		EntityID:        100,
		Slots:           make([]Slot, 38),
		ContainerSlots:  2,
		PlayerSlotStart: 2,
	}

	err := horse.OnClose()
	require.NoError(t, err, "onClose should not error")
}
