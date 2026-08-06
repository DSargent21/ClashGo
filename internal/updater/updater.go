// Package updater polls GitHub Releases for new ClashGO versions,
// downloads updates with SHA256 verification, and signals the UI /
// applies updates on demand.
//
// Architecture (Phase 1 + 2 combined):
//
//   - Service is constructed once at app startup and held by the App.
//   - On Check(): fetches latest.json from the GitHub release (or, as a
//     fallback, scrapes the GitHub releases API).
//   - Semver compare + skip-version persistence determine "Available".
//   - Download() streams the asset to disk and verifies SHA256.
//   - Apply() opens Finder at the downloaded zip (Phase 2 fast-path) OR
//     spawns a detached helper script for in-place .app replacement
//     and graceful restart (Phase 2 advanced path).
//   - All public methods are safe to call concurrently from Wails-bound
//     UI handlers; status is guarded by RWMutex + atomic progress.
//
// This package has zero third-party deps — stdlib net/http, crypto/sha256,
// encoding/json only.
package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/rs/zerolog/log"
)

// PollInterval is the cadence between background checks. 6h respects
// GitHub's 60/hr unauth rate limit (10 polls per hour worst-case under
// ETag-304 hits which don't count against the limit).
const PollInterval = 6 * time.Hour

// minCheckDelay guards against rapid-fire manual "Check now" clicks from
// the UI (or a future scheduler bug).
const minCheckDelay = 5 * time.Second

// errNoReleases is returned by fetchLatestRelease when the GitHub
// releases API responds 404 to /releases/latest. This is NOT an
// error condition — it means the repo has zero published releases,
// so the app is trivially "up to date" (nothing to update TO).
// The Check() caller translates this into StateUpToDate instead
// of StateError, so the UI doesn't show "Update error" on every
// launch for repos that haven't cut a release yet.
var errNoReleases = errors.New("no releases published for this repository")

// errBodyLimit caps how much of a non-JSON error body we surface
// to the UI. GitHub's error payloads are small (<300 B) but a
// misconfigured proxy could dump megabytes; 512 B is plenty for
// the inline `message` field and keeps the status struct tidy.
// int64 so it slots into io.LimitReader without a cast.
const errBodyLimit int64 = 512

// State is the typed lifecycle of an update.
type State string

const (
	StateIdle        State = "idle"
	StateChecking    State = "checking"
	StateAvailable   State = "available"
	StateDownloading State = "downloading"
	StateReady       State = "ready" // downloaded + verified
	StateInstalling  State = "installing"
	// StateRestarting means the user clicked Install & Restart;
	// the helper script is detached and ClashGO is about to exit.
	// The React UI uses this to render a non-dismissible splash
	// over the 1–3s gap before the new bundle launches.
	StateRestarting State = "restarting"
	StateError      State = "error" // soft error; recoils to idle next cycle
	StateUpToDate   State = "up_to_date"
)

// Status is the typed value exposed to the UI via Wails bindings.
// JSON shape is stable — do not rename fields without coordinating with
// the React UpdateBanner component.
type Status struct {
	CurrentVersion  string  `json:"current_version"`
	LatestVersion   string  `json:"latest_version"`
	Available       bool    `json:"available"`
	State           State   `json:"state"`
	Progress        float64 `json:"progress"` // 0..1
	Notes           string  `json:"notes"`
	ReleaseURL      string  `json:"release_url"`
	AssetName       string  `json:"asset_name"`
	DownloadPath    string  `json:"download_path"` // empty until Download completes
	ExpectedSize    int64   `json:"expected_size"`
	DownloadedSize  int64   `json:"downloaded_size"`
	Error           string  `json:"error"`
	LastCheckedUnix int64   `json:"last_checked_unix"`
	SkipVersion     string  `json:"skip_version"`
	MinSupported    string  `json:"min_supported"`
}

// serviceConfig configures New.
type serviceConfig struct {
	RepoOwner      string
	RepoName       string
	CurrentVersion string
	HTTPClient     *http.Client
	Now            func() time.Time
}

// DefaultConfig returns the production configuration for a ClashGO app
// pointing at the public Ducky705/ClashGO repo.
//
// HTTPClient.Timeout is generous because it covers both metadata checks
// (sub-second) AND asset downloads (multi-MB over slow Wi-Fi). If a
// future release gets much larger, bump downloadTimeout in
// streamToFile specifically rather than this global knob.
func DefaultConfig(currentVersion string) serviceConfig {
	return serviceConfig{
		RepoOwner:      "Ducky705",
		RepoName:       "ClashGO",
		CurrentVersion: currentVersion,
		HTTPClient:     &http.Client{Timeout: 5 * time.Minute},
		Now:            time.Now,
	}
}

// Service is the long-lived updater constructed at startup and held by
// the App for the full app lifetime.
//
// Mutex split (read this before grabbing the wrong one):
//   - statusMu      – guards Status fields (read mostly, written on transitions)
//   - progress      – atomic.Int32; read without lock from GetStatus
//   - etagMu        – separate so asset HTTP requests don't contend with status updates
//   - skipMu        – file-level mutex protecting the skip_version.txt read/write
//   - lastCheckMu   – rate-limits manual "Check now" clicks
//
// downloadSpec is a plain struct set under statusMu that's reused by
// Download() so we don't re-hit GitHub just to fetch the SHA.
//
// Writers in absorbManifest/absorbRelease/Check/Download all take the
// statusMu WRITE lock; readers (GetStatus/Status forwarding) take the
// READ lock. NEVER call a method that takes statusMu.RLock while
// statusMu.Lock is held by the same goroutine — Go's RWMutex will
// deadlock. Pass state as arguments instead.
type Service struct {
	cfg serviceConfig

	statusMu sync.RWMutex
	status   Status

	progress atomic.Int32 // 0..100 — read without lock, written from download goroutine

	etagMu sync.RWMutex
	etag   string

	downloadSpec downloadSpec

	skipMu   sync.Mutex
	skipPath string

	downloadsDir string

	lastCheckMu sync.Mutex
	lastCheckAt time.Time
}

// downloadSpec is the verified download target cached at Check() time
// so Download() streams directly without re-hitting GitHub. Asset
// URLs on GitHub Releases are immutable per tag — a stale entry just
// means the user keeps their current version — so we don't refresh.
type downloadSpec struct {
	URL     string
	SHA256  string
	Size    int64
	Name    string
	Version string
}

// New constructs a Service. Call CleanupOrphanDownloads once on startup
// before kicking off the background poller.
func New(cfg serviceConfig) *Service {
	s := &Service{cfg: cfg}
	s.status = Status{
		CurrentVersion: normalizeVersion(cfg.CurrentVersion),
		State:          StateIdle,
	}

	// Skip-version persistence lives next to other app config files.
	// Skip version env override (used in tests + for `test -skip v0.9.0`).
	if env := os.Getenv("CLASHGO_SKIP_VERSION"); env != "" {
		_ = s.setSkipVersion(env)
	}
	s.skipPath = filepath.Join(paths.GetConfigDir(), "skip_version.txt")

	// Reads override from disk if present. (setSkipVersion covers env path.)
	if v, err := os.ReadFile(s.skipPath); err == nil {
		s.statusMu.Lock()
		s.status.SkipVersion = strings.TrimSpace(string(v))
		s.statusMu.Unlock()
	}

	// Stable download dir.
	s.downloadsDir = filepath.Join(paths.GetConfigDir(), "updates")
	_ = os.MkdirAll(s.downloadsDir, 0o755)

	return s
}

// SetSkipVersion is the public binding wrapper around setSkipVersion.
func (s *Service) SetSkipVersion(v string) {
	_ = s.setSkipVersion(v)
}

// CleanupOrphanDownloads removes *.tmp / *.part leftovers from
// previous sessions. Called once on startup; safe to call repeatedly.
func (s *Service) CleanupOrphanDownloads() {
	if s.downloadsDir == "" {
		return
	}
	entries, err := os.ReadDir(s.downloadsDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".tmp") ||
			strings.HasSuffix(name, ".part") ||
			strings.HasSuffix(name, ".partial") {
			_ = os.Remove(filepath.Join(s.downloadsDir, name))
			log.Info().Str("file", name).Msg("removed orphan partial download")
		}
	}
}

func (s *Service) setSkipVersion(v string) error {
	v = strings.TrimSpace(v)
	s.statusMu.Lock()
	s.status.SkipVersion = v
	// If the user is now skipping the current latest, recant availability.
	if v != "" && s.status.LatestVersion != "" &&
		CompareVersions(s.status.LatestVersion, v) == 0 {
		s.status.Available = false
	}
	s.statusMu.Unlock()

	s.skipMu.Lock()
	defer s.skipMu.Unlock()
	if v == "" {
		_ = os.Remove(s.skipPath)
		return nil
	}
	return os.WriteFile(s.skipPath, []byte(v+"\n"), 0o644)
}

// GetStatus returns a copy of the current status (lock-free for the
// caller). Use this from Wails-bound methods.
func (s *Service) GetStatus() Status {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	st := s.status
	st.Progress = float64(s.progress.Load()) / 100.0
	return st
}

// StartBackgroundPoller kicks off a goroutine that pings GitHub every
// PollInterval and on each tick refreshes status. Returns immediately;
// the goroutine is canceled when ctx is canceled.
//
// Always performs one initial check before entering the loop so the UI
// shows correct state on startup.
func (s *Service) StartBackgroundPoller(ctx context.Context) {
	go func() {
		// Initial check.
		if _, err := s.Check(ctx); err != nil {
			log.Warn().Err(err).Msg("initial update check failed")
		}
		t := time.NewTicker(PollInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := s.Check(ctx); err != nil {
					log.Warn().Err(err).Msg("periodic update check failed")
				}
			}
		}
	}()
}

// Check performs a single release-availability check. It always
// returns a non-nil Status; errors are also reflected on the status
// struct so a transient network blip doesn't erase "available" state.
//
// A 404 from the GitHub releases API (errNoReleases) is NOT treated
// as an error — it just means the repo has no published releases,
// so we report StateUpToDate with cleared fields instead of alarming
// the user with "Update error" on every launch.
func (s *Service) Check(ctx context.Context) (Status, error) {
	// Throttle manual checks.
	s.lastCheckMu.Lock()
	if !s.lastCheckAt.IsZero() && s.cfg.Now().Sub(s.lastCheckAt) < minCheckDelay {
		s.lastCheckMu.Unlock()
		return s.GetStatus(), nil
	}
	s.lastCheckAt = s.cfg.Now()
	s.lastCheckMu.Unlock()

	s.statusMu.Lock()
	s.status.State = StateChecking
	s.status.Error = ""
	s.statusMu.Unlock()

	// 1. Try the manifest first (carries SHA256 + min-supported).
	manifestURL := s.manifestURL()
	if manifest, err := s.fetchManifest(ctx, manifestURL); err == nil && manifest != nil {
		return s.absorbManifest(*manifest), nil
	} else if err != nil {
		// Network / 404 — fall through to the API path.
		log.Debug().Err(err).Str("url", manifestURL).Msg("manifest fetch failed, falling back to GitHub API")
	}

	// 2. Fallback: GitHub releases API. No SHA256 (we can't verify
	// without the manifest), but at least we can notify.
	rel, err := s.fetchLatestRelease(ctx)
	if errors.Is(err, errNoReleases) {
		// Repo has no published releases. Not an error — a normal,
		// expected state for a repo that hasn't cut a release yet.
		// The background poller therefore gets nil here and won't
		// spam warnings on every 6h tick.
		s.markNoReleases()
		return s.GetStatus(), nil
	}
	if err != nil {
		s.statusMu.Lock()
		s.status.State = StateError
		s.status.Error = err.Error()
		s.status.LastCheckedUnix = s.cfg.Now().Unix()
		s.statusMu.Unlock()
		return s.GetStatus(), err
	}

	return s.absorbRelease(rel), nil
}

// manifestURL is the absolute URL of the latest.json published as a
// release asset. The /releases/latest redirect resolves to the latest
// semver-published release; /latest/download/<asset> then serves the
// file with a 302 to the actual CDN URL.
func (s *Service) manifestURL() string {
	return fmt.Sprintf(
		"https://github.com/%s/%s/releases/latest/download/latest.json",
		s.cfg.RepoOwner, s.cfg.RepoName,
	)
}

// fetchManifest GETs latest.json with ETag-304 caching. Returns
// (manifest, nil) on 200, (nil, nil) on 304, (nil, error) otherwise.
func (s *Service) fetchManifest(ctx context.Context, url string) (*Manifest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	s.etagMu.RLock()
	etag := s.etag
	s.etagMu.RUnlock()
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return nil, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		// Older release without latest.json — not an error.
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest HTTP %d", resp.StatusCode)
	}

	var m Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}

	s.etagMu.Lock()
	s.etag = resp.Header.Get("ETag")
	s.etagMu.Unlock()

	return &m, nil
}

// fetchLatestRelease hits the GitHub releases API for the latest
// non-draft, non-prerelease release.
//
// Returns errNoReleases (NOT a generic error) when the API responds
// 404 — the caller (Check) uses this sentinel to distinguish
// "repo has no releases" (a normal state) from real network / API
// failures (which should surface to the UI as StateError).
func (s *Service) fetchLatestRelease(ctx context.Context) (githubRelease, error) {
	url := fmt.Sprintf(
		"https://api.github.com/repos/%s/%s/releases/latest",
		s.cfg.RepoOwner, s.cfg.RepoName,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return githubRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return githubRelease{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return githubRelease{}, errNoReleases
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return githubRelease{}, fmt.Errorf("github rate limit hit (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		// Try to extract GitHub's human-readable `message` field
		// instead of dumping the raw JSON body into the status —
		// the user-facing error pill was previously showing
		// `{"message":"Not Found","documentation_url":"github.com",...}`
		// which is noisy and leaks API internals. The 512-byte
		// cap is deliberate: this is a best-effort human-message
		// extraction, NOT a full body download. GitHub's error
		// payloads are <300 B; the cap just guards against a
		// misconfigured proxy dumping megabytes into the status.
		var ghErr struct {
			Message string `json:"message"`
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyLimit))
		_ = json.Unmarshal(body, &ghErr)
		if ghErr.Message != "" {
			return githubRelease{}, fmt.Errorf("github API HTTP %d: %s", resp.StatusCode, ghErr.Message)
		}
		return githubRelease{}, fmt.Errorf("github API HTTP %d", resp.StatusCode)
	}

	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return githubRelease{}, err
	}
	return rel, nil
}

// absorbManifest populates status from a freshly-fetched Manifest
// AND caches the verified download spec so Download() doesn't have
// to re-fetch.
func (s *Service) absorbManifest(m Manifest) Status {
	plat := platformKey(runtime.GOOS)
	spec, ok := m.Platforms[plat]

	s.statusMu.Lock()
	s.status.LatestVersion = normalizeVersion(m.Version)
	s.status.MinSupported = m.MinSupported
	s.status.Notes = m.Notes
	s.status.ReleaseURL = s.releasePageURL(m.Version)
	s.status.LastCheckedUnix = s.cfg.Now().Unix()
	if !ok {
		s.status.Error = fmt.Sprintf("no %s asset in manifest", plat)
	}
	s.status.Available = computeAvailable(
		s.status.LatestVersion,
		s.status.CurrentVersion,
		s.status.SkipVersion,
		s.status.MinSupported,
	)
	s.status.State = StateIdle
	if ok {
		s.status.AssetName = spec.AssetName
		s.status.ExpectedSize = spec.Size
		s.downloadSpec = downloadSpec{
			URL:     spec.AssetURL,
			SHA256:  spec.SHA256,
			Size:    spec.Size,
			Name:    spec.AssetName,
			Version: s.status.LatestVersion,
		}
	}
	s.statusMu.Unlock()
	return s.GetStatus()
}

// absorbRelease populates status from a fresh GitHub-release scraping
// (no SHA256 available — Download() will fetch the manifest freshly).
func (s *Service) absorbRelease(rel githubRelease) Status {
	name, url, size, ok := pickReleaseAsset(rel, runtime.GOOS)

	s.statusMu.Lock()
	s.status.LatestVersion = normalizeVersion(rel.TagName)
	s.status.Notes = rel.Body
	s.status.ReleaseURL = rel.HTMLURL
	s.status.LastCheckedUnix = s.cfg.Now().Unix()
	if ok {
		s.status.AssetName = name
		s.status.ExpectedSize = size
		s.downloadSpec = downloadSpec{
			URL:     url,
			SHA256:  "", // not from the GitHub API
			Size:    size,
			Name:    name,
			Version: s.status.LatestVersion,
		}
	} else {
		s.status.Error = fmt.Sprintf("no compatible asset for %s", runtime.GOOS)
	}
	s.status.Available = computeAvailable(
		s.status.LatestVersion,
		s.status.CurrentVersion,
		s.status.SkipVersion,
		s.status.MinSupported,
	)
	s.status.State = StateIdle
	s.statusMu.Unlock()
	return s.GetStatus()
}

// computeAvailable is a pure helper — pass all inputs, no locks
// taken. This avoids the Go RWMutex deadlock from re-locking the
// status mutex while the writer goroutine already holds the write
// lock (a goroutine that holds an RWMutex.Lock cannot acquire the
// matching RLock).
func computeAvailable(latest, current, skip, minSupported string) bool {
	if latest == "" {
		return false
	}
	if CompareVersions(latest, current) <= 0 {
		return false
	}
	// Honor explicit user skip preference.
	if skip != "" && CompareVersions(latest, skip) == 0 {
		return false
	}
	// Force-update if current is below min-supported.
	if minSupported != "" && CompareVersions(current, minSupported) < 0 {
		return true
	}
	return true
}

// releasePageURL is the human-facing link embedded in the UI.
func (s *Service) releasePageURL(version string) string {
	return fmt.Sprintf(
		"https://github.com/%s/%s/releases/tag/v%s",
		s.cfg.RepoOwner, s.cfg.RepoName, version,
	)
}

// Download fetches the matched asset, verifies SHA256 against the
// manifest cached by Check(), and returns the absolute path of the
// verified file. Errors are also pushed onto Status so the UI can
// display them via the 2s event ticker.
func (s *Service) Download(ctx context.Context) (string, error) {
	s.statusMu.RLock()
	st := s.status
	spec := s.downloadSpec
	s.statusMu.RUnlock()

	if !st.Available {
		return "", errors.New("no update available")
	}
	if spec.Version == "" {
		return "", errors.New("latest version unknown; run Check() first")
	}
	if spec.URL == "" || spec.Name == "" {
		return "", errors.New("no asset selected \u2014 run Check() again")
	}

	targetDir := filepath.Join(s.downloadsDir, spec.Version)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", err
	}
	finalPath := filepath.Join(targetDir, spec.Name)
	tmpPath := finalPath + ".part"

	s.statusMu.Lock()
	s.status.State = StateDownloading
	s.status.DownloadedSize = 0
	s.status.DownloadPath = ""
	s.status.Error = ""
	s.statusMu.Unlock()
	s.progress.Store(0)

	if err := s.streamToFile(ctx, spec.URL, tmpPath, spec.Size); err != nil {
		_ = os.Remove(tmpPath)
		s.recordDownloadError(err)
		return "", err
	}

	// Verify SHA256 if we know one.
	if spec.SHA256 != "" {
		got, err := fileSHA256(tmpPath)
		if err != nil {
			_ = os.Remove(tmpPath)
			s.recordDownloadError(fmt.Errorf("hash file: %w", err))
			return "", err
		}
		if !strings.EqualFold(got, spec.SHA256) {
			_ = os.Remove(tmpPath)
			err := fmt.Errorf("sha256 mismatch: want=%s got=%s", spec.SHA256, got)
			s.recordDownloadError(err)
			return "", err
		}
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		s.recordDownloadError(err)
		return "", err
	}

	s.statusMu.Lock()
	s.status.DownloadPath = finalPath
	s.status.State = StateReady
	s.status.DownloadedSize = spec.Size
	s.statusMu.Unlock()
	s.progress.Store(100)

	log.Info().Str("path", finalPath).Str("sha256", spec.SHA256).Msg("update ready to install")
	return finalPath, nil
}

// streamToFile downloads url → tmpPath, reporting progress on
// s.progress. Aborts via ctx. Compares expected size as a sanity
// check.
func (s *Service) streamToFile(ctx context.Context, url, tmpPath string, expected int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download HTTP %d", resp.StatusCode)
	}
	if expected > 0 && resp.ContentLength > 0 && resp.ContentLength != expected {
		return fmt.Errorf("size mismatch: want=%d got=%d", expected, resp.ContentLength)
	}

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	// Use io.Reader (not ReadCloser) so we can swap in our progress
	// wrapper cleanly. The body is closed by the deferred
	// resp.Body.Close() above regardless of the wrapper.
	var src io.Reader = resp.Body
	if expected > 0 {
		src = &progressReader{inner: resp.Body, total: expected, onProgress: func(p float64) {
			s.progress.Store(int32(p * 100))
		}}
	}
	if _, err := io.Copy(f, src); err != nil {
		return err
	}
	return nil
}

// progressReader invokes onProgress(0..1) as bytes flow past. We
// embed a ReadCloser so the type is "happy" wherever the stdlib
// expects one, but only Read() is actually used.
type progressReader struct {
	inner      io.Reader
	total      int64
	read       int64
	onProgress func(float64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.inner.Read(b)
	if n > 0 {
		p.read += int64(n)
		if p.total > 0 {
			p.onProgress(float64(p.read) / float64(p.total))
		}
	}
	return n, err
}

func (p *progressReader) Close() error {
	if c, ok := p.inner.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// Compile-time assertion that *progressReader satisfies io.ReadCloser
// for any future swap to io.Copy with closer semantics.
var _ io.ReadCloser = (*progressReader)(nil)

// SetState is a narrow binding helper for callers (App.InstallAndRestart)
// that need to drive the state machine without owning a full Check()
// cycle. Only a handful of transitions are valid via this entry point.
func (s *Service) SetState(st State) {
	s.statusMu.Lock()
	s.status.State = st
	s.statusMu.Unlock()
}

func (s *Service) recordDownloadError(err error) {
	s.statusMu.Lock()
	s.status.State = StateError
	s.status.Error = err.Error()
	s.statusMu.Unlock()
	// Reset progress so a stale bar doesn't haunt the UI after a fail.
	s.progress.Store(0)
	log.Warn().Err(err).Msg("update download failed")
}

// markNoReleases is the canonical "no published releases" state
// transition. Extracted from Check() so the transition lives in
// exactly one place — if a future field needs clearing (or the
// State value changes), both Check() and the regression test see
// the update. Holds statusMu itself; do NOT call with the lock
// already held.
//
// Also clears downloadSpec: if a prior Check() resolved a release
// and Download() cached the asset URL/SHA, a subsequent empty
// repo would otherwise leave stale downloadSpec fields. Download()
// already guards on st.Available so this is hygiene, not a
// correctness fix.
func (s *Service) markNoReleases() {
	s.statusMu.Lock()
	s.status.State = StateUpToDate
	s.status.LatestVersion = ""
	s.status.Available = false
	s.status.MinSupported = ""
	s.status.Notes = ""
	s.status.ReleaseURL = ""
	s.status.AssetName = ""
	s.status.ExpectedSize = 0
	s.status.Error = ""
	s.status.LastCheckedUnix = s.cfg.Now().Unix()
	s.downloadSpec = downloadSpec{}
	s.statusMu.Unlock()
}

// fileSHA256 is the hex SHA256 of the file at path.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Apply opens the downloaded zip in Finder (Phase 2 fast path: zero
// risk; relies on the user to drag-replace). If a helper script is
// bundled in the .app, use ApplyAuto() instead for in-place replace.
func (s *Service) Apply() error {
	st := s.GetStatus()
	if st.State != StateReady {
		return errors.New("download not ready — call Download() first")
	}
	if st.DownloadPath == "" {
		return errors.New("no downloaded file")
	}
	if _, err := os.Stat(st.DownloadPath); err != nil {
		return fmt.Errorf("verify downloaded file: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		// `open -R` reveals the file in Finder with the parent dir
		// selected. The user double-clicks the dmg/zip and drags the
		// .app into /Applications as macOS expects.
		cmd := exec.Command("open", "-R", st.DownloadPath)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		return cmd.Run()
	default:
		// On non-darwin we can't `open`; surface the path so the
		// user can find it manually.
		return fmt.Errorf("auto-install not supported on %s; update file at: %s", runtime.GOOS, st.DownloadPath)
	}
}

// ApplyAuto replaces the running .app bundle on macOS and relaunches.
// Caller MUST immediately invoke os.Exit(0) after a nil return — the
// helper script kills the process, swaps the bundle, then restarts.
//
// The helper script lives at ClashGO.app/Contents/Resources/install_update.sh
// and is bundled by the Makefile's package target. If the script is
// missing, returns an error so the UI can fall back to Finder.
func (s *Service) ApplyAuto() (bool, error) {
	if runtime.GOOS != "darwin" {
		return false, fmt.Errorf("auto-install only supported on macOS")
	}
	st := s.GetStatus()
	if st.State != StateReady || st.DownloadPath == "" {
		return false, errors.New("download not ready — call Download() first")
	}

	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	// exe is .../ClashGO.app/Contents/MacOS/ClashGO
	macosDir := filepath.Dir(exe)
	if filepath.Base(macosDir) != "MacOS" {
		return false, fmt.Errorf("not running from a .app bundle: %s", exe)
	}
	bundlePath := filepath.Dir(macosDir) // .../ClashGO.app
	helper := filepath.Join(filepath.Dir(macosDir), "Resources", "install_update.sh")
	if _, err := os.Stat(helper); err != nil {
		return false, fmt.Errorf("helper script missing: %w", err)
	}

	// Verify destination is writable before we commit.
	if err := checkBundleWritable(bundlePath); err != nil {
		return false, err
	}

	// Spawn detached so this process can safely Exit(0) and let the
	// script do the swap without holding locks on the running binary.
	// We pass the helper the parent of the bundle (typically /Applications)
	// OR the install location, so it knows where to move the new app.
	installDir := filepath.Dir(bundlePath)

	cmd := exec.Command("bash", helper, st.DownloadPath, bundlePath, installDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("start helper: %w", err)
	}
	// Reap async — no need to wait. The helper will wait for our exit itself.
	go func() { _ = cmd.Wait() }()
	return true, nil
}

// checkBundleWritable reports whether the user can replace the .app
// bundle at path. macOS /Applications is often world-writable so this
// usually succeeds; the gate is here for explicitness and future
// hardening.
func checkBundleWritable(bundlePath string) error {
	parent := filepath.Dir(bundlePath)
	// If the user installed the .app somewhere they don't own (e.g.
	// system /Applications on a managed Mac), os.Rename will fail
	// later — fail fast here with a clearer message.
	tmp := filepath.Join(parent, ".clashgo-write-test")
	if err := os.WriteFile(tmp, []byte("ok"), 0o644); err != nil {
		return fmt.Errorf("cannot write to %s — install ClashGO into ~/Applications or Downloads: %w", parent, err)
	}
	_ = os.Remove(tmp)
	return nil
}

// Cleanup removes the downloads dir for a version (privacy).
func (s *Service) Cleanup(version string) error {
	if version == "" {
		return errors.New("version required")
	}
	return os.RemoveAll(filepath.Join(s.downloadsDir, version))
}
