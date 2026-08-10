package main

import (
	"log"
	"os"

	"recodex-go/internal/bridgeapp"
)

func main() {
	if err := bridgeapp.Run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}
