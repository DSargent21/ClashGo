package bot

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"

	"gocv.io/x/gocv"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
)

type mockADBServer struct {
	listener net.Listener
	mu       sync.Mutex
	step     int
	taps     []string
	swipes   []string
}

func startMockADB(t *testing.T) *mockADBServer {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}

	s := &mockADBServer{
		listener: l,
	}

	go s.run()
	return s
}

func (s *mockADBServer) run() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *mockADBServer) handle(conn net.Conn) {
	defer conn.Close()
	for {
		lenBuf := make([]byte, 4)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return
		}
		var length uint32
		fmt.Sscanf(string(lenBuf), "%04x", &length)
		payload := make([]byte, length)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return
		}
		req := string(payload)

		if _, err := conn.Write([]byte("OKAY")); err != nil {
			return
		}

		if req == "host:devices" {
			list := "mock-device\tdevice\n"
			resp := fmt.Sprintf("%04x%s", len(list), list)
			conn.Write([]byte(resp))
			return
		}

		if strings.HasPrefix(req, "host:transport:") {
			continue
		}

		if req == "shell:getprop sys.boot_completed" {
			conn.Write([]byte("1\n"))
			return
		}

		if req == "shell:wm size" {
			conn.Write([]byte("Physical size: 860x732\n"))
			return
		}

		if strings.HasPrefix(req, "shell:input tap ") {
			s.mu.Lock()
			s.taps = append(s.taps, req)
			// Transition steps depending on where we tap
			if s.step == 0 {
				s.step = 1 // After tapping builder head
			} else if s.step == 2 {
				s.step = 3 // After tapping wall suggestion
			} else if s.step == 3 {
				s.step = 4 // After tapping upgrade button
			} else if s.step == 4 {
				s.step = 5 // After confirming
			}
			s.mu.Unlock()
			conn.Write([]byte("OK\n"))
			return
		}

		if strings.HasPrefix(req, "shell:input swipe ") {
			s.mu.Lock()
			s.swipes = append(s.swipes, req)
			if s.step == 1 {
				s.step = 2 // After menu scroll
			}
			s.mu.Unlock()
			conn.Write([]byte("OK\n"))
			return
		}

		if req == "exec:/system/bin/screencap" {
			s.mu.Lock()
			step := s.step
			s.mu.Unlock()

			// Load the screenshot matching the current step
			var filename string
			switch step {
			case 0:
				filename = "before_builder_click.png"
			case 1:
				filename = "step1_after_builder_head.png"
			case 2:
				filename = "step2_after_menu_scroll.png"
			case 3:
				filename = "step3_after_wall_click.png"
			case 4:
				filename = "step4_after_upgrade_click.png"
			default:
				filename = "step5_after_confirm_click.png"
			}

			img := gocv.IMRead(filename, gocv.IMReadColor)
			if img.Empty() {
				// Fallback to black screen if image not found
				w, h := uint32(860), uint32(732)
				header := make([]byte, 12)
				binary.LittleEndian.PutUint32(header[0:4], w)
				binary.LittleEndian.PutUint32(header[4:8], h)
				binary.LittleEndian.PutUint32(header[8:12], 1)
				conn.Write(header)
				pixels := make([]byte, w*h*4)
				conn.Write(pixels)
				return
			}
			defer img.Close()

			// Convert to RGBA
			rgba := gocv.NewMat()
			defer rgba.Close()
			gocv.CvtColor(img, &rgba, gocv.ColorBGRToRGBA)

			w, h := uint32(rgba.Cols()), uint32(rgba.Rows())
			header := make([]byte, 12)
			binary.LittleEndian.PutUint32(header[0:4], w)
			binary.LittleEndian.PutUint32(header[4:8], h)
			binary.LittleEndian.PutUint32(header[8:12], 1)
			conn.Write(header)

			conn.Write(rgba.ToBytes())
			return
		}

		if strings.HasPrefix(req, "shell:") {
			conn.Write([]byte("OK\n"))
			return
		}
	}
}

func (s *mockADBServer) Close() {
	s.listener.Close()
}

func TestWallUpgradeSequence(t *testing.T) {
	// Change working directory to project root so that hardcoded relative paths (like "assets/templates") resolve correctly
	err := os.Chdir("../../")
	if err != nil {
		t.Fatalf("Failed to change directory to project root: %v", err)
	}

	s := startMockADB(t)
	defer s.Close()

	addr := s.listener.Addr().String()
	host, portStr, _ := net.SplitHostPort(addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	cfg := config.DefaultConfig()
	cfg.Device.ADBHost = host
	cfg.Device.ADBPort = port
	cfg.Device.DeviceID = "mock-device"
	cfg.Device.RestartOnStartup = false
	cfg.Upgrade.UpgradeWalls = true

	b, err := NewBot(cfg)
	if err != nil {
		t.Fatalf("Failed to create bot: %v", err)
	}
	defer b.Stop()

	gc := game.NewGameContext()
	
	// Run Wall Upgrade Sequence
	b.UpgradeWalls(gc)

	s.mu.Lock()
	finalStep := s.step
	tapsCount := len(s.taps)
	swipesCount := len(s.swipes)
	s.mu.Unlock()

	t.Logf("Test completed: step=%d, taps=%d, swipes=%d", finalStep, tapsCount, swipesCount)

	if finalStep < 5 {
		t.Errorf("Wall upgrade sequence did not complete successfully. End step: %d", finalStep)
	}
}
