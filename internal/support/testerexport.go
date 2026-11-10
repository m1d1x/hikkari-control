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
		"singbox":         "вывод ядра sing-box (уровень warn): ошибки рукопожатия, таймауты, отказ транспорта",
		"xray":            "вывод ядра xray, если выбран этот движок",
		"diag":            "границы снимков диагностики: baseline (туннель выключен), post-start, periodic #N",
		"diag:stun":       "STUN-пробы UDP: через туннель (tun/default-route) и с физического интерфейса. Нет ответа через туннель: сервер или протокол не пропускает UDP (голос Discord, звонки)",
		"downtime":        "главная метрика (клиент новее 1.0.78): минуты, когда туннель включён, строка minutes from=<unix начала первой минуты> states=…, по букве на минуту: o трафик шёл, i туннель включён и им не пользовались, d спросили через туннель и ничего не пришло (сеть мимо туннеля отвечает), c ядро лежало при включённом туннеле, n не отвечала и сама сеть тестера, - минуту не видели (сон). Простой по метрике: d и c. Hub складывает минуты в product_tunnel_minutes, график по людям вверху обзора продукта в админке",
		"diag:voice":      "звонок на пробу (клиент новее 1.0.59, режим тестера): 30 с по 160 Б раз в 20 мс STUN-запросами на stun.cloudflare.com, раз в 30 минут, на мобильной сети раз в 2 часа, и по запросу (probe_voice, инструмент probe_voice). path=tun: через туннель на выбранном профиле (profile, proto); path=direct: с физической карты мимо туннеля, только Windows: сколько теряет сама сеть тестера. isp: AS сети, isp_at=card (сейчас, Windows) или baseline (снимок до туннеля), mobile=1: мобильный оператор. Главное: gaps60/gaps150/gap_max, то есть паузы между ответами больше 60/150 мс, их слышит звонок. Внутри TCP (VLESS, Naive) потеря держится около нуля, а пауза есть: TCP чинит сегмент и отдаёт пачкой. loss_run: самая длинная серия потерь, jitter: по RFC 3550. Сравнивай tun с direct того же раунда: если паузы есть и в direct, дело в сети тестера. Все серии лежат в датасете voice_runs (инструмент voice) с вердиктом: audible, если пауз больше 60 мс раз в минуту и чаще",
		"diag:stun-bound": "UDP с адреса физической карты: ответ с адресом провайдера значит, что WebRTC идёт мимо туннеля",
		"diag:delay":      "задержка до сервера через Clash API, раз в минуту; скачки и таймауты указывают на канал или сервер",
		"diag:conn":       "живые соединения: процесс, хост, правило, выход (proxy/direct). Показывает, куда реально идёт трафик приложения",
		"diag:reach":      "раз в 10 минут только у тестера: сеть провайдера (self isp=AS), контроль (control flows=N/12 до 1.1.1.1) и по каждому профилю протокол, сеть сервера, пинг через ядро и сколько из 12 TCP-потоков открылось с физической карты. 0/12 при живом контроле значит, что TCP к адресу режут; 3/12 значит, что часть потоков теряется",
		"diag:load":       "раз в 30 минут, сразу после старта VPN и по запросу оператора (probe_load), только Windows и режим тестера: каждый профиль в отдельном ядре мимо туннеля. Новые клиенты пишут после proto ручки конфига: fp, sni, port, flow, alpn, у XHTTP mode, у Reality key_share. dl=получено/1048576 end=ok|timeout|closed|httpNNN, затем burst=N/50 timeout=N (50 запросов разом), затем after=N/3. dl оборвался: режут по объёму; burst низкий при целом dl: режут по числу соединений; after=0/3: после всплеска путь закрылся; упали все профили раунда: сеть тестера; load_error: ядро не запустилось. addr_dead=1 tcp=N/12 reply=0/12 вместо счётов: адрес не ответил на ClientHello ни по одному потоку, это бан адреса, ручки тут ни при чём (tcp=0/12 значит, что не открылся и TCP; сравни с другими профилями: упали все, значит сеть тестера). На мобильной сети залп 8, а не 50, и раунд не чаще раза в 30 минут, в том числе по запросу. Раунд по запросу закрывает строка «on request: done». Все раунды лежат в датасете релеев (инструмент relay_runs) с ручками из строки или из diag:config; hub сверяет эти ручки с выданными узлу для сети тестера: расхождение старше 2 ч после смены даёт knob.mismatch в errors и в инструменте knob_check. Строки diag:wire у клиентов 1.0.54–1.0.55 остались от прежней схемы, hub их не читает: ClientHello ядра проверяет тест релиза",
		"diag:sys":        "ОС, архитектура, часовой пояс",
		"diag:ifaces":     "сетевые интерфейсы до и после старта",
		"testerlog":       "служебные отметки потока логов: момент включения режима тестера",
		"support":         "отправка обращений в поддержку",
		"connect":         "что нажал пользователь: start, stop, switch с профилем, протоколом и исходом. Точка отсчёта для всех строк вокруг",
		"probe":           "проба адресов по запросу оператора: по 12 TCP-соединений с физической карты мимо туннеля. Метка заменяет адрес; control это контроль до 1.1.1.1. 0/12 при живом контроле значит, что TCP к адресу с этой сети не открывается",
		"provoke":         "опыт 0 каскада по запросу оператора (provoke_targets): стимулы, которые ТСПУ режет, с физической карты мимо туннеля к приёмникам (edge EDGE_PROTO=provoke). Метка приёмника и его сеть, затем stim: flows (100 TCP-потоков, opened и refused), control (SNI www.microsoft.com, отрицательный контроль), sni-blocked (www.youtube.com, калибровка: режут по всей РФ), sni-random (случайный домен раунда, ни в одном списке), sni-none (без SNI), по 64 КБ после рукопожатия; volume и volume-random (4 МБ одним потоком с контрольным и случайным доменом, обрыв на 16–20 КБ виден как got около 16384 и end=stall), upload (1 МБ к приёмнику), udp-voice (250 по 160 Б раз в 20 мс), udp-rate (1000 по 1200 Б раз в 2 мс). Клиентская половина: hs (ok, timeout, reset, eof, tcp-*), got, end (done, stall, reset, eof, short). Половина приёмника: srv_hello=1 значит хелло дошло, srv_out/srv_in байты, srv_udp датаграммы; srv=? и seen=fail значат, что приёмник не ответил на контрольный запрос. hello дошло, а hs не ok: рукопожатие срезали после приёмника или на обратном пути. Стимул засчитывается, только если его режут к приёмнику за границей в том же раунде. Хелло собирает crypto/tls Go, не браузер: одинаково к обоим приёмникам",
		"doctor":          "отчёт сетевого доктора: проверка у тестера или запрос оператора. Поле nonce в источнике связывает строки с запросом request_diag; строки идут как key=json: прокси, DNS, hosts, DPI-утилиты, чужие WFP-фильтры, прямой путь, Secure DNS браузеров",
		"netdoctor":       "сетевой доктор в движке: выбор интерфейса и резолвера, прямой путь при выключенном туннеле, шаги ремонта",
		"snapshot":        "снимок по запросу оператора: система, интерфейсы, живые соединения, STUN и задержка до сервера",
	},
	"how_to_diagnose": []string{
		"Сравни diag:delay и singbox-ошибки по времени с жалобой: рост задержки и таймауты на одном сервере при чистых diag:conn указывают на сервер или канал до него.",
		"Если diag:stun через туннель молчит, а прямой baseline отвечает, сервер не держит UDP; голос и звонки будут лагать или не работать.",
		"Если diag:conn показывает трафик приложения через direct или diag:stun-bound отвечает адресом провайдера, это маршрутизация клиента, а не сервер.",
		"Записи type=gap означают потерянные клиентом строки (hub был недоступен): не делай выводов о тишине в этом промежутке.",
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
// Query: from, to (unix seconds; default: the last 24 hours); src keeps only
// the log lines whose source starts with it («diag:conn», «doctor nonce=ab12»).
func TesterExportHandler(store TesterStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		install, alias, err := store.ResolveInstall(r.Context(), r.PathValue("install"))
		if err != nil {
			writeResolveErr(w, err)
			return
		}
		srcPrefix := r.URL.Query().Get("src")
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
				if srcPrefix != "" && !strings.HasPrefix(rec.Src, srcPrefix) {
					continue
				}
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
			"alias":      alias,
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

		name := fmt.Sprintf("%s-%s.ndjson", alias, to.UTC().Format("2006-01-02-15-04"))
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
