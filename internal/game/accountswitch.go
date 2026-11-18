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

// SwitchSlotMax is how many villages one emulator's Supercell ID list holds.
// accounts.MaxSlot mirrors it; the switch rejects anything outside 1..SwitchSlotMax.
const SwitchSlotMax = 5

// Template matching settings shared by the runtime switch and the capture
// helper, so "verified at the runtime threshold" means the same matcher with
// the same scale ladder — not a looser check in the helper.
const (
	switchScaleMin   = 0.85
	switchScaleMax   = 1.15
	switchScaleSteps = 7
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

// switchRuntimeTemplates are the templates the switch taps. acct_profile is
// installed too (it records the village HUD at capture time) but is not a tap
// target: the banner carries the player's name, which changes with the account.
// The HUD corner is addressed by geometry instead — see ProfileTapPoint.
var switchRuntimeTemplates = []string{AcctSettingsTemplate, AcctSupercellTemplate, AcctSlotListTemplate}

// ProfileTapPoint is where the switch taps to open the profile: the level badge
// in the top-left HUD corner. It is geometry, not a template, because the
// banner's pixels contain the current village's name.
func ProfileTapPoint(width, height int) image.Point {
	return image.Pt(int(math.Round(0.040*float64(width))), int(math.Round(0.065*float64(height))))
}

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
	// 0 is legal: the capture helper anchors on account row 1 itself, so the
	// first row sits exactly at the anchor's centre.
	if c.SlotRow1Offset < 0 || c.SlotRow1Offset >= 1 {
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
		len(switchRuntimeTemplates), len(e.Missing), e.Dir)
	missing := make(map[string]bool, len(e.Missing))
	for _, name := range e.Missing {
		missing[name] = true
	}
	for _, h := range switchTemplateHelp {
		if missing[h.Name] {
			fmt.Fprintf(&b, "  %-16s — %s\n", h.Name, h.What)
		}
	}
	b.WriteString("install them in one command against the running emulator:\n")
	b.WriteString("  go run ./cmd/account_templates\n")
	b.WriteString("It drives profile → Settings → Supercell ID itself, verifies every crop,\n")
	b.WriteString("derives the row geometry, and refuses anything that would fail later.")
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
		return &MissingTemplatesError{Dir: s.cfg.TemplateDir, Missing: switchRuntimeTemplates}
	}
	var missing []string
	for _, name := range switchRuntimeTemplates {
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
	if slot < 1 || slot > SwitchSlotMax {
		return Identity{}, fmt.Errorf("Supercell ID slot must be 1-%d, got %d", SwitchSlotMax, slot)
	}
	if err := s.CheckTemplates(); err != nil {
		return Identity{}, err
	}
	if strings.TrimSpace(expect.Tag) == "" && strings.TrimSpace(expect.Name) == "" {
		return Identity{}, fmt.Errorf("account has neither a player tag nor a name, so the switch cannot verify which village is logged in — set either on the account")
	}

	if err := s.tapProfile("open the profile"); err != nil {
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

	// The game reloads the village after a slot tap and the profile panel can
	// take a while to draw, so the identity read is retried: tap the banner,
	// OCR the profile, and only then decide. A readable tag that is not the
	// expected one is a mismatch immediately; an unreadable screen means "still
	// loading" and is retried until the timeout.
	observed, err := s.waitIdentity(slot, expect)
	if err != nil {
		return observed, err
	}
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

// tapProfile opens the profile by tapping the level badge, which is the same
// control in every village (the banner's text is not).
func (s *AccountSwitcher) tapProfile(what string) error {
	screen, err := s.dev.CaptureToMat()
	if err != nil {
		return fmt.Errorf("capture screen before opening the profile: %w", err)
	}
	w, h := screen.Cols(), screen.Rows()
	screen.Close()
	point := ProfileTapPoint(w, h)
	if err := s.dev.TapRandomized(point.X, point.Y); err != nil {
		return fmt.Errorf("tap %s at (%d,%d): %w", what, point.X, point.Y, err)
	}
	s.log.Info().Str("step", what).Int("x", point.X).Int("y", point.Y).Msg("account switch: tapped profile corner")
	s.sleep(s.cfg.settle())
	return nil
}

// waitIdentity taps the banner and OCRs the profile until the village says who
// it is, or the timeout expires.
func (s *AccountSwitcher) waitIdentity(slot int, expect Identity) (Identity, error) {
	reader := s.readIdentity
	if reader == nil {
		reader = readIdentityFromScreen
	}
	deadline := time.Now().Add(s.cfg.timeout())
	var lastErr error
	for {
		if err := s.tapProfile("read the logged-in village"); err != nil {
			return Identity{}, err
		}
		screen, err := s.dev.CaptureToMat()
		if err != nil {
			lastErr = err
		} else {
			observed, rerr := reader(screen)
			screen.Close()
			if rerr == nil {
				_ = s.dev.Back() // close the profile; the bot wants the village
				if verr := verifyIdentity(expect, observed); verr != nil {
					var mismatch *IdentityMismatchError
					if errors.As(verr, &mismatch) {
						mismatch.Slot = slot
					}
					return observed, verr
				}
				s.log.Info().
					Int("slot", slot).
					Str("village", observed.String()).
					Msg("game switched to the expected village")
				return observed, nil
			}
			lastErr = rerr
		}
		if time.Now().After(deadline) {
			break
		}
		s.sleep(s.cfg.poll())
	}
	return Identity{}, fmt.Errorf("slot %d was tapped but the profile never showed a readable village within %s (last read error: %v)", slot, s.cfg.timeout(), lastErr)
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
	y := SlotRowY(anchor.Point.Y, anchor.Height, slot, s.cfg)
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

	point, conf, found, err := MatchSwitchTemplate(screen, tpl, name, s.cfg)
	if err != nil || !found {
		return switchHit{}, false, err
	}
	return switchHit{Point: point, Conf: conf, Width: screen.Cols(), Height: screen.Rows()}, true, nil
}

// MatchSwitchTemplate runs the switch's matcher: the same scale ladder and
// threshold the runtime uses. The capture helper (accountcapture.go) calls it
// too, so "verified at the runtime threshold" cannot drift from the switch.
func MatchSwitchTemplate(screen, tpl gocv.Mat, name string, cfg SwitchConfig) (image.Point, float64, bool, error) {
	return MatchSwitchTemplateROI(screen, tpl, name, cfg, image.Rect(0, 0, screen.Cols(), screen.Rows()))
}

// MatchSwitchTemplateROI is MatchSwitchTemplate inside roi. Template capture
// verification searches around the crop it just took: TM_CCOEFF_NORMED reports
// near-1.0 scores on flat areas (zero variance), so a global search can prefer
// an empty patch over the real target.
func MatchSwitchTemplateROI(screen, tpl gocv.Mat, name string, cfg SwitchConfig, roi image.Rectangle) (image.Point, float64, bool, error) {
	if screen.Cols() < 1 || screen.Rows() < 1 || tpl.Cols() < 1 || tpl.Rows() < 1 {
		return image.Point{}, 0, false, fmt.Errorf("empty screen or template")
	}
	matches, err := vision.MatchMultiScaleROICached(screen, tpl, name, switchScaleMin, switchScaleMax, switchScaleSteps,
		float32(cfg.Threshold), roi)
	if err != nil {
		return image.Point{}, 0, false, err
	}
	if len(matches) == 0 {
		return image.Point{}, 0, false, nil
	}
	return matches[0].Point, matches[0].Confidence, true, nil
}

// SlotRowY is the y the switch taps for slot: the account-list anchor's centre
// plus slot_row1_offset plus (slot-1) * slot_row_spacing, in physical pixels.
// The capture helper derives those fractions from the live panel and validates
// the resulting rows with this same function.
func SlotRowY(anchorCentreY, screenHeight, slot int, cfg SwitchConfig) int {
	if screenHeight <= 0 {
		return anchorCentreY
	}
	return anchorCentreY + int(math.Round((cfg.SlotRow1Offset+float64(slot-1)*cfg.SlotRowSpacing)*float64(screenHeight)))
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
	regions, err := OCRSwitchText(screen)
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
