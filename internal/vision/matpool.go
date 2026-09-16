package vision

import (
	"fmt"
	"gocv.io/x/gocv"
	"image"
	"sync"
)

// matKey identifies a mat pool by its (type, rows, cols) triple. A struct
// key avoids the per-call fmt.Sprintf allocation a string key incurred on
// every Get/Put — the mat pool sits on the per-frame vision hot path.
// (The previous implementation cast dimensions to a single rune, which
// collapsed distinct dimensions into the same key; a struct key can't
// collide that way either.)
type matKey struct {
	matType gocv.MatType
	rows    int
	cols    int
}

type MatPool struct {
	pools map[matKey]*sync.Pool
	mu    sync.Mutex
}

func NewMatPool() *MatPool {
	return &MatPool{
		pools: make(map[matKey]*sync.Pool),
	}
}

func (p *MatPool) Get(rows, cols int, matType gocv.MatType) gocv.Mat {
	key := matKey{matType: matType, rows: rows, cols: cols}
	p.mu.Lock()
	pool, ok := p.pools[key]
	if !ok {
		pool = &sync.Pool{
			New: func() interface{} {
				return gocv.NewMatWithSize(rows, cols, matType)
			},
		}
		p.pools[key] = pool
	}
	p.mu.Unlock()

	mat := pool.Get().(gocv.Mat)
	if mat.Empty() {
		return gocv.NewMatWithSize(rows, cols, matType)
	}
	return mat
}

func (p *MatPool) Put(mat gocv.Mat) {
	if mat.Empty() {
		return
	}
	key := matKey{matType: mat.Type(), rows: mat.Rows(), cols: mat.Cols()}
	p.mu.Lock()
	pool, ok := p.pools[key]
	p.mu.Unlock()
	if ok {
		mat.SetTo(gocv.Scalar{})
		pool.Put(mat)
	} else {
		mat.Close()
	}
}

func (p *MatPool) GetFromPool(src gocv.Mat) gocv.Mat {
	if src.Empty() {
		return gocv.NewMat()
	}
	return p.Get(src.Rows(), src.Cols(), src.Type())
}

var globalMatPool = NewMatPool()

func GetMat(rows, cols int, matType gocv.MatType) gocv.Mat {
	return globalMatPool.Get(rows, cols, matType)
}

func PutMat(mat gocv.Mat) {
	globalMatPool.Put(mat)
}

func GetMatFrom(src gocv.Mat) gocv.Mat {
	return globalMatPool.GetFromPool(src)
}

type ScaledTemplateCache struct {
	cache map[string][]gocv.Mat
	mu    sync.RWMutex
}

func NewScaledTemplateCache() *ScaledTemplateCache {
	return &ScaledTemplateCache{
		cache: make(map[string][]gocv.Mat),
	}
}

// scaleKey uniquely identifies a scaled-template set by template name and the
// exact scale parameters. Different step counts must not share a cache entry:
// a caller requesting 20 steps would otherwise silently reuse 60 pre-built
// mats (and the loop would iterate 60 times), defeating step-count tuning.
func scaleKey(name string, minScale, maxScale float64, steps int) string {
	return fmt.Sprintf("%s#%.4f#%.4f#%d", name, minScale, maxScale, steps)
}

func (c *ScaledTemplateCache) GetOrBuild(name string, template gocv.Mat, minScale, maxScale float64, steps int) []gocv.Mat {
	key := scaleKey(name, minScale, maxScale, steps)
	c.mu.RLock()
	cached, ok := c.cache[key]
	c.mu.RUnlock()
	if ok {
		return cached
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if cached, ok = c.cache[key]; ok {
		return cached
	}

	scaled := make([]gocv.Mat, 0, steps)
	for i := 0; i < steps; i++ {
		scale := minScale
		if steps > 1 {
			scale = minScale + (maxScale-minScale)*float64(i)/float64(steps-1)
		}

		scaledTpl := gocv.NewMat()
		gocv.Resize(template, &scaledTpl, image.Point{}, scale, scale, gocv.InterpolationLinear)

		if !scaledTpl.Empty() && scaledTpl.Cols() >= 2 && scaledTpl.Rows() >= 2 {
			scaled = append(scaled, scaledTpl)
		} else {
			scaledTpl.Close()
		}
	}

	c.cache[key] = scaled
	return scaled
}

func (c *ScaledTemplateCache) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, mats := range c.cache {
		for _, m := range mats {
			m.Close()
		}
	}
	c.cache = make(map[string][]gocv.Mat)
}

var globalTemplateCache = NewScaledTemplateCache()

func GetScaledTemplates(name string, template gocv.Mat, minScale, maxScale float64, steps int) []gocv.Mat {
	return globalTemplateCache.GetOrBuild(name, template, minScale, maxScale, steps)
}

func CloseTemplateCache() {
	globalTemplateCache.Close()
}
