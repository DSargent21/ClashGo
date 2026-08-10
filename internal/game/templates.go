package game

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/internal/vision"
	"gocv.io/x/gocv"
)

type TemplateStore struct {
	mu        sync.RWMutex
	dir       string
	templates map[string]gocv.Mat
	registry  map[string]TemplateMeta
}

type TemplateMeta struct {
	Name      string
	State     GameState
	X, Y      int
	W, H      int
	Hash      uint64
	CreatedAt int64
}

func NewTemplateStore(dir string) (*TemplateStore, error) {
	if dir == "" {
		dir = paths.Resolve("templates")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create template dir: %w", err)
	}
	return &TemplateStore{
		dir:       dir,
		templates: make(map[string]gocv.Mat),
		registry:  make(map[string]TemplateMeta),
	}, nil
}

// NewEmptyTemplateStore returns a VALID TemplateStore that holds no
// templates. Resilience fallback: when the template directory cannot
// be created/read (read-only assets mount, misconfigured asset path,
// cwd-dependent resolution failing under launchd), callers used to
// receive a nil *TemplateStore and then panic on the first
// ts.Get()/Match() dereference (observed live: SIGSEGV in
// LootRecognizer.prepareDigitTemplates killing the bot mid-attack).
// With an empty store every lookup returns "not found" and the bot
// falls back to its color/pinpoint heuristics instead of crashing.
func NewEmptyTemplateStore() *TemplateStore {
	return &TemplateStore{
		dir:       "",
		templates: make(map[string]gocv.Mat),
		registry:  make(map[string]TemplateMeta),
	}
}

func (ts *TemplateStore) Save(name string, state GameState, rgn image.Rectangle, screen gocv.Mat) error {
	if screen.Empty() || !rgn.In(image.Rect(0, 0, screen.Cols(), screen.Rows())) {
		return fmt.Errorf("invalid region or empty screen")
	}

	cropped := screen.Region(rgn)
	defer cropped.Close()

	ts.mu.Lock()
	defer ts.mu.Unlock()

	filename := fmt.Sprintf("%s_%s.png", state.String(), name)
	path := filepath.Join(ts.dir, filename)

	if ok := gocv.IMWrite(path, cropped); !ok {
		return fmt.Errorf("save template: failed to write %s", path)
	}

	ts.templates[name] = cropped.Clone()
	ts.registry[name] = TemplateMeta{
		Name:      name,
		State:     state,
		X:         rgn.Min.X,
		Y:         rgn.Min.Y,
		W:         rgn.Dx(),
		H:         rgn.Dy(),
		Hash:      0,
		CreatedAt: 0,
	}

	return nil
}

func (ts *TemplateStore) Match(screen gocv.Mat, state GameState, threshold float32) (bool, string, float64) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	bestName := ""
	bestConf := float32(0.0)

	for name, meta := range ts.registry {
		if meta.State != state {
			continue
		}

		tmpl, exists := ts.templates[name]
		if !exists || tmpl.Empty() {
			continue
		}

		if tmpl.Cols() > screen.Cols() || tmpl.Rows() > screen.Rows() {
			continue
		}

		result := gocv.NewMat()
		gocv.MatchTemplate(screen, tmpl, &result, gocv.TmCcoeffNormed, vision.EmptyMask())
		_, maxVal, _, _ := gocv.MinMaxLoc(result)
		result.Close()

		if maxVal > bestConf {
			bestConf = maxVal
			bestName = name
		}
	}

	return bestConf >= threshold, bestName, float64(bestConf)
}

func (ts *TemplateStore) MatchMultiScale(screen gocv.Mat, state GameState, minScale, maxScale float64, steps int, threshold float32) (bool, string, float64) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	bestName := ""
	bestConf := float32(0.0)

	if steps < 1 {
		steps = 1
	}

	for name, meta := range ts.registry {
		if meta.State != state {
			continue
		}

		tmpl, exists := ts.templates[name]
		if !exists || tmpl.Empty() {
			continue
		}

		if tmpl.Cols() > screen.Cols() || tmpl.Rows() > screen.Rows() {
			continue
		}

		// Pre-build the scaled templates once for this template so we don't
		// re-Resize on every call. Keyed by name+scale params in the cache.
		scaled := vision.GetScaledTemplates(name, tmpl, minScale, maxScale, steps)
		for _, resized := range scaled {
			if resized.Empty() || resized.Cols() > screen.Cols() || resized.Rows() > screen.Rows() {
				continue
			}
			result := gocv.NewMat()
			gocv.MatchTemplate(screen, resized, &result, gocv.TmCcoeffNormed, vision.EmptyMask())
			_, maxVal, _, _ := gocv.MinMaxLoc(result)
			result.Close()

			if maxVal > bestConf {
				bestConf = maxVal
				bestName = name
			}
		}
	}

	return bestConf >= threshold, bestName, float64(bestConf)
}

func (ts *TemplateStore) LoadTemplates() error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	err := filepath.Walk(ts.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || filepath.Ext(path) != ".png" {
			return nil
		}

		mat := gocv.IMRead(path, gocv.IMReadColor)
		if mat.Empty() {
			return nil
		}

		// Use relative path from ts.dir as the name to allow subfolder categorization
		rel, err := filepath.Rel(ts.dir, path)
		if err != nil {
			rel = info.Name()
		}
		// Strip .png extension
		name := strings.TrimSuffix(rel, ".png")

		ts.templates[name] = mat
		ts.registry[name] = TemplateMeta{
			Name: name,
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("walk template dir: %w", err)
	}

	return nil
}

func (ts *TemplateStore) List(state GameState) []string {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	var names []string
	for name, meta := range ts.registry {
		if state == StateUnknown || meta.State == state {
			names = append(names, name)
		}
	}
	return names
}

func (ts *TemplateStore) Get(name string) (gocv.Mat, bool) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	m, ok := ts.templates[name]
	if !ok {
		m, ok = ts.templates[name+".png"]
	}
	return m, ok
}

func (ts *TemplateStore) Count() int {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return len(ts.templates)
}

// Close releases all backing OpenCV buffers held by the store. Each Mat is
// closed defensively: a stray double-close or a CGO finalizer race can panic
// with SIGSEGV, so we recover per-Mat and continue freeing the remainder
// instead of aborting and leaking everything.
func (ts *TemplateStore) Close() {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	for name, m := range ts.templates {
		if !m.Empty() {
			func() {
				defer func() { _ = recover() }()
				m.Close()
			}()
		}
		delete(ts.templates, name)
	}
	ts.templates = make(map[string]gocv.Mat)
	ts.registry = make(map[string]TemplateMeta)
}
