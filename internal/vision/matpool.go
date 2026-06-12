package vision

import (
	"gocv.io/x/gocv"
	"image"
	"sync"
)

type MatPool struct {
	pools map[string]*sync.Pool
	mu    sync.Mutex
}

func NewMatPool() *MatPool {
	return &MatPool{
		pools: make(map[string]*sync.Pool),
	}
}

func (p *MatPool) getPoolKey(rows, cols int, matType gocv.MatType) string {
	return string(rune(matType)) + "_" + string(rune(rows)) + "_" + string(rune(cols))
}

func (p *MatPool) Get(rows, cols int, matType gocv.MatType) gocv.Mat {
	key := p.getPoolKey(rows, cols, matType)
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
	key := p.getPoolKey(mat.Rows(), mat.Cols(), mat.Type())
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

func (c *ScaledTemplateCache) GetOrBuild(name string, template gocv.Mat, minScale, maxScale float64, steps int) []gocv.Mat {
	c.mu.RLock()
	cached, ok := c.cache[name]
	c.mu.RUnlock()
	if ok {
		return cached
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if cached, ok = c.cache[name]; ok {
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

	c.cache[name] = scaled
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