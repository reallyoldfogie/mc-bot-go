package main

import (
	"context"
	"log"

	"github.com/Tnze/go-mc/yggdrasil"
	"github.com/reallyoldfogie/mc-bot-go/bot"
	"github.com/reallyoldfogie/mc-protocol-go/data/versions"
)

func main() {
	pktMgr := versions.GetPacketMgrForVersion("1.21.4")
	c := bot.NewClient(pktMgr)

	ctx := context.Background()
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

	c.SetAuth(bot.Auth{
		UUID:        id,
		Name:        name,
		AccessToken: accessToken,
	})

	// Connect server
	err = c.JoinServer(ctx, "127.0.0.1")
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
	err = c.HandleGame(ctx)
	if err != nil {
		log.Fatal(err)
	}
}
