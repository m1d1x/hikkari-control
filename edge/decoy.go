package main

import (
	"fmt"
	"hash/fnv"
	"html"
	"net/http"
	"path"
	"strings"
	"time"
)

// decoy is the site an XHTTP edge shows to everyone without the secret path:
// a browser, a scanner, an active probe. SiteDir, when set, is served as nginx
// would serve it.
// Otherwise a one-page site is drawn from the domain, so relays do not share
// one byte-identical page a censor could fingerprint across addresses.
//
// ⚠️ The built-in page is a placeholder level of cover: plausible, small, not
// a crafted site. A relay that needs more gets EDGE_SITE_DIR.
func decoy(domain, siteDir string) http.Handler {
	if siteDir != "" {
		return likeNginx(siteFiles(siteDir))
	}
	page := []byte(decoyPage(domain))
	modified := time.Now().UTC().Truncate(24 * time.Hour)
	return likeNginx(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			nginxError(w, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Last-Modified", modified.Format(http.TimeFormat))
		w.Header().Set("Accept-Ranges", "bytes")
		_, _ = w.Write(page)
	}))
}

// likeNginx is what a scanner reads besides the page: the Server header, and
// the error a static nginx gives. Go's own answers carry "404 page not found",
// X-Content-Type-Options and a text/plain error body, which nginx never does
// (NTC Party 25993: what a scanner sees is what gets an address banned).
//
// The order of headers is the one difference left: net/http sorts them. It shows
// on HTTP/1.1 only to whoever compares the order; the certificate and ALPN of
// the same answer say h2 first.
func likeNginx(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "nginx")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			nginxError(w, http.StatusMethodNotAllowed)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// siteFiles serves dir as nginx would with no autoindex: index.html for a
// directory, and a 404 for a directory without one, not a listing.
func siteFiles(dir string) http.Handler {
	root := http.Dir(dir)
	files := http.FileServer(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, err := root.Open(path.Clean("/" + r.URL.Path))
		if err != nil {
			nginxError(w, http.StatusNotFound)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			nginxError(w, http.StatusNotFound)
			return
		}
		if st.IsDir() {
			index, err := root.Open(path.Join(path.Clean("/"+r.URL.Path), "index.html"))
			if err != nil {
				nginxError(w, http.StatusNotFound)
				return
			}
			_ = index.Close()
		}
		files.ServeHTTP(w, r)
	})
}

// nginxStatus are the titles of the errors a static site gives. nginx calls 405
// "Not Allowed", not Go's "Method Not Allowed".
var nginxStatus = map[int]string{
	http.StatusNotFound:         "404 Not Found",
	http.StatusMethodNotAllowed: "405 Not Allowed",
}

// nginxError writes the error page nginx writes, byte for byte.
func nginxError(w http.ResponseWriter, code int) {
	title := nginxStatus[code]
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w, "<html>\r\n<head><title>%s</title></head>\r\n<body>\r\n<center><h1>%s</h1></center>\r\n<hr><center>nginx</center>\r\n</body>\r\n</html>\r\n", title, title)
}

var (
	decoyTopics = []struct{ title, line string }{
		{"Field notes", "Photographs and short notes from walks, mostly in the off season."},
		{"Workshop", "Small woodworking projects, jigs and the mistakes behind them."},
		{"Kitchen log", "Recipes tested more than once, with the changes that stuck."},
		{"Signal & noise", "Occasional writing on radio, antennas and old receivers."},
		{"Paper trail", "Reading notes: what I finished, what I gave up on, and why."},
		{"Slow miles", "Cycling routes, gear lists and the weather that ruined them."},
	}
	decoyColors = []struct{ bg, fg, accent string }{
		{"#fbfaf7", "#222", "#8a5a2b"},
		{"#f4f6f8", "#1d2733", "#2f6f9f"},
		{"#fffdf5", "#2b2b2b", "#6b7f3a"},
		{"#f7f4fb", "#26222e", "#6d4c9f"},
	}
)

func decoyPage(domain string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(domain))
	n := h.Sum32()
	topic := decoyTopics[n%uint32(len(decoyTopics))]
	c := decoyColors[(n/7)%uint32(len(decoyColors))]
	name := html.EscapeString(strings.SplitN(domain, ".", 2)[0])
	return fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%[1]s · %[2]s</title>
<style>
body{margin:0;background:%[4]s;color:%[5]s;font:17px/1.6 Georgia,serif}
main{max-width:38rem;margin:12vh auto;padding:0 1.25rem}
h1{font-weight:normal;color:%[6]s;margin:0 0 .5rem}
footer{margin-top:3rem;font-size:.85rem;opacity:.6}
</style>
</head>
<body>
<main>
<h1>%[1]s</h1>
<p>%[3]s</p>
<p>New posts are on hold for a while. Older ones will come back after the move.</p>
<footer>%[2]s</footer>
</main>
</body>
</html>
`, html.EscapeString(topic.title), name, html.EscapeString(topic.line), c.bg, c.fg, c.accent)
}
