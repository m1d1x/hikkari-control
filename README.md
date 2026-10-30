# hikkari-control

Серверная часть Hikkari. Сейчас здесь только **даундетектор**, этап 1 (MVP для друзей).
Клиент и публичный протокол отчёта живут в `m1d1x/hikkari-client`: план в `docs/DOWNDETECTOR.md`,
схема отчёта в `engine/core/downdetector/report.go`, FFI в `engine/core/api/contract.md`.

Клиенты раз в 5 минут присылают анонимные агрегаты своих проб. Сервер суммирует их в памяти,
сам пробует серверы из реестра снаружи и отвечает, почему у юзера не работает сервер:
`server_down`, `isp_block`, `country_block`, `local` или `unknown`.

## Приватность

- Сырые отчёты не хранятся нигде: отчёт прибавляется к суммам в памяти и выбрасывается.
  Ни тело запроса, ни IP, ни токен не логируются (`net/http` ErrorLog заглушён, у Caddy нет access-лога,
  `X-Forwarded-For` / `X-Real-IP` до сервиса не доходят).
- `install` живёт только в памяти, внутри 5-минутных бакетов. В памяти по нему хранится вклад установки
  в каждую ячейку, это нужно для замены повторного отчёта за то же окно и для подсчёта разных установок.
- На диск (SQLite, `rollup_hourly`) пишутся только часовые агрегаты:
  - сервер, который за час прислала одна установка, не пишется вообще (скорее всего, личный сервер);
  - ASN, где меньше k установок, сворачивается в `asn=0`, затем регион с меньше чем k — в `region="other"`;
  - строки, где и после свёртки меньше k установок, не пишутся.
- Токены хранятся и сравниваются только как sha256. Связь токен ↔ отчёт нигде не сохраняется.
- Секреты лежат в репо только в зашифрованном виде: `deploy/secrets.enc.yaml` (SOPS + age).

## Запуск локально

Нужны Docker, `sops` и `age`, плюс age-ключ, которым зашифрован `deploy/secrets.enc.yaml`
(`~/.config/sops/age/keys.txt` или `SOPS_AGE_KEY_FILE`).

```bash
./scripts/decrypt-secrets.sh
```

```bash
HTTP_PORT=8088 HTTPS_PORT=8443 docker compose up -d --build
```

Caddy с `DOMAIN=localhost` выпускает сертификат своим внутренним CA, поэтому curl с `-k`:

```bash
TOKEN=$(sed -n 2p secrets/secrets.yaml | sed 's/.*- //')
NOW=$(date +%s); END=$((NOW - NOW % 300)); START=$((END - 300))
curl -k https://localhost:8443/v1/reports -H "Authorization: Bearer $TOKEN" -d "{\"install\":\"r-0123456789abcdef01234567\",\"asn\":12389,\"country\":\"ru\",\"region\":\"moscow\",\"window\":[$START,$END],\"servers\":[{\"id\":\"s:7a19192edd834db17881c0ad69c22fa9\",\"proto\":\"vless-reality-xhttp\",\"src\":\"tunnel\",\"ok\":9,\"fail\":1,\"rtt_p50\":74}]}"
curl -k "https://localhost:8443/v1/status?asn=12389&country=ru&ids=s:7a19192edd834db17881c0ad69c22fa9" -H "Authorization: Bearer $TOKEN"
```

Без Docker:

```bash
go test ./...
go run ./cmd/downdetector -config deploy/config.yaml   # поправьте db_path и secrets_path на локальные
```

На проде задайте `DOMAIN=dd.example.com` в окружении compose. Caddy сам получит сертификат.

## Инвайт-токены

1. Сгенерировать токен: `openssl rand -hex 24`.
2. Открыть зашифрованный файл в редакторе: `sops deploy/secrets.enc.yaml` (нужен age-ключ).
3. Добавить токен в `invite_tokens` (админские — в `admin_tokens`), сохранить. SOPS зашифрует файл обратно.
4. Закоммитить `deploy/secrets.enc.yaml`, на сервере выполнить `./scripts/decrypt-secrets.sh` и
   `docker compose restart downdetector`.

Отозвать токен: удалить его из файла тем же путём. Добавить ещё одного владельца ключа: дописать его
age-recipient в `.sops.yaml` и выполнить `sops updatekeys deploy/secrets.enc.yaml`.

## Реестр нод

Ноды для внешнего проббера лежат в `deploy/config.yaml`, в `nodes`:

```yaml
nodes:
  - name: provider-de-1          # для людей
    host: de1.example.net
    port: 443
    proto: vless-reality-xhttp   # как в отчёте клиента
  - name: fra-1
    host: 203.0.113.10
    port: 443
    proto: vless-reality
    slug: fra-1                  # наша нода: id = n:fra-1
```

id сервера считается так же, как у клиента: `s:` + первые 16 байт `sha256(lower(host) + ":" + port + ":" + proto)`
в hex (тест `TestServerIDMatchesClient` закрепляет значение). После правки: `docker compose restart downdetector`.

Проббер раз в 60 с делает TCP-connect с таймаутом 2 с. После 3 неудач подряд сервер `down`,
после успеха `up`. Для серверов не из реестра и до первой пробы `external = unknown`.

## Конфиг

`deploy/config.yaml`:

| поле | по умолчанию | смысл |
|---|---|---|
| `listen` | `:8080` | адрес сервиса |
| `db_path` | `data/downdetector.db` | SQLite с часовыми агрегатами |
| `secrets_path` | `secrets/secrets.yaml` | расшифрованные секреты |
| `k` | `3` | минимум разных установок (для друзей `2`) |
| `token_rate_per_min` | `120` | лимит запросов на токен |
| `overview_max_hours` | `720` | верхняя граница `hours` в `/v1/overview` |
| `nodes` | — | реестр нод |

## API

Все запросы с `Authorization: Bearer <token>`. Ошибки: `{"error": "…"}`.

### `POST /v1/reports` (инвайт-токен)

Тело — ровно отчёт клиента, других полей нет (лишнее поле → 400). Ответ `204`.

400, если: тело > 64 КБ; серверов > 200; `window` не выровнено по 300 с, не длиной 300 с, из будущего или
старше 2 ч; `src` не `tunnel`/`tcp`/`icmp`; `id` не `^s:[0-9a-f]{32}$|^n:[a-z0-9-]{1,32}$`;
`country` не две латинские строчные буквы (пустая = неизвестна); `region` не slug ≤ 64 (пустой = неизвестен);
`install` не `r-<24 hex>`; `ok`/`fail` отрицательные или оба нулевые; повтор пары (id, src).

Повторный отчёт той же установки за то же окно заменяет предыдущий. Больше 3 отправок за одно окно → 429.
Общий лимит на токен (`token_rate_per_min`) → 429.

### `GET /v1/status?asn=…&country=…&ids=a,b,c` (инвайт-токен)

До 200 id. По каждому:

```json
{
  "id": "s:…",
  "external": "up|down|unknown",
  "same_asn": {"installs": 3, "ok": 20, "fail": 2},
  "same_country_other_asn": {"installs": 5, "ok": 40, "fail": 1},
  "abroad": {"installs": 2, "ok": 10, "fail": 0},
  "verdict_if_down": "server_down|isp_block|country_block|local|unknown"
}
```

Группы считаются за последние 15 минут (3 закрытых 5-минутных окна), только `src=tunnel`.
Отчёты с неизвестной страной в группы не попадают. Группа **живая**: installs ≥ k и ok ≥ 70 %;
**мёртвая**: installs ≥ k и fail ≥ 70 %. Вердикт, по порядку:

1. `external=down`, или external неизвестен и все группы с данными мёртвые → `server_down`;
2. `same_asn` мёртвая, `same_country_other_asn` мёртвая или пустая, `abroad` живая → `country_block`;
3. `same_asn` мёртвая, `same_country_other_asn` живая → `isp_block`;
4. `same_asn` живая → `local`;
5. иначе `unknown`.

### `GET /v1/overview?hours=24` (админ-токен)

Агрегат из SQLite за последние `hours` часов: провайдер × регион × протокол × час.

```json
[{"hour": 1761796800, "asn": 12389, "country": "ru", "region": "moscow", "proto": "vless-reality",
  "src": "tunnel", "servers": 4, "ok": 120, "fail": 6, "rtt_p50_avg": 81, "installs_max": 5}]
```

`installs_max` — максимум по серверам: установки между серверами не складываются.

### Свёртка в SQLite

Каждые 5 минут сервис забирает из памяти часы, за которые отчёты больше прийти не могут
(конец часа + 2 ч), и пишет их в `rollup_hourly`. При остановке пишется всё, что есть в памяти.
Записанный час помечается в `rollup_done` и второй раз не пишется.
