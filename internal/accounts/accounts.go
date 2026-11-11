// Package accounts owns the set of Clash of Clans accounts ClashGO can
// operate. Each account is a device-bound profile: its own ADB serial, its
// own config, stats and attack history under <configDir>/accounts/<id>.
//
// Why device-bound rather than credential-bound: Supercell ID's only
// supported way to run several villages is either a separate game instance
// per village or its in-game fast switcher. Automating the login form would
// mean storing Supercell credentials and replaying a flow with email
// verification — a security and ToS liability for no reliability gain. An
// emulator instance that is already signed in is deterministic, and the
// switch is just "stop this bot, target that serial". See docs/ACCOUNTS.md.
package accounts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/paths"
)

const (
	// RegistryVersion is bumped when the on-disk schema changes in a way
	// that needs migration. Readers tolerate unknown fields.
	RegistryVersion = 1
	// DefaultAccountID is the account bootstrapped from the legacy
	// single-profile config on first launch.
	DefaultAccountID = "default"
	// DefaultCooldownMinutes is the pause after an account's session so a
	// rotation does not hammer the same village back-to-back.
	DefaultCooldownMinutes = 10
	// DefaultAttacksPerAccount keeps rotation sessions finite. Without a
	// cap a rotated account would never hand the emulator back.
	DefaultAttacksPerAccount = 20

	OrderRoundRobin = "round_robin"
	OrderPriority   = "priority"
)

var (
	playerTagRe = regexp.MustCompile(`^#[0-9A-Z]{3,15}$`)
	slugStripRe = regexp.MustCompile(`[^a-z0-9]+`)
)

// Device is the emulator binding for one account. Host/port address the
// local adb-server; DeviceID is the serial BlueStacks/MuMu exposes, e.g.
// "localhost:5556" for the second instance.
type Device struct {
	ADBHost  string `json:"adb_host"`
	ADBPort  int    `json:"adb_port"`
	DeviceID string `json:"device_id"`
}

// Normalized fills defaults and trims values.
func (d Device) Normalized() Device {
	d.ADBHost = strings.TrimSpace(d.ADBHost)
	d.DeviceID = strings.TrimSpace(d.DeviceID)
	if d.ADBHost == "" {
		d.ADBHost = "127.0.0.1"
	}
	if d.ADBPort <= 0 {
		d.ADBPort = 5037
	}
	return d
}

// Usage is the per-day activity counters rotation uses for its caps. Date
// is the local calendar day (YYYY-MM-DD) the counters belong to; a new day
// resets them lazily.
type Usage struct {
	Date     string `json:"date"`
	Attacks  int    `json:"attacks"`
	Switches int    `json:"switches"`
}

// Account is one village profile.
type Account struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	PlayerTag string `json:"player_tag,omitempty"`
	TownHall  int    `json:"town_hall,omitempty"`
	// Email is a login HINT for the operator ("which address is this
	// village on"), never a credential. ClashGO deliberately stores no
	// Supercell password or session token.
	Email   string `json:"email,omitempty"`
	Notes   string `json:"notes,omitempty"`
	Device  Device `json:"device"`
	Enabled bool   `json:"enabled"`

	// Priority is used by the priority rotation order; higher wins.
	Priority int `json:"priority"`
	// CooldownMinutes is the minimum gap before rotation picks this
	// account again after a session.
	CooldownMinutes int `json:"cooldown_minutes"`
	// MaxAttacksPerDay is a rotation filter (0 = unlimited). When the
	// bot runs past it mid-session the coordinator also stops the
	// session so the next account can run.
	MaxAttacksPerDay int `json:"max_attacks_per_day"`
	// MaxAttacksPerSession overrides the global attack cap for this
	// account (0 = use the rotation default).
	MaxAttacksPerSession int `json:"max_attacks_per_session"`

	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
	Usage      Usage     `json:"usage"`
}

// Input is the create/update payload from the GUI and CLI. ID is honored on
// create when supplied (used by import); updates keep the existing ID.
type Input struct {
	ID                   string `json:"id,omitempty"`
	Name                 string `json:"name"`
	PlayerTag            string `json:"player_tag"`
	TownHall             int    `json:"town_hall"`
	Email                string `json:"email"`
	Notes                string `json:"notes"`
	Device               Device `json:"device"`
	Enabled              bool   `json:"enabled"`
	Priority             int    `json:"priority"`
	CooldownMinutes      int    `json:"cooldown_minutes"`
	MaxAttacksPerDay     int    `json:"max_attacks_per_day"`
	MaxAttacksPerSession int    `json:"max_attacks_per_session"`
}

// Rotation is the fleet policy. AttacksPerAccount is the fallback session
// cap used whenever Enabled so each account actually hands over.
type Rotation struct {
	Enabled           bool   `json:"enabled"`
	Order             string `json:"order"`
	AutoStart         bool   `json:"auto_start"`
	AttacksPerAccount int    `json:"attacks_per_account"`
}

func (r Rotation) Normalized() Rotation {
	if r.Order != OrderPriority {
		r.Order = OrderRoundRobin
	}
	if r.AttacksPerAccount <= 0 {
		r.AttacksPerAccount = DefaultAttacksPerAccount
	}
	return r
}

// Registry is the accounts.json document.
type Registry struct {
	Version  int       `json:"version"`
	ActiveID string    `json:"active_id"`
	Rotation Rotation  `json:"rotation"`
	Accounts []Account `json:"accounts"`
}

// Store is a concurrency-safe registry backed by one atomic-write file.
// Save keeps a .bak of the previous document; a corrupt file is surfaced,
// never silently replaced.
type Store struct {
	mu   sync.RWMutex
	path string
	dir  string
	reg  Registry
}

// Open loads (or bootstraps) the registry in dir. On first launch it
// creates the "default" account from the existing config.json and copies
// that config into the account's profile tree, so upgrading users keep
// their settings exactly as they were.
func Open(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, "accounts.json"), dir: dir}

	data, err := os.ReadFile(s.path)
	switch {
	case err == nil:
		if uerr := json.Unmarshal(data, &s.reg); uerr != nil {
			return nil, fmt.Errorf("parse %s: %w (file left untouched)", s.path, uerr)
		}
		s.reg = normalizeRegistry(s.reg, dir)
	case os.IsNotExist(err):
		s.reg = bootstrapRegistry(dir)
	default:
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}

	for _, a := range s.reg.Accounts {
		if err := os.MkdirAll(paths.ProfileDir(a.ID), 0o755); err != nil {
			return nil, fmt.Errorf("create profile for %s: %w", a.ID, err)
		}
	}
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// Path is the registry file, exposed for logs and diagnostics.
func (s *Store) Path() string { return s.path }

// List returns a copy of every account in registry order.
func (s *Store) List() []Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Account, len(s.reg.Accounts))
	copy(out, s.reg.Accounts)
	return out
}

// Get returns one account by ID.
func (s *Store) Get(id string) (Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.reg.Accounts {
		if a.ID == id {
			return a, true
		}
	}
	return Account{}, false
}

// Active returns the account currently selected.
func (s *Store) Active() (Account, bool) { return s.Get(s.ActiveID()) }

// ActiveID returns the selected account ID.
func (s *Store) ActiveID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reg.ActiveID
}

// Rotation returns the current fleet policy.
func (s *Store) Rotation() Rotation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reg.Rotation
}

// Create validates input, allocates a unique ID, materializes the profile
// tree (copying the active account's config as the template) and persists.
func (s *Store) Create(in Input) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateInput(in); err != nil {
		return Account{}, err
	}
	if in.PlayerTag != "" {
		if err := s.uniqueTagLocked(in.PlayerTag, ""); err != nil {
			return Account{}, err
		}
	}
	if err := s.uniqueDeviceLocked(in.Device, ""); err != nil {
		return Account{}, err
	}

	id := sanitizeID(in.ID)
	if id == "" {
		id = sanitizeID(in.Name)
	}
	if id == "" {
		id = "account"
	}
	id = s.uniqueIDLocked(id)

	now := time.Now()
	a := Account{
		ID:                   id,
		Name:                 strings.TrimSpace(in.Name),
		PlayerTag:            normalizeTag(in.PlayerTag),
		TownHall:             in.TownHall,
		Email:                strings.TrimSpace(in.Email),
		Notes:                strings.TrimSpace(in.Notes),
		Device:               in.Device.Normalized(),
		Enabled:              in.Enabled,
		Priority:             clamp(in.Priority, 0, 1000),
		CooldownMinutes:      clamp(in.CooldownMinutes, 0, 1440),
		MaxAttacksPerDay:     max0(in.MaxAttacksPerDay),
		MaxAttacksPerSession: max0(in.MaxAttacksPerSession),
		CreatedAt:            now,
		Usage:                Usage{Date: day(now)},
	}

	template := s.templateConfigLocked()
	if err := writeProfileConfig(id, a.Device, template); err != nil {
		return Account{}, err
	}

	s.reg.Accounts = append(s.reg.Accounts, a)
	if s.reg.ActiveID == "" {
		s.reg.ActiveID = a.ID
	}
	if err := s.saveLocked(); err != nil {
		s.reg.Accounts = s.reg.Accounts[:len(s.reg.Accounts)-1]
		return Account{}, err
	}
	return a, nil
}

// Update changes an existing account. The ID is immutable; device changes
// take effect on the next Start (config stays as the user's tuning).
func (s *Store) Update(id string, in Input) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateInput(in); err != nil {
		return Account{}, err
	}
	idx := s.indexLocked(id)
	if idx < 0 {
		return Account{}, fmt.Errorf("account %q not found", id)
	}
	if in.PlayerTag != "" {
		if err := s.uniqueTagLocked(in.PlayerTag, id); err != nil {
			return Account{}, err
		}
	}
	if err := s.uniqueDeviceLocked(in.Device, id); err != nil {
		return Account{}, err
	}

	a := s.reg.Accounts[idx]
	a.Name = strings.TrimSpace(in.Name)
	a.PlayerTag = normalizeTag(in.PlayerTag)
	a.TownHall = in.TownHall
	a.Email = strings.TrimSpace(in.Email)
	a.Notes = strings.TrimSpace(in.Notes)
	a.Device = in.Device.Normalized()
	a.Enabled = in.Enabled
	a.Priority = clamp(in.Priority, 0, 1000)
	a.CooldownMinutes = clamp(in.CooldownMinutes, 0, 1440)
	a.MaxAttacksPerDay = max0(in.MaxAttacksPerDay)
	a.MaxAttacksPerSession = max0(in.MaxAttacksPerSession)

	s.reg.Accounts[idx] = a
	if err := s.saveLocked(); err != nil {
		return Account{}, err
	}
	return a, nil
}

// Delete removes an account and moves its profile tree to accounts/.trash
// so a mistaken click never destroys history. The active account cannot be
// deleted; switch first so the running profile is unambiguous.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.indexLocked(id)
	if idx < 0 {
		return fmt.Errorf("account %q not found", id)
	}
	if s.reg.ActiveID == id {
		return fmt.Errorf("cannot delete the active account; switch to another account first")
	}
	if len(s.reg.Accounts) == 1 {
		return fmt.Errorf("cannot delete the only account")
	}

	src := paths.ProfileDir(id)
	trash := filepath.Join(s.dir, "accounts", ".trash")
	if _, err := os.Stat(src); err == nil {
		if err := os.MkdirAll(trash, 0o755); err != nil {
			return fmt.Errorf("prepare trash: %w", err)
		}
		dst := filepath.Join(trash, fmt.Sprintf("%s-%d", id, time.Now().Unix()))
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("archive profile %s: %w", id, err)
		}
	}

	s.reg.Accounts = append(s.reg.Accounts[:idx], s.reg.Accounts[idx+1:]...)
	return s.saveLocked()
}

// SetActive selects an account and records the switch in its usage counters.
func (s *Store) SetActive(id string) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.indexLocked(id)
	if idx < 0 {
		return Account{}, fmt.Errorf("account %q not found", id)
	}
	now := time.Now()
	a := s.reg.Accounts[idx]
	a.LastUsedAt = now
	if a.Usage.Date != day(now) {
		a.Usage = Usage{Date: day(now)}
	}
	a.Usage.Switches++
	s.reg.Accounts[idx] = a
	s.reg.ActiveID = id
	if err := s.saveLocked(); err != nil {
		return Account{}, err
	}
	return a, nil
}

// RecordAttacks adds one session's completed attacks to an account's daily
// usage. Bot counters are per-session, so callers pass the session total.
func (s *Store) RecordAttacks(id string, attacks int) {
	if attacks <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.indexLocked(id)
	if idx < 0 {
		return
	}
	now := time.Now()
	a := s.reg.Accounts[idx]
	if a.Usage.Date != day(now) {
		a.Usage = Usage{Date: day(now)}
	}
	a.Usage.Attacks += attacks
	s.reg.Accounts[idx] = a
	if err := s.saveLocked(); err != nil {
		// Usage counters are advisory; a failed write must not fail a
		// stop. The next successful save retries the whole document.
		_ = err
	}
}

// SetRotation validates and persists the fleet policy.
func (s *Store) SetRotation(r Rotation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Order != "" && r.Order != OrderRoundRobin && r.Order != OrderPriority {
		return fmt.Errorf("unknown rotation order %q", r.Order)
	}
	if r.AttacksPerAccount < 0 {
		return fmt.Errorf("attacks_per_account cannot be negative")
	}
	s.reg.Rotation = r.Normalized()
	return s.saveLocked()
}

// ResetUsage clears an account's daily counters. Rotation uses it as a
// manual override when an account is blocked by its daily cap.
func (s *Store) ResetUsage(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.indexLocked(id)
	if idx < 0 {
		return fmt.Errorf("account %q not found", id)
	}
	s.reg.Accounts[idx].Usage = Usage{Date: day(time.Now())}
	return s.saveLocked()
}

// Next picks the account a rotation should run after currentID. It returns
// false when rotation is disabled, no account is eligible (cooldown/daily
// cap/disabled) or only the current account remains.
func (s *Store) Next(currentID string, now time.Time) (Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.reg.Rotation.Enabled {
		return Account{}, false
	}
	eligible := make([]Account, 0, len(s.reg.Accounts))
	for _, a := range s.reg.Accounts {
		if a.ID == currentID || !a.Enabled {
			continue
		}
		if a.CooldownMinutes > 0 && !a.LastUsedAt.IsZero() &&
			now.Before(a.LastUsedAt.Add(time.Duration(a.CooldownMinutes)*time.Minute)) {
			continue
		}
		if a.MaxAttacksPerDay > 0 && a.usageOn(now).Attacks >= a.MaxAttacksPerDay {
			continue
		}
		eligible = append(eligible, a)
	}
	if len(eligible) == 0 {
		return Account{}, false
	}

	if s.reg.Rotation.Order == OrderPriority {
		sort.SliceStable(eligible, func(i, j int) bool {
			if eligible[i].Priority != eligible[j].Priority {
				return eligible[i].Priority > eligible[j].Priority
			}
			return eligible[i].LastUsedAt.Before(eligible[j].LastUsedAt)
		})
		return eligible[0], true
	}

	// Round robin: preserve registry order, starting just after the
	// current account so a fleet cycles instead of favouring index 0.
	start := 0
	for i, a := range s.reg.Accounts {
		if a.ID == currentID {
			start = i
			break
		}
	}
	for offset := 1; offset <= len(s.reg.Accounts); offset++ {
		want := s.reg.Accounts[(start+offset)%len(s.reg.Accounts)].ID
		for _, a := range eligible {
			if a.ID == want {
				return a, true
			}
		}
	}
	return eligible[0], true
}

// usageOn returns usage counters as they apply on now, treating a stale
// date as zero without mutating the stored document.
func (a Account) usageOn(now time.Time) Usage {
	if a.Usage.Date == day(now) {
		return a.Usage
	}
	return Usage{Date: day(now)}
}

// Status is the derived operator-facing state of an account.
func (a Account) Status(now time.Time, active, running bool) string {
	switch {
	case active && running:
		return "running"
	case !a.Enabled:
		return "disabled"
	}
	if a.CooldownMinutes > 0 && !a.LastUsedAt.IsZero() &&
		now.Before(a.LastUsedAt.Add(time.Duration(a.CooldownMinutes)*time.Minute)) {
		return "cooldown"
	}
	if a.MaxAttacksPerDay > 0 && a.usageOn(now).Attacks >= a.MaxAttacksPerDay {
		return "daily_limit"
	}
	return "ready"
}

// ---- internals ----

func (s *Store) indexLocked(id string) int {
	for i, a := range s.reg.Accounts {
		if a.ID == id {
			return i
		}
	}
	return -1
}

func (s *Store) uniqueIDLocked(base string) string {
	id := base
	for n := 2; s.indexLocked(id) >= 0; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	return id
}

func (s *Store) uniqueTagLocked(tag, exceptID string) error {
	tag = normalizeTag(tag)
	for _, a := range s.reg.Accounts {
		if a.ID != exceptID && a.PlayerTag == tag {
			return fmt.Errorf("player tag %s is already used by account %q", tag, a.Name)
		}
	}
	return nil
}

// uniqueDeviceLocked enforces one account per emulator serial. Two accounts
// claiming the same instance would farm whichever village happens to be
// signed in while reporting it as the other account — the exact
// mis-attribution this feature exists to prevent.
func (s *Store) uniqueDeviceLocked(dev Device, exceptID string) error {
	serial := dev.Normalized().DeviceID
	for _, a := range s.reg.Accounts {
		if a.ID != exceptID && a.Device.DeviceID == serial {
			return fmt.Errorf("device %s is already bound to account %q; start another emulator instance and use its serial", serial, a.Name)
		}
	}
	return nil
}

// templateConfigLocked returns the config a new account starts from: the
// active profile's file, else the legacy root config.
func (s *Store) templateConfigLocked() string {
	if s.reg.ActiveID != "" {
		p := paths.ResolveProfileConfig(s.reg.ActiveID, "config.json")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	root := filepath.Join(s.dir, "config.json")
	if _, err := os.Stat(root); err == nil {
		return root
	}
	return ""
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.reg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal accounts: %w", err)
	}
	if err := writeAtomic(s.path, data, 0o600); err != nil {
		return fmt.Errorf("save accounts: %w", err)
	}
	return nil
}

// writeAtomic replaces path via a synced temp file, keeping one .bak of the
// previous document. A crash mid-write can lose the newest change, never
// the whole registry.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if prev, err := os.ReadFile(path); err == nil {
		_ = os.WriteFile(path+".bak", prev, perm)
	}
	tmp, err := os.CreateTemp(dir, ".accounts-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func bootstrapRegistry(dir string) Registry {
	cfg := config.DefaultConfig()
	if c, err := config.Load(filepath.Join(dir, "config.json")); err == nil {
		cfg = c
	}
	now := time.Now()
	a := Account{
		ID:        DefaultAccountID,
		Name:      "Main",
		Device:    Device{ADBHost: cfg.Device.ADBHost, ADBPort: cfg.Device.ADBPort, DeviceID: cfg.Device.DeviceID}.Normalized(),
		Enabled:   true,
		Priority:  100,
		CreatedAt: now,
		Usage:     Usage{Date: day(now)},
	}
	if a.Device.DeviceID == "" {
		a.Device.DeviceID = "localhost:5555"
	}
	// The copy below must land in an existing tree; Open's mkdir runs
	// after bootstrap, so create it here first.
	_ = os.MkdirAll(paths.ProfileDir(a.ID), 0o755)

	// Copy the legacy state into the new profile so the user's current
	// settings, totals and history survive the upgrade. Copy, never move.
	for _, name := range []string{"config.json", "stats.json", "attack_history.json"} {
		src := filepath.Join(dir, name)
		dst := paths.ResolveProfileConfig(a.ID, name)
		if _, err := os.Stat(src); err == nil {
			if _, err := os.Stat(dst); os.IsNotExist(err) {
				if b, rerr := os.ReadFile(src); rerr == nil {
					_ = os.WriteFile(dst, b, 0o644)
				}
			}
		}
	}

	return normalizeRegistry(Registry{
		Version:  RegistryVersion,
		ActiveID: a.ID,
		Rotation: Rotation{Order: OrderRoundRobin, AutoStart: true, AttacksPerAccount: DefaultAttacksPerAccount},
		Accounts: []Account{a},
	}, dir)
}

func normalizeRegistry(r Registry, dir string) Registry {
	r.Version = RegistryVersion
	r.Rotation = r.Rotation.Normalized()

	if len(r.Accounts) == 0 {
		r.Accounts = bootstrapRegistry(dir).Accounts
	}
	seen := make(map[string]bool, len(r.Accounts))
	for i := range r.Accounts {
		a := &r.Accounts[i]
		a.ID = sanitizeID(a.ID)
		if a.ID == "" || seen[a.ID] {
			base := a.ID
			if base == "" {
				base = sanitizeID(a.Name)
			}
			if base == "" {
				base = "account"
			}
			id := base
			for n := 2; seen[id]; n++ {
				id = fmt.Sprintf("%s-%d", base, n)
			}
			a.ID = id
		}
		seen[a.ID] = true

		a.Name = strings.TrimSpace(a.Name)
		if a.Name == "" {
			a.Name = a.ID
		}
		a.PlayerTag = normalizeTag(a.PlayerTag)
		a.Device = a.Device.Normalized()
		a.Priority = clamp(a.Priority, 0, 1000)
		a.CooldownMinutes = clamp(a.CooldownMinutes, 0, 1440)
		a.MaxAttacksPerDay = max0(a.MaxAttacksPerDay)
		a.MaxAttacksPerSession = max0(a.MaxAttacksPerSession)
		if a.CreatedAt.IsZero() {
			a.CreatedAt = time.Now()
		}
	}

	if r.ActiveID == "" || !seen[r.ActiveID] {
		r.ActiveID = r.Accounts[0].ID
	}
	return r
}

func validateInput(in Input) error {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return fmt.Errorf("account name is required")
	}
	if len(name) > 48 {
		return fmt.Errorf("account name must be 48 characters or fewer")
	}
	dev := in.Device.Normalized()
	if dev.DeviceID == "" {
		return fmt.Errorf("ADB device serial is required (e.g. localhost:5555)")
	}
	if dev.ADBPort <= 0 || dev.ADBPort > 65535 {
		return fmt.Errorf("ADB server port must be between 1 and 65535")
	}
	if tag := strings.TrimSpace(in.PlayerTag); tag != "" {
		if !playerTagRe.MatchString(normalizeTag(tag)) {
			return fmt.Errorf("player tag %q is not a valid tag (example: #2P0Y8LQGR)", tag)
		}
	}
	if in.TownHall < 0 || in.TownHall > 18 {
		return fmt.Errorf("town hall must be between 1 and 18 (0 = unknown)")
	}
	if in.Priority < 0 || in.Priority > 1000 {
		return fmt.Errorf("priority must be between 0 and 1000")
	}
	if in.CooldownMinutes < 0 || in.CooldownMinutes > 1440 {
		return fmt.Errorf("cooldown must be between 0 and 1440 minutes")
	}
	if in.MaxAttacksPerDay < 0 || in.MaxAttacksPerSession < 0 {
		return fmt.Errorf("attack caps cannot be negative")
	}
	if len(in.Email) > 200 || len(in.Notes) > 2000 {
		return fmt.Errorf("email or notes are too long")
	}
	return nil
}

func writeProfileConfig(id string, dev Device, template string) error {
	cfg := config.DefaultConfig()
	if template != "" {
		if c, err := config.Load(template); err == nil {
			cfg = c
		}
	}
	cfg.Device.ADBHost = dev.ADBHost
	cfg.Device.ADBPort = dev.ADBPort
	cfg.Device.DeviceID = dev.DeviceID

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal profile config: %w", err)
	}
	if err := os.MkdirAll(paths.ProfileDir(id), 0o755); err != nil {
		return fmt.Errorf("create profile dir: %w", err)
	}
	path := paths.ResolveProfileConfig(id, "config.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write profile config: %w", err)
	}
	return nil
}

func sanitizeID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugStripRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 32 {
		s = strings.Trim(s[:32], "-")
	}
	return s
}

func normalizeTag(tag string) string {
	tag = strings.ToUpper(strings.TrimSpace(tag))
	if tag == "" {
		return ""
	}
	if !strings.HasPrefix(tag, "#") {
		tag = "#" + tag
	}
	return tag
}

func day(t time.Time) string { return t.Format("2006-01-02") }

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}
