// Demo builds and supervises a complete local RoomPlay installation.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type child struct {
	name, logPath string
	cmd           *exec.Cmd
	done          chan struct{}
}

type childExit struct {
	name    string
	err     error
	logPath string
}
type supervisor struct {
	root, bin, logs string
	children        []*child
	exits           chan childExit
}

func main() {
	simulation := flag.Bool("simulation", false, "run every room silently instead of using audio devices")
	noBrowser := flag.Bool("no-browser", false, "print the URL without opening a browser")
	webPort := flag.Int("web-port", 8080, "browser HTTP port")
	libraryPort := flag.Int("library-port", 8090, "music library HTTP port")
	brokerPort := flag.Int("broker-port", 4222, "NATS broker port")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *simulation, *noBrowser, *webPort, *libraryPort, *brokerPort); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "RoomPlay demo:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, simulation, noBrowser bool, webPort, libraryPort, brokerPort int) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	for _, path := range []string{"go.mod", "web/index.html", "assets/music/catalog.json"} {
		if _, err := os.Stat(filepath.Join(root, path)); err != nil {
			return fmt.Errorf("run this command from the RoomPlay repository folder: %w", err)
		}
	}
	ports := map[int]bool{}
	for _, port := range []int{webPort, libraryPort, brokerPort} {
		if port < 1 || port > 65535 || ports[port] {
			return fmt.Errorf("ports must be distinct numbers between 1 and 65535")
		}
		ports[port] = true
		listener, err := net.Listen("tcp", address(port))
		if err != nil {
			return fmt.Errorf("port %d is already in use; stop the previous demo or choose another port: %w", port, err)
		}
		listener.Close()
	}
	bin := filepath.Join(root, ".demo-bin")
	logRoot := filepath.Join(root, ".demo-logs")
	for _, path := range []string{bin, logRoot} {
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
	}
	logs, err := os.MkdirTemp(logRoot, "run-")
	if err != nil {
		return err
	}
	s := &supervisor{root: root, bin: bin, logs: logs, exits: make(chan childExit, 6)}
	defer s.shutdown()
	fmt.Println("Building RoomPlay (the first run downloads dependencies)...")
	for _, target := range []struct{ name, pkg string }{{"nats-server", "github.com/nats-io/nats-server/v2"}, {"library", "./cmd/library"}, {"coordinator", "./cmd/coordinator"}, {"player", "./cmd/player"}} {
		build := exec.CommandContext(ctx, "go", "build", "-o", s.executable(target.name), target.pkg)
		build.Dir = root
		build.Env = environment(map[string]string{"CGO_ENABLED": "0"})
		hideChild(build)
		output, err := build.CombinedOutput()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("build %s: %w\n%s", target.name, err, output)
		}
	}
	libraryURL := "http://" + address(libraryPort)
	base := "http://" + address(webPort)
	common := map[string]string{"NATS_URL": "nats://" + address(brokerPort), "LIBRARY_URL": libraryURL,
		"LIBRARY_ADDR": address(libraryPort), "LIBRARY_CATALOG": filepath.Join(root, "assets", "music", "catalog.json"),
		"LIBRARY_AUDIO_ROOT": filepath.Join(root, "assets", "music"), "COORDINATOR_ADDR": address(webPort), "WEB_ROOT": filepath.Join(root, "web")}
	if err := s.start("broker", s.executable("nats-server"), []string{"-a", "127.0.0.1", "-p", strconv.Itoa(brokerPort)}, common); err != nil {
		return err
	}
	if err := s.ready(ctx, "broker", func() bool {
		conn, err := net.DialTimeout("tcp", address(brokerPort), 200*time.Millisecond)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}); err != nil {
		return err
	}
	if err := s.start("library", s.executable("library"), nil, common); err != nil {
		return err
	}
	client := &http.Client{Timeout: time.Second}
	if err := s.ready(ctx, "library", func() bool {
		response, err := client.Get(libraryURL + "/tracks")
		if err != nil {
			return false
		}
		response.Body.Close()
		return response.StatusCode == 200
	}); err != nil {
		return err
	}
	if err := s.start("coordinator", s.executable("coordinator"), nil, common); err != nil {
		return err
	}
	for _, room := range []string{"bedroom", "kitchen", "livingroom"} {
		settings := map[string]string{}
		for key, value := range common {
			settings[key] = value
		}
		settings["ROOM_ID"] = room
		settings["ROOM_NAME"] = map[string]string{"bedroom": "Bedroom", "kitchen": "Kitchen", "livingroom": "Living Room"}[room]
		settings["PLAYER_MODE"] = "simulation"
		if !simulation {
			settings["PLAYER_MODE"] = "audio"
		}
		if err := s.start(room, s.executable("player"), nil, settings); err != nil {
			return err
		}
	}
	if err := s.ready(ctx, "three online rooms", func() bool {
		response, err := client.Get(base + "/api/v1/rooms")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		var result struct {
			Rooms []struct {
				ID, Availability string
				StateStale       bool `json:"state_stale"`
				State            json.RawMessage
			}
		}
		if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result) != nil {
			return false
		}
		fresh := map[string]bool{}
		for _, room := range result.Rooms {
			fresh[room.ID] = room.Availability == "online" && !room.StateStale && len(room.State) > 0 && string(room.State) != "null"
		}
		return fresh["bedroom"] && fresh["kitchen"] && fresh["livingroom"]
	}); err != nil {
		return err
	}
	fmt.Printf("\nRoomPlay is ready: %s\nLogs: %s\n", base, logs)
	if simulation {
		fmt.Println("All rooms are simulated (silent).")
	} else {
		fmt.Println("Every room has audio. Select any room and Play now; playing rooms overlap on the default output.")
	}
	fmt.Println("Keep this terminal open. Press Ctrl+C to stop all six services.")
	if !noBrowser {
		if err := openBrowser(ctx, base); err != nil {
			fmt.Println("Open the URL above in your browser:", err)
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case exited := <-s.exits:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return exitError(exited)
	}
}

func address(port int) string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }
func (s *supervisor) executable(name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(s.bin, name)
}

func environment(overrides map[string]string) []string {
	settings := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			if runtime.GOOS == "windows" {
				key = strings.ToUpper(key)
			}
			settings[key] = value
		}
	}
	for key, value := range overrides {
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		settings[key] = value
	}
	result := make([]string, 0, len(settings))
	for key, value := range settings {
		result = append(result, key+"="+value)
	}
	return result
}

func (s *supervisor) start(name, executable string, args []string, env map[string]string) error {
	path := filepath.Join(s.logs, name+".log")
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	cmd := exec.Command(executable, args...)
	cmd.Dir = s.root
	cmd.Env = environment(env)
	cmd.Stdout = file
	cmd.Stderr = file
	hideChild(cmd)
	if err := cmd.Start(); err != nil {
		file.Close()
		return fmt.Errorf("start %s: %w", name, err)
	}
	p := &child{name: name, logPath: path, cmd: cmd, done: make(chan struct{})}
	s.children = append(s.children, p)
	go func() { err := cmd.Wait(); file.Close(); close(p.done); s.exits <- childExit{name, err, path} }()
	return nil
}

func exitError(event childExit) error {
	return fmt.Errorf("%s exited (%v); see %s", event.name, event.err, event.logPath)
}
func (s *supervisor) ready(ctx context.Context, name string, check func() bool) error {
	deadline := time.NewTimer(25 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case exited := <-s.exits:
			return exitError(exited)
		default:
		}
		if check() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case exited := <-s.exits:
			return exitError(exited)
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for %s; see logs in %s", name, s.logs)
		case <-tick.C:
		}
	}
}

func (s *supervisor) shutdown() {
	for i := len(s.children) - 1; i >= 0; i-- {
		p := s.children[i]
		select {
		case <-p.done:
			continue
		default:
		}
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

func openBrowser(parent context.Context, url string) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", url)
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32.exe", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", url)
	}
	hideChild(cmd)
	return cmd.Run()
}
