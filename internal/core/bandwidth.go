package core

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// The bandwidth cap applies to all downloads together. Downloads made by this program (HTTP,
// HLS, and yt-dlp's through the local proxy) draw from one token bucket; torrents run in aria2c,
// whose own overall limit is set to what the other downloads leave over, and vice versa. The
// cap is also the target: the scheduler adds downloads while the total stays below it.

type Bandwidth struct {
	mu     sync.Mutex
	cap    float64 // bytes/s for everything; 0 = unlimited
	rate   float64 // bytes/s for this program's own downloads
	tokens float64
	last   time.Time

	bytes     atomic.Int64 // this program's downloaded bytes
	sampleN   int64
	sampleAt  time.Time
	own, tor  float64 // smoothed speeds
	torLimit  float64
	lastTotal float64
}

var bw = &Bandwidth{}

// SetCap sets the overall limit in bytes per second (0: unlimited).
func (b *Bandwidth) SetCap(bps float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cap = max(0, bps)
	b.recomputeLocked()
}

func (b *Bandwidth) Cap() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cap
}

func (b *Bandwidth) recomputeLocked() {
	if b.cap <= 0 {
		b.rate, b.torLimit = 0, 0
		return
	}
	// Each side may use what the other isn't using, but never less than a quarter.
	b.rate = max(b.cap*0.25, b.cap-b.tor)
	b.torLimit = max(b.cap*0.25, b.cap-b.own*1.05)
	if b.tokens > b.burstLocked() {
		b.tokens = b.burstLocked()
	}
}

func (b *Bandwidth) burstLocked() float64 { return max(b.rate/8, 64<<10) }

// take accounts for n bytes just received, waiting if the cap requires it.
func (b *Bandwidth) take(ctx context.Context, n int) error {
	if n <= 0 {
		return nil
	}
	b.bytes.Add(int64(n))
	b.mu.Lock()
	if b.rate <= 0 {
		b.mu.Unlock()
		return nil
	}
	now := time.Now()
	if b.last.IsZero() {
		b.last = now
	}
	b.tokens = min(b.tokens+b.rate*now.Sub(b.last).Seconds(), b.burstLocked())
	b.last = now
	b.tokens -= float64(n)
	var wait time.Duration
	if b.tokens < 0 {
		wait = time.Duration(-b.tokens / b.rate * float64(time.Second))
	}
	b.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Reader wraps r so that reading from it counts against the cap.
func (b *Bandwidth) Reader(ctx context.Context, r io.Reader) io.Reader {
	return &limitedReader{ctx: ctx, r: r, b: b}
}

type limitedReader struct {
	ctx context.Context
	r   io.Reader
	b   *Bandwidth
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if len(p) > 32<<10 {
		p = p[:32<<10]
	}
	n, err := l.r.Read(p)
	if werr := l.b.take(l.ctx, n); werr != nil && err == nil {
		err = werr
	}
	return n, err
}

// sample updates the measured speeds; torrent is aria2c's current overall download speed.
// It returns the limit to give aria2c (0: none).
func (b *Bandwidth) sample(torrent float64) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	n := b.bytes.Load()
	if !b.sampleAt.IsZero() {
		if dt := now.Sub(b.sampleAt).Seconds(); dt > 0 {
			inst := float64(n-b.sampleN) / dt
			b.own = 0.5*b.own + 0.5*inst
		}
	}
	b.sampleAt, b.sampleN = now, n
	b.tor = 0.5*b.tor + 0.5*torrent
	b.lastTotal = b.own + b.tor
	b.recomputeLocked()
	return b.torLimit
}

// Speeds returns the smoothed speeds of this program's downloads and of torrents, and the cap.
func (b *Bandwidth) Speeds() (own, torrent, cap float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.own, b.tor, b.cap
}
