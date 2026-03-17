package main

import (
	"os"

	"tofi-core/cmd/tofi/cli"

	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
