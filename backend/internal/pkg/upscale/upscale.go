package upscale

import (
	"bytes"
	"container/list"
	"errors"
	"fmt"
	gimage "image"
	"image/png"
	"runtime"
	"sync"

	_ "image/gif"
	_ "image/jpeg"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	ScaleNone = ""
	Scale2K   = "2k"
	Scale4K   = "4k"
)

func ValidateScale(s string) string {
	switch s {
	case Scale2K, Scale4K:
		return s
	default:
		return ScaleNone
	}
}

func longSideOf(scale string) int {
	switch scale {
	case Scale2K:
		return 2560
	case Scale4K:
		return 3840
	default:
		return 0
	}
}

var ErrDecode = errors.New("upscale: decode source failed")

func Do(src []byte, scale string) ([]byte, string, error) {
	scale = ValidateScale(scale)
	target := longSideOf(scale)
	if target == 0 || len(src) == 0 {
		return src, "", nil
	}

	srcImg, _, err := gimage.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrDecode, err)
	}

	b := srcImg.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return nil, "", ErrDecode
	}

	long := sw
	if sh > long {
		long = sh
	}
	if long >= target {
		return src, "", nil
	}

	var dw, dh int
	if sw >= sh {
		dw = target
		dh = int(float64(sh) * float64(target) / float64(sw))
		if dh < 1 {
			dh = 1
		}
	} else {
		dh = target
		dw = int(float64(sw) * float64(target) / float64(sh))
		if dw < 1 {
			dw = 1
		}
	}

	dst := gimage.NewRGBA(gimage.Rect(0, 0, dw, dh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), srcImg, b, draw.Src, nil)

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, dst); err != nil {
		return nil, "", fmt.Errorf("upscale: png encode: %w", err)
	}
	return buf.Bytes(), "image/png", nil
}

// Cache is a process-level LRU byte cache with a concurrency semaphore.
type Cache struct {
	mu       sync.Mutex
	items    map[string]*list.Element
	order    *list.List
	maxBytes int64
	curBytes int64
	sem      chan struct{}
}

type cacheEntry struct {
	key         string
	data        []byte
	contentType string
}

func NewCache(maxBytes int64, concurrency int) *Cache {
	if maxBytes <= 0 {
		maxBytes = 512 * 1024 * 1024
	}
	if concurrency <= 0 {
		concurrency = runtime.NumCPU()
		if concurrency < 2 {
			concurrency = 2
		}
	}
	return &Cache{
		items:    make(map[string]*list.Element),
		order:    list.New(),
		maxBytes: maxBytes,
		sem:      make(chan struct{}, concurrency),
	}
}

func (c *Cache) Get(key string) ([]byte, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, "", false
	}
	c.order.MoveToBack(el)
	e := el.Value.(*cacheEntry)
	return e.data, e.contentType, true
}

func (c *Cache) Put(key string, data []byte, contentType string) {
	if len(data) == 0 || int64(len(data)) > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		old := el.Value.(*cacheEntry)
		c.curBytes -= int64(len(old.data))
		old.data = data
		old.contentType = contentType
		c.curBytes += int64(len(data))
		c.order.MoveToBack(el)
		return
	}
	e := &cacheEntry{key: key, data: data, contentType: contentType}
	el := c.order.PushBack(e)
	c.items[key] = el
	c.curBytes += int64(len(data))
	for c.curBytes > c.maxBytes {
		front := c.order.Front()
		if front == nil {
			break
		}
		old := front.Value.(*cacheEntry)
		c.order.Remove(front)
		delete(c.items, old.key)
		c.curBytes -= int64(len(old.data))
	}
}

func (c *Cache) Acquire() { c.sem <- struct{}{} }
func (c *Cache) Release() { <-c.sem }
