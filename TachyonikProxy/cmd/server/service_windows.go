// TachyonikProxy
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package main

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

const serviceName = "TachyonikTachyonikProxy"
const serviceDisplayName = "Tachyonik TachyonikProxy"
const serviceDescription = "Tachyonik TachyonikProxy MCP server for security tool integration"

// platformMain detects whether the process was started by the Windows
// Service Control Manager.  If so it runs as a service; otherwise it
// handles install/uninstall commands or starts interactively.
func platformMain() {
	// Check for service management commands first
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "install":
			if err := installService(); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to install service: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("Service installed successfully.")
			return
		case "uninstall":
			if err := uninstallService(); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to uninstall service: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("Service uninstalled successfully.")
			return
		}
	}

	isService, err := svc.IsWindowsService()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to detect service mode: %v\n", err)
		os.Exit(1)
	}

	if isService {
		if err := svc.Run(serviceName, &proxyService{}); err != nil {
			fmt.Fprintf(os.Stderr, "Service failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Interactive mode
	runServer()
}

// proxyService implements svc.Handler.
type proxyService struct{}

func (s *proxyService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}

	// Run the server in a goroutine
	done := make(chan struct{})
	go func() {
		runServer()
		close(done)
	}()

	changes <- svc.Status{
		State:   svc.Running,
		Accepts: svc.AcceptStop | svc.AcceptShutdown,
	}

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
				time.Sleep(100 * time.Millisecond)
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				// Signal the server to stop via SIGTERM equivalent
				p, _ := os.FindProcess(os.Getpid())
				p.Signal(os.Interrupt)
				return false, 0
			}
		case <-done:
			return false, 0
		}
	}
}

func installService() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not determine executable path: %w", err)
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("could not connect to service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err == nil {
		s.Close()
		return fmt.Errorf("service %s already exists", serviceName)
	}

	s, err = m.CreateService(serviceName, exePath, mgr.Config{
		DisplayName: serviceDisplayName,
		Description: serviceDescription,
		StartType:   mgr.StartAutomatic,
	})
	if err != nil {
		return fmt.Errorf("could not create service: %w", err)
	}
	defer s.Close()

	// Create event log source
	err = eventlog.InstallAsEventCreate(serviceName, eventlog.Error|eventlog.Warning|eventlog.Info)
	if err != nil {
		s.Delete()
		return fmt.Errorf("could not install event log: %w", err)
	}

	return nil
}

func uninstallService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("could not connect to service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %s not found: %w", serviceName, err)
	}
	defer s.Close()

	if err := s.Delete(); err != nil {
		return fmt.Errorf("could not delete service: %w", err)
	}

	_ = eventlog.Remove(serviceName)

	return nil
}

func printPlatformHelp() {
	fmt.Println("  install   Install as a Windows service")
	fmt.Println("  uninstall Uninstall the Windows service")
}
