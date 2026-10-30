# Hikkari Control: флот и даундетектор

## Локальный хаб

Требуются Docker Compose v2 с `include`, OpenSSL, SOPS и age-ключ в
`~/.config/sops/age/keys.txt`. Команда ниже читает зашифрованный `deploy/hub.env.enc`,
создаёт локальные пароли БД, ключ JWT и токены даундетектора в закрытой `secrets/`.
Существующие ключи не перезаписываются. Открытые секреты запрещено добавлять в Git.

```sh
./scripts/hub-local-init.sh
docker compose up --build -d
docker compose ps
```

Хаб, Postgres 16 с TLS, ClickHouse 24.8, Svelte-админка и Caddy поднимаются одним
стеком. Данные — в именованных томах. БД и API администратора не публикуются отдельно.
Админка: `http://localhost:9443`, публичный API: `https://localhost:9444`.
Caddy выпускает локальный сертификат; для браузера импортируйте локальный CA:

```sh
docker compose cp caddy:/data/caddy/pki/authorities/local/root.crt /tmp/hikkari-local-ca.crt
open /tmp/hikkari-local-ca.crt
```

В Keychain Access явно доверьте **только** этому локальному корню. Автоматические
браузерные тесты используют изолированный профиль с локальным сертификатом.

Создание владельца (пароль читается со stdin, код первого passkey показывается один раз):

```sh
docker compose exec hub hub admin-create --role=owner owner
```

Откройте `/login`, введите пароль и код консоли, зарегистрируйте passkey и сохраните
10 кодов восстановления. Раздел «Антиблок» содержит обзор, автопилот, машины,
инциденты, пробник, домены и провайдеров. Опасные действия требуют свежего passkey.

Вход в панель — как в НЕККО: панель и её API слушают только `127.0.0.1:9443` машины,
попасть туда можно лишь SSH-туннелем, то есть с SSH-ключом:

```sh
scripts/admin.sh <ssh-host>          # туннель к хабу на сервере, Ctrl-C закрывает
scripts/admin.sh local               # локальный стек
scripts/admin.sh enroll owner <ssh-host|local>   # новый код подключения passkey
scripts/admin.sh passwd owner <ssh-host|local>   # новый пароль (пустой ввод — сгенерировать)
```

Раздел «Продукт» (обзор, ошибки, обращения, донаты, пресеты) перенесён из макета
`hikkari-client/admin` как есть: данные демонстрационные (`admin/src/lib/product/mock.ts`),
бэкенда для них пока нет.

Создание и отзыв подписки:

```sh
docker compose exec hub hub subscriber-add alice
docker compose exec hub hub subscriber-revoke alice
```

Команда выдаёт токен один раз. Подписка: `https://<public-host>/sub/<token>`.
В БД лежит только SHA-256 токена. По умолчанию выдаются Reality и XHTTP.
Для Hikkari с выбранным движком **Xray** используйте `/sub/<token>?engine=xray`:
добавляются Hysteria2 с `pinSHA256` сертификата и `finalmask` (весь набор Salamander + Noise).
Sing-box должен явно отказаться от таких профилей, иначе pin или Noise будут потеряны.
Эти ссылки требуют клиентского изменения из локального плана переноса, до его выпуска
обычную подписку сохраняйте без `engine=xray`.

### Первое подключение Vultr

В «Антиблок → Провайдеры» выберите Vultr и введите API-ключ в форме подключения;
сохранение требует passkey. Ключ хранится зашифрованным ключом хаба и не возвращается
в API/аудите. Поля и доступность аккаунта проверяются перед включением.
Начальные суточный и месячный лимиты обеих групп равны **0**, автопилот выключен
в `compose.hub.yaml`. Сам запуск стека не заказывает машины. Установку ненулевого
лимита, включение автопилота и живой заказ выполняйте только с явного разрешения владельца.
Для этой задачи ключ для живого прогона передаётся через SOPS, не через чат.

### Ноды и даундетектор

Хаб обслуживает `/v1/reports`, `/v1/status`, `/v1/overview` из того же процесса;
токены читаются из `secrets/hub-downdetector.yaml`. Raw-отчёты и install ID остаются
в оперативной памяти; SQLite хранит только часовые агрегаты с k>=3.
`entry_nodes` связывает устойчивый slug с текущим релеем. При замене недоступного
релея слот переиспользуется в том же протоколе/регионе; IP остаётся в cryptobox.
В ссылке сохраняется `hikkari-node=n:<slug>` и устойчивое имя.

`server_down` дополняет диагноз недоступности; SSH/grid отделяет отказ провайдера
от блока. `isp_block`/`country_block` выбирают ротацию Reality; для XHTTP — смену
домена, для Hysteria2 — выселение с последующим пополнением флота. Сохраняются
владельческие режимы, cooldown, wave pause и денежные лимиты. Одного TCP-успеха
недостаточно для подтверждения здоровья туннеля; недостаточные/просроченные отчёты
не закрывают инцидент. UDP Hysteria2 не проверяется TCP-пробой.

При смене адреса исключаются старые окна отчётов и результаты незавершённых проб.
Для первого допуска новой машины по-прежнему нужен доверенный admission-пробник:
анонимная клиентская статистика не заменяет проверку допуска.

### Хранилище, обновление, приватность

Новый baseline Postgres содержит только control plane, без аккаунтов мессенджера,
чатов, файлов и SFU. ClickHouse создаёт только 5 `entry_*` datasets. Старая
экспериментальная история миграций НЕККО отвергается: выберите **новую БД**, не
удаляйте существующую автоматически. Миграции сериализованы между хабом и CLI.

Caddy и nginx не ведут access log, Caddy не пишет запросы в error log, HTTP-сервер
хаба не пишет IP через стандартный error logger. Query/session logs ClickHouse
отключены; адреса машин и провайдерские ключи в Postgres зашифрованы.
Админский listener доступен с хоста через loopback-порт Caddy; публичный listener
не содержит маршрутов администратора. Удалённый запуск здесь не выполняется.

Проверки Go требуют отдельные БД, не рабочую базу флота:

```sh
GOFLAGS=-mod=mod TEST_DATABASE_URL='postgres://hc:hc@127.0.0.1:55442/hc_clean?sslmode=disable' \
 CLICKHOUSE_TEST_DSN='clickhouse://hc:hc@127.0.0.1:59000/hc' \
 NEKKO_TEST_CLICKHOUSE='clickhouse://hc:hc@127.0.0.1:59000/hc' go test -p 1 -count=1 ./...
(cd edge && go test ./...)
(cd admin && npm ci && npm run check && npm test && npm run build)
```

Локальная проверка браузера выполняется с виртуальным WebAuthn-аутентификатором,
без обхода парольной проверки, CSRF, регистрации passkey или выдачи сессии.

---

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
HTTP_PORT=8088 HTTPS_PORT=8443 docker compose -f compose.downdetector.yaml up -d --build
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
   `docker compose -f compose.downdetector.yaml restart downdetector`.

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
в hex (тест `TestServerIDMatchesClient` закрепляет значение). После правки: `docker compose -f compose.downdetector.yaml restart downdetector`.

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
