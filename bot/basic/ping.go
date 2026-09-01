package basic

import (
	pk "github.com/Tnze/go-mc/net/packet"
)

func (p *player) handlePingPacket(packet pk.Packet) error {
	var pingID pk.Int
	if err := packet.Scan(&pingID); err != nil {
		return Error{err}
	}

	// Response
	err := p.client.Conn().WritePacket(pk.Packet{
		// ID:   int32(packetid.ServerboundPong),
		ID:   int32(p.packetMgr.GetServerboundPacketID("ServerboundPong")),
		Data: packet.Data,
	})
	if err != nil {
		return Error{err}
	}
	return nil
}
