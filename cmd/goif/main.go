package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/preflight"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: goif preflight -config config/sepolia.json")
	}
	flags := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	path := flags.String("config", "config/sepolia.json", "public configuration file")
	if e := flags.Parse(os.Args[2:]); e != nil {
		return e
	}
	c, e := config.Load(*path)
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch os.Args[1] {
	case "preflight":
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		report, e := preflight.Run(ctx, c)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	default:
		return fmt.Errorf("unknown command")
	}
}
