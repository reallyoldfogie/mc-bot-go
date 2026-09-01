package bot

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/reallyoldfogie/mc-protocol-go/models"

	"github.com/Tnze/go-mc/chat"
	mcnet "github.com/Tnze/go-mc/net"
	"github.com/Tnze/go-mc/net/CFB8"
	pk "github.com/Tnze/go-mc/net/packet"
)

type LoginErr struct {
	Stage string
	Err   error
}

func (l LoginErr) Error() string {
	return "bot: login error: [" + l.Stage + "] " + l.Err.Error()
}

func (l LoginErr) Unwrap() error {
	return l.Err
}

func (c *client) joinLogin(conn *mcnet.Conn) error {
	var err error
	if c.auth.UUID != "" {
		c.uuid, err = uuid.Parse(c.auth.UUID)
		if err != nil {
			return LoginErr{"login start", err}
		}
	}

	// Use version handler if available
	if c.versionHandler != nil {
		fmt.Println("[Version Handler] Sending LoginStart for", c.auth.Name)
		err = c.versionHandler.Login().SendLoginStart(conn, c.auth.Name, c.uuid)
	} else {
		fmt.Println("writing ServerboundLoginHello", c.auth.Name, c.uuid)
		err = conn.WritePacket(pk.Marshal(
			c.packetMgr.GetServerboundLoginPacketID("ServerboundLoginHello"),
			pk.String(c.auth.Name),
			pk.UUID(c.uuid),
		))
	}
	if err != nil {
		return LoginErr{"login start", err}
	}
	receiving := "encrypt start"
	for {
		// Receive Packet
		var p pk.Packet
		if err = conn.ReadPacket(&p); err != nil {
			fmt.Println("Failed to read packet: ", err.Error())
			return LoginErr{receiving, err}
		}

		// Skip Set Compression packet for replay recording - ReplayMod expects recordings to start from Login Success
		// Set Compression happens before the Netty pipeline is fully configured
		// Skip Set Compression packet for replay recording - ReplayMod expects recordings to start from Login Success
		// Set Compression happens before the Netty pipeline is fully configured
		if models.ClientboundPacketID(p.ID) != c.packetMgr.GetClientboundLoginPacketID("ClientboundLoginLoginCompression") {
			recordForReplay(c.replayRecorder, p)
		} else {
			fmt.Printf("[LOGIN] Skipping SetCompression packet from replay (threshold=%d will be applied to connection)\n", -1)
		}

		fmt.Println("receiving:", receiving, c.packetMgr.ClientboundLoginToString(models.ClientboundPacketID(p.ID)))
		fmt.Printf("[DEBUG] Packet ID: %d\n", p.ID)
		fmt.Printf("[DEBUG] ClientboundLoginGameProfile ID: %d\n", c.packetMgr.GetClientboundLoginPacketID("ClientboundLoginGameProfile"))

		// Handle Packet
		switch models.ClientboundPacketID(p.ID) {
		case c.packetMgr.GetClientboundLoginPacketID("ClientboundLoginLoginDisconnect"): // LoginDisconnect
			var reason chat.JsonMessage
			err = p.Scan(&reason)
			if err != nil {
				return LoginErr{"disconnect", err}
			}
			return LoginErr{"disconnect", DisconnectErr(reason)}

		case c.packetMgr.GetClientboundLoginPacketID("ClientboundLoginHello"): // Encryption Request
			if err := handleEncryptionRequest(conn, c, p); err != nil {
				return LoginErr{"encryption", err}
			}
			receiving = "set compression"

		case c.packetMgr.GetClientboundLoginPacketID("ClientboundLoginGameProfile"): // Login Success
			fallthrough
		case c.packetMgr.GetClientboundLoginPacketID("ClientboundLoginLoginFinished"): // Login Success
			fmt.Println("[DEBUG] Entering Login Success handler", c.packetMgr.ClientboundLoginToString(models.ClientboundPacketID(p.ID)))
			fmt.Printf("[DEBUG] versionHandler=%v\n", c.versionHandler)
			// Use version handler if available
			if c.versionHandler != nil {
				c.name, c.uuid, err = c.versionHandler.Login().ParseLoginSuccess(p)
				if err != nil {
					return LoginErr{"login success", err}
				}
				err = c.versionHandler.Login().SendLoginAcknowledged(conn)
			} else {
				err := p.Scan(
					(*pk.UUID)(&c.uuid),
					(*pk.String)(&c.name),
				)
				if err != nil {
					return LoginErr{"login success", err}
				}
				err = conn.WritePacket(pk.Marshal(c.packetMgr.GetServerboundLoginPacketID("ServerboundLoginLoginAcknowledged")))
			}
			if err != nil {
				return LoginErr{"login success", err}
			}
			if c.replayRecorder != nil {
				// Entity ID is supplied later in play login; set SelfID when known there.
				// Here we only have UUID/Name.
			}
			return nil

		case c.packetMgr.GetClientboundLoginPacketID("ClientboundLoginLoginCompression"): // Set Compression
			var threshold pk.VarInt
			if err := p.Scan(&threshold); err != nil {
				return LoginErr{"compression", err}
			}
			fmt.Printf("[LOGIN] SetCompression: threshold=%d (packets >= %d bytes will be compressed)\n", threshold, threshold)
			conn.SetThreshold(int(threshold))
			fmt.Printf("[LOGIN] Threshold set on connection. Subsequent packets will be decompressed by ReadPacket()\n")
			receiving = "login success"

		case c.packetMgr.GetClientboundLoginPacketID("ClientboundLoginCustomQuery"): // Login Plugin Request
			var (
				msgid   pk.VarInt
				channel pk.Identifier
				data    pk.PluginMessageData
			)
			if err := p.Scan(&msgid, &channel, &data); err != nil {
				return LoginErr{"Login Plugin", err}
			}

			var PluginMessageData pk.Option[pk.PluginMessageData, *pk.PluginMessageData]
			if handler, ok := c.loginPlugin[string(channel)]; ok {
				PluginMessageData.Has = true
				PluginMessageData.Val, err = handler(data)
				if err != nil {
					return LoginErr{"Login Plugin", err}
				}
			}

			if err := conn.WritePacket(pk.Marshal(
				c.packetMgr.GetServerboundLoginPacketID("ServerboundLoginCustomQueryAnswer"),
				msgid, PluginMessageData,
			)); err != nil {
				return LoginErr{"login Plugin", err}
			}

		case c.packetMgr.GetClientboundLoginPacketID("ClientboundLoginCookieRequest"):
			var key pk.Identifier
			err := p.Scan(&key)
			if err != nil {
				return LoginErr{"cookie request", err}
			}
			cookieContent := c.cookies[string(key)]
			err = conn.WritePacket(pk.Marshal(
				c.packetMgr.GetServerboundLoginPacketID("ServerboundLoginCookieResponse"),
				key, pk.OptionEncoder[pk.ByteArray]{
					Has: cookieContent != nil,
					Val: pk.ByteArray(cookieContent),
				},
			))
			if err != nil {
				return LoginErr{"cookie response", err}
			}
		}
	}
}

// Auth includes an account
type Auth struct {
	Name        string
	UUID        string
	AccessToken string
}

func handleEncryptionRequest(conn *mcnet.Conn, c *client, p pk.Packet) error {
	// 创建AES对称加密密钥
	key, encoStream, decoStream := newSymmetricEncryption()

	// Read EncryptionRequest
	var er encryptionRequest
	if err := p.Scan(&er); err != nil {
		return err
	}

	err := loginAuth(c.auth, key, er) // 向Mojang验证
	if err != nil {
		return fmt.Errorf("login fail: %v", err)
	}

	// 响应加密请求
	// Write Encryption Key Response
	p, err = genEncryptionKeyResponse(key, er.PublicKey, er.VerifyToken, c.packetMgr)
	if err != nil {
		return fmt.Errorf("gen encryption key response fail: %v", err)
	}

	err = conn.WritePacket(p)
	if err != nil {
		return err
	}

	// 设置连接加密
	conn.SetCipher(encoStream, decoStream)
	return nil
}

type encryptionRequest struct {
	ServerID    string
	PublicKey   []byte
	VerifyToken []byte
}

func (e *encryptionRequest) ReadFrom(r io.Reader) (int64, error) {
	return pk.Tuple{
		(*pk.String)(&e.ServerID),
		(*pk.ByteArray)(&e.PublicKey),
		(*pk.ByteArray)(&e.VerifyToken),
	}.ReadFrom(r)
}

// authDigest computes a special SHA-1 digest required for Minecraft web
// authentication on Premium servers (online-mode=true).
// Source: http://wiki.vg/Protocol_Encryption#Server
func authDigest(serverID string, sharedSecret, publicKey []byte) string {
	h := sha1.New()
	h.Write([]byte(serverID))
	h.Write(sharedSecret)
	h.Write(publicKey)
	hash := h.Sum(nil)

	// Check for negative hashes
	negative := (hash[0] & 0x80) == 0x80
	if negative {
		hash = twosComplement(hash)
	}

	// Trim away zeroes
	res := strings.TrimLeft(hex.EncodeToString(hash), "0")
	if negative {
		res = "-" + res
	}

	return res
}

// little endian
func twosComplement(p []byte) []byte {
	carry := true
	for i := len(p) - 1; i >= 0; i-- {
		p[i] = ^p[i]
		if carry {
			carry = p[i] == 0xff
			p[i]++
		}
	}
	return p
}

type profile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type request struct {
	AccessToken     string  `json:"accessToken"`
	SelectedProfile profile `json:"selectedProfile"`
	ServerID        string  `json:"serverId"`
}

func loginAuth(auth Auth, shareSecret []byte, er encryptionRequest) error {
	digest := authDigest(er.ServerID, shareSecret, er.PublicKey)

	requestPacket, err := json.Marshal(
		request{
			AccessToken: auth.AccessToken,
			SelectedProfile: profile{
				ID:   auth.UUID,
				Name: auth.Name,
			},
			ServerID: digest,
		},
	)
	if err != nil {
		return fmt.Errorf("create request packet to yggdrasil faile: %v", err)
	}

	PostRequest, err := http.NewRequest(http.MethodPost, "https://sessionserver.mojang.com/session/minecraft/join",
		bytes.NewReader(requestPacket))
	if err != nil {
		return fmt.Errorf("make request error: %v", err)
	}
	PostRequest.Header.Set("User-agent", "go-mc")
	PostRequest.Header.Set("Connection", "keep-alive")
	PostRequest.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(PostRequest)
	if err != nil {
		return fmt.Errorf("post fail: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("auth fail: %s", string(body))
	}
	return nil
}

// AES/CFB8 with random key
func newSymmetricEncryption() (key []byte, encoStream, decoStream cipher.Stream) {
	key = make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}

	b, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	decoStream = CFB8.NewCFB8Decrypt(b, key)
	encoStream = CFB8.NewCFB8Encrypt(b, key)
	return
}

func genEncryptionKeyResponse(shareSecret, publicKey, verifyToken []byte, packetMgr models.PacketMgr) (erp pk.Packet, err error) {
	iPK, err := x509.ParsePKIXPublicKey(publicKey) // Decode Public Key
	if err != nil {
		err = fmt.Errorf("decode public key fail: %v", err)
		return
	}
	rsaKey := iPK.(*rsa.PublicKey)
	cryptPK, err := rsa.EncryptPKCS1v15(rand.Reader, rsaKey, shareSecret)
	if err != nil {
		err = fmt.Errorf("encryption share secret fail: %v", err)
		return
	}

	verifyT, err := rsa.EncryptPKCS1v15(rand.Reader, rsaKey, verifyToken)
	if err != nil {
		err = fmt.Errorf("encryption verfy tokenfail: %v", err)
		return erp, err
	}
	return pk.Marshal(
		packetMgr.GetServerboundLoginPacketID("ServerboundLoginKey"),
		pk.ByteArray(cryptPK),
		pk.ByteArray(verifyT),
	), nil
}
