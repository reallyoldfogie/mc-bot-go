package bot

import (
	"log"
	"sort"
	"strconv"

	pk "github.com/Tnze/go-mc/net/packet"

	"github.com/reallyoldfogie/mc-protocol-go/models"
)

type Events interface {
	AddListener(listeners ...PacketHandler)
	AddGeneric(listeners ...PacketHandler)

	GetGenericListeners() []PacketHandler
	GetListeners() [][]PacketHandler
}
type events struct {
	generic  []PacketHandler   // for every packet
	handlers [][]PacketHandler // for specific packet id only
}

func NewEvents(numClientboundPackets int32, cloneSrc Events) Events {
	newEvents := &events{handlers: make([][]PacketHandler, numClientboundPackets)}
	if cloneSrc != nil {
		for _, handler := range cloneSrc.GetGenericListeners() {
			newEvents.AddGeneric(handler)
		}

		for _, handlers := range cloneSrc.GetListeners() {
			newEvents.AddListener(handlers...)
		}
	}
	return newEvents
}

func (e *events) GetGenericListeners() []PacketHandler {
	return e.generic
}

func (e *events) GetListeners() [][]PacketHandler {
	return e.handlers
}

func (e *events) AddListener(listeners ...PacketHandler) {
	for _, l := range listeners {
		// panic if l.ID is invalid
		if l.ID < 0 || int(l.ID) >= len(e.handlers) {
			log.Print("[ERROR] Invalid packet ID (" + strconv.Itoa(int(l.ID)) + ")[" + l.Name + "], Not adding listener")
			continue
		}

		if s := e.handlers[l.ID]; s == nil {
			e.handlers[l.ID] = []PacketHandler{l}
		} else {
			e.handlers[l.ID] = append(s, l)
			sortPacketHandlers(e.handlers[l.ID])
		}
	}
}

// AddGeneric adds listeners like AddListener, but the packet ID is ignored.
// Generic listener is always called before specific packet listener.
func (e *events) AddGeneric(listeners ...PacketHandler) {
	e.generic = append(e.generic, listeners...)
	sortPacketHandlers(e.generic)
}

type (
	PacketHandlerFunc func(p pk.Packet) error
	PacketHandler     struct {
		ID       models.ClientboundPacketID
		Name     string
		Priority int
		F        PacketHandlerFunc
	}
)

func sortPacketHandlers(slice []PacketHandler) {
	sort.SliceStable(slice, func(i, j int) bool {
		return slice[i].Priority > slice[j].Priority
	})
}
