package entry

import (
	"fmt"
	"hash/fnv"
	"html"
	"strings"
)

// frontSite is the cover site a real nginx serves on an XHTTP relay's domain.
// The owner's rule for an XHTTP node (H10, research 2026-11-05-xhttp-knobs): a
// genuine site of at least 20 KB, as zovfy's is (~19.6 KB), so a crawler that
// grades the domain finds a lived-in page, not an empty one a censor bans
// faster. The edge's built-in decoy (edge/decoy.go) is a ~700-byte placeholder;
// this is the real thing a fronted node serves instead.
//
// The page is drawn from the domain, so no two relays share one byte-identical
// page a censor could fingerprint across addresses, and it is deterministic:
// the same domain always renders the same site, so a rerun of provisioning does
// not churn it. It is plain static HTML with inline CSS — nothing for nginx to
// execute, nothing that calls home.
func frontSite(domain string) []byte {
	rng := newDomainRand(domain)
	name := html.EscapeString(strings.SplitN(domain, ".", 2)[0])

	theme := frontThemes[rng.pick(len(frontThemes))]
	persona := frontPersonas[rng.pick(len(frontPersonas))]

	var b strings.Builder
	fmt.Fprintf(&b, `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%[1]s — %[2]s</title>
<meta name="description" content="%[3]s">
<style>
:root{--bg:%[4]s;--fg:%[5]s;--accent:%[6]s;--muted:%[7]s}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font:17px/1.65 Georgia,"Times New Roman",serif}
header{border-bottom:1px solid var(--muted);padding:2rem 1.25rem}
header .wrap,main,footer .wrap{max-width:44rem;margin:0 auto}
h1{font-weight:normal;color:var(--accent);margin:0 0 .35rem;font-size:1.9rem}
nav{margin-top:.75rem;font-size:.95rem}
nav a{color:var(--fg);text-decoration:none;margin-right:1.1rem;border-bottom:1px dotted var(--muted)}
main{padding:2rem 1.25rem}
article{margin:0 0 2.75rem}
article h2{font-size:1.35rem;margin:0 0 .25rem}
article h2 a{color:var(--fg);text-decoration:none}
article .date{color:var(--accent);font-size:.82rem;letter-spacing:.03em;text-transform:uppercase}
article p{margin:.8rem 0}
aside{margin:2.5rem 0;padding:1rem 1.25rem;border-left:3px solid var(--accent);background:rgba(0,0,0,.03);font-size:.95rem}
footer{border-top:1px solid var(--muted);padding:1.75rem 1.25rem;color:var(--muted);font-size:.85rem}
</style>
</head>
<body>
<header><div class="wrap">
<h1>%[2]s</h1>
<div class="tagline">%[3]s</div>
<nav><a href="/">Home</a><a href="/archive/">Archive</a><a href="/notes/">Notes</a><a href="/about/">About</a><a href="/feed.xml">RSS</a></nav>
</div></header>
<main>
`, name, html.EscapeString(persona.site), html.EscapeString(persona.tagline),
		theme.bg, theme.fg, theme.accent, theme.muted)

	// Posts until the page is comfortably past 20 KB. Deterministic by domain:
	// the count and every line come from the one seeded stream.
	const minBytes = 22 * 1024
	year := 2024 + rng.pick(2)
	month := 12
	for i := 0; b.Len() < minBytes || i < 5; i++ {
		title := rng.title()
		day := 1 + rng.pick(27)
		if month < 1 {
			month = 12
			year--
		}
		fmt.Fprintf(&b, `<article>
<div class="date">%s %d, %d</div>
<h2><a href="/%s/">%s</a></h2>
`, frontMonths[month-1], day, year, rng.slug(), html.EscapeString(title))
		paras := 3 + rng.pick(3)
		for p := 0; p < paras; p++ {
			fmt.Fprintf(&b, "<p>%s</p>\n", html.EscapeString(rng.paragraph(3+rng.pick(3))))
		}
		if rng.pick(3) == 0 {
			fmt.Fprintf(&b, "<aside>%s</aside>\n", html.EscapeString(rng.paragraph(2)))
		}
		b.WriteString("</article>\n")
		month--
	}

	fmt.Fprintf(&b, `</main>
<footer><div class="wrap">
<p>%s</p>
<p>&copy; %d %s. Written by hand. No trackers, no ads.</p>
</div></footer>
</body>
</html>
`, html.EscapeString(persona.footer), year, html.EscapeString(persona.site))
	return []byte(b.String())
}

// domainRand is a tiny deterministic stream seeded by the domain: the same
// domain always draws the same site, different domains draw different ones. Not
// for anything but drawing a plausible page.
type domainRand struct{ s uint64 }

func newDomainRand(domain string) *domainRand {
	h := fnv.New64a()
	_, _ = h.Write([]byte(domain))
	return &domainRand{s: h.Sum64() | 1}
}

// next is a xorshift step: enough spread for a page, no dependencies.
func (r *domainRand) next() uint64 {
	r.s ^= r.s << 13
	r.s ^= r.s >> 7
	r.s ^= r.s << 17
	return r.s
}

func (r *domainRand) pick(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next() % uint64(n))
}

func (r *domainRand) word() string { return frontWords[r.pick(len(frontWords))] }

func (r *domainRand) title() string {
	n := 3 + r.pick(4)
	words := make([]string, n)
	for i := range words {
		words[i] = r.word()
	}
	t := strings.Join(words, " ")
	return strings.ToUpper(t[:1]) + t[1:]
}

func (r *domainRand) slug() string {
	n := 2 + r.pick(2)
	words := make([]string, n)
	for i := range words {
		words[i] = r.word()
	}
	return strings.Join(words, "-")
}

// paragraph builds sentences of plausible length from the word bank. Content is
// filler, but its shape — sentences, capitals, commas, full stops — is a page's,
// not lorem ipsum a grader flags at once.
func (r *domainRand) paragraph(sentences int) string {
	var out []string
	for s := 0; s < sentences; s++ {
		n := 7 + r.pick(12)
		words := make([]string, n)
		for i := range words {
			words[i] = r.word()
		}
		if n > 6 {
			// A comma somewhere in the middle, as prose has.
			mid := 2 + r.pick(n-4)
			words[mid] += ","
		}
		sent := strings.Join(words, " ")
		sent = strings.ToUpper(sent[:1]) + sent[1:] + "."
		out = append(out, sent)
	}
	return strings.Join(out, " ")
}

var frontMonths = []string{"January", "February", "March", "April", "May", "June",
	"July", "August", "September", "October", "November", "December"}

var frontThemes = []struct{ bg, fg, accent, muted string }{
	{"#fbfaf7", "#232019", "#8a5a2b", "#cdc6ba"},
	{"#f4f6f8", "#1d2733", "#2f6f9f", "#c3ccd6"},
	{"#fffdf5", "#2b2b26", "#6b7f3a", "#d2cfbf"},
	{"#f7f4fb", "#26222e", "#6d4c9f", "#cdc5d9"},
	{"#f6faf7", "#1f2a24", "#3a7a5a", "#c3d4c9"},
}

var frontPersonas = []struct{ site, tagline, footer string }{
	{"Field Notes", "Photographs and short notes from walks, mostly in the off season.",
		"A quiet notebook kept between trips. New entries when there is something worth keeping."},
	{"The Workshop Log", "Small woodworking projects, the jigs, and the mistakes behind them.",
		"A workbench diary. Everything here was built at least once, usually twice."},
	{"Kitchen Notes", "Recipes tested more than once, with the changes that actually stuck.",
		"Cooking notes, kept honest. If a recipe is here, it survived a second try."},
	{"Signal & Noise", "Occasional writing on radio, antennas, and old receivers.",
		"Notes from the bench. Mostly shortwave, sometimes nothing but static."},
	{"Slow Miles", "Cycling routes, gear that lasted, and the weather that ruined the rest.",
		"A ride journal. Distances are honest; the climbs feel longer than they read."},
}

// frontWords is a plain, neutral vocabulary — the kind a hobby journal uses.
// Joined into sentences it reads as filler prose, not a recognisable passage.
var frontWords = []string{
	"morning", "light", "slow", "quiet", "path", "river", "field", "cold", "warm", "bench",
	"wood", "grain", "edge", "corner", "shelf", "handle", "measure", "twice", "cut", "once",
	"coffee", "kitchen", "window", "table", "notebook", "pencil", "line", "page", "margin", "note",
	"radio", "signal", "static", "antenna", "night", "distant", "faint", "clear", "tuned", "band",
	"road", "hill", "climb", "descent", "wind", "rain", "sun", "shadow", "long", "short",
	"made", "kept", "left", "found", "lost", "fixed", "broke", "learned", "tried", "again",
	"small", "plain", "simple", "rough", "smooth", "worn", "new", "old", "steady", "careful",
	"spring", "autumn", "winter", "summer", "week", "year", "hour", "later", "early", "home",
	"stone", "iron", "paper", "glass", "cloth", "string", "nail", "screw", "glue", "clamp",
	"and", "the", "a", "to", "of", "in", "with", "over", "under", "between", "after", "before",
}
