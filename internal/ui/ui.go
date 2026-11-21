// Package ui is the plain progress window: a small page served on 127.0.0.1 (random port, random path) and shown in its own
// app-style browser window. No ads, no extras; a per-app status list, a log link and Close.
package ui

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

//go:embed page.html
var page []byte

// The Fileion mark for the window's title bar and taskbar (the page's favicon and web manifest): the same images as the .exe icon (winres/icons, packed by tools/mkicon).
//
//go:embed icon-32.png icon-64.png icon-192.png icon-512.png
var icons embed.FS

const manifestJSON = `{"name":"Fileion Installer","short_name":"Fileion Installer","start_url":"./","display":"standalone","background_color":"#ffffff","theme_color":"#2563eb",` +
	`"icons":[{"src":"icon-192.png","sizes":"192x192","type":"image/png"},{"src":"icon-512.png","sizes":"512x512","type":"image/png"}]}`

// Row is one app in the list.
type Row struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Link    string `json:"link,omitempty"`
	Done    int64  `json:"done,omitempty"`  // bytes of the download that have arrived
	Total   int64  `json:"total,omitempty"` // bytes in all, when the vendor says
	Since   int64  `json:"since,omitempty"` // unix ms when this status began
}

// State is what the page polls.
type State struct {
	Note     string `json:"note"`
	Rows     []Row  `json:"rows"`
	Finished bool   `json:"finished"`
	OK       int    `json:"ok"`
	Bad      int    `json:"bad"`
	Admin    bool   `json:"admin"`
	Send     bool   `json:"send"`
	NoCode   bool   `json:"nocode"`
	LogPath  string `json:"logPath"` // the real log file, so a window that lost the installer can still say where it is
}

// Server owns the state and the HTTP side.
type Server struct {
	mu       sync.Mutex
	state    State
	lastPoll time.Time
	polled   bool
	LogPath  string
	// OpenLog opens the log file itself (set by main; the window uses it after the run ends instead of a localhost link)
	OpenLog func()
	Closed  chan struct{}
	token   string
	addr    string
	once    sync.Once
}

// New starts listening and returns the server and the address to open.
func New(logPath string, admin bool) (*Server, string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return nil, "", err
	}
	s := &Server{LogPath: logPath, Closed: make(chan struct{}), token: hex.EncodeToString(b), addr: ln.Addr().String()}
	s.state.Admin, s.state.Send, s.state.LogPath = admin, true, logPath // the count is sent unless the visitor ticks "Don't send"
	mux := http.NewServeMux()
	base := "/t/" + s.token + "/"
	mux.HandleFunc(base, s.guard(s.index(base)))
	mux.HandleFunc(base+"state", s.guard(s.getState))
	mux.HandleFunc(base+"log", s.guard(s.log))
	mux.HandleFunc(base+"close", s.guard(s.close))
	mux.HandleFunc(base+"report", s.guard(s.report))
	mux.HandleFunc(base+"open-log", s.guard(s.openLog))
	mux.HandleFunc(base+"manifest.webmanifest", s.guard(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/manifest+json")
		w.Write([]byte(manifestJSON))
	}))
	for _, name := range []string{"icon-32.png", "icon-64.png", "icon-192.png", "icon-512.png"} {
		name := name
		mux.HandleFunc(base+name, s.guard(func(w http.ResponseWriter, r *http.Request) {
			b, _ := icons.ReadFile(name)
			w.Header().Set("Content-Type", "image/png")
			w.Write(b)
		}))
	}
	go http.Serve(ln, mux)
	return s, "http://" + s.addr + base, nil
}

// guard refuses requests whose Host is not this server (a page on the internet cannot reach it by DNS rebinding).
func (s *Server) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.addr {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		h(w, r)
	}
}

func (s *Server) index(base string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != base {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; img-src 'self'; manifest-src 'self'")
		w.Write(page)
	}
}

func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.lastPoll, s.polled = time.Now(), true
	st := s.state
	st.Rows = append([]Row(nil), s.state.Rows...)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(st)
}

func (s *Server) log(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(s.LogPath)
	if err != nil {
		b = []byte("No log yet.")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(b)
}

func (s *Server) close(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	s.once.Do(func() { close(s.Closed) })
	w.WriteHeader(http.StatusNoContent)
}

// openLog opens the log file in the person's own viewer (POST): after the run the window links to the file itself, not to this server.
func (s *Server) openLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	if s.OpenLog == nil {
		http.Error(w, "not available", http.StatusNotImplemented)
		return
	}
	s.OpenLog()
	w.WriteHeader(http.StatusNoContent)
}

// report is the "Don't send" box: POST ?send=0 stops the count, ?send=1 turns it back on.
func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	s.state.Send = r.URL.Query().Get("send") != "0"
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// SendReport is false once the visitor has ticked "Don't send"; main asks it at the end of the run, so a late tick counts.
func (s *Server) SendReport() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.state.Send }

// NoCode switches the window to the one message of a file that has no app list (the page adds the link to the setup page).
func (s *Server) NoCode(msg string) {
	s.mu.Lock()
	s.state.Note, s.state.NoCode, s.state.Finished = msg, true, true
	s.mu.Unlock()
}

// Note sets the line above the list ("Reading your list…").
func (s *Server) Note(text string) { s.mu.Lock(); s.state.Note = text; s.mu.Unlock() }

// SetApps lists the apps, all queued.
func (s *Server) SetApps(names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Rows = make([]Row, len(names))
	for i, n := range names {
		s.state.Rows[i] = Row{Name: n, Status: "queued", Message: "Waiting"}
	}
}

// Update changes one row. A new status starts its clock (Since) and clears the download figures.
func (s *Server) Update(i int, status, msg, link string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= 0 && i < len(s.state.Rows) {
		r := &s.state.Rows[i]
		if r.Status != status {
			r.Since, r.Done, r.Total = time.Now().UnixMilli(), 0, 0
		}
		r.Status, r.Message, r.Link = status, msg, safeLink(link)
	}
}

// Transfer sets how much of a row's download has arrived (total 0 = the vendor did not say).
func (s *Server) Transfer(i int, done, total int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= 0 && i < len(s.state.Rows) {
		s.state.Rows[i].Done, s.state.Rows[i].Total = done, total
	}
}

// Finish marks the run done and sets the counts shown at the end.
func (s *Server) Finish(ok, bad int) {
	s.mu.Lock()
	s.state.Finished, s.state.OK, s.state.Bad = true, ok, bad
	s.mu.Unlock()
}

// Abandoned is true once the run is finished and the window has stopped asking (it was closed), or never asked at all.
func (s *Server) Abandoned(finishedAt time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.state.Finished {
		return false
	}
	if !s.polled {
		return time.Since(finishedAt) > 60*time.Second
	}
	return time.Since(s.lastPoll) > 30*time.Second
}

// Sanitize keeps a vendor- or manifest-supplied text to one plain line.
func Sanitize(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 {
			return ' '
		}
		return r
	}, s)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// safeLink passes only an https address on to the page.
func safeLink(l string) string {
	if strings.HasPrefix(l, "https://") {
		return l
	}
	return ""
}
