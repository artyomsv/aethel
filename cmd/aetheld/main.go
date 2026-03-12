package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/artyomsv/aethel/internal/config"
	"github.com/artyomsv/aethel/internal/daemon"
)

func main() {
	logFile := initLogging()
	if logFile != nil {
		defer logFile.Close()
	}

	cfg := config.Default()

	cfgPath := config.ConfigPath()
	if _, err := os.Stat(cfgPath); err == nil {
		loaded, err := config.Load(cfgPath)
		if err != nil {
			log.Printf("warning: failed to load config: %v", err)
		} else {
			cfg = loaded
		}
	}

	d := daemon.New(cfg)
	log.Println("aetheld starting...")
	fmt.Println("aetheld — starting daemon...")
	if err := d.Start(); err != nil {
		log.Printf("failed to start daemon: %v", err)
		fmt.Fprintf(os.Stderr, "failed to start daemon: %v\n", err)
		os.Exit(1)
	}

	log.Printf("aetheld ready (pid %d)", os.Getpid())
	fmt.Printf("aetheld ready (pid %d). Press Ctrl+C to stop.\n", os.Getpid())
	d.Wait()
}

func initLogging() *os.File {
	logDir := config.AethelDir()
	if logDir == "" {
		return nil
	}
	os.MkdirAll(logDir, 0700)
	f, err := os.OpenFile(filepath.Join(logDir, "aetheld.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil
	}
	log.SetOutput(f)
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	return f
}
