package support

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// NDJSON export of a tester's log, shaped to be handed to an LLM as is: one
// self-contained object per line with a `type` discriminator. The first line
// is `meta` (who, which window, counts, and a guide to the log's sources),
// the second `summary` (lines per source, warnings and errors), then one
// `log` per line in time order, with a `gap` where the client lost lines.

// maxTesterExportChunks covers the whole retention at a chunk a minute.
const maxTesterExportChunks = 7 * 24 * 60

// testerLogGuide tells the reader of an export what the sources mean and how
// to tell a bad server from a client problem. It travels in every export so
// the file is enough on its own.
var testerLogGuide = map[string]any{
	"what": "Лог движка VPN-клиента hikkari (sing-box/xray) от альфа-тестера, вычищенный на клиенте: IP, ключи, UUID, токены и домашняя папка заменены метками [REDACTED_*] и ~. Домены и имена процессов сохранены.",
	"sources": map[string]string{
		"singbox":          "вывод ядра sing-box (уровень warn): ошибки рукопожатия, таймауты, отказ транспорта",
		"xray":             "вывод ядра xray, если выбран этот движок",
		"diag":             "границы снимков диагностики: baseline (туннель выключен), post-start, periodic #N",
		"diag:stun":        "STUN-пробы UDP: через туннель (tun/default-route) и с физического интерфейса. Нет ответа через туннель — сервер или протокол не пропускает UDP (голос Discord, звонки)",
		"diag:stun-bound":  "UDP с адреса физической карты: ответ с адресом провайдера значит, что WebRTC идёт мимо туннеля",
		"diag:proxy-delay": "задержка до сервера через Clash API, раз в минуту; скачки и таймауты указывают на канал или сервер",
		"diag:conn":        "живые соединения: процесс, хост, правило, выход (proxy/direct). Показывает, куда реально идёт трафик приложения",
		"diag:sys":         "ОС, архитектура, часовой пояс",
		"diag:ifaces":      "сетевые интерфейсы до и после старта",
		"testerlog":        "служебные отметки потока логов: момент включения режима тестера",
		"support":          "отправка обращений в поддержку",
	},
	"how_to_diagnose": []string{
		"Сравни diag:proxy-delay и singbox-ошибки по времени с жалобой: рост задержки и таймауты на одном сервере при чистых diag:conn — проблема сервера или канала до него.",
		"Если diag:stun через туннель молчит, а прямой baseline отвечает — сервер не держит UDP; голос и звонки будут лагать или не работать.",
		"Если diag:conn показывает трафик приложения через direct или diag:stun-bound отвечает адресом провайдера — это маршрутизация клиента, а не сервер.",
		"Записи type=gap означают потерянные клиентом строки (hub был недоступен) — не делай выводов о тишине в этом промежутке.",
	},
}

var (
	reTesterTS    = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)?)\s+`)
	reTesterSrc   = regexp.MustCompile(`^\[([^\]]+)\]\s*`)
	reTesterLevel = regexp.MustCompile(`\b(FATAL|PANIC|ERROR|WARN(?:ING)?|INFO|DEBUG|TRACE)\b`)
)

// testerLogRecord is one parsed line of the export.
type testerLogRecord struct {
	Type  string `json:"type"`
	TS    string `json:"ts,omitempty"`
	Src   string `json:"src,omitempty"`
	Level string `json:"level,omitempty"`
	Msg   string `json:"msg"`
}

// parseTesterLine splits "2026/11/01 15:04:05.000000 [src] msg" into its
// parts. Lines that do not fit keep their text in msg. The client's log is
// written in the tester's local time, so ts carries no zone.
func parseTesterLine(line string) testerLogRecord {
	rec := testerLogRecord{Type: "log", Msg: line}
	if m := reTesterTS.FindStringSubmatch(line); m != nil {
		if t, err := time.Parse("2006/01/02 15:04:05.999999", m[1]); err == nil {
			rec.TS = t.Format("2006-01-02T15:04:05.000")
		}
		line = line[len(m[0]):]
	}
	if m := reTesterSrc.FindStringSubmatch(line); m != nil {
		rec.Src = m[1]
		line = line[len(m[0]):]
	}
	if m := reTesterLevel.FindStringSubmatch(line); m != nil {
		rec.Level = strings.ToLower(strings.TrimSuffix(m[1], "ING"))
	}
	rec.Msg = line
	return rec
}

// sourceGroup folds "diag:ifaces/pre" and "diag:stun/tun" into their family
// for the summary; the record keeps the full source.
func sourceGroup(src string) string {
	if i := strings.IndexAny(src, "/ "); i > 0 {
		return src[:i]
	}
	return src
}

// TesterExportHandler streams an install's log for a window as NDJSON.
// Query: from, to (unix seconds; default: the last 24 hours).
func TesterExportHandler(store TesterStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		install := strings.TrimSpace(r.PathValue("install"))
		if install == "" {
			writeErr(w, http.StatusBadRequest, "install is required")
			return
		}
		to := time.Now()
		from := to.Add(-24 * time.Hour)
		for key, dst := range map[string]*time.Time{"from": &from, "to": &to} {
			if v := r.URL.Query().Get(key); v != "" {
				sec, err := strconv.ParseInt(v, 10, 64)
				if err != nil {
					writeErr(w, http.StatusBadRequest, "invalid "+key)
					return
				}
				*dst = time.Unix(sec, 0)
			}
		}
		if !from.Before(to) {
			writeErr(w, http.StatusBadRequest, "from must be before to")
			return
		}
		chunks, err := store.ReadChunks(r.Context(), install, from, to, maxTesterExportChunks)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to read tester logs")
			return
		}

		var (
			records  []any
			bySource = map[string]int{}
			levels   = map[string]int{}
			platform string
			version  string
			versions = map[string]bool{}
			dropped  int
		)
		for _, c := range chunks {
			platform, version = c.Platform, c.Version
			versions[c.Version] = true
			if c.Dropped > 0 {
				dropped += c.Dropped
				records = append(records, map[string]any{
					"type": "gap", "lost_lines": c.Dropped,
					"received_at": c.ReceivedAt.UTC().Format(time.RFC3339),
				})
			}
			if c.Body == "" {
				continue
			}
			for _, line := range strings.Split(c.Body, "\n") {
				rec := parseTesterLine(line)
				bySource[sourceGroup(rec.Src)]++
				if rec.Level != "" {
					levels[rec.Level]++
				}
				records = append(records, rec)
			}
		}
		versionList := make([]string, 0, len(versions))
		for v := range versions {
			versionList = append(versionList, v)
		}
		sort.Strings(versionList)

		lines := len(records)
		meta := map[string]any{
			"type":       "meta",
			"kind":       "hikkari-tester-log",
			"install_id": install,
			"platform":   platform,
			"version":    version,
			"versions":   versionList,
			"window": map[string]string{
				"from": from.UTC().Format(time.RFC3339),
				"to":   to.UTC().Format(time.RFC3339),
			},
			"counts": map[string]int{"chunks": len(chunks), "records": lines, "lost_lines": dropped},
			"guide":  testerLogGuide,
		}
		summary := map[string]any{"type": "summary", "by_source": bySource, "by_level": levels}

		name := fmt.Sprintf("tester-%s-%s.ndjson", install, to.UTC().Format("2006-01-02-15-04"))
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(meta)
		_ = enc.Encode(summary)
		for _, rec := range records {
			if err := enc.Encode(rec); err != nil {
				return
			}
		}
	}
}
