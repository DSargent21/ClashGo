package game

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gocv.io/x/gocv"
)

type TemplateStore struct {
	mu       sync.RWMutex
	dir      string
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
		dir = "assets/templates"
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create template dir: %w", err)
	}
	return &TemplateStore{
		dir:      dir,
		templates: make(map[string]gocv.Mat),
		registry:  make(map[string]TemplateMeta),
	}, nil
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
		gocv.MatchTemplate(screen, tmpl, &result, gocv.TmCcoeffNormed, gocv.NewMat())
		defer result.Close()

		_, maxVal, _, _ := gocv.MinMaxLoc(result)
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

		scaleStep := (maxScale - minScale) / float64(steps)
		for s := minScale; s <= maxScale; s += scaleStep {
			w := int(float64(tmpl.Cols()) * s)
			h := int(float64(tmpl.Rows()) * s)
			if w < 5 || h < 5 {
				continue
			}

			resized := gocv.NewMat()
			gocv.Resize(tmpl, &resized, image.Point{X: w, Y: h}, 0, 0, gocv.InterpolationLinear)
			defer resized.Close()

			result := gocv.NewMat()
			gocv.MatchTemplate(screen, resized, &result, gocv.TmCcoeffNormed, gocv.NewMat())
			defer result.Close()

			_, maxVal, _, _ := gocv.MinMaxLoc(result)
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

func (ts *TemplateStore) Close() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for _, mat := range ts.templates {
		mat.Close()
	}
	ts.templates = make(map[string]gocv.Mat)
}