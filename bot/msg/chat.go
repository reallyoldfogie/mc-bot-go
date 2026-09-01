package msg

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/Tnze/go-mc/chat"
	"github.com/Tnze/go-mc/chat/sign"
	pk "github.com/Tnze/go-mc/net/packet"

	"github.com/reallyoldfogie/mc-bot-go/bot"
	"github.com/reallyoldfogie/mc-bot-go/bot/basic"
	"github.com/reallyoldfogie/mc-bot-go/bot/playerlist"
	"github.com/reallyoldfogie/mc-protocol-go/models"
)

const verifySession = false

// The Manager is used to receive and send chat messages.
type Manager struct {
	c         bot.Client
	p         basic.Player
	pl        playerlist.PlayerList
	events    EventsHandler
	packetMgr models.PacketMgr

	sign.SignatureCache
}

// New returns a new chat manager.
func New(c bot.Client, p basic.Player, pl playerlist.PlayerList, events EventsHandler, packetMgr models.PacketMgr) *Manager {
	m := &Manager{
		c:              c,
		p:              p,
		pl:             pl,
		events:         events,
		SignatureCache: sign.NewSignatureCache(),
		packetMgr:      packetMgr,
	}
	if events.SystemChat != nil {
		c.Events().AddListener(bot.PacketHandler{
			Priority: 64, ID: packetMgr.GetClientboundPacketID("ClientboundSystemChat"),
			F: m.handleSystemChat,
		})
	}
	if events.PlayerChatMessage != nil {
		c.Events().AddListener(bot.PacketHandler{
			Priority: 64, ID: packetMgr.GetClientboundPacketID("ClientboundPlayerChat"),
			F: m.handlePlayerChat,
		})
	}
	if events.DisguisedChat != nil {
		c.Events().AddListener(bot.PacketHandler{
			Priority: 64, ID: packetMgr.GetClientboundPacketID("ClientboundDisguisedChat"),
			F: m.handleDisguisedChat,
		})
	}
	return m
}

func (m *Manager) handleSystemChat(p pk.Packet) error {
	var msg chat.Message
	var overlay pk.Boolean
	if err := p.Scan(&msg, &overlay); err != nil {
		return err
	}
	return m.events.SystemChat(msg, bool(overlay))
}

func (m *Manager) handlePlayerChat(packet pk.Packet) error {
	var (
		sender    pk.UUID
		index     pk.VarInt
		signature *sign.Signature
		// signature       pk.Option[sign.Signature, *sign.Signature]
		body            sign.PackedMessageBody
		unsignedContent pk.Option[chat.Message, *chat.Message]
		filter          sign.FilterMask
		chatType        chat.Type
	)

	pkt, err := m.packetMgr.GetClientboundPacketByID(models.ClientboundPacketID(packet.ID))
	if err != nil {
		return fmt.Errorf("failed to get packet by id: %w", err)
	}

	err = pkt.Scan(packet)
	if err != nil {
		return fmt.Errorf("failed to scan packet: %w", err)
	}

	// fmt.Printf("\n[DEBUG] pkt: %s\n", spew.Sdump(pkt))

	var ok bool

	body.PlainMsg, ok = models.GetPacketFieldAs[string](pkt, "PlainMessage")
	if !ok {
		return InvalidChatPacket{fmt.Errorf("failed to get PlainMessage field")}
	}

	timestamp, ok := models.GetPacketFieldAs[int64](pkt, "Timestamp")
	if !ok {
		return InvalidChatPacket{fmt.Errorf("failed to get Timestamp field")}
	}

	body.Timestamp = time.Unix(timestamp, 0)

	body.Salt, ok = models.GetPacketFieldAs[int64](pkt, "Salt")
	if !ok {
		return InvalidChatPacket{fmt.Errorf("failed to get Salt field")}
	}

	previousMessages, ok := models.GetPacketFieldValue(pkt, "PreviousMessages")
	if !ok {
		return InvalidChatPacket{fmt.Errorf("failed to get PreviousMessages field")}
	}

	body.LastSeen, err = models.ConvertPreviousMessagesToPackedSignatures(previousMessages)
	if err != nil {
		return InvalidChatPacket{fmt.Errorf("failed to convert PreviousMessages to PackedSignatures: %v", err)}
	}

	var unpackedMsg *sign.MessageBody
	unpackedMsg, err = body.Unpack(&m.SignatureCache)
	if err != nil {
		return InvalidChatPacket{err}
	}

	sender, ok = models.GetPacketFieldAs[pk.UUID](pkt, "SenderUuid")
	if !ok {
		return InvalidChatPacket{fmt.Errorf("failed to get SenderUuid field")}
	}

	senderInfo, ok := m.pl.GetPlayerInfo(uuid.UUID(sender))
	if !ok {
		// Player not yet in player list - this can happen during login sequence
		// when PlayerChat arrives before PlayerInfo. Log and ignore rather than crash.
		log.Printf("[WARN] received chat from unknown player %s, ignoring (player list not yet populated)", uuid.UUID(sender).String())
		return nil // Ignore message, don't crash
	}

	rawSignature, ok := models.GetPacketFieldAs[models.Option[models.FixedBuffer256]](pkt, "Signature")
	if !ok {
		return InvalidChatPacket{fmt.Errorf("failed to get Signature field")}
	}

	signature = fixedBuffer256ToSignaturePtr(rawSignature.Pointer())

	unsignedContentRaw, ok := models.GetPacketFieldAs[models.Option[models.AnonymousNBT]](pkt, "UnsignedChatContent")
	if !ok {
		return InvalidChatPacket{fmt.Errorf("failed to get UnsignedChatContent field")}
	}

	unsignedContent, err = nbtOptionToChat(unsignedContentRaw)
	if err != nil {
		return fmt.Errorf("failed to convert NBTField to chat.Message")
	}

	// TODO: Get chat type from m.c.RegistryData instead of tnze registry
	_ = chatType.ID // Use variable to avoid unused warning
	// ct := m.c.Registries.ChatType.GetByID(chatType.ID)
	// if ct == nil {
	// 	return InvalidChatPacket{ErrUnknownChatType}
	// }

	var message sign.Message
	// Note: ChatSession verification disabled in 1.21.5 due to packet format changes
	// The HasChatSession flag indicates if chat is initialized but we can't verify signatures
	message.Prev = sign.Prev{
		Index:   int(index),
		Sender:  uuid.UUID(sender),
		Session: uuid.Nil, // Session not available in new format
	}
	message.Signature = signature
	message.MessageBody = unpackedMsg
	message.Unsigned = unsignedContent.Pointer()
	message.FilterMask = filter

	// Chat signature verification disabled - signature format changed in 1.21.5
	validated := false
	var content chat.Message
	if unsignedContent.Has {
		content = unsignedContent.Val
	} else {
		content = chat.Text(body.PlainMsg)
	}
	// TODO: Decorate with chat type from registry
	// msg := chatType.Decorate(content, &ct.Chat)
	// For now, just use the content without decoration
	return m.events.PlayerChatMessage(*senderInfo, content, validated)
}

func fixedBuffer256ToSignaturePtr(fb *models.FixedBuffer256) *sign.Signature {
	if fb == nil {
		return nil
	}
	s := sign.Signature(*fb)
	return &s
}

func nbtOptionToChat(opt models.Option[models.AnonymousNBT]) (pk.Option[chat.Message, *chat.Message], error) {
	var out pk.Option[chat.Message, *chat.Message]
	out.Has = opt.Has
	if !opt.Has {
		return out, nil
	}
	// Empty NBT: ok; decodes to zero-value Message
	if opt.Val == nil {
		return out, nil
	}
	var buf bytes.Buffer
	if _, err := opt.Val.WriteTo(&buf); err != nil {
		return out, err
	}
	var msg chat.Message
	if _, err := msg.ReadFrom(&buf); err != nil {
		return out, err
	}
	out.Val = msg
	return out, nil
}

func (m *Manager) handleDisguisedChat(packet pk.Packet) error {
	var (
		message  chat.Message
		chatType chat.Type
	)
	if err := packet.Scan(&message, &chatType); err != nil {
		log.Printf("[ERROR] failed to scan disguised chat message: %#v", err)
		return err
	}

	// TODO: Get chat type from m.c.RegistryData instead of tnze registry
	_ = chatType.ID // Use variable to avoid unused warning
	// ct := m.c.Registries.ChatType.GetByID(chatType.ID)
	// if ct == nil {
	// 	log.Printf("[ERROR] received invalid disguised chat type %v", chatType.ID)
	// 	return InvalidChatPacket{ErrUnknownChatType}
	// }
	// msg := chatType.Decorate(message, &ct.Chat)
	// For now, just use the raw message without decoration
	return m.events.DisguisedChat(message)
}

// const chatEnabled = false
const chatEnabled = true

// SendMessage send chat message to server.
// Doesn't support sending message with signature currently.
func (m *Manager) SendMessage(msg string) error {
	if len(msg) > 256 {
		return errors.New("message length greater than 256")
	}

	var salt int64
	if err := binary.Read(rand.Reader, binary.BigEndian, &salt); err != nil {
		return err
	}

	if chatEnabled {
		outMsgPacket, err := m.packetMgr.GetServerboundPacketByID(m.packetMgr.GetServerboundPacketID("ServerboundChat"))
		if err != nil {
			return err
		}

		message := chat.Text(msg)
		// message := chat.Text("Test")
		// fmt.Printf("sending chat: [%s] (%d) [%v] (%d)\n", msg, len(msg), []byte(msg), len([]byte(msg)))
		fmt.Printf("[%s] sending chat: [%s] (%d)\n", m.c.Name(), msg, len(msg))
		fields := outMsgPacket.GetFields()

		// fbs := pk.NewFixedBitSet(20)
		// log.Println("FixedBitSet(20).Len = ", fbs.Len())
		fbs := models.FixedBuffer3{}

		msgStr := pk.String(message.String())
		fields["Message"] = &msgStr //pk.String(msg)
		fields["Acknowledged"] = &fbs
		checksum := pk.Byte(0)
		fields["Checksum"] = &checksum
		offset := pk.VarInt(0)
		fields["Offset"] = &offset
		saltVal := pk.Long(salt)
		fields["Salt"] = &saltVal
		fields["Signature"] = &models.Option[models.FixedBuffer256]{Has: false}
		timestamp := pk.Long(time.Now().UnixMilli())
		fields["Timestamp"] = &timestamp
		// fmt.Printf("\n[AFTER ]%s\n", spew.Sdump(fields))
		outMsgPacket.SetFields(fields)

		err = m.c.Conn().WritePacket(outMsgPacket.Marshal())

		return err

		// err := m.c.Conn.WritePacket(pk.Marshal(
		// 	// packetid.ServerboundChat,
		// 	m.packetMgr.GetServerboundPacketID("ServerboundChat"),
		// pk.String(msg),
		// pk.Long(time.Now().UnixMilli()),
		// pk.Long(salt),
		// pk.Boolean(false), // signature
		// sign.HistoryUpdate{
		// 	Acknowledged: pk.NewFixedBitSet(20),
		// },
		// ))
		// return err
	} else {
		fmt.Println("CHAT NOT CURRENTLY ENEABLED, DIDN'T SEND MESSAGE: ", msg)
	}
	return nil
}

// SendMessage send chat message to server.
// Doesn't support sending message with signature currently.
func (m *Manager) SendCommand(command string) error {
	if len(command) > 256 {
		return errors.New("message length greater than 256")
	}
	// var salt int64
	// if err := binary.Read(rand.Reader, binary.BigEndian, &salt); err != nil {
	// 	return err
	// }

	// err := m.c.Conn.WritePacket(pk.Marshal(
	// 	// packetid.ServerboundChatCommand,
	// 	m.packetMgr.GetServerboundPacketID("ServerboundChatCommand"),
	// 	pk.String(command),
	// 	pk.Long(time.Now().UnixMilli()),
	// 	pk.Long(salt),
	// 	pk.Ary[pk.VarInt]{Ary: []pk.Tuple{}},
	// 	sign.HistoryUpdate{
	// 		Acknowledged: pk.NewFixedBitSet(20),
	// 	},
	// ))

	pkt, err := m.packetMgr.GetServerboundPacketByID(m.packetMgr.GetServerboundPacketID("ServerboundChatCommand"))
	if err != nil {
		return err
	}

	fields := pkt.GetFields()
	fields["Command"] = pk.String(command)
	pkt.SetFields(fields)

	err = m.c.Conn().WritePacket(pkt.Marshal())
	return err
}

type InvalidChatPacket struct {
	err error
}

func (i InvalidChatPacket) Error() string {
	if i.err == nil {
		return "invalid chat packet"
	}
	return "invalid chat packet: " + i.err.Error()
}

func (i InvalidChatPacket) Unwrap() error {
	return i.err
}

var (
	ErrUnknownPlayer          = errors.New("unknown player")
	ErrUnknownChatType        = errors.New("unknown chat type")
	ErrValidationFailed error = bot.DisconnectErr(chat.TranslateMsg("multiplayer.disconnect.chat_validation_failed"))
)
