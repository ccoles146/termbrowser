package main

import (
	"encoding/hex"
	"flag"
	"io/fs"
	"log"
	"os"

	"github.com/chris/termbrowser/auth"
	"github.com/chris/termbrowser/config"
	"github.com/chris/termbrowser/server"
	"github.com/chris/termbrowser/terminal"

	"embed"
)

//go:embed web
var webFiles embed.FS

func main() {
	configPath := flag.String("config", config.DefaultPath(), "config file path")
	setupFlag := flag.Bool("setup", false, "re-run setup wizard")
	flag.Parse()

	if *setupFlag {
		if _, err := config.RunFirstSetup(*configPath); err != nil {
			log.Fatalf("setup failed: %v", err)
		}
		os.Exit(0)
	}

	cfg, err := config.Load(*configPath)
	if os.IsNotExist(err) {
		cfg, err = config.RunFirstSetup(*configPath)
	}
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	jwtSecret, err := hex.DecodeString(cfg.JWTSecret)
	if err != nil {
		log.Fatalf("invalid jwt_secret in config: %v", err)
	}

	authMgr := auth.NewManager(cfg.PasswordHash, cfg.TOTPSecret, jwtSecret)
	termMgr := terminal.NewManager()

	webRoot, err := fs.Sub(webFiles, "web")
	if err != nil {
		log.Fatalf("web embed: %v", err)
	}

	srv := server.New(cfg, authMgr, termMgr, webRoot)
	log.Printf("termbrowser listening on :%d", cfg.Port)
	if err := srv.Run(); err != nil {
		log.Fatalf("server: %v", err)
	}
}
