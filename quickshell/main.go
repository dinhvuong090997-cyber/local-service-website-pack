// quickshell — lightweight Go dev server with live reload
// Zero external dependencies. Single binary. Tokyo Night aesthetic.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ── Tokyo Night ANSI Colors ──
const (
	Reset    = "\033[0m"
	Bold     = "\033[1m"
	Dim      = "\033[2m"

	BgDark   = "\033[48;2;26;27;38m"  // #1a1b26
	FgText   = "\033[38;2;192;202;245m" // #c0caf5
	FgDim    = "\033[38;2;86;95;137m"   // #565f89
	FgBlue   = "\033[38;2;122;162;247m" // #7aa2f7
	FgPurple = "\033[38;2;187;154;247m" // #bb9af7
	FgGreen  = "\033[38;2;158;206;106m" // #9ece6a
	FgRed    = "\033[38;2;247;118;142m" // #f7768e
	FgYellow = "\033[38;2;224;175;104m" // #e0af68
	FgCyan   = "\033[38;2;125;207;255m" // #7dcfff
	FgOrange = "\033[38;2;255;158;100m" // #ff9e64
)

// ── SSE Hub ──
type hub struct {
	mu      sync.RWMutex
	clients map[chan string]struct{}
}

func newHub() *hub {
	return &hub{clients: make(map[chan string]struct{})}
}

func (h *hub) add() chan string {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan string, 16)
	h.clients[ch] = struct{}{}
	return ch
}

func (h *hub) remove(ch chan string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, ch)
}

func (h *hub) broadcast(msg string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.clients {
		select {
		case ch <- msg:
		default:
		}
	}
}

// ── File Watcher (polling, zero deps) ──
type watcher struct {
	root   string
	hashes map[string]string
	hub    *hub
	log    func(string, ...interface{})
	stopCh chan struct{}
}

func newWatcher(root string, hub *hub, logFn func(string, ...interface{})) *watcher {
	return &watcher{
		root:   root,
		hashes: make(map[string]string),
		hub:    hub,
		log:    logFn,
		stopCh: make(chan struct{}),
	}
}

func (w *watcher) start() {
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				w.poll()
			case <-w.stopCh:
				return
			}
		}
	}()
}

func (w *watcher) stop() {
	close(w.stopCh)
}

// Only watch files we care about (HTML, CSS, JS, Go). Skip .git.
var watchRe = regexp.MustCompile(`\.(html?|css|js|go|json|yaml|yml|toml|md)$`)

func (w *watcher) poll() {
	changed := false
	filepath.Walk(w.root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() {
				base := filepath.Base(path)
				if base == ".git" || base == ".worktrees" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !watchRe.MatchString(path) {
			return nil
		}

		rel, _ := filepath.Rel(w.root, path)
		hash, err := fileHash(path)
		if err != nil {
			return nil
		}

		prev, exists := w.hashes[rel]
		w.hashes[rel] = hash
		if exists && prev != hash {
			w.log(FgYellow+"  ● %s changed"+Reset, rel)
			changed = true
		}
		return nil
	})
	if changed {
		w.hub.broadcast("reload")
	}
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)[:8]), nil
}

// ── Inject live-reload script into HTML ──
var htmlRe = regexp.MustCompile(`(?i)</head>`)

func injectScript(body []byte, port int) []byte {
	script := fmt.Sprintf(`<script>new EventSource("http://localhost:%d/sse").onmessage=e=>{if("reload"===e.data)location.reload()};</script></head>`, port)
	return htmlRe.ReplaceAll(body, []byte(script))
}

// ── Main ──
func main() {
	port := flag.Int("p", 8080, "port")
	dir := flag.String("d", ".", "root directory")
	flag.Parse()

	root, _ := filepath.Abs(*dir)
	h := newHub()
	log := func(format string, args ...interface{}) {
		fmt.Printf(FgDim+time.Now().Format("15:04:05")+Reset+" "+format+"\n", args...)
	}

	w := newWatcher(root, h, log)

	// Middleware: inject SSE script into HTML, log requests
	handler := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		path := req.URL.Path
		realPath := filepath.Join(root, path)

		// Check if file exists
		info, err := os.Stat(realPath)
		if err != nil {
			// Try index.html for directories
			if strings.HasSuffix(path, "/") || path == "" {
				realPath = filepath.Join(root, path, "index.html")
				info, err = os.Stat(realPath)
			}
			if err != nil {
				http.NotFound(rw, req)
				log(FgRed+"  ✗ 404 %s"+Reset, path)
				return
			}
		}
		if info.IsDir() {
			realPath = filepath.Join(realPath, "index.html")
			if _, err := os.Stat(realPath); err != nil {
				http.NotFound(rw, req)
				log(FgRed+"  ✗ 404 %s"+Reset, path)
				return
			}
		}

		// Read file
		data, err := os.ReadFile(realPath)
		if err != nil {
			http.Error(rw, err.Error(), 500)
			log(FgRed+"  ✗ ERR %s: %v"+Reset, path, err)
			return
		}

		// Detect content type
		ext := strings.ToLower(filepath.Ext(realPath))
		ct := mimeType(ext)

		// Inject live reload only into HTML
		if ext == ".html" || ext == ".htm" {
			data = injectScript(data, *port)
			ct = "text/html; charset=utf-8"
		}

		rw.Header().Set("Content-Type", ct)
		rw.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		rw.WriteHeader(200)
		rw.Write(data)

		// Pretty log
		method := req.Method
		methodColor := FgCyan
		if method == "POST" {
			methodColor = FgYellow
		}
		log("%s%s%s %s%s%s %s→%s %s%d%s",
			methodColor, method, Reset,
			FgText, path, Reset,
			FgDim, Reset,
			FgGreen, 200, Reset,
		)
	})

	// SSE endpoint
	http.HandleFunc("/sse", func(rw http.ResponseWriter, req *http.Request) {
		flusher, ok := rw.(http.Flusher)
		if !ok {
			http.Error(rw, "streaming unsupported", 500)
			return
		}
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.Header().Set("Cache-Control", "no-cache")
		rw.Header().Set("Connection", "keep-alive")
		rw.Header().Set("Access-Control-Allow-Origin", "*")
		rw.WriteHeader(200)

		ch := h.add()
		defer h.remove(ch)

		// Send initial keepalive
		fmt.Fprintf(rw, ": keepalive\n\n")
		flusher.Flush()

		for {
			select {
			case msg := <-ch:
				fmt.Fprintf(rw, "data: %s\n\n", msg)
				flusher.Flush()
			case <-req.Context().Done():
				return
			}
		}
	})

	http.Handle("/", handler)

	// Header
	fmt.Print(BgDark)
	fmt.Printf("%s%s  quickshell  %s\n", Bold+FgBlue, "⚡", Reset+FgDim+"v0.1.0"+Reset)
	fmt.Printf("%s  %s%s %s %s\n",
		FgDim, "⎯", strings.Repeat("⎯", 50), Reset,
		BgDark,
	)
	fmt.Printf("  %s●%s %s %shttp://localhost:%d%s\n", FgGreen, Reset, FgText+root+FgDim, FgBlue, *port, Reset)
	fmt.Printf("  %s●%s %sLive reload%s  (edit HTML/CSS/JS → browser auto-refresh)\n", FgPurple, Reset, FgText, Reset)
	fmt.Printf("%s\n", Reset)

	w.start()
	defer w.stop()

	log(FgGreen+"  ✓ serving at http://localhost:%d"+Reset, *port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", *port), nil); err != nil {
		log(FgRed+"  ✗ %v"+Reset, err)
	}
}

func mimeType(ext string) string {
	switch ext {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "application/javascript"
	case ".json":
		return "application/json"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		return "image/x-icon"
	case ".webp":
		return "image/webp"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".pdf":
		return "application/pdf"
	case ".xml":
		return "application/xml"
	default:
		return "application/octet-stream"
	}
}
