-- Режим маршрута по умолчанию у пресета (D3 плана «через VPN только
-- заблокированное», hikkari-client plans/2026-11-16-default-mode-blocked-via-vpn.md).
-- Пустая строка: каталог без поля, клиент остаётся в своём bypass. Черновик
-- пресета держит режим, релиз замораживает его вместе с группами, так что режим
-- раскатывается стадиями, как и список.
ALTER TABLE presets ADD COLUMN default_mode text NOT NULL DEFAULT ''
    CHECK (default_mode IN ('', 'bypass', 'proxy'));
ALTER TABLE preset_releases ADD COLUMN default_mode text NOT NULL DEFAULT ''
    CHECK (default_mode IN ('', 'bypass', 'proxy'));
