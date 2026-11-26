package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDemoHelperProcess(t *testing.T) {
	if os.Getenv("ROOMPLAY_DEMO_HELPER") == "exit" {
		os.Exit(7)
	}
	if os.Getenv("ROOMPLAY_DEMO_HELPER") == "wait" {
		for {
			time.Sleep(time.Hour)
		}
	}
}

func testSupervisor(t *testing.T) *supervisor {
	t.Helper()
	root := t.TempDir()
	s := &supervisor{root: root, logs: root, exits: make(chan childExit, 6)}
	t.Cleanup(s.shutdown)
	return s
}

func TestReadinessFailsPromptlyWhenAChildExits(t *testing.T) {
	s := testSupervisor(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.start("failed", executable, []string{"-test.run=^TestDemoHelperProcess$"}, map[string]string{"ROOMPLAY_DEMO_HELPER": "exit"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = s.ready(ctx, "unreachable service", func() bool { return false })
	if err == nil || !strings.Contains(err.Error(), "failed exited") || !strings.Contains(err.Error(), "failed.log") {
		t.Fatalf("missing child failure/log context: %v", err)
	}
}

func TestShutdownStopsEveryOwnedChildAndReleasesLogs(t *testing.T) {
	s := testSupervisor(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two"} {
		if err := s.start(name, executable, []string{"-test.run=^TestDemoHelperProcess$"}, map[string]string{"ROOMPLAY_DEMO_HELPER": "wait"}); err != nil {
			t.Fatal(err)
		}
	}
	s.shutdown()
	s.shutdown()
	for _, p := range s.children {
		select {
		case <-p.done:
		default:
			t.Fatalf("%s is still running", p.name)
		}
		if err := os.Remove(filepath.Join(s.logs, p.name+".log")); err != nil {
			t.Fatal(err)
		}
	}
}
