package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		version := "0.19.1"
		if os.Getenv("RESTIC_HELPER_MODE") == "wrong-version" {
			version = "0.18.1"
		}
		fmt.Printf("restic %s\n", version)
		return
	}
	if len(os.Args) >= 4 && os.Args[1] == "--repo" && os.Args[3] == "cat" {
		fmt.Println(`{"version":2}`)
		return
	}
	mode := os.Getenv("RESTIC_HELPER_MODE")
	if mode == "partial" {
		fmt.Println(`{"message_type":"summary","snapshot_id":"abc123"}`)
		os.Exit(3)
	}
	if mode == "interrupted" {
		fmt.Println(`{"message_type":"summary","snapshot_id":"abc123"}`)
		os.Exit(130)
	}
	fmt.Println(`{"message_type":"summary","snapshot_id":"abc123"}`)
}
