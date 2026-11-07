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
		desc: "Запросить диагностику у тестера на связи и дождаться результата (до 150 с). Только чтение на машине тестера. Тестер не на связи — сразу ошибка.",
		args: []toolArg{whoArg, {"kind", "run_doctor (по умолчанию): отчёт сетевого доктора; snapshot: система, интерфейсы, соединения, STUN", false}}},
	{mcpName: "probe_targets", command: "probe", positional: []string{"who"},
		desc: "Открыть с физической карты тестера по 12 TCP-соединений к адресам, мимо туннеля, и показать счёт. Сравнивает наш пробник с реальной сетью тестера. Только Windows, клиент с 1.0.25, до 16 адресов, ждёт до 150 с.",
		args: []toolArg{whoArg, {"targets", "label=ip:port через запятую: ams7-443=89.124.99.31:443", true}}},
	{mcpName: "probe_load", command: "load", positional: []string{"who"},
		desc: "Прогнать сейчас каждый профиль тестера под нагрузкой в отдельном ядре мимо туннеля: 1 МБ, 50 соединений разом, 3 запроса после. Строки [diag:load], как у раунда раз в 30 минут. Ждёт конца раунда, около минуты на профиль, до 15 мин. Windows, режим тестера, клиент с 1.0.33.",
		args: []toolArg{whoArg}},
	{mcpName: "matrix", command: "matrix",
		desc: "Стенд матрицы: профиль подписки, одна ручка конфига и её значения, с nekko-spb (РФ) и hikkari-main (Латвия) на ядре клиента. Каждый раунд: тот же адрес без протокола, затем каждое значение — 1 МБ, 50 соединений разом, 3 после. Сводка и строки датасета NDJSON; строки пишутся в датасет на hub (инструмент relay_runs). Около 25 с на значение за раунд.",
		args: []toolArg{{"sub", "URL подписки", true}, {"profile", "Метка профиля: amsterdam-1-002", true},
			{"knob", "Ручка и значения: fp=chrome,firefox,safari; также sni, port, flow, alpn (h2/http/1.1) и burst=3,4,8,50 (сколько запросов разом, не ручка конфига)", true},
			{"base", "Ручки, общие для всех значений, через ;: fp=chrome;alpn=h2. Варьируемой ручки среди них быть не может", false},
			{"vantage", "spb,lv (по умолчанию обе)", false}, {"rounds", "Раундов на значение, по умолчанию 3", false},
			{"baseline_path", "Страница сайта-прикрытия для замера без протокола, например /us/ у www.samsung.com; по умолчанию /", false}}},
	{mcpName: "stand", command: "stand",
		desc: "Стенд по расписанию на hub: что меряет (включён ли — меряет все живые релеи флота, интервал, ручки и кандидаты) и последние прогоны по точкам замера (статус, ошибка, ядро, сколько строк ушло в датасет). Сами строки — инструмент relay_runs, source stand, vantage = слот точки (ru-spb, lv-main).",
		args: []toolArg{{"limit", "Сколько последних прогонов, по умолчанию 50", false}}},
	{mcpName: "knobs", command: "knobs",
		desc: "Контроллер ручек на hub: политика (пороги доли залпа, окно, пауза, точки-контроль), текущие значения ручек живых нод и журнал: переключения (из чего, во что, почему, на каких строках), «все кандидаты красные», «режут адрес».",
		args: []toolArg{{"profile", "Метка профиля: amsterdam-1-002", false}, {"days", "Журнал за сколько дней, по умолчанию 7", false}}},
	{mcpName: "wire", command: "wire", positional: []string{"who"},
		desc: "ClientHello, который ядро тестера реально отправило ([diag:wire], зеркало на loopback): ручки конфига, ревизия ядра, сеть, ja4, hk, ALPN на проводе, группы, и что нашли проверки hub: wire.mismatch (хелло не эталон ядра для fp или ALPN не тот), wire.go_hello (ушёл хелло crypto/tls), wire.no_reference (эталона ядра нет), wire.unread, knob.mismatch (в конфиге не то, что hub выдаёт узлу для сети тестера). Новые сначала.",
		args: []toolArg{{"who", "Тестер: tester-003, 003, install_id или префикс; без него все", false},
			{"profile", "Метка профиля: amsterdam-1-002", false}, {"last", "Окно назад: 6h, 24h, 7d; по умолчанию 24h", false},
			{"findings", "1: только строки с находками", false}, {"limit", "Сколько строк, до 2000", false}}},
	{mcpName: "wire_reference", command: "wire-ref",
		desc: "Эталон ядра на hub: какой хелло (hk, ja4) ревизия ядра шлёт для каждого имени fp; fp go — TLS без uTLS. По нему hub находит wire.mismatch.",
		args: []toolArg{{"core", "12 знаков ревизии ядра; без него все", false}}},
	{mcpName: "wire_reference_add", command: "wire-ref-add",
		desc: "Записать эталон ядра из reference.json, который пишет запись хелло (hikkari-client diagnostics: HIKKARI_TEST_CORE=… HIKKARI_WIRE_RECORD=1 go test -run TestRecordWireHellos). Строки той же ревизии и fp заменяются.",
		args: []toolArg{{"file", "Путь к reference.json", true}}},
	{mcpName: "relay_runs", command: "runs",
		desc: "Датасет релеев на hub: строки прогонов стенда matrix и раундов [diag:load] тестеров (точка, время, ядро, профиль, ручка, значение, все параметры, dl, burst, after). NDJSON, старые сначала.",
		args: []toolArg{{"profile", "Метка профиля: amsterdam-1-002", false}, {"source", "stand или tester", false},
			{"last", "Окно назад: 6h, 24h, 7d; по умолчанию 24h", false}, {"limit", "Сколько последних строк, до 5000", false}}},
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
