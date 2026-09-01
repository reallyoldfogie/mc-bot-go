package bot

import (
	"context"
	"encoding/hex"
	"log"

	"github.com/Tnze/go-mc/offline"
	"github.com/Tnze/go-mc/yggdrasil"
	"github.com/reallyoldfogie/mc-protocol-go/data/versions"
)

func ExamplePingAndList() {
	resp, delay, err := PingAndList("localhost:25565")
	if err != nil {
		log.Fatalf("ping and list server fail: %v", err)
	}

	log.Println("Status:", string(resp))
	log.Println("Delay:", delay)
}

func ExampleClient_JoinServer_offline() {
	pktMgr := versions.GetPacketMgrForVersion("1.21.4")
	name := "Me-Offline-Example" // set its name before login.

	// optional, get uuid of offline mode game
	id := offline.NameToUUID(name)
	c := NewClient(pktMgr)
	c.SetAuth(Auth{
		Name: name,
		UUID: hex.EncodeToString(id[:]),
	})

	ctx := context.Background()
	// Login
	err := c.JoinServer(ctx, "127.0.0.1")
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Login success")

	// Register event handlers
	// c.Events.AddListener(...)

	// JoinGame
	err = c.HandleGame(ctx)
	if err != nil {
		log.Fatal(err)
	}
}

func ExampleClient_JoinServer_online() {
	pktMgr := versions.GetPacketMgrForVersion("1.21.4")
	c := NewClient(pktMgr)

	// Login Mojang account to get AccessToken
	// To use Microsoft Account, see issue #106
	// https://github.com/Tnze/go-mc/issues/106
	auth, err := yggdrasil.Authenticate("Your E-mail", "Your Password")
	if err != nil {
		panic(err)
	}

	// As long as you set these three fields correctly,
	// the client can connect to the online-mode server
	id, name := auth.SelectedProfile()
	accessToken := auth.AccessToken()

	c.SetAuth(Auth{
		Name:        name,
		UUID:        hex.EncodeToString([]byte(id)[:]),
		AccessToken: accessToken,
	})

	// Connect server
	err = c.JoinServer(context.Background(), "127.0.0.1")
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Login success")

	// Register event handlers
	// 	c.Events.GameStart = onGameStartFunc
	// 	c.Events.ChatMsg = onChatMsgFunc
	// 	c.Events.Disconnect = onDisconnectFunc
	//	...

	// Join the game
	err = c.HandleGame(context.Background())
	if err != nil {
		log.Fatal(err)
	}
}
