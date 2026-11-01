// A group bundles every rule about one thing: steam, discord, banks. It is
// the engine's catalog service (engine/core/presets/catalog.go): domains and
// ips hold everywhere; apps and folders are written per OS, in that OS's own
// form: an exe on windows, the binary inside the .app on macos. Every client
// gets the whole catalog and resolves the shared rules plus its own OS's, so
// a preset needs no per-platform targeting. The engine knows windows, darwin
// and linux; android has no client yet, so it has no rules here.
//
// A preset is a set of groups, each put on one side: past the vpn or through
// it, which becomes the service's defaultModes in the catalog the client
// fetches. Presets are what gets rolled out: a release freezes the groups as
// they are into a version, which goes to a share of devices, then everyone.
// The hub owns all of it (hikkari-control internal/presets); this module is
// the editor's view of the same shapes.

/** The engine's platform names, in the order the editor shows them. */
export const OSES = ['windows', 'darwin', 'linux'] as const;
export type Os = (typeof OSES)[number];
export const OS_LABEL: Record<Os, string> = { windows: 'windows', darwin: 'macos', linux: 'linux' };

export interface Rules {
	domains: string[];
	ips: string[];
	/** process names: the exe on windows, the binary in Contents/MacOS on macos */
	winApps: string[];
	/** path fragments: every binary under them, wherever installed */
	winFolders: string[];
	macApps: string[];
	macFolders: string[];
	linuxApps: string[];
	linuxFolders: string[];
}

export type RuleKind = keyof Rules;

/** Where each per-OS list lives in the engine's catalog. */
const OS_KINDS: Record<Os, { apps: RuleKind; folders: RuleKind }> = {
	windows: { apps: 'winApps', folders: 'winFolders' },
	darwin: { apps: 'macApps', folders: 'macFolders' },
	linux: { apps: 'linuxApps', folders: 'linuxFolders' },
};

export const RULE_SECTIONS: { label: string; kinds: { key: RuleKind; label: string; hint: string }[] }[] = [
	{
		label: 'везде',
		kinds: [
			{ key: 'domains', label: 'домены', hint: '+ домен' },
			{ key: 'ips', label: 'ip / подсети', hint: '+ ip или подсеть' },
		],
	},
	...OSES.map((os) => ({
		label: OS_LABEL[os],
		kinds: [
			{
				key: OS_KINDS[os].apps,
				label: 'приложения',
				hint: os === 'darwin' ? '+ имя бинаря из .app' : '+ имя процесса',
			},
			{ key: OS_KINDS[os].folders, label: 'папки', hint: '+ кусок пути со слешем' },
		],
	})),
];

/** Every rule list, with the OS it belongs to; null for the shared ones. */
export const RULE_KINDS = RULE_SECTIONS.flatMap((s, i) => s.kinds.map((k) => ({ ...k, os: i === 0 ? null : s.label })));

export interface Group {
	id: string;
	name: string;
	/** icon id the client knows; empty when it has none */
	icon: string;
	rules: Rules;
}

export type Side = 'bypass' | 'proxy' | 'disabled';
export const SIDES: { key: Side; label: string }[] = [
	{ key: 'bypass', label: 'мимо vpn' },
	{ key: 'proxy', label: 'через vpn' },
	{ key: 'disabled', label: 'выключено' },
];

export const emptyRules = (r: Partial<Rules> = {}): Rules => ({
	domains: [],
	ips: [],
	winApps: [],
	winFolders: [],
	macApps: [],
	macFolders: [],
	linuxApps: [],
	linuxFolders: [],
	...r,
});

// ---------- engine catalog ----------

/** One service of the engine's catalog, field for field. */
export interface CatalogService {
	id: string;
	name: string;
	icon: string;
	domains: string[];
	ips: string[];
	processes: Partial<Record<Os, string[]>>;
	folders?: Partial<Record<Os, string[]>>;
	defaultModes: Side[];
}

interface Catalog {
	schemaVersion: 1;
	version: number;
	services: CatalogService[];
}

/** A group's rules as the hub and the engine store them. */
export interface WireRules {
	domains: string[];
	ips: string[];
	processes: Partial<Record<Os, string[]>>;
	folders?: Partial<Record<Os, string[]>>;
}

export function rulesFromWire(w: WireRules): Rules {
	const rules = emptyRules({ domains: [...(w.domains ?? [])], ips: [...(w.ips ?? [])] });
	for (const os of OSES) {
		rules[OS_KINDS[os].apps] = [...(w.processes?.[os] ?? [])];
		rules[OS_KINDS[os].folders] = [...(w.folders?.[os] ?? [])];
	}
	return rules;
}

export function rulesToWire(r: Rules): WireRules {
	const processes: WireRules['processes'] = {};
	const folders: NonNullable<WireRules['folders']> = {};
	for (const os of OSES) {
		const apps = r[OS_KINDS[os].apps];
		const dirs = r[OS_KINDS[os].folders];
		if (apps.length) processes[os] = [...apps];
		if (dirs.length) folders[os] = [...dirs];
	}
	return { domains: [...r.domains], ips: [...r.ips], processes, ...(Object.keys(folders).length ? { folders } : {}) };
}

/** The engine's category of a rule list, for the hub's normalizer. */
export function categoryOf(k: RuleKind): 'domains' | 'ips' | 'processes' | 'folders' {
	if (k === 'domains' || k === 'ips') return k;
	return k.endsWith('Apps') ? 'processes' : 'folders';
}

export function groupFromService(s: CatalogService): Group {
	return { id: s.id, name: s.name, icon: s.icon, rules: rulesFromWire(s) };
}

const RU_MAP: Record<string, string> = {
	а: 'a', б: 'b', в: 'v', г: 'g', д: 'd', е: 'e', ё: 'yo', ж: 'zh',
	з: 'z', и: 'i', й: 'y', к: 'k', л: 'l', м: 'm', н: 'n', о: 'o',
	п: 'p', р: 'r', с: 's', т: 't', у: 'u', ф: 'f', х: 'kh', ц: 'ts',
	ч: 'ch', ш: 'sh', щ: 'shch', ъ: '', ы: 'y', ь: '', э: 'e', ю: 'yu', я: 'ya',
};

/** Converts a title to a valid client ID (`^[a-z0-9][a-z0-9-]{0,63}$`). */
export function slugify(text: string): string {
	const lower = text.toLowerCase();
	let translit = '';
	for (const ch of lower) {
		translit += RU_MAP[ch] ?? ch;
	}
	return translit
		.replace(/[^a-z0-9]+/g, '-')
		.replace(/^-+|-+$/g, '')
		.slice(0, 64);
}

/**
 * Why the engine would reject this group, or null: the quick checks, shown
 * while typing. The hub runs the engine's full checks on save and on release
 * (internal/presets/validate.go) and its refusal is the one that counts.
 */
export function groupProblem(g: Group): string | null {
	if (!/^[a-z0-9][a-z0-9-]{0,63}$/.test(g.id)) return `id «${g.id}»: латиница, цифры и дефис`;
	if (!g.name.trim()) return 'нет названия';
	const n = RULE_KINDS.reduce((k, x) => k + g.rules[x.key].length, 0);
	if (!n) return 'ни одного правила';
	for (const os of OSES) {
		const bad = g.rules[OS_KINDS[os].folders].find((f) => !f.includes('/') || /[*?<>|"\\]/.test(f));
		if (bad) return `папка «${bad}» (${OS_LABEL[os]}): нужен кусок пути со слешем, без масок`;
	}
	return null;
}

/** Icon identifiers supported by the Flutter client's ServiceIconBadge. */
export const SERVICE_ICONS: { id: string; label: string }[] = [
	{ id: 'ru', label: 'РФ / Рунет' },
	{ id: 'yandex', label: 'Яндекс' },
	{ id: 'yandex-music', label: 'Яндекс Музыка' },
	{ id: 'yandex-taxi', label: 'Яндекс Go и Такси' },
	{ id: 'yandex-maps', label: 'Яндекс Карты' },
	{ id: 'yandex-disk', label: 'Яндекс Диск' },
	{ id: 'yandex-market', label: 'Яндекс Маркет' },
	{ id: 'vk', label: 'ВКонтакте' },
	{ id: 'vk-video', label: 'VK Видео' },
	{ id: 'vk-play', label: 'VK Play' },
	{ id: 'mailru', label: 'Mail.ru' },
	{ id: 'ok', label: 'Одноклассники' },
	{ id: 'kinopoisk', label: 'Кинопоиск' },
	{ id: 'ivi', label: 'Иви' },
	{ id: 'okko', label: 'Okko' },
	{ id: 'wink', label: 'Wink' },
	{ id: 'streaming-ru', label: 'Онлайн-кинотеатры РФ' },
	{ id: 'rutube', label: 'Rutube' },
	{ id: 'sber', label: 'Сбербанк' },
	{ id: 'tbank', label: 'Т-Банк' },
	{ id: 'vtb', label: 'ВТБ' },
	{ id: 'alfa', label: 'Альфа-Банк' },
	{ id: 'banks', label: 'Другие банки РФ' },
	{ id: 'gosuslugi', label: 'Госуслуги' },
	{ id: 'transport-ru', label: 'РЖД и Авиабилеты' },
	{ id: 'marketplaces', label: 'Маркетплейсы' },
	{ id: 'ozon', label: 'Ozon' },
	{ id: 'wildberries', label: 'Wildberries' },
	{ id: 'avito', label: 'Авито' },
	{ id: 'steam', label: 'Steam' },
	{ id: 'faceit', label: 'FACEIT' },
	{ id: 'discord', label: 'Discord' },
	{ id: 'telegram', label: 'Telegram' },
	{ id: 'youtube', label: 'YouTube' },
	{ id: 'openai', label: 'ChatGPT / OpenAI' },
	{ id: 'claude', label: 'Claude / Anthropic' },
	{ id: 'gemini', label: 'Google Gemini' },
	{ id: 'instagram', label: 'Instagram / Meta' },
	{ id: 'x', label: 'X / Twitter' },
	{ id: 'spotify', label: 'Spotify' },
	{ id: 'twitch', label: 'Twitch' },
	{ id: 'notion', label: 'Notion' },
	{ id: 'figma', label: 'Figma' },
	{ id: 'github', label: 'GitHub & Copilot' },
	{ id: 'google', label: 'Google' },
	{ id: 'habr', label: 'Хабр' },
	{ id: 'kaspersky', label: 'Kaspersky' },
	{ id: '2gis', label: '2ГИС' },
];

// ---------- seed ----------

/**
 * The catalog the client ships with: engine/core/presets/defaults.json in
 * hikkari-client, copied here so an empty hub can start from it.
 */
const BUNDLED: Catalog = {
	schemaVersion: 1,
	version: 2,
	services: [
		{
			id: 'ru',
			name: 'Сайты РФ (.RU)',
			icon: 'ru',
			domains: [
				'ru',
				'xn--p1ai',
				'su',
				'xn--p1acf',
				'xn--80adxhks',
				'moscow',
				'tatar',
				'xn--d1acj3b',
				'mts.ru',
				'megafon.ru',
				'beeline.ru',
				't2.ru',
				'tele2.ru',
				'rt.ru',
				'rostelecom.ru',
				'selectel.ru',
				'selectel.com',
				'timeweb.com',
				'timeweb.cloud',
				'reg.ru',
				'beget.com',
				'habr.com',
				'habrastorage.org',
				'pikabu.ru',
				'vc.ru',
				'dtf.ru',
				'rbc.ru',
				'kommersant.ru',
				'lenta.ru',
				'ria.ru',
				'tass.ru',
				'interfax.ru',
				'fontanka.ru',
				'gismeteo.ru',
				'gismeteo.net',
				'kaspersky.ru',
				'kaspersky.com',
				'drweb.ru',
				'drweb.com',
				'1c.ru',
				'bitrix24.ru',
				'bitrix24.com',
				'kontur.ru',
				'diadoc.ru',
				'tensor.ru',
				'sbis.ru',
			],
			ips: [
				'79.137.128.0/17',
				'91.194.226.0/23',
				'95.213.0.0/16',
				'128.140.168.0/21',
				'178.22.88.0/21',
				'185.5.136.0/22',
				'185.16.148.0/22',
				'185.71.64.0/22',
				'185.71.68.0/22',
				'185.71.76.0/22',
				'185.157.96.0/22',
				'185.207.88.0/22',
				'188.93.56.0/21',
				'194.186.0.0/16',
				'195.146.66.0/23',
				'199.36.240.0/22',
				'212.49.96.0/19',
				'217.12.96.0/20',
			],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'yandex',
			name: 'Яндекс',
			icon: 'yandex',
			domains: [
				'yandex.ru',
				'yandex.com',
				'yandex.net',
				'yandex.by',
				'yandex.kz',
				'ya.ru',
				'yastatic.net',
				'yandexcloud.net',
				'clck.ru',
				'dzen.ru',
				'mail.yandex.ru',
				'passport.yandex.ru',
			],
			ips: [
				'5.45.192.0/18',
				'5.255.192.0/18',
				'37.9.64.0/18',
				'37.140.128.0/18',
				'77.88.0.0/18',
				'84.201.128.0/18',
				'87.250.224.0/19',
				'93.158.128.0/18',
				'95.108.128.0/17',
				'141.8.128.0/18',
				'178.154.128.0/17',
				'213.180.192.0/19',
			],
			processes: { windows: ['browser.exe'], darwin: ['Yandex'], linux: ['yandex-browser'] },
			defaultModes: ['bypass'],
		},
		{
			id: 'yandex-music',
			name: 'Яндекс Музыка',
			icon: 'yandex-music',
			domains: [
				'music.yandex.ru',
				'music.yandex.net',
				'music.yandex.com',
				'music.yandex.kz',
				'music.yandex.by',
				'music.ya.ru',
				'yandex-music.ru',
			],
			ips: [],
			processes: { windows: ['YandexMusic.exe'], darwin: ['Yandex Music'], linux: ['yandex-music'] },
			defaultModes: ['bypass'],
		},
		{
			id: 'yandex-taxi',
			name: 'Яндекс Go и Такси',
			icon: 'yandex-taxi',
			domains: [
				'taxi.yandex.ru',
				'go.yandex',
				'taxi.yandex.com',
				'taxi.yandex.net',
				'eda.yandex',
				'eda.yandex.ru',
				'lavka.yandex',
				'lavka.yandex.ru',
				'dostavka.yandex.ru',
				'delivery.yandex.ru',
			],
			ips: [],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'yandex-maps',
			name: 'Яндекс Карты',
			icon: 'yandex-maps',
			domains: [
				'maps.yandex.ru',
				'n.maps.yandex.ru',
				'maps.yandex.com',
				'maps.yandex.net',
				'core-renderer-tiles.maps.yandex.net',
				'api-maps.yandex.ru',
			],
			ips: [],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'yandex-disk',
			name: 'Яндекс Диск',
			icon: 'yandex-disk',
			domains: [
				'disk.yandex.ru',
				'disk.yandex.com',
				'disk.yandex.net',
				'downloader.disk.yandex.ru',
			],
			ips: [],
			processes: { windows: ['YandexDisk2.exe'], darwin: ['Yandex Disk'], linux: ['yandex-disk'] },
			defaultModes: ['bypass'],
		},
		{
			id: 'yandex-market',
			name: 'Яндекс Маркет',
			icon: 'yandex-market',
			domains: [
				'market.yandex.ru',
				'market-delivery.yandex.ru',
				'pokupki.market.yandex.ru',
				'm.market.yandex.ru',
			],
			ips: [],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'vk',
			name: 'ВКонтакте',
			icon: 'vk',
			domains: [
				'vk.com',
				'vk.ru',
				'vk.me',
				'userapi.com',
				'vkuser.net',
				'vk-cdn.net',
				'vk-portal.net',
			],
			ips: [
				'87.240.128.0/18',
				'93.186.224.0/20',
				'95.163.0.0/16',
				'178.248.232.0/21',
				'185.16.244.0/22',
				'185.32.187.0/24',
			],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'vk-video',
			name: 'VK Видео',
			icon: 'vk-video',
			domains: ['vkvideo.ru', 'vkuservideo.net'],
			ips: [],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'vk-play',
			name: 'VK Play',
			icon: 'vk-play',
			domains: ['vkplay.ru'],
			ips: [],
			processes: { windows: ['VKPlayLoader.exe'] },
			defaultModes: ['bypass'],
		},
		{
			id: 'mailru',
			name: 'Mail.ru',
			icon: 'mailru',
			domains: [
				'mail.ru',
				'imgsmail.ru',
				'cloud.mail.ru',
			],
			ips: ['217.69.128.0/20'],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'ok',
			name: 'Одноклассники',
			icon: 'ok',
			domains: [
				'ok.ru',
				'odnoklassniki.ru',
				'mycdn.me',
			],
			ips: ['217.20.144.0/20'],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'kinopoisk',
			name: 'Кинопоиск',
			icon: 'kinopoisk',
			domains: ['kinopoisk.ru'],
			ips: [],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'ivi',
			name: 'Иви',
			icon: 'ivi',
			domains: ['ivi.ru'],
			ips: [],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'okko',
			name: 'Okko',
			icon: 'okko',
			domains: ['okko.tv'],
			ips: [],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'wink',
			name: 'Wink',
			icon: 'wink',
			domains: ['wink.ru'],
			ips: [],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'streaming-ru',
			name: 'Онлайн-кинотеатры РФ',
			icon: 'streaming-ru',
			domains: [
				'premier.one',
				'start.ru',
				'kion.ru',
				'smotrim.ru',
			],
			ips: [],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'rutube',
			name: 'Rutube',
			icon: 'rutube',
			domains: [
				'rutube.ru',
				'rtbcdn.ru',
			],
			ips: [],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'sber',
			name: 'Сбербанк',
			icon: 'sber',
			domains: [
				'sberbank.ru',
				'sberbank.com',
				'sber.ru',
				'sberdevices.ru',
			],
			ips: ['194.54.14.0/23'],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'tbank',
			name: 'Т-Банк (Тинькофф)',
			icon: 'tbank',
			domains: [
				'tbank.ru',
				'tinkoff.ru',
				'tinkoff.com',
				'tcsbank.ru',
				'cdn-tinkoff.ru',
			],
			ips: [],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'vtb',
			name: 'ВТБ',
			icon: 'vtb',
			domains: [
				'vtb.ru',
				'vtb.com',
			],
			ips: ['195.218.168.0/22'],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'alfa',
			name: 'Альфа-Банк',
			icon: 'alfa',
			domains: [
				'alfabank.ru',
				'alfabank.com',
				'alfaclick.ru',
			],
			ips: [],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'banks',
			name: 'Другие банки РФ',
			icon: 'banks',
			domains: [
				'gazprombank.ru',
				'raiffeisen.ru',
				'psbank.ru',
				'sovcombank.ru',
				'rshb.ru',
				'mkb.ru',
				'uralsib.ru',
				'tochka.com',
				'nspk.ru',
				'mirpay.ru',
				'yoomoney.ru',
				'qiwi.com',
				'moex.com',
			],
			ips: [],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'gosuslugi',
			name: 'Госуслуги',
			icon: 'gosuslugi',
			domains: [
				'gosuslugi.ru',
				'gu-st.ru',
				'nalog.gov.ru',
				'mos.ru',
				'emiass.info',
				'pochta.ru',
			],
			ips: [
				'109.207.1.0/24',
			],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'transport-ru',
			name: 'РЖД и Авиабилеты',
			icon: 'transport-ru',
			domains: [
				'rzd.ru',
				'aeroflot.ru',
				's7.ru',
				'pobeda.aero',
				'tutu.ru',
				'aviasales.ru',
			],
			ips: [],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'ozon',
			name: 'Ozon',
			icon: 'ozon',
			domains: [
				'ozon.ru',
				'ozon.com',
				'ozon.by',
				'ozon.kz',
				'ozone.ru',
				'ozonusercontent.com',
			],
			ips: [],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'wildberries',
			name: 'Wildberries',
			icon: 'wildberries',
			domains: [
				'wildberries.ru',
				'wildberries.by',
				'wildberries.kz',
				'wb.ru',
				'wbbasket.ru',
				'wbstatic.net',
			],
			ips: [],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'avito',
			name: 'Авито',
			icon: 'avito',
			domains: [
				'avito.ru',
				'avito.st',
			],
			ips: [],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'marketplaces',
			name: 'Маркетплейсы',
			icon: 'marketplaces',
			domains: [
				'lamoda.ru',
				'lamoda.by',
				'lamoda.kz',
				'dns-shop.ru',
				'citilink.ru',
				'mvideo.ru',
				'eldorado.ru',
				'detmir.ru',
				'vseinstrumenti.ru',
				'aliexpress.ru',
				'kuper.ru',
				'samokat.ru',
				'cian.ru',
				'domclick.ru',
				'hh.ru',
				'drom.ru',
				'2gis.ru',
				'2gis.com',
			],
			ips: [],
			processes: {},
			defaultModes: ['bypass'],
		},
		{
			id: 'steam',
			name: 'Steam',
			icon: 'steam',
			domains: [
				'steampowered.com',
				'steamcommunity.com',
				'steamgames.com',
				'steamstatic.com',
				'steamcontent.com',
				'steamserver.net',
				'steamusercontent.com',
				'steam-chat.com',
				'steamdeck.com',
				'underlords.com',
				'valvesoftware.com',
				's.team',
				'dota2.com',
				'playartifact.com',
				'counter-strike.net',
				'csgo.com',
				'teamfortress.com',
			],
			ips: [],
			processes: { windows: ['steam.exe', 'steamwebhelper.exe', 'steamservice.exe', 'GameOverlayUI.exe', 'steamerrorreporter.exe', 'steamerrorreporter64.exe', 'streaming_client.exe'], darwin: ['steam_osx', 'steam', 'steamwebhelper', 'steam_monitor'], linux: ['steam', 'steamwebhelper', 'steam-runtime-launcher-service'] },
			defaultModes: ['bypass'],
		},
		{
			id: 'faceit',
			name: 'FACEIT',
			icon: 'faceit',
			domains: [
				'faceit.com',
				'faceit-cdn.net',
				'faceitcdn.com',
				'faceitstatus.com',
				'faceit.tv',
				'faceit.media',
				'faceittracker.com',
				'faceitfinder.com',
				'faceitstats.com',
				'hiperz.com',
			],
			ips: [],
			processes: {
				windows: [
					'FACEIT.exe',
					'faceit.exe',
					'faceitclient.exe',
					'FACEITClient.exe',
					'faceitservice.exe',
					'FACEITService.exe',
					'faceitac.exe',
					'FACEITAC.exe',
					'FACEIT AC.exe',
					'FACEIT Anti-Cheat.exe',
					'faceit_client.exe',
					'faceit_anticheat.exe',
					'FACEIT_Start_Protected_Game.exe',
					'faceit_start_protected_game.exe',
					'FACEITAC_Installer.exe',
					'hiperz_test.exe',
					'cs2.exe',
					'csgo.exe',
				],
				darwin: ['FACEIT', 'faceit'],
				linux: ['faceit', 'faceit-client', 'cs2'],
			},
			folders: {
				windows: [
					'FACEIT Client/',
					'FACEIT AC/',
					'FACEIT/',
				],
			},
			defaultModes: ['bypass'],
		},
		{
			id: 'discord',
			name: 'Discord',
			icon: 'discord',
			domains: [
				'discord.com',
				'discord.gg',
				'discordapp.com',
				'discordapp.net',
				'discord.media',
				'discordcdn.com',
				'discord.co',
				'discord.design',
				'discord.dev',
				'discord.gift',
				'discord.gifts',
				'discord.new',
				'discord.store',
				'discord.tools',
				'dis.gd',
				'discordstatus.com',
				'discord-activities.com',
				'discordactivities.com',
				'discordapp.io',
				'discordpartygames.com',
				'airhorn.solutions',
				'airhornbot.com',
				'bigbeans.solutions',
				'watchanimeattheoffice.com',
			],
			ips: [],
			processes: { windows: ['Discord.exe', 'DiscordPTB.exe', 'DiscordCanary.exe', 'DiscordDevelopment.exe'], darwin: ['Discord', 'Discord PTB', 'Discord Canary', 'Discord Helper', 'Discord Helper (Renderer)', 'Discord Helper (GPU)', 'Discord Helper (Plugin)'], linux: ['Discord', 'discord', 'DiscordPTB', 'DiscordCanary', 'vesktop', 'webcord'] },
			defaultModes: ['proxy'],
		},
		{
			id: 'telegram',
			name: 'Telegram',
			icon: 'telegram',
			domains: [
				'telegram.org',
				't.me',
				'telegram.me',
				'telegra.ph',
				'telesco.pe',
				'tdesktop.com',
				'tx.me',
				'tg.dev',
				'fragment.com',
				'graph.org',
				'quiz.directory',
				'telegram.dog',
				'telegram.space',
				'cdn-telegram.org',
				'comments.app',
				'contest.com',
			],
			ips: [
				'91.105.192.0/23',
				'91.108.4.0/22',
				'91.108.8.0/22',
				'91.108.12.0/22',
				'91.108.16.0/22',
				'91.108.20.0/22',
				'91.108.56.0/22',
				'95.161.64.0/20',
				'149.154.160.0/20',
				'185.76.151.0/24',
				'2001:67c:4e8::/48',
				'2001:b28:f23c::/47',
				'2001:b28:f23f::/48',
				'2a0a:f280::/32',
			],
			processes: { windows: ['Telegram.exe', 'Unigram.exe', '64Gram.exe', 'AyuGram.exe', 'Kotatogram.exe'], darwin: ['Telegram', 'Telegram Lite', 'AyuGram'], linux: ['telegram-desktop', 'Telegram', '64gram-desktop', 'ayugram-desktop', 'kotatogram-desktop'] },
			defaultModes: ['proxy'],
		},
		{
			id: 'youtube',
			name: 'YouTube',
			icon: 'youtube',
			domains: [
				'youtube.com',
				'youtu.be',
				'googlevideo.com',
				'ytimg.com',
				'ytimg.l.google.com',
				'ggpht.com',
				'yt3.ggpht.com',
				'youtubei.googleapis.com',
				'youtubeembeddedplayer.googleapis.com',
				'youtube-nocookie.com',
				'youtube-ui.l.google.com',
				'wide-youtube.l.google.com',
				'yt.be',
				'withyoutube.com',
				'youtubegaming.com',
				'youtubekids.com',
				'youtubemusic.com',
				'youtuber.com',
				'youtubeeducation.com',
				'youtubefanfest.com',
				'youtubego.com',
				'returnyoutubedislikeapi.com',
				'jnn-pa.googleapis.com',
			],
			ips: [],
			processes: {},
			defaultModes: ['proxy'],
		},
		{
			id: 'openai',
			name: 'ChatGPT / OpenAI',
			icon: 'openai',
			domains: [
				'openai.com',
				'chatgpt.com',
				'oaistatic.com',
				'oaiusercontent.com',
				'sora.com',
				'chat.com',
				'searchgpt.com',
				'openai.org',
			],
			ips: [],
			processes: { windows: ['ChatGPT.exe'], darwin: ['ChatGPT'], linux: ['chatgpt'] },
			defaultModes: ['proxy'],
		},
		{
			id: 'claude',
			name: 'Claude / Anthropic',
			icon: 'claude',
			domains: [
				'anthropic.com',
				'claude.ai',
				'claude.com',
				'claudeusercontent.com',
				'anthropic.statuspage.io',
			],
			ips: [],
			processes: { windows: ['Claude.exe'], darwin: ['Claude'], linux: ['claude-desktop'] },
			defaultModes: ['proxy'],
		},
		{
			id: 'gemini',
			name: 'Google Gemini',
			icon: 'gemini',
			domains: [
				'gemini.google.com',
				'aistudio.google.com',
				'makersuite.google.com',
				'bard.google.com',
				'notebooklm.google.com',
				'notebooklm.google',
				'generativelanguage.googleapis.com',
				'alkalimakersuite-pa.clients6.google.com',
				'proactivebackend-pa.googleapis.com',
				'deepmind.com',
				'deepmind.google',
				'ai.google.dev',
				'idx.dev',
				'idx.google.com',
				'labs.google',
			],
			ips: [],
			processes: {},
			defaultModes: ['proxy'],
		},
		{
			id: 'instagram',
			name: 'Instagram / Meta',
			icon: 'instagram',
			domains: [
				'instagram.com',
				'cdninstagram.com',
				'ig.me',
				'igsonar.com',
				'threads.net',
				'threads.com',
				'facebook.com',
				'fb.com',
				'fbcdn.net',
				'fb.me',
				'fbsbx.com',
				'facebook.net',
				'messenger.com',
				'm.me',
				'whatsapp.com',
				'whatsapp.net',
				'wa.me',
				'meta.com',
				'meta.ai',
				'oculus.com',
				'oculuscdn.com',
			],
			ips: [],
			processes: { windows: ['WhatsApp.exe', 'Messenger.exe', 'Instagram.exe'], darwin: ['WhatsApp', 'Messenger'], linux: ['whatsapp-for-linux', 'zapzap'] },
			defaultModes: ['proxy'],
		},
		{
			id: 'x',
			name: 'X / Twitter',
			icon: 'x',
			domains: [
				'x.com',
				'twitter.com',
				't.co',
				'twimg.com',
				'twitpic.com',
				'twitter.co',
				'twitterinc.com',
				'twitterstat.us',
				'periscope.tv',
				'pscp.tv',
				'vine.co',
				'x.ai',
				'grok.com',
				'ads-twitter.com',
				'cms-twdigitalassets.com',
			],
			ips: [],
			processes: {},
			defaultModes: ['proxy'],
		},
		{
			id: 'spotify',
			name: 'Spotify',
			icon: 'spotify',
			domains: [
				'spotify.com',
				'scdn.co',
				'spotifycdn.com',
				'spotifycdn.net',
				'spotilocal.com',
				'pscdn.co',
				'spoti.fi',
				'spotifycharts.com',
				'spotifycodes.com',
				'spotifyforbrands.com',
				'spotifyjobs.com',
				'audio-ak-spotify-com.akamaized.net',
				'heads4-ak-spotify-com.akamaized.net',
			],
			ips: [],
			processes: { windows: ['Spotify.exe'], darwin: ['Spotify'], linux: ['spotify'] },
			defaultModes: ['proxy'],
		},
		{
			id: 'twitch',
			name: 'Twitch',
			icon: 'twitch',
			domains: [
				'twitch.tv',
				'ttvnw.net',
				'jtvnw.net',
				'twitchcdn.net',
				'twitchsvc.net',
				'ext-twitch.tv',
				'twitch.map.fastly.net',
			],
			ips: [],
			processes: {},
			defaultModes: ['proxy'],
		},
		{
			id: 'notion',
			name: 'Notion',
			icon: 'notion',
			domains: [
				'notion.so',
				'notion.com',
				'notion.site',
				'notion-static.com',
				'notion.new',
				'makenotion.com',
			],
			ips: [],
			processes: { windows: ['Notion.exe', 'Notion Calendar.exe'], darwin: ['Notion', 'Notion Calendar'], linux: ['notion-app', 'notion-snap-reborn'] },
			defaultModes: ['proxy'],
		},
		{
			id: 'figma',
			name: 'Figma',
			icon: 'figma',
			domains: [
				'figma.com',
				'figma.net',
				'figmacdn.com',
			],
			ips: [],
			processes: { windows: ['Figma.exe', 'figma_agent.exe'], darwin: ['Figma', 'FigmaAgent'], linux: ['figma-linux'] },
			defaultModes: ['proxy'],
		},
		{
			id: 'github',
			name: 'GitHub & Copilot',
			icon: 'github',
			domains: [
				'github.com',
				'github.io',
				'githubusercontent.com',
				'githubassets.com',
				'githubcopilot.com',
				'github.dev',
				'github.blog',
				'github.community',
				'githubstatus.com',
				'ghcr.io',
				'git.io',
				'octocaptcha.com',
				'npmjs.com',
				'npmjs.org',
			],
			ips: [],
			processes: { windows: ['GitHubDesktop.exe'], darwin: ['GitHub Desktop'], linux: ['github-desktop'] },
			defaultModes: ['proxy'],
		},
	],
};

export const BUNDLED_GROUPS: Group[] = BUNDLED.services.map(groupFromService);

/** The baseline RU preset matching the client's defaultModes, to seed an empty hub. */
export const BUNDLED_PRESET = {
	id: 'ru',
	name: 'Россия',
	default: true,
	countries: ['RU'],
	bypass: BUNDLED.services.filter((s) => s.defaultModes.includes('bypass')).map((s) => s.id),
	proxy: BUNDLED.services.filter((s) => s.defaultModes.includes('proxy')).map((s) => s.id),
	disabled: BUNDLED.services.filter((s) => s.defaultModes.length === 0).map((s) => s.id),
};
