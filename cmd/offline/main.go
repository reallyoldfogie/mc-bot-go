package main

import (
	"context"
	"encoding/hex"
	"log"

	"github.com/Tnze/go-mc/offline"
	"github.com/reallyoldfogie/mc-bot-go/bot"
	"github.com/reallyoldfogie/mc-protocol-go/data/versions"
)

func main() {
	log.Println("getting packet manager for version 1.21.5")
	pktMgr := versions.GetPacketMgrForVersion("1.21.5")
	c := bot.NewClient(pktMgr)
	name := "reallyoldfogie-offline" // set its name before login.

	ctx := context.Background()
	id := offline.NameToUUID(name) // optional, get uuid of offline mode game
	authUUID := hex.EncodeToString(id[:])

	c.SetAuth(bot.Auth{
		Name: name,
		UUID: authUUID,
	})

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
