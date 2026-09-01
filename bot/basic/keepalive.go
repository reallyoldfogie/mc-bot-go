package basic

import (
	"time"

	pk "github.com/Tnze/go-mc/net/packet"
)

const keepAliveDuration = time.Second * 20

func (p *player) resetKeepAliveDeadline() {
	newDeadline := time.Now().Add(keepAliveDuration)
	p.client.Conn().Socket.SetReadDeadline(newDeadline)
}

func (p *player) handleKeepAlivePacket(packet pk.Packet) error {
	var KeepAliveID pk.Long
	if err := packet.Scan(&KeepAliveID); err != nil {
		return Error{err}
	}

	p.resetKeepAliveDeadline()

	// Response
	err := p.client.Conn().WritePacket(pk.Packet{
		ID:   int32(p.packetMgr.GetServerboundPacketID("ServerboundKeepAlive")),
		Data: packet.Data,
	})
	if err != nil {
		return Error{err}
	}
	return nil
}
