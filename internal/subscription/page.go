package subscription

import (
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/m1d1x/hikkari-control/internal/config"
)

// Explicit HTML navigation opts in; wildcard Accept and protocol features keep
// the client response, including sealed relay-metrics-v2, unchanged.
func wantsSubscriptionPage(r *http.Request) bool {
	if r.URL.Query().Get("engine") != "" || r.Header.Get(featuresHeader) != "" {
		return false
	}
	for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
		kind, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil || kind != "text/html" {
			continue
		}
		if raw, ok := params["q"]; ok {
			q, err := strconv.ParseFloat(raw, 64)
			if err != nil || q <= 0 || q > 1 {
				continue
			}
		}
		return true
	}
	return false
}

// The landing owns presentation and motion. Browser navigation lands on the
// same minimal /open page as email; protocol clients keep their own format.
func (s *Service) servePage(w http.ResponseWriter, r *http.Request, token string) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	http.Redirect(w, r, config.Default().BaseURL()+"/open#url="+url.QueryEscape(LinkURL(token)), http.StatusSeeOther)
}
