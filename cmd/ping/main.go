package main

import (
	"log"

	"github.com/reallyoldfogie/mc-bot-go/bot"
)

func main() {
	resp, delay, err := bot.PingAndList("localhost:25565")
	if err != nil {
		log.Fatalf("ping and list server fail: %v", err)
	}

	log.Println("Status:", string(resp))
	log.Println("Delay:", delay)
}
