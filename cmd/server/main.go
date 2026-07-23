package main

import (
	"log"
	"os"

	"github.com/dyhhhhhh/chatgpt-web-voice/internal/app"
)

func main() {
	if err := app.Run(); err != nil {
		log.Printf("%v", err)
		os.Exit(1)
	}
}
