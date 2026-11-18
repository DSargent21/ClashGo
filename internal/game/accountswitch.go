package game

// accountswitch.go drives Clash of Clans' own account switcher so a fleet can
// share ONE emulator:
//
//	player banner (top left) → Settings → Supercell ID → account row
//
// Nothing about Supercell ID is ever stored or typed: the fast switcher lists
// villages that are already signed in on the device, and this code only taps
// the row for the requested slot. After the tap it reads the profile screen and
// refuses to hand control to the bot unless the logged-in village matches the
// account the operator expected — a slot tap that lands one row off must fail
// loudly instead of farming a different village.
//
// Navigation is template matching, not fixed coordinates, so it survives
// resolution and density changes. The four templates are captured once from a
// running emulator with `cmd/account_templates`; when one is missing the switch
// fails with the exact names and the capture command, never with a guess.

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// Template names the in-game switcher needs. Kept next to the capture helper
// (cmd/account_templates) and docs/ACCOUNTS.md; renaming one without the other
// is what turns a setup screen into a mystery.
const (
	AcctProfileTemplate   = "acct_profile"
	AcctSettingsTemplate  = "acct_settings"
	AcctSupercellTemplate = "acct_supercell"
	AcctSlotListTemplate  = "acct_slot_list"
)

// switchTemplateHelp is what each missing PNG has to show, in operator words.
var switchTemplateHelp = []struct {
	Name string
	What string
}{
	{AcctProfileTemplate, "the player banner button at the top left of the main village"},
	{AcctSettingsTemplate, "the gear that opens Settings, on the profile screen"},
	{AcctSupercellTemplate, "the \"Supercell ID\" row in Settings"},
	{AcctSlotListTemplate, "the account list panel that opens from that row (its header is enough)"},
}

var accountSwitchTemplateNames = func() []string {
	names := make([]string, 0, len(switchTemplateHelp))
	for _, h := range switchTemplateHelp {
		names = append(names, h.Name)
	}
	return names
}()

// SwitchConfig tunes the navigation. Defaults work for a 1280x720 BlueStacks
// instance; every value is overridable because slot geometry is the one thing
// that changes between game layouts.
type SwitchConfig struct {
	// TemplateDir defaults to <assets>/templates.
	TemplateDir string `json:"template_dir"`
	// SlotRow1Offset is where the first account row's centre sits, as a
	// fraction of screen height below the account-list anchor's centre.
	SlotRow1Offset float64 `json:"slot_row1_offset"`
	// SlotRowSpacing is the distance between consecutive account rows, as a
	// fraction of screen height.
	SlotRowSpacing float64 `json:"slot_row_spacing"`
	SettleMS       int     `json:"settle_ms"`
	TimeoutMS      int     `json:"timeout_ms"`
	PollMS         int     `json:"poll_ms"`
	Threshold      float64 `json:"threshold"`
}

// DefaultSwitchConfig returns the shipped defaults.
func DefaultSwitchConfig() SwitchConfig {
	return SwitchConfig{
		SlotRow1Offset: 0.16,
		SlotRowSpacing: 0.075,
		SettleMS:       1200,
		TimeoutMS:      20000,
		PollMS:         350,
		Threshold:      0.80,
	}
}

// LoadSwitchConfig reads path, falling back to defaults when it does not
// exist. A present-but-broken file is reported so an operator who tried to
// tune the row geometry is not silently ignored.
func LoadSwitchConfig(path string) (SwitchConfig, error) {
	cfg := DefaultSwitchConfig()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return DefaultSwitchConfig(), fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.normalize()
	return cfg, nil
}

func (c *SwitchConfig) normalize() {
	if c.TemplateDir == "" {
		c.TemplateDir = paths.Resolve("templates")
	}
	if c.SlotRow1Offset <= 0 || c.SlotRow1Offset >= 1 {
		c.SlotRow1Offset = DefaultSwitchConfig().SlotRow1Offset
	}
	if c.SlotRowSpacing <= 0 || c.SlotRowSpacing >= 1 {
		c.SlotRowSpacing = DefaultSwitchConfig().SlotRowSpacing
	}
	if c.SettleMS < 0 {
		c.SettleMS = 0
	}
	if c.TimeoutMS <= 0 {
		c.TimeoutMS = DefaultSwitchConfig().TimeoutMS
	}
	if c.PollMS <= 0 {
		c.PollMS = DefaultSwitchConfig().PollMS
	}
	if c.Threshold <= 0 || c.Threshold > 1 {
		c.Threshold = DefaultSwitchConfig().Threshold
	}
}

func (c SwitchConfig) settle() time.Duration { return time.Duration(c.SettleMS) * time.Millisecond }
func (c SwitchConfig) timeout() time.Duration {
	return time.Duration(c.TimeoutMS) * time.Millisecond
}
func (c SwitchConfig) poll() time.Duration { return time.Duration(c.PollMS) * time.Millisecond }

// Identity is who the game says it is logged in as.
type Identity struct {
	Name string
	Tag  string
}

// String renders the identity the way a log line or a dialog should.
func (i Identity) String() string {
	switch {
	case i.Tag != "" && i.Name != "":
		return fmt.Sprintf("%s (%s)", i.Name, i.Tag)
	case i.Tag != "":
		return i.Tag
	case i.Name != "":
		return i.Name
	default:
		return "an unidentified village"
	}
}

// MissingTemplatesError is the actionable failure for a first run: it names
// every PNG that is absent and the one command that captures them.
type MissingTemplatesError struct {
	Dir     string
	Missing []string
}

func (e *MissingTemplatesError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "in-game account switching needs %d template PNGs and %d are missing from %s:\n",
		len(switchTemplateHelp), len(e.Missing), e.Dir)
	missing := make(map[string]bool, len(e.Missing))
	for _, name := range e.Missing {
		missing[name] = true
	}
	for _, h := range switchTemplateHelp {
		if missing[h.Name] {
			fmt.Fprintf(&b, "  %-16s — %s\n", h.Name, h.What)
		}
	}
	b.WriteString("capture them once from a running emulator, in this order:\n")
	b.WriteString("  1. village screen:            go run ./cmd/account_templates\n")
	b.WriteString("     (saves the screenshot and prints the exact crop commands)\n")
	b.WriteString("  2. crop each template:        go run ./cmd/account_templates -profile x1,y1,x2,y2 -settings x1,y1,x2,y2 \\\n")
	b.WriteString("                                   -supercell x1,y1,x2,y2 -slot-list x1,y1,x2,y2\n")
	b.WriteString("  3. verify:                    re-run the switch; a wrong row aborts with the actual village")
	return b.String()
}

// IdentityMismatchError is returned when the slot tap logged the game into a
// different village than the account expects. Costing a start is the point:
// farming a mis-attributed village is worse than doing nothing.
type IdentityMismatchError struct {
	Slot     int
	Expected Identity
	Observed Identity
}

func (e *IdentityMismatchError) Error() string {
	return fmt.Sprintf("slot %d logged the game in as %s, but this account expects %s — nothing was started; correct the slot or the player tag/name and try again",
		e.Slot, e.Observed, e.Expected)
}

// AccountSwitcher drives one emulator's in-game account switcher.
type AccountSwitcher struct {
	dev       Device
	templates *TemplateStore
	cfg       SwitchConfig
	log       zerolog.Logger

	// readIdentity is the seam tests replace. nil means "OCR the profile
	// screenshot", which needs the macOS Vision helper.
	readIdentity func(gocv.Mat) (Identity, error)
}

// NewAccountSwitcher builds a switcher for one device. templates must already
// be loaded (the app builds it from SwitchConfig.TemplateDir).
func NewAccountSwitcher(dev Device, templates *TemplateStore, cfg SwitchConfig, log zerolog.Logger) *AccountSwitcher {
	cfg.normalize()
	return &AccountSwitcher{
		dev:       dev,
		templates: templates,
		cfg:       cfg,
		log:       log.With().Str("component", "account-switch").Logger(),
	}
}

// SetIdentityReader overrides how the profile screen is read. Tests use it to
// prove the mismatch path without an OCR toolchain.
func (s *AccountSwitcher) SetIdentityReader(fn func(gocv.Mat) (Identity, error)) {
	s.readIdentity = fn
}

// Config exposes the normalized settings (tests and logs read the geometry).
func (s *AccountSwitcher) Config() SwitchConfig { return s.cfg }

// CheckTemplates reports the missing capture PNGs, if any.
func (s *AccountSwitcher) CheckTemplates() error {
	if s.templates == nil {
		return &MissingTemplatesError{Dir: s.cfg.TemplateDir, Missing: accountSwitchTemplateNames}
	}
	var missing []string
	for _, name := range accountSwitchTemplateNames {
		if _, ok := s.templates.Get(name); !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return &MissingTemplatesError{Dir: s.templates.Dir(), Missing: missing}
	}
	return nil
}

// Switch taps through the game's account switcher to slot and returns the
// identity the profile screen shows. On a mismatch the game is left on that
// village (the caller decides whether to switch back) and the error carries
// both identities.
func (s *AccountSwitcher) Switch(slot int, expect Identity) (Identity, error) {
	if slot < 1 || slot > 5 {
		return Identity{}, fmt.Errorf("Supercell ID slot must be 1-5, got %d", slot)
	}
	if err := s.CheckTemplates(); err != nil {
		return Identity{}, err
	}
	if strings.TrimSpace(expect.Tag) == "" && strings.TrimSpace(expect.Name) == "" {
		return Identity{}, fmt.Errorf("account has neither a player tag nor a name, so the switch cannot verify which village is logged in — set either on the account")
	}

	if err := s.tapTemplate(AcctProfileTemplate, "player profile"); err != nil {
		return Identity{}, err
	}
	if err := s.tapTemplate(AcctSettingsTemplate, "Settings"); err != nil {
		return Identity{}, err
	}
	if err := s.tapTemplate(AcctSupercellTemplate, "Supercell ID"); err != nil {
		return Identity{}, err
	}
	if err := s.tapSlot(slot); err != nil {
		return Identity{}, err
	}

	// The game reloads the village after a slot tap. Waiting for the player
	// banner is how we know the switch finished rather than merely started.
	if _, err := s.waitFor(AcctProfileTemplate, "village after the slot tap"); err != nil {
		return Identity{}, fmt.Errorf("slot %d was tapped but the game did not return to a village: %w", slot, err)
	}
	if err := s.tapTemplate(AcctProfileTemplate, "player profile"); err != nil {
		return Identity{}, err
	}
	screen, err := s.dev.CaptureToMat()
	if err != nil {
		return Identity{}, fmt.Errorf("capture profile screen: %w", err)
	}
	defer screen.Close()

	reader := s.readIdentity
	if reader == nil {
		reader = readIdentityFromScreen
	}
	observed, err := reader(screen)
	_ = s.dev.Back() // close the profile; the bot wants the village
	if err != nil {
		return Identity{}, fmt.Errorf("could not read which village is logged in: %w", err)
	}
	if err := verifyIdentity(expect, observed); err != nil {
		var mismatch *IdentityMismatchError
		if errors.As(err, &mismatch) {
			mismatch.Slot = slot
		}
		return observed, err
	}
	s.log.Info().
		Int("slot", slot).
		Str("village", observed.String()).
		Msg("game switched to the expected village")
	return observed, nil
}

// tapTemplate waits for name and taps its centre.
func (s *AccountSwitcher) tapTemplate(name, what string) error {
	hit, err := s.waitFor(name, what)
	if err != nil {
		return err
	}
	if err := s.dev.TapRandomized(hit.Point.X, hit.Point.Y); err != nil {
		return fmt.Errorf("tap %s at (%d,%d): %w", what, hit.Point.X, hit.Point.Y, err)
	}
	s.log.Info().Str("step", what).Int("x", hit.Point.X).Int("y", hit.Point.Y).Float64("conf", hit.Conf).Msg("account switch: tapped")
	s.sleep(s.cfg.settle())
	return nil
}

// tapSlot taps the account row for slot, derived from the account-list anchor:
// rows are laid out at a fixed pitch, so one anchor plus two fractions covers
// all five without needing five near-identical templates that the matcher
// could confuse with each other.
func (s *AccountSwitcher) tapSlot(slot int) error {
	anchor, err := s.waitFor(AcctSlotListTemplate, "Supercell ID account list")
	if err != nil {
		return err
	}
	y := anchor.Point.Y + int(math.Round((s.cfg.SlotRow1Offset+float64(slot-1)*s.cfg.SlotRowSpacing)*float64(anchor.Height)))
	if y < 0 || y >= anchor.Height {
		return fmt.Errorf("slot %d lands at y=%d, outside the %dx%d screen: adjust slot_row1_offset/slot_row_spacing in account_switch.json (currently %.3f/%.3f)",
			slot, y, anchor.Width, anchor.Height, s.cfg.SlotRow1Offset, s.cfg.SlotRowSpacing)
	}
	if err := s.dev.TapRandomized(anchor.Point.X, y); err != nil {
		return fmt.Errorf("tap slot %d at (%d,%d): %w", slot, anchor.Point.X, y, err)
	}
	s.log.Info().Int("slot", slot).Int("x", anchor.Point.X).Int("y", y).Msg("account switch: tapped slot row")
	s.sleep(s.cfg.settle())
	return nil
}

type switchHit struct {
	Point  image.Point
	Conf   float64
	Width  int
	Height int
}

func (s *AccountSwitcher) waitFor(name, what string) (switchHit, error) {
	deadline := time.Now().Add(s.cfg.timeout())
	var lastErr error
	for {
		hit, ok, err := s.findTemplate(name)
		if err != nil {
			lastErr = err
		} else if ok {
			return hit, nil
		}
		if time.Now().After(deadline) {
			break
		}
		s.sleep(s.cfg.poll())
	}
	hint := ""
	if lastErr != nil {
		hint = fmt.Sprintf(" (last capture error: %v)", lastErr)
	}
	return switchHit{}, fmt.Errorf("could not find %q (%s) on screen within %s%s — keep the game on the main village screen and recapture it with `go run ./cmd/account_templates`",
		name, what, s.cfg.timeout(), hint)
}

func (s *AccountSwitcher) findTemplate(name string) (switchHit, bool, error) {
	tpl, ok := s.templates.Get(name)
	if !ok || tpl.Empty() {
		return switchHit{}, false, fmt.Errorf("template %q is not loaded", name)
	}
	screen, err := s.dev.CaptureToMat()
	if err != nil {
		return switchHit{}, false, err
	}
	defer screen.Close()

	matches, err := vision.MatchMultiScaleROICached(screen, tpl, name, 0.85, 1.15, 7, float32(s.cfg.Threshold),
		image.Rect(0, 0, screen.Cols(), screen.Rows()))
	if err != nil || len(matches) == 0 {
		return switchHit{}, false, err
	}
	best := matches[0]
	return switchHit{Point: best.Point, Conf: best.Confidence, Width: screen.Cols(), Height: screen.Rows()}, true, nil
}

func (s *AccountSwitcher) sleep(d time.Duration) {
	if d > 0 {
		time.Sleep(d)
	}
}

// verifyIdentity compares the profile screen against the account. A tag is
// authoritative; a name is the fallback for profiles without one.
func verifyIdentity(expect, observed Identity) error {
	wantTag := normalizePlayerTag(expect.Tag)
	gotTag := normalizePlayerTag(observed.Tag)
	if wantTag != "" {
		if gotTag != wantTag {
			return &IdentityMismatchError{Expected: expect, Observed: observed}
		}
		return nil
	}
	wantName := strings.TrimSpace(expect.Name)
	if wantName == "" || !strings.Contains(strings.ToLower(observed.Name), strings.ToLower(wantName)) {
		return &IdentityMismatchError{Expected: expect, Observed: observed}
	}
	return nil
}

var profileTagRe = regexp.MustCompile(`#[0-9A-Z]{3,15}`)

func normalizePlayerTag(tag string) string {
	tag = strings.ToUpper(strings.Join(strings.Fields(tag), ""))
	if tag == "" {
		return ""
	}
	if !strings.HasPrefix(tag, "#") {
		tag = "#" + tag
	}
	return tag
}

// readIdentityFromScreen OCRs a captured profile screen. The tag is the first
// tag-shaped token anywhere on it; the name is the text in the top strip, which
// is where the game paints it on the profile card.
func readIdentityFromScreen(screen gocv.Mat) (Identity, error) {
	if screen.Empty() {
		return Identity{}, fmt.Errorf("empty profile screenshot")
	}
	path := filepath.Join(os.TempDir(), fmt.Sprintf("clashgo_switch_%d.png", time.Now().UnixNano()))
	if !gocv.IMWrite(path, screen) {
		return Identity{}, fmt.Errorf("write profile screenshot to %s", path)
	}
	defer func() { _ = os.Remove(path) }()

	regions, err := vision.OCRTextRegions(path)
	if err != nil {
		return Identity{}, fmt.Errorf("OCR helper unavailable (%v) — it needs tools/ocr.swift and Xcode command line tools", err)
	}
	var out Identity
	var nameParts []string
	top := screen.Rows() / 8
	for _, region := range regions {
		text := strings.TrimSpace(region.Text)
		if text == "" {
			continue
		}
		if tag := profileTagRe.FindString(strings.ToUpper(strings.Join(strings.Fields(text), ""))); tag != "" {
			if out.Tag == "" {
				out.Tag = tag
			}
			continue
		}
		if region.Center().Y <= top {
			nameParts = append(nameParts, text)
		}
	}
	if out.Tag == "" && len(nameParts) == 0 {
		return Identity{}, fmt.Errorf("no player tag or name found on the profile screen — open the profile by hand and check the capture templates")
	}
	out.Name = strings.Join(nameParts, " ")
	return out, nil
}
