package basic

import (
	pk "github.com/Tnze/go-mc/net/packet"
)

func (p *player) handleCookieRequestPacket(packet pk.Packet) error {
	var key pk.Identifier
	err := packet.Scan(&key)
	if err != nil {
		return Error{err}
	}
	cookieContent := p.client.Cookies()[string(key)]
	err = p.client.Conn().WritePacket(pk.Marshal(
		// packetid.ServerboundCookieResponse,
		p.packetMgr.GetServerboundPacketID("ServerboundCookieResponse"),
		key, pk.OptionEncoder[pk.ByteArray]{
			Has: cookieContent != nil,
			Val: pk.ByteArray(cookieContent),
		},
	))
	if err != nil {
		return Error{err}
	}
	return nil
}

func (p *player) handleStoreCookiePacket(packet pk.Packet) error {
	var key pk.Identifier
	var payload pk.ByteArray
	err := packet.Scan(&key, &payload)
	if err != nil {
		return Error{err}
	}
	cookies := p.client.Cookies()
	cookies[string(key)] = []byte(payload)
	p.client.SetCookies(cookies)
	return nil
}
