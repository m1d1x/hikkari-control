package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// A minimal MCP server over stdio: newline-delimited JSON-RPC, tools only. It
// has one client (the local agent) and these few methods are the whole of the
// protocol it needs, so no SDK.

type toolArg struct {
	name, desc string
	required   bool
}

type tool struct {
	mcpName, command, desc string
	args                   []toolArg
	// positional names the arguments the shell form takes without --name.
	positional []string
}

var whoArg = toolArg{"who", "Тестер: tester-003, 003, install_id или уникальный префикс", true}

var tools = []tool{
	{mcpName: "testers", command: "testers", desc: "Тестеры, что шлют логи: алиас, install_id, версия, ОС, last_seen, online."},
	{mcpName: "logs", command: "logs", positional: []string{"who"},
		desc: "Логи тестера в NDJSON: meta с гидом по источникам, summary, затем записи log и gap. По умолчанию последний час и последние 1000 записей.",
		args: []toolArg{whoArg,
			{"last", "Окно назад от to: 30m, 2h, 3d", false},
			{"from", "Начало: unix-секунды или RFC 3339", false},
			{"to", "Конец: unix-секунды или RFC 3339", false},
			{"src", "Только источники с этим префиксом: diag:conn, singbox, doctor", false},
			{"tail", "Сколько последних записей оставить", false}}},
	{mcpName: "request_diag", command: "diag", positional: []string{"who"},
		desc: "Запросить диагностику у тестера на связи и дождаться результата (до 150 с). Только чтение на машине тестера. Если тестер не на связи, сразу ошибка.",
		args: []toolArg{whoArg, {"kind", "run_doctor (по умолчанию): отчёт сетевого доктора; snapshot: система, интерфейсы, соединения, STUN", false}}},
	{mcpName: "probe_targets", command: "probe", positional: []string{"who"},
		desc: "Открыть с физической карты тестера по 12 TCP-соединений к адресам, мимо туннеля, и показать счёт. Сравнивает наш пробник с реальной сетью тестера. Только Windows, клиент с 1.0.25, до 16 адресов, ждёт до 150 с.",
		args: []toolArg{whoArg, {"targets", "label=ip:port через запятую: ams7-443=89.124.99.31:443", true}}},
	{mcpName: "provoke_targets", command: "provoke", positional: []string{"who"},
		desc: "Опыт 0 каскада: с физической карты тестера, мимо туннеля, к 1–3 приёмникам (edge EDGE_PROTO=provoke, один в РФ, один за границей) разом стимулы, которые ТСПУ режет: 100 TCP-потоков по source port, TLS по 64 КБ с SNI www.microsoft.com (контроль), www.youtube.com (калибровка: режут везде), случайный домен и без SNI, по потоку 4 МБ с контрольным и случайным доменом (обрыв на 16–20 КБ), выгрузка 1 МБ, UDP 50 пак/с по 160 Б и 500 пак/с по 1200 Б. По каждому стимулу строка: что увидел клиент (hs, got, end: done, stall, reset, eof) и что дошло до приёмника (srv_hello, srv_out, srv_in, srv_udp). Стимул засчитывается, только если его режут к приёмнику за границей. Windows, клиент с provoke_targets, ждёт до 4 мин.",
		args: []toolArg{whoArg, {"targets", "label=ip:port через запятую, до 3: ru-spb=1.2.3.4:443,ams3=5.6.7.8:443", true}}},
	{mcpName: "probe_load", command: "load", positional: []string{"who"},
		desc: "Прогнать сейчас каждый профиль тестера под нагрузкой в отдельном ядре мимо туннеля: 1 МБ, 50 соединений разом, 3 запроса после. Строки [diag:load], как у раунда раз в 30 минут. Ждёт конца раунда, около минуты на профиль, до 15 мин. Windows, режим тестера, клиент с 1.0.33.",
		args: []toolArg{whoArg}},
	{mcpName: "probe_voice", command: "probe-voice", positional: []string{"who"},
		desc: "Звонок на пробу у тестера сейчас: 30 с по 160 Б раз в 20 мс (STUN-запросы на stun.cloudflare.com) через туннель на выбранном профиле, на Windows ещё с физической карты мимо туннеля. Строки [diag:voice]: loss, loss_run, rtt, jitter и главное gaps60/gaps150/gap_max: паузы, которые слышит звонок. Туннель должен быть включён. Ждёт до 4 мин. Клиент новее 1.0.59, режим тестера.",
		args: []toolArg{whoArg}},
	{mcpName: "voice", command: "voice",
		desc: "Датасет голоса на hub (voice_runs): серии [diag:voice] всех тестеров: периодические раз в 30 мин (на мобильной сети раз в 2 ч) и по probe_voice. groups: по тестеру, пути (tun: через туннель, direct: голая сеть тестера, только Windows), протоколу профиля и сети (AS, mobile): минуты замера, потери, паузы больше 60 и 150 мс в минуту, худшая пауза, медианы RTT и джиттера, verdict: audible (пауз больше 60 мс раз в минуту и чаще), ok (реже), no_data. Отвечает на вопрос, страдают ли звонки через TCP-профили и нужен ли UDP-вход.",
		args: []toolArg{{"who", "Тестер: tester-003, 003, install_id или префикс; без него все", false},
			{"proto", "Только профили этого протокола: vless-reality, hysteria2, naive", false},
			{"last", "Окно назад: 24h, 7d, 30d; по умолчанию 7d", false},
			{"rows", "1: приложить сами серии", false}}},
	{mcpName: "hikkari", command: "hikkari",
		desc: "Наблюдаемость протокола Hikkari v1.04 (hikkari_obs): что каждая сессия видела сама, временные ряды. side relay: релей по аккаунту, path direct (свой путь клиента), entry (пришло транком через вход каскада: tcp/quic там цифры самой машины), trunk (члены пула транка). side client: ядро клиента по установке, relay = тег профиля. tcp.*: TCP_INFO соединения Naive (rtt мс, retrans_pm ретрансмиты на 1000). quic.datagram/quic.voice: потери на 1000 (loss_pm), rtt. dup: какая копия пришла первой (first_dgram/first_stream), only_stream: пакеты, которые спасла копия по Naive, both, lead_*: на сколько мс первая опередила вторую (у релея пакеты вверх, у клиента вниз). calls: звонки детектора, short (<10 с) и one_sided вероятно ложные. tunnels.*: как кончились туннели CONNECT. Счётчики суммируются по шагу, p50/p95 усредняются, max берётся наибольший.",
		args: []toolArg{{"who", "Тестер (tester-001, 001) для client или имя аккаунта для relay; без него все", false},
			{"side", "relay или client; без него обе", false},
			{"path", "direct, entry, trunk", false},
			{"metric", "Метрика или её начало: dup., calls., quic.voice.loss_pm, tcp.rtt", false},
			{"last", "Окно назад: 6h, 24h, 7d; по умолчанию 24h", false},
			{"step", "Шаг ряда: 10m, 1h, 1d; по умолчанию от окна", false}}},
	{mcpName: "legs", command: "legs",
		desc: "Участки пути тестера через каскад: клиент → вход (TCP клиента глазами входа), вход → выход (магистраль: переотправки, смерти членов пула под потоком, ожидание подтверждения), по каждому потоку и потоку датаграмм тестера, оба конца рядом под одним id. Пишутся только потоки тестеров (аккаунты с all_relays), которые пришли через вход. Что сессия видела дальше выхода: инструмент hikkari, side relay, path entry.",
		args: []toolArg{{"who", "Имя аккаунта тестера; без него все тестеры", false},
			{"last", "Окно назад: 1h, 24h, 7d; по умолчанию 24h", false}}},
	{mcpName: "matrix", command: "matrix",
		desc: "Стенд матрицы: профиль подписки, одна ручка конфига и её значения, с nekko-spb (РФ) и hikkari-main (Латвия) на ядре клиента. Каждый раунд: тот же адрес без протокола, затем каждое значение: 1 МБ, 50 соединений разом, 3 после. Сводка и строки датасета NDJSON; строки пишутся в датасет на hub (инструмент relay_runs). Около 25 с на значение за раунд.",
		args: []toolArg{{"sub", "URL подписки", true}, {"profile", "Метка профиля: amsterdam-1-002", true},
			{"knob", "Ручка и значения: fp=chrome,firefox,safari; также sni, port, flow, alpn (h2/http/1.1), mode у XHTTP (auto, packet-up, stream-up, stream-one) и burst=3,4,8,50 (сколько запросов разом, не ручка конфига)", true},
			{"base", "Ручки, общие для всех значений, через ;: fp=chrome;alpn=h2. Варьируемой ручки среди них быть не может", false},
			{"vantage", "spb,lv (по умолчанию обе)", false}, {"rounds", "Раундов на значение, по умолчанию 3", false},
			{"baseline_path", "Страница сайта-прикрытия для замера без протокола, например /us/ у www.samsung.com; по умолчанию /", false}}},
	{mcpName: "stand", command: "stand",
		desc: "Стенд по расписанию на hub: что меряет (включён ли, а включённый меряет все живые релеи флота; интервал, ручки и кандидаты) и последние прогоны по точкам замера (статус, ошибка, ядро, сколько строк ушло в датасет). Сами строки отдаёт инструмент relay_runs, source stand, vantage = слот точки (ru-spb, lv-main).",
		args: []toolArg{{"limit", "Сколько последних прогонов, по умолчанию 50", false}}},
	{mcpName: "knobs", command: "knobs",
		desc: "Контроллер ручек на hub: политика (пороги доли залпа, окно, пауза, точки-контроль), текущие значения ручек живых нод и журнал: переключения (из чего, во что, почему, на каких строках), «все кандидаты красные», «режут адрес».",
		args: []toolArg{{"profile", "Метка профиля: amsterdam-1-002", false}, {"days", "Журнал за сколько дней, по умолчанию 7", false}}},
	{mcpName: "edge_hellos", command: "edge-hellos",
		desc: "Что дошло до Reality-нод: приёмник хелло на edge (EDGE_HELLO_TAP=1) считает каждый ClientHello по cover и hk. По каждому релею: fp, который нода выдаёт, expect: hk ядра релиза для этого fp, expected: сколько соединений с ним, total: всего (считаются все: наши клиенты, пробники, сканеры). name: чей это hk (fp:chrome_155, browser:chromium-152). Наш клиент шлёт expect; если у ноды с живыми тестерами expect нет, а доминирует hk, которого ни одно ядро не шлёт, значит, путь переписывает хелло.",
		args: []toolArg{{"relay", "Имя релея: ams3-1; без него все", false}, {"last", "Окно назад: 6h, 24h, 7d; по умолчанию 24h", false}}},
	{mcpName: "knob_check", command: "knob-check", positional: []string{"who"},
		desc: "Сверка ручек раундов diag:load тестеров (датасет relay_runs): профиль, сеть, что hub выдаёт узлу для этой сети (declared), что стояло в конфиге клиента (config), когда. state: clean, mismatch, unknown (не наш узел или раунд без ручек), settling (ручки сменились меньше 2 ч назад). Находки knob.mismatch идут и в errors. Какой ClientHello шлёт ядро, у тестеров не меряется: это тест релиза. Новые сначала.",
		args: []toolArg{{"who", "Тестер: tester-003, 003, install_id или префикс; без него все", false},
			{"profile", "Метка профиля: amsterdam-1-002", false}, {"last", "Окно назад: 6h, 24h, 7d; по умолчанию 24h", false},
			{"mismatch", "1: только расхождения", false}, {"limit", "Сколько строк, до 2000", false}}},
	{mcpName: "trunk", command: "trunk",
		desc: "Датасет каскада на hub (trunk_runs): вход магистрали и его выход по слоям. entry: строка stats входа раз в минуту (члены пула, stalled, resent, port_kills, потоки, байты, датаграммы, RTT членов); event: член пула умер (после скольких секунд, stalled или broken), смена эпохи выхода, старт входа; раз в 10 минут раунд с одним round: vps (steal, load, память, потери интерфейса на входе и выходе), path (голый TCP с 12 source port и ICMP от входа и от контрольной точки к выходу), trunk (куски provoke 48 × 256 КиБ single и pool, прямо через границу и через магистраль), protocol (ядро клиента на Hikkari-релее выхода с точек стенда, прямо и через вход: 1 МиБ, залп 50, 3 после, звонок 30 с), verdict (первый красный слой раунда: vps, path, trunk, protocol); call: звонок магистрали каждую минуту без перерыва (вход шлёт датаграмму раз в 20 мс на эхо выхода и обратно, target copies1 и copies2: sent, lost, rtt, gaps60, gaps150, gap_max_ms, silent_ms, worst — самые длинные паузы со временем; красная минута — пауза больше 150 мс, тишина от 500 мс даёт cascade.call_gap). summary: рост счётчиков за окно, смерти членов пула, вердикты, слои по точке и цели. Тревоги идут в errors группой cascade.*.",
		args: []toolArg{{"last", "Окно назад: 1h, 6h, 24h, 7d; по умолчанию 6h", false},
			{"layer", "Только строки слоя: entry, event, vps, path, trunk, protocol, verdict, call, wire, load", false},
			{"entry", "Бокс входа", false}, {"rows", "1: приложить строки всех слоёв", false},
			{"limit", "Сколько последних строк, до 20000; по умолчанию 2000", false}}},
	{mcpName: "cascade_load", command: "cascade-load",
		desc: "Синтетические пользователи через вход каскада (симуляция, не люди): точка стенда в РФ (не на самом входе) поднимает users ядер клиента на профиле через вход, у каждого звонок 50 пак/с по 160 Б и игра game_pps пак/с по 80 Б на UDP-эхо echo за выходом (приёмник provoke, порт 2053; не на самом выходе: релей не ходит на свои адреса), minutes минут. Строка на поток идёт в слой load инструмента trunk: sent, got, gaps60, gaps150, gap_max_ms, rtt, jitter, gaps60_per_min. Звонок магистрали тех же минут: слой call. Одна задача на точку за раз.",
		args: []toolArg{{"slot", "Точка стенда в РФ: ru-eurodir", true}, {"entry", "Бокс входа", true},
			{"users", "Сколько пользователей, 1–8", true}, {"minutes", "Сколько минут, 1–30", true},
			{"game_pps", "Игра на пользователя, пак/с, 0–500; 0 без игры", true}, {"echo", "UDP-эхо за выходом ip:port", true}}},
	{mcpName: "relay_runs", command: "runs",
		desc: "Датасет релеев на hub: строки прогонов стенда matrix и раундов [diag:load] тестеров (точка, время, ядро, профиль, ручка, значение, все параметры, dl, burst, after). NDJSON, старые сначала.",
		args: []toolArg{{"profile", "Метка профиля: amsterdam-1-002", false}, {"source", "stand или tester", false},
			{"last", "Окно назад: 6h, 24h, 7d; по умолчанию 24h", false}, {"limit", "Сколько последних строк, до 5000", false}}},
	{mcpName: "runs_record", command: "runs-record",
		desc: "Дослать в датасет relay_runs строки сохранённого вывода matrix (файл со строками NDJSON), если hub отказал им во время прогона.",
		args: []toolArg{{"file", "Путь к сохранённому выводу matrix", true}}},
	{mcpName: "census_add", command: "census-add", positional: []string{"url"},
		desc: "Сохранить подписку, оплаченную владельцем, и разобрать её входы: адрес, порт, протокол, транспорт, SNI, ASN. URL шифруется на hub и нигде не выводится.",
		args: []toolArg{{"url", "URL подписки", true}, {"name", "Имя сервиса", true}}},
	{mcpName: "census_probe", command: "census-probe",
		desc: "TCP-проба входов и наших релеев с пробника в СПб и с тестеров, не чаще раза в час на цель из одной точки. Тестерам нужны Windows и клиент с 1.0.25.",
		args: []toolArg{{"who", "Тестеры через запятую: 002,005. Без них только СПб", false}}},
	{mcpName: "census_add_addr", command: "census-add-addr",
		desc: "Сервис без импорта подписки: адреса, которые его приложение использует у владельца. Только сеть и TCP-счёт, без протокола.",
		args: []toolArg{{"name", "Имя сервиса", true}, {"addrs", "ip:port через запятую", true}}},
	{mcpName: "census_exit_link", command: "census-exit-link",
		desc: "Ссылка для сервиса без импорта: открыть с включённым VPN, hub запишет IP выхода и его ASN. Вход так не узнать.",
		args: []toolArg{{"name", "Имя сервиса", true}}},
	{mcpName: "census_proto", command: "census-proto",
		desc: "Подключиться через профили подписки в отдельном sing-box на машине тестера, мимо его туннеля, и сделать запрос. Адрес жив, а протокол нет: режут протокол. Windows, клиент с поддержкой proto_targets, не чаще раза в час.",
		args: []toolArg{{"who", "Тестеры через запятую: 002,005", true}}},
	{mcpName: "census_report", command: "census-report",
		desc: "Таблица: сервис, вход, сеть, протокол, адрес жив, протокол жив, сколько живёт, рядом наши релеи той же сети, и вывод по каждой сети."},
	{mcpName: "rename", command: "rename", positional: []string{"who", "alias"}, desc: "Переименовать тестера.",
		args: []toolArg{whoArg, {"alias", "Новое имя: строчные буквы, цифры, дефисы", true}, {"note", "Заметка оператора", false}}},
	{mcpName: "errors", command: "errors", desc: "Группы ошибок клиентов из телеметрии.",
		args: []toolArg{{"window", "24h, 7d или 30d", false}}},
	{mcpName: "tickets", command: "tickets", desc: "Обращения в поддержку (H-XXXX).",
		args: []toolArg{{"status", "Фильтр по статусу", false}}},
	{mcpName: "ticket", command: "ticket", positional: []string{"id"}, desc: "Одно обращение с логами.",
		args: []toolArg{{"id", "Идентификатор обращения", true}}},
	{mcpName: "ci_status", command: "ci", desc: "Последние прогоны GitHub Actions.",
		args: []toolArg{{"repo", "client (по умолчанию) или control", false}, {"ref", "Ветка", false}, {"limit", "Сколько прогонов", false}}},
	{mcpName: "release_status", command: "release", desc: "Какие версии отдают каналы обновлений stable и beta, как их видит клиент."},
}

func toolsByCommand() map[string]tool {
	out := make(map[string]tool, len(tools))
	for _, t := range tools {
		out[t.command] = t
	}
	return out
}

func toolSchemas() []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		props := map[string]any{}
		required := []string{}
		for _, a := range t.args {
			props[a.name] = map[string]any{"type": "string", "description": a.desc}
			if a.required {
				required = append(required, a.name)
			}
		}
		out = append(out, map[string]any{
			"name":        t.mcpName,
			"description": t.desc,
			"inputSchema": map[string]any{"type": "object", "properties": props, "required": required},
		})
	}
	return out
}

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func serveMCP(c *client, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<24)
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	for sc.Scan() {
		var req rpcRequest
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		if len(req.ID) == 0 {
			continue // a notification: nothing to answer
		}
		result, rpcErr := handleRPC(c, req)
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rpcErr != nil {
			resp["error"] = rpcErr
		} else {
			resp["result"] = result
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func handleRPC(c *client, req rpcRequest) (any, map[string]any) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		return map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "hikkari-ops", "version": "1"},
			"instructions":    "Логи и диагностика тестеров Hikkari, ошибки, обращения, CI и каналы обновлений. Петля разбора описана в hikkari-client/AGENTS.md.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolSchemas()}, nil
	case "tools/call":
		var p struct {
			Name      string            `json:"name"`
			Arguments map[string]string `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, map[string]any{"code": -32602, "message": err.Error()}
		}
		for _, t := range tools {
			if t.mcpName != p.Name {
				continue
			}
			text, err := c.call(t.command, p.Arguments)
			if err != nil {
				if text != "" {
					text += "\n"
				}
				return toolResult(text+err.Error(), true), nil
			}
			return toolResult(text, false), nil
		}
		return nil, map[string]any{"code": -32602, "message": fmt.Sprintf("unknown tool %q", p.Name)}
	}
	return nil, map[string]any{"code": -32601, "message": "method not found: " + req.Method}
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}
