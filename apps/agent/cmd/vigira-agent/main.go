package main

import (
	"log/slog"
	"os"
)

func main() {
	slog.New(slog.NewJSONHandler(os.Stdout, nil)).Info("Vigira agent scaffold", "status", "not enrolled")
}
