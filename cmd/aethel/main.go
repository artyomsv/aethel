package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/artyomsv/aethel/internal/config"
	"github.com/artyomsv/aethel/internal/ipc"
	"github.com/artyomsv/aethel/internal/tui"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "daemon":
			handleDaemon()
			return
		case "version":
			fmt.Println("aethel v" + version)
			return
		}
	}

	launchTUI()
}

func handleDaemon() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: aethel daemon [start|stop]")
		os.Exit(1)
	}

	switch os.Args[2] {
	case "start":
		startDaemon()
	case "stop":
		stopDaemon()
	default:
		fmt.Fprintf(os.Stderr, "unknown daemon command: %s\n", os.Args[2])
		os.Exit(1)
	}
}

func startDaemon() {
	sockPath := config.SocketPath()

	// Check if daemon is already running
	if client, err := ipc.NewClient(sockPath); err == nil {
		client.Close()
		fmt.Println("daemon already running")
		return
	}

	// Find aetheld binary
	aetheld, err := exec.LookPath("aetheld")
	if err != nil {
		aetheld = "aetheld"
	}

	cmd := exec.Command(aetheld)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start daemon: %v\n", err)
		os.Exit(1)
	}

	cmd.Process.Release()
	fmt.Printf("daemon started (pid %d)\n", cmd.Process.Pid)
}

func stopDaemon() {
	sockPath := config.SocketPath()
	client, err := ipc.NewClient(sockPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "daemon not running")
		os.Exit(1)
	}
	defer client.Close()

	msg, _ := ipc.NewMessage(ipc.MsgShutdown, nil)
	client.Send(msg)
	fmt.Println("daemon stopped")
}

func launchTUI() {
	// Set up logging early
	logDir := config.AethelDir()
	if logDir != "" {
		os.MkdirAll(logDir, 0700)
	}
	logPath := filepath.Join(logDir, "aethel.log")
	logFile, err := tea.LogToFile(logPath, "aethel")
	if err == nil && logFile != nil {
		defer logFile.Close()
	}

	// Panic recovery — write to log before crashing
	defer func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("PANIC: %v\n%s", r, debug.Stack())
			log.Print(msg)
			fmt.Fprintf(os.Stderr, "%s\n", msg)
			os.Exit(1)
		}
	}()

	sockPath := config.SocketPath()

	cfg := config.Default()
	if cfgPath := config.ConfigPath(); fileExists(cfgPath) {
		if loaded, err := config.Load(cfgPath); err == nil {
			cfg = loaded
		}
	}
	log.Printf("config loaded, AutoStart=%v", cfg.Daemon.AutoStart)

	// Try connecting; auto-start if needed
	client, err := ipc.NewClient(sockPath)
	if err != nil && cfg.Daemon.AutoStart {
		log.Printf("daemon not reachable, auto-starting...")
		startDaemon()
		for i := 0; i < 20; i++ {
			time.Sleep(100 * time.Millisecond)
			client, err = ipc.NewClient(sockPath)
			if err == nil {
				break
			}
		}
	}
	if err != nil {
		log.Printf("cannot connect to daemon: %v", err)
		fmt.Fprintf(os.Stderr, "cannot connect to daemon: %v\nRun 'aethel daemon start' first.\n", err)
		os.Exit(1)
	}
	defer client.Close()
	log.Print("connected to daemon")

	model := tui.NewModel(client, cfg)
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		log.Printf("TUI error: %v", err)
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	log.Print("TUI exited normally")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
