package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/kxn/mobilemod/internal/config"
	"github.com/kxn/mobilemod/internal/domain"
	"github.com/kxn/mobilemod/internal/service"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mobilemod run|check|devices|send|version")
	}
	if args[0] == "version" {
		fmt.Println(version)
		return nil
	}
	if args[0] == "devices" {
		ports, err := listPorts()
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(ports)
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	path := f.String("config", "config.json", "private JSON configuration")
	module := f.String("module", "", "module id (send)")
	to := f.String("to", "", "destination (send)")
	text := f.String("text", "", "short SMS text (send)")
	id := f.String("id", "", "unique request id (send; persisted to prevent retries)")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if args[0] != "run" && args[0] != "check" && args[0] != "send" {
		return fmt.Errorf("unknown command")
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	if args[0] == "check" {
		fmt.Printf("configuration valid: %d modules, %d allowed users\n", len(c.Modules), len(c.Feishu.AllowedUsers))
		return nil
	}
	if args[0] == "send" && (*module == "" || *to == "" || *text == "" || *id == "") {
		return fmt.Errorf("send requires -module -to -text -id")
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	r, err := service.New(c, logger)
	if err != nil {
		return err
	}
	defer r.Store.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if args[0] == "send" {
		result, err := r.LocalSend(ctx, *module, *to, *text, domain.Key(*id))
		if err == nil {
			fmt.Println(result)
		}
		return err
	}
	return r.Run(ctx)
}
