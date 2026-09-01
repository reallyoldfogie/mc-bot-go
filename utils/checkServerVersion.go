package utils

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/Tnze/go-mc/bot"
	"github.com/Tnze/go-mc/data/packetid"
	mcnet "github.com/Tnze/go-mc/net"
	pk "github.com/Tnze/go-mc/net/packet"
)

func CheckServerVersion(addr string, protocolVersion uint) (string, uint, error) {
	// data, timeSince, err := bot.PingAndListTimeout(addr, 30*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	serverInfo, err := GetServerInfo(ctx, addr, protocolVersion)
	if err != nil {
		return "", 0, err
	}
	fmt.Printf("serverInfo: %#v\n", serverInfo)

	return serverInfo.Version.Name, serverInfo.Version.Protocol, nil
}

type ServerInfo struct {
	Version     Version `json:"version,omitempty"`
	Description string  `json:"description,omitempty"`
	// PlayerInfo  PlayerInfo `json:"player_info,omitempty"`
	Players PlayerInfo `json:"players,omitempty"`
	FavIcon string     `json:"favicon,omitempty"` // png image
}

func (si *ServerInfo) UnmarshalJSON(data []byte) error {
	// Define an intermediate map to handle flexible key names
	var rawMap map[string]any
	if err := json.Unmarshal(data, &rawMap); err != nil {
		return err
	}

	// fmt.Printf("%#v\n\n", rawMap)

	if versionInfoStr, ok := rawMap["version"]; ok {
		fmt.Printf("versionInfoStr: %#v\n", versionInfoStr)
		if viMap, ok := versionInfoStr.(map[string]any); ok {
			if name, ok := viMap["name"].(string); ok {
				si.Version.Name = name
			}
			if protocol, ok := viMap["protocol"].(float64); ok {
				si.Version.Protocol = uint(protocol)
			}
		}
	}

	if desciption, ok := rawMap["description"]; ok {
		fmt.Printf("desciption: %#v\n", desciption)
		if desc, ok := desciption.(string); ok {
			si.Description = desc
		}
	}

	var playerInfoStr map[string]any
	// Manually extract the player_info field (depending on the version may be in players or playerinfo)
	if playerInfo, ok := rawMap["player_info"].(map[string]any); ok {
		fmt.Printf("player_info playerInfo: %#v\n", playerInfo)
		playerInfoStr = playerInfo
	} else if playerInfo, ok := rawMap["players"].(map[string]any); ok {
		fmt.Printf("players playerInfo: %#v\n", playerInfo)
		playerInfoStr = playerInfo
	}

	if playerInfoStr != nil {
		tmp, err := json.Marshal(playerInfoStr)
		if err != nil {
			return err
		}
		err = json.Unmarshal(tmp, &si.Players)
		if err != nil {
			return err
		}
	}

	// if favIcon, ok := rawMap["favicon"]; ok {
	// 	si.FavIcon = favIcon.(string)
	// }
	return nil
}

type Version struct {
	Name     string `json:"name,omitempty"`
	Protocol uint   `json:"protocol,omitempty"`
}

type PlayerInfo struct {
	Max    uint `json:"max,omitempty"`
	Online uint `json:"online,omitempty"`
	Sample []PlayerSample
}

type PlayerSample struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

func GetServerInfo(ctx context.Context, addr string, protocolVersion uint) (ServerInfo, error) {
	conn, err := mcnet.DefaultDialer.DialMCContext(ctx, addr)
	if err != nil {
		return ServerInfo{}, err
	}
	data, delay, err := getServerInfo(ctx, addr, protocolVersion, conn)

	// fmt.Printf("%s\n\t%s\n\t%#v\n", string(data), delay.String(), err)
	fmt.Printf("delay: %s  err: %v\n", delay.String(), err)
	var serverInfo ServerInfo
	err = json.Unmarshal(data, &serverInfo)
	if err != nil {
		return ServerInfo{}, err
	}
	return serverInfo, nil
}

func getServerInfo(ctx context.Context, addr string, protocolVersion uint, conn *mcnet.Conn) (data []byte, delay time.Duration, err error) {
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		if err := conn.Socket.SetDeadline(deadline); err != nil {
			return nil, 0, err
		}
		defer func() {
			// Reset deadline
			if err2 := conn.Socket.SetDeadline(time.Time{}); err2 != nil {
				if err == nil {
					err = err2
				}
				return
			}
			// Map error type
			if errors.Is(err, os.ErrDeadlineExceeded) {
				err = context.DeadlineExceeded
			}
		}()
	}
	// Split Host and Port
	host, portStr, err := net.SplitHostPort(addr)
	var port uint64
	if err != nil {
		var addrErr *net.AddrError
		const missingPort = "missing port in address"
		if errors.As(err, &addrErr) && addrErr.Err == missingPort {
			host, port, err = addr, bot.DefaultPort, nil
		} else {
			return nil, 0, bot.LoginErr{Stage: "split address", Err: err}
		}
	} else {
		port, err = strconv.ParseUint(portStr, 0, 16)
		if err != nil {
			return nil, 0, bot.LoginErr{Stage: "parse port", Err: err}
		}
	}

	const Handshake = 0x00
	// 握手
	err = conn.WritePacket(pk.Marshal(
		Handshake,                  // Handshake packet ID
		pk.VarInt(protocolVersion), // Protocol version
		pk.String(host),            // Server's address
		pk.UnsignedShort(port),
		pk.Byte(1),
	))
	if err != nil {
		return nil, 0, fmt.Errorf("bot: send handshake packect fail: %v", err)
	}

	// LIST
	// 请求服务器状态
	err = conn.WritePacket(pk.Marshal(
		packetid.ServerboundStatusStatusRequest,
	))
	if err != nil {
		return nil, 0, fmt.Errorf("bot: send list packect fail: %v", err)
	}

	var p pk.Packet
	// 服务器返回状态
	if err := conn.ReadPacket(&p); err != nil {
		return nil, 0, fmt.Errorf("bot: recv list packect fail: %v", err)
	}
	var s pk.String
	err = p.Scan(&s)
	if err != nil {
		return nil, 0, fmt.Errorf("bot: scan list packect fail: %v", err)
	}

	// PING
	startTime := time.Now()
	err = conn.WritePacket(pk.Marshal(
		packetid.ServerboundStatusPingRequest,
		pk.Long(startTime.Unix()),
	))
	if err != nil {
		return nil, 0, fmt.Errorf("bot: send ping packect fail: %v", err)
	}

	if err = conn.ReadPacket(&p); err != nil {
		return nil, 0, fmt.Errorf("bot: recv pong packect fail: %v", err)
	}
	var t pk.Long
	err = p.Scan(&t)
	if err != nil {
		return nil, 0, fmt.Errorf("bot: scan pong packect fail: %v", err)
	}
	if t != pk.Long(startTime.Unix()) {
		return nil, 0, fmt.Errorf("bot: pong packect no match: %v", err)
	}

	return []byte(s), time.Since(startTime), err
}
