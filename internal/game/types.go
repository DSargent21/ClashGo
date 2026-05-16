// Package game provides Clash of Clans screen state detection, navigation,
// and exploration. It uses pixel-based detection for speed with template
// matching as a fallback, and supports auto-calibration for any screen resolution.
//
// The central type is GameContext, which holds the current state and is safe
// for concurrent read access by all subsystems (training, attack, search).
//
// Auto-calibration:
//   - Call calibrator := game.NewCalibrator(client) on startup
//   - Call cal := calibrator.Calibrate() to detect screen dimensions
//   - Pass cal to NewClassifier and NewNavigator
//
// Detection loop:
//   for {
//       ctx.Detect(ctx, classifier)
//       select {
//       case t := <-ctx.StateChange:
//           // react to state transitions
//       case <-ctx.Done:
//           return
//       default:
//       }
//   }
package game

import (
	"fmt"
	"sync"
	"time"

	"gocv.io/x/gocv"
)

// GameState represents a specific screen in Clash of Clans.
type GameState int

const (
	StateUnknown GameState = iota
	StateMainVillage
	StateBuilderBase
	StateBattle
	StateBattleEnd
	StateArmyCamp
	StateSearchMap
	StateFindMatch
	StateSettings
	StateObstacleDialog
	StateGemDialog
	StateChatOpen
	StateShieldInfo
	StateReturnHome
	StateLoading
)

var stateNames = map[GameState]string{
	StateUnknown:        "Unknown",
	StateMainVillage:    "MainVillage",
	StateBuilderBase:    "BuilderBase",
	StateBattle:         "Battle",
	StateBattleEnd:       "BattleEnd",
	StateArmyCamp:        "ArmyCamp",
	StateSearchMap:       "SearchMap",
	StateFindMatch:       "FindMatch",
	StateSettings:        "Settings",
	StateObstacleDialog:  "ObstacleDialog",
	StateGemDialog:       "GemDialog",
	StateChatOpen:        "ChatOpen",
	StateShieldInfo:      "ShieldInfo",
	StateReturnHome:      "ReturnHome",
	StateLoading:         "Loading",
}

func (s GameState) String() string {
	if name, ok := stateNames[s]; ok {
		return name
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// TransitionAction describes how to navigate from one state to another.
type TransitionAction int

const (
	ActionTap TransitionAction = iota
	ActionBack
	ActionSwipe
	ActionHold
	ActionNone
)

func (a TransitionAction) String() string {
	switch a {
	case ActionTap:
		return "tap"
	case ActionBack:
		return "back"
	case ActionSwipe:
		return "swipe"
	case ActionHold:
		return "hold"
	case ActionNone:
		return "none"
	default:
		return "?"
	}
}

// StateTransition represents a possible navigation from one state to another.
type StateTransition struct {
	From      GameState
	To        GameState
	Action    TransitionAction
	X, Y      int
	X2, Y2    int // for swipe
	Duration  time.Duration
	Cost      time.Duration
	ElementID string
}

// Clickable represents a detected interactive element on screen.
type Clickable struct {
	Type      string // "button", "tab", "dialog", "resource"
	Region    Rectangle
	Center    Point
	Color     RGB
	Confidence float64
}

// Rectangle mirrors geo.Rect but uses int for screen coordinates.
type Rectangle struct {
	X1, Y1, X2, Y2 int
}

func (r Rectangle) Width() int  { return r.X2 - r.X1 }
func (r Rectangle) Height() int { return r.Y2 - r.Y1 }
func (r Rectangle) CenterX() int { return (r.X1 + r.X2) / 2 }
func (r Rectangle) CenterY() int { return (r.Y1 + r.Y2) / 2 }
func (r Rectangle) Contains(px, py int) bool {
	return px >= r.X1 && px <= r.X2 && py >= r.Y1 && py <= r.Y2
}

// Point is a 2D coordinate.
type Point struct {
	X, Y int
}

// RGB represents an RGB color.
type RGB struct {
	R, G, B uint8
}

func (c RGB) String() string {
	return fmt.Sprintf("RGB(%d,%d,%d)", c.R, c.G, c.B)
}

// Resources holds the player's current resource amounts.
type Resources struct {
	Gold       int
	Elixir     int
	DarkElixir int
	Trophies   int
}

// ScreenMetadata holds timing and quality info about a capture.
type ScreenMetadata struct {
	CapturedAt time.Time
	CaptureMs  time.Duration
	Width      int
	Height     int
	Sharp      bool // true if no blur detected
}

// SystemHealth tracks connectivity and performance.
type SystemHealth struct {
	ADBConnected     bool
	LastCapture      time.Time
	AvgCaptureMs     float64
	ConsecutiveFails int
}

func (h SystemHealth) IsHealthy() bool {
	return h.ADBConnected && h.ConsecutiveFails < 3
}

// StateChange represents a confirmed state transition event.
type StateChange struct {
	From      GameState
	To        GameState
	At        time.Time
	Duration  time.Duration // time spent in From state
}

// Device defines the interface for an automation device (like ADB).
type Device interface {
	Tap(x, y int) error
	TapRandomized(x, y int) error
	Swipe(x1, y1, x2, y2 int, ms int) error
	Pinch(x1, y1, x2, y2, x3, y3, x4, y4, ms int) error
	ZoomOut() error
	ZoomIn() error
	Hold(x, y int, ms int) error
	KeyEvent(code int) error
	Text(text string) error
	Back() error
	CaptureToMat() (gocv.Mat, error)
	Close() error
}

// Interrupt represents an unexpected overlay or dialog.
type Interrupt struct {
	Type    string // "gem", "update", "ban", "reward", "connection"
	State   GameState
	Recover string // recommended recovery action
}

// PixelCheck describes a single pixel verification rule.
type PixelCheck struct {
	X, Y      int
	R, G, B   uint8
	Tolerance int
}

// StateRule describes how to detect a specific game state.
type StateRule struct {
	State     GameState
	Checks    []PixelCheck
	Template  string // optional template name to match
	MinPass   int    // minimum checks that must pass
	Weight    int    // score boost when matched
	Priority  int    // higher = checked first
	Desc      string
}

// ExplorerConfig holds parameters for the auto-explorer.
type ExplorerConfig struct {
	MaxDepth       int
	SettleTime     time.Duration
	MinDiffPixels  int
	ClickJitter     int
	BackTimeout    time.Duration
	MaxRetries     int
}

func DefaultExplorerConfig() ExplorerConfig {
	return ExplorerConfig{
		MaxDepth:       3,
		SettleTime:     1500 * time.Millisecond,
		MinDiffPixels:  50,
		ClickJitter:    5,
		BackTimeout:    3 * time.Second,
		MaxRetries:     3,
	}
}

// ClassifierConfig holds detection parameters.
type ClassifierConfig struct {
	ColorTolerance  int
	TemplateThreshold float32
	ConfirmFrames   int
	RequireSharp    bool
}

func DefaultClassifierConfig() ClassifierConfig {
	return ClassifierConfig{
		ColorTolerance:    20,
		TemplateThreshold: 0.60,
		ConfirmFrames:     2,
		RequireSharp:      false,
	}
}

// NavigatorConfig holds navigation parameters.
type NavigatorConfig struct {
	TapJitter       int
	SettleTime      time.Duration
	MaxRetries     int
	InterruptDepth  int
}

func DefaultNavigatorConfig() NavigatorConfig {
	return NavigatorConfig{
		TapJitter:      5,
		SettleTime:    1200 * time.Millisecond,
		MaxRetries:    3,
		InterruptDepth: 5,
	}
}

type safeScreen struct {
	mu       sync.RWMutex
	mat      gocv.Mat
	at       time.Time
	hasMat   bool
}

func (s *safeScreen) Store(mat gocv.Mat) {
	s.mu.Lock()
	if s.hasMat && !s.mat.Closed() {
		s.mat.Close()
	}
	s.mat = mat
	s.at = time.Now()
	s.hasMat = true
	s.mu.Unlock()
}

func (s *safeScreen) Read() (gocv.Mat, time.Time, func()) {
	s.mu.RLock()
	mat := s.mat
	at := s.at
	return mat, at, s.mu.RUnlock
}

func (s *safeScreen) Close() {
	s.mu.Lock()
	if s.hasMat && !s.mat.Closed() {
		s.mat.Close()
	}
	s.hasMat = false
	s.mu.Unlock()
}

func absDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}