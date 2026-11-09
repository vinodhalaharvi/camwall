package wall

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/vinodhalaharvi/camwall/feeds"
)

// Hub runs one FFmpeg process for a moving feed and fans its JPEG frames out
// to every viewer. It starts when the first viewer arrives, stops a while
// after the last one leaves, and restarts with back-off if the feed drops.
type Hub struct {
	ID      int
	Feed    feeds.Feed
	Tools   feeds.Tools
	FPS     int
	MaxSide int // longest side of frames sent to browsers
	Quality int // JPEG quality for FFmpeg's -q:v, 2 (best) … 31
	Linger  time.Duration

	mu      sync.Mutex
	frame   []byte
	seq     uint64
	last    time.Time
	status  string
	subs    map[chan struct{}]struct{}
	cancel  context.CancelFunc
	done    chan struct{} // closed when the current run (and its FFmpeg) has ended
	stopper *time.Timer
	closed  bool
}

// Status is what the page shows on a tile.
type Status struct {
	State string `json:"state"` // starting, live, retrying, idle
	Age   int    `json:"age"`   // milliseconds since the last frame, -1 if none
}

func (h *Hub) Status() Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	age := -1
	if !h.last.IsZero() {
		age = int(time.Since(h.last) / time.Millisecond)
	}
	st := h.status
	if st == "" {
		st = "idle"
	}
	return Status{st, age}
}

// Latest returns the newest frame and its sequence number.
func (h *Hub) Latest() ([]byte, uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.frame, h.seq
}

// Subscribe registers a viewer. The channel gets a signal (dropped if one is
// already pending) whenever a new frame arrives. Call the returned func to leave.
func (h *Hub) Subscribe(ch chan struct{}) func() {
	h.mu.Lock()
	if h.subs == nil {
		h.subs = map[chan struct{}]struct{}{}
	}
	h.subs[ch] = struct{}{}
	if h.stopper != nil {
		h.stopper.Stop()
		h.stopper = nil
	}
	if h.cancel == nil && !h.closed {
		ctx, cancel := context.WithCancel(context.Background())
		prev, done := h.done, make(chan struct{})
		h.cancel, h.done = cancel, done
		h.status = "starting"
		go func() {
			defer close(done)
			if prev != nil {
				<-prev // never two FFmpegs for one feed
			}
			h.run(ctx)
		}()
	}
	h.mu.Unlock()
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.subs, ch)
		if len(h.subs) == 0 && h.cancel != nil && h.stopper == nil {
			h.stopper = time.AfterFunc(h.Linger, func() {
				h.mu.Lock()
				defer h.mu.Unlock()
				if len(h.subs) == 0 && h.cancel != nil {
					h.cancel()
					h.cancel, h.stopper, h.status = nil, nil, "idle"
				}
			})
		}
	}
}

// Stop ends the feed's FFmpeg for good and waits for it to exit.
func (h *Hub) Stop() {
	h.mu.Lock()
	h.closed = true
	if h.stopper != nil {
		h.stopper.Stop()
		h.stopper = nil
	}
	if h.cancel != nil {
		h.cancel()
		h.cancel, h.status = nil, "idle"
	}
	done := h.done
	h.mu.Unlock()
	if done != nil {
		<-done
	}
}

// SnapshotEvery is how often a picture on a server is fetched again.
var SnapshotEvery = 2 * time.Second

func (h *Hub) args(f feeds.Feed) []string {
	scale := "scale='if(gte(iw,ih),min(" + strconv.Itoa(h.MaxSide) + ",iw),-2)':'if(gte(iw,ih),-2,min(" + strconv.Itoa(h.MaxSide) + ",ih))'"
	vf := "fps=" + strconv.Itoa(h.FPS) + "," + scale
	if f.Filter != "" {
		vf = f.Filter + "," + vf
	}
	a := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	if f.Snapshot() { // one fresh picture per run; run fetches it again
		a = append(a, f.SnapshotArgs()...)
		return append(a, "-an", "-vf", scale, "-frames:v", "1", "-c:v", "mjpeg", "-q:v", strconv.Itoa(h.Quality), "-f", "image2pipe", "pipe:1")
	}
	if f.Kind == feeds.Live {
		// pick frames by when they arrive: some live feeds (MJPEG over HTTP)
		// carry no times of their own, and after a stall only the newest
		// frame of the burst is shown
		vf = "setpts=(RTCTIME-RTCSTART)/(TB*1000000)," + vf
	}
	a = append(a, f.InputArgs(h.FPS)...)
	return append(a, "-an", "-vf", vf, "-c:v", "mjpeg", "-q:v", strconv.Itoa(h.Quality), "-f", "image2pipe", "pipe:1")
}

func (h *Hub) run(ctx context.Context) {
	defer func() {
		// a stopped feed shows nothing rather than an old picture as if live
		h.mu.Lock()
		h.frame, h.last = nil, time.Time{}
		h.seq++
		h.mu.Unlock()
	}()
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		h.mu.Lock()
		seq := h.seq
		h.mu.Unlock()
		err := h.once(ctx)
		if ctx.Err() != nil {
			return
		}
		if h.Feed.Snapshot() {
			h.mu.Lock()
			fresh := h.seq != seq
			h.mu.Unlock()
			if fresh { // fetched: fetch again shortly, like a camera's own viewer
				select {
				case <-ctx.Done():
					return
				case <-time.After(SnapshotEvery):
				}
				backoff = time.Second
				continue
			}
		}
		h.mu.Lock()
		h.status = "retrying"
		played := h.seq-seq > 1 // it showed pictures before it stopped
		h.mu.Unlock()
		if played || time.Since(start) > 30*time.Second {
			// a feed that played and then ended (a recorded HLS playlist, a
			// camera that restarted) comes back quickly; only feeds that keep
			// failing back off
			backoff = time.Second
		}
		if !(played && err.Error() == "feed ended" && h.Feed.Kind != feeds.Live) { // a file played to its end: not news
			log.Printf("feed %d (%s): %v; retrying in %v", h.ID+1, h.Feed.Name, err, backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, 30*time.Second)
	}
}

type tail struct{ b []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 2048 {
		t.b = t.b[len(t.b)-2048:]
	}
	return len(p), nil
}

func (h *Hub) once(ctx context.Context) error {
	// a web page's media URL expires: resolve it again on every start
	octx, cancel := context.WithTimeout(ctx, 30*time.Second)
	f, err := h.Feed.Opened(octx, h.Tools)
	cancel()
	if err != nil {
		return &ffError{feeds.Redact(err.Error())}
	}
	cmd := feeds.Child(exec.CommandContext(ctx, h.Tools.FFmpeg, h.args(f)...))
	cmd.WaitDelay = 5 * time.Second
	errs := &tail{}
	cmd.Stderr = errs
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	readErr := splitJPEG(bufio.NewReaderSize(out, 1<<16), h.publish)
	waitErr := cmd.Wait()
	if msg := bytes.TrimSpace(errs.b); len(msg) > 0 {
		if i := bytes.LastIndexByte(msg, '\n'); i >= 0 {
			msg = msg[i+1:]
		}
		return &ffError{feeds.Redact(string(msg))}
	}
	if waitErr != nil {
		return waitErr
	}
	if readErr != nil && readErr != io.EOF {
		return readErr
	}
	return &ffError{"feed ended"}
}

type ffError struct{ msg string }

func (e *ffError) Error() string { return e.msg }

func (h *Hub) publish(jpg []byte) {
	h.mu.Lock()
	h.frame, h.last, h.status = jpg, time.Now(), "live"
	h.seq++
	for ch := range h.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	h.mu.Unlock()
}

// splitJPEG cuts a stream of concatenated JPEGs (as FFmpeg's image2pipe muxer
// writes them) into frames by walking their markers: header segments are
// skipped by their length, and inside entropy-coded data a 0xFF is only a
// marker when not followed by 0x00 or a restart code. So the end-of-image
// marker is found exactly, whatever bytes the tables or pixels contain.
func splitJPEG(r *bufio.Reader, emit func([]byte)) error {
	for {
		frame, err := readJPEG(r)
		if err != nil {
			return err
		}
		if frame != nil {
			emit(frame)
		}
	}
}

const maxFrame = 16 << 20

func readJPEG(r *bufio.Reader) ([]byte, error) {
	// find start of image
	for {
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if b != 0xFF {
			continue
		}
		nb, err := r.Peek(1)
		if err != nil {
			return nil, err
		}
		if nb[0] == 0xD8 {
			r.ReadByte()
			break
		}
	}
	buf := []byte{0xFF, 0xD8}
	marker := func() (byte, error) { // reads FF xx, skipping fill bytes
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		if b != 0xFF {
			return 0, errBadJPEG
		}
		for {
			m, err := r.ReadByte()
			if err != nil {
				return 0, err
			}
			if m != 0xFF {
				return m, nil
			}
		}
	}
	m, err := marker()
	for {
		if err != nil {
			if err == errBadJPEG {
				return nil, nil // resync on the next start of image
			}
			return nil, err
		}
		buf = append(buf, 0xFF, m)
		switch {
		case m == 0xD9:
			return buf, nil
		case m == 0x01 || (m >= 0xD0 && m <= 0xD7):
			m, err = marker()
			continue
		}
		var lb [2]byte
		if _, err = io.ReadFull(r, lb[:]); err != nil {
			return nil, err
		}
		n := int(lb[0])<<8 | int(lb[1])
		if n < 2 || len(buf)+n > maxFrame {
			return nil, nil
		}
		buf = append(buf, lb[0], lb[1])
		seg := make([]byte, n-2)
		if _, err = io.ReadFull(r, seg); err != nil {
			return nil, err
		}
		buf = append(buf, seg...)
		if m != 0xDA {
			m, err = marker()
			continue
		}
		// entropy-coded data until the next real marker
		for {
			b, e := r.ReadByte()
			if e != nil {
				return nil, e
			}
			if b != 0xFF {
				buf = append(buf, b)
				if len(buf) > maxFrame {
					return nil, nil
				}
				continue
			}
			nx, e := r.ReadByte()
			if e != nil {
				return nil, e
			}
			for nx == 0xFF { // fill bytes
				if nx, e = r.ReadByte(); e != nil {
					return nil, e
				}
			}
			if nx == 0x00 || (nx >= 0xD0 && nx <= 0xD7) {
				buf = append(buf, 0xFF, nx)
				continue
			}
			m, err = nx, nil
			break
		}
	}
}

var errBadJPEG = &ffError{"bad JPEG"}
