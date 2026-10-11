package main

import (
	"os"
	"strings"
)

func main() {
	args := os.Args[1:]
	for i, arg := range args {
		if arg == "--file" && i+1 < len(args) {
			if os.Getenv("PGDATABASE") != "fixture" {
				os.Exit(10)
			}
			if err := os.WriteFile(args[i+1], []byte("custom archive fixture"), 0600); err != nil {
				os.Exit(11)
			}
			return
		}
	}
	if len(args) == 2 && args[0] == "--list" && strings.HasSuffix(args[1], ".partial") {
		if _, err := os.Stat(args[1]); err == nil {
			return
		}
	}
	os.Exit(12)
}
