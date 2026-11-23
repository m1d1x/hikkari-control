package stand

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// LoadBulk is what the downloading user of a synthetic users task pulled
// (LoadSpec.Bulk): through its core on the task's path, or straight from the
// point on the raw path.
type LoadBulk struct {
	// Streams is how many TCP downloads ran at once.
	Streams int `json:"streams"`
	// MbpsPerMinute is the megabits a second each minute of the task
	// carried, all streams together; MB the megabytes of all its minutes.
	MbpsPerMinute []float64 `json:"mbps_per_min"`
	MB            float64   `json:"mb"`
	// Failed counts downloads that ended in an error before the task did.
	Failed int    `json:"failed"`
	Error  string `json:"error,omitempty"`
}

// loadBulkURL is what the downloading user pulls, again and again: OVH's
// public 1 GB test file. Cloudflare's speed test answers 403 to a download
// of 100 MiB from the exit aeznl1 (23.11, 1 MB passes), and a run of 1 MB
// requests would read the round trip, not the line; OVH gave aeznl1 47 MB/s.
const loadBulkURL = "https://proof.ovh.net/files/1Gb.dat"

// loadBulkRetry is the wait after a download that failed.
const loadBulkRetry = time.Second

// runBulk runs n downloads of src at once from warm to end, each again as
// soon as it is over, through the HTTP proxy at proxy ("" for none), and
// counts what arrived from start to end by minute. Each stream is a TCP
// connection of its own: HTTP/2 would carry them all on one.
func runBulk(ctx context.Context, proxy, src string, n int, warm, start, end time.Time) LoadBulk {
	minutes := int(math.Round(end.Sub(start).Minutes()))
	b := LoadBulk{Streams: n, MbpsPerMinute: make([]float64, minutes)}
	tr := &http.Transport{
		ForceAttemptHTTP2:   false,
		TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{},
		DisableCompression:  true,
		MaxIdleConnsPerHost: n,
	}
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil {
			b.Error = err.Error()
			return b
		}
		tr.Proxy = http.ProxyURL(u)
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}
	select {
	case <-ctx.Done():
		b.Error = ctx.Err().Error()
		return b
	case <-time.After(time.Until(warm)):
	}
	runCtx, cancel := context.WithDeadline(ctx, end)
	defer cancel()
	perMinute := make([]atomic.Int64, minutes)
	var failed atomic.Int64
	var lastErr atomic.Value
	count := func(k int) {
		now := time.Now()
		if now.Before(start) || !now.Before(end) {
			return
		}
		if i := int(now.Sub(start) / time.Minute); i < minutes {
			perMinute[i].Add(int64(k))
		}
	}
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 64<<10)
			for runCtx.Err() == nil {
				err := pull(runCtx, client, src, buf, count)
				if err == nil || runCtx.Err() != nil {
					continue
				}
				failed.Add(1)
				lastErr.Store(err.Error())
				select {
				case <-runCtx.Done():
				case <-time.After(loadBulkRetry):
				}
			}
		}()
	}
	wg.Wait()
	var total int64
	for i := range perMinute {
		v := perMinute[i].Load()
		total += v
		b.MbpsPerMinute[i] = math.Round(float64(v)*8/60/1e6*10) / 10
	}
	b.MB = math.Round(float64(total)/1e6*10) / 10
	b.Failed = int(failed.Load())
	if total == 0 {
		if e, _ := lastErr.Load().(string); e != "" {
			b.Error = e
		} else {
			b.Error = "ни байта"
		}
	}
	return b
}

// pull downloads src once, handing count every read's length.
func pull(ctx context.Context, client *http.Client, src string, buf []byte, count func(int)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("http " + resp.Status)
	}
	for {
		k, err := resp.Body.Read(buf)
		if k > 0 {
			count(k)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
