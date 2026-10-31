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
			],
			ips: [],
			processes: {
				windows: ['steam.exe', 'steamwebhelper.exe', 'steamservice.exe'],
				darwin: ['steam_osx', 'steam', 'steamwebhelper'],
				linux: ['steam', 'steamwebhelper'],
			},
			defaultModes: ['bypass', 'proxy'],
		},
		{
			id: 'discord',
			name: 'Discord',
			icon: 'discord',
			domains: ['discord.com', 'discord.gg', 'discordapp.com', 'discordapp.net', 'discord.media', 'discordcdn.com'],
			ips: [],
			processes: {
				windows: ['Discord.exe', 'DiscordPTB.exe', 'DiscordCanary.exe'],
				darwin: ['Discord', 'Discord Helper', 'Discord Helper (Renderer)', 'Discord Helper (GPU)'],
				linux: ['Discord', 'discord'],
			},
			defaultModes: ['bypass', 'proxy'],
		},
		{
			id: 'telegram',
			name: 'Telegram',
			icon: 'telegram',
			domains: ['telegram.org', 't.me', 'telegram.me', 'telegra.ph', 'telesco.pe'],
			ips: [],
			processes: {
				windows: ['Telegram.exe'],
				darwin: ['Telegram'],
				linux: ['telegram-desktop', 'Telegram'],
			},
			defaultModes: ['bypass', 'proxy'],
		},
	],
};

/** The groups the client ships with, to seed an empty hub. */
export const BUNDLED_GROUPS: Group[] = BUNDLED.services.map(groupFromService);
