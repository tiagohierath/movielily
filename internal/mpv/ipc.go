// Package mpv drives mpv for live logging (JSON IPC) and sequence review
// (mpv EDL). movielily never decodes or renders video itself.
package mpv

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// Event is an asynchronous message from mpv (e.g. a client-message raised by a
// key binding).
type Event struct {
	Event string   `json:"event"`
	Args  []string `json:"args"`
}

type response struct {
	RequestID int             `json:"request_id"`
	Error     string          `json:"error"`
	Data      json.RawMessage `json:"data"`
}

// Client is a JSON IPC connection to a running mpv instance. It is safe for
// concurrent use (e.g. a key-event loop and a clock ticker).
type Client struct {
	conn    net.Conn
	mu      sync.Mutex
	writeMu sync.Mutex
	nextID  int
	wait    map[int]chan response
	events  chan Event
	closed  chan struct{}
	once    sync.Once
}

// Dial connects to mpv's IPC socket, retrying until timeout (mpv creates the
// socket a moment after launch).
func Dial(socket string, timeout time.Duration) (*Client, error) {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.Dial("unix", socket)
		if err == nil {
			c := &Client{
				conn:   conn,
				nextID: 1,
				wait:   map[int]chan response{},
				events: make(chan Event, 64),
				closed: make(chan struct{}),
			}
			go c.readLoop()
			return c, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("could not connect to mpv IPC at %s: %w", socket, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (c *Client) readLoop() {
	sc := bufio.NewScanner(c.conn)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var probe struct {
			Event     string `json:"event"`
			RequestID *int   `json:"request_id"`
		}
		if json.Unmarshal(line, &probe) != nil {
			continue
		}
		switch {
		case probe.Event != "":
			var e Event
			if json.Unmarshal(line, &e) == nil {
				select {
				case c.events <- e:
				default: // drop if nobody is keeping up
				}
			}
		case probe.RequestID != nil:
			var r response
			if json.Unmarshal(line, &r) == nil {
				c.mu.Lock()
				ch := c.wait[r.RequestID]
				delete(c.wait, r.RequestID)
				c.mu.Unlock()
				if ch != nil {
					ch <- r
				}
			}
		}
	}
	c.close()
}

// Events delivers asynchronous mpv events.
func (c *Client) Events() <-chan Event { return c.events }

// Done is closed when the connection drops (mpv quit).
func (c *Client) Done() <-chan struct{} { return c.closed }

func (c *Client) close() {
	c.once.Do(func() {
		close(c.closed)
		c.conn.Close()
	})
}

// Close shuts the connection.
func (c *Client) Close() error { c.close(); return nil }

// Command sends a command and waits for its response data.
func (c *Client) Command(args ...interface{}) (json.RawMessage, error) {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	ch := make(chan response, 1)
	c.wait[id] = ch
	c.mu.Unlock()

	b, err := json.Marshal(map[string]interface{}{"command": args, "request_id": id})
	if err != nil {
		return nil, err
	}
	c.writeMu.Lock()
	_, werr := c.conn.Write(append(b, '\n'))
	c.writeMu.Unlock()
	if werr != nil {
		return nil, werr
	}

	select {
	case r := <-ch:
		if r.Error != "success" && r.Error != "" {
			return nil, fmt.Errorf("mpv: %s", r.Error)
		}
		return r.Data, nil
	case <-c.closed:
		return nil, fmt.Errorf("mpv connection closed")
	case <-time.After(3 * time.Second):
		return nil, fmt.Errorf("mpv command timed out")
	}
}

// TimePos returns the current playback position in seconds.
func (c *Client) TimePos() (float64, error) {
	data, err := c.Command("get_property", "time-pos")
	if err != nil {
		return 0, err
	}
	var f float64
	if err := json.Unmarshal(data, &f); err != nil {
		return 0, err
	}
	return f, nil
}

// Bind binds a key to broadcast a script-message we can recognise over IPC.
func (c *Client) Bind(key, message string) error {
	_, err := c.Command("keybind", key, "script-message "+message)
	return err
}

// Chapter is a point on mpv's seekbar (a marker, or an IN/OUT point).
type Chapter struct {
	Title string  `json:"title"`
	Time  float64 `json:"time"`
}

// SetChapters replaces mpv's chapter list so the points show as ticks on the
// seekbar (and can be jumped between). Best-effort: visualisation only, the
// text files remain the source of truth.
func (c *Client) SetChapters(chs []Chapter) error {
	_, err := c.Command("set_property", "chapter-list", chs)
	return err
}

// SetProp sets an mpv property. Used for ab-loop-a/ab-loop-b, which draw the
// IN/OUT region directly on the seekbar (pass "no" to clear a loop point).
func (c *Client) SetProp(name string, value interface{}) error {
	_, err := c.Command("set_property", name, value)
	return err
}

// OSDOverlay draws persistent ASS text on the video — an always-visible HUD,
// independent of the OSC/seekbar. Re-call with the same id to update it.
func (c *Client) OSDOverlay(id int, ass string) error {
	_, err := c.Command("osd-overlay", id, "ass-events", ass)
	return err
}

// OSD shows a short on-screen message.
func (c *Client) OSD(msg string) { _, _ = c.Command("show-text", msg, 1500) }
