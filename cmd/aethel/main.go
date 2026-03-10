package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stukans/aethel/internal/config"
	"github.com/stukans/aethel/internal/ipc"
	"github.com/stukans/aethel/internal/tui"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "daemon":
			handleDaemon()
			return
		case "version":
			fmt.Println("aethel v0.1.0")
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
	sockPath := config.SocketPath()

	cfg := config.Default()
	if cfgPath := config.ConfigPath(); fileExists(cfgPath) {
		if loaded, err := config.Load(cfgPath); err == nil {
			cfg = loaded
		}
	}

	// Try connecting; auto-start if needed
	client, err := ipc.NewClient(sockPath)
	if err != nil && cfg.Daemon.AutoStart {
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
		fmt.Fprintf(os.Stderr, "cannot connect to daemon: %v\nRun 'aethel daemon start' first.\n", err)
		os.Exit(1)
	}
	defer client.Close()

	model := tui.NewModel(client)
	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
