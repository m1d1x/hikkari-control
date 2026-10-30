package main

import (
	"fmt"
	"hash/fnv"
	"html"
	"net/http"
	"strings"
	"time"
)

// decoy is the site an XHTTP edge shows to everyone without the secret path:
// a browser, a scanner, an active probe. SiteDir, when set, is served as is.
// Otherwise a one-page site is drawn from the domain, so relays do not share
// one byte-identical page a censor could fingerprint across addresses.
//
// ⚠️ The built-in page is a placeholder level of cover: plausible, small, not
// a crafted site. A relay that needs more gets EDGE_SITE_DIR.
func decoy(domain, siteDir string) http.Handler {
	if siteDir != "" {
		return onlyRead(http.FileServer(http.Dir(siteDir)))
	}
	page := []byte(decoyPage(domain))
	notFound := []byte(decoyNotFound)
	modified := time.Now().UTC().Truncate(24 * time.Hour)
	return onlyRead(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write(notFound)
			return
		}
		w.Header().Set("Last-Modified", modified.Format(http.TimeFormat))
		_, _ = w.Write(page)
	}))
}

// onlyRead answers GET and HEAD; a static site has nothing else.
func onlyRead(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.ServeHTTP(w, r)
	})
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

const decoyNotFound = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>404 Not Found</title></head>
<body><h1>Not Found</h1><p>The requested URL was not found on this server.</p></body></html>
`
