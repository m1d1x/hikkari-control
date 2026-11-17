-- Запасной выход приёмника (hikkari-client plans/2026-11-16-hidden-border-receiver-and-egress.md, H4; шаг 3 очереди).
-- spare: второй выход, вторым пиром WireGuard в туннеле приёмника, тёплый. Сторож на приёмнике
-- (hikkari-egress-watch) раз в секунду пингует оба выхода через туннель и интернет через тот, что везёт, и
-- при его смерти отдаёт маршруты запасному одним вызовом wg. spare_pub: открытый ключ запасного, нужен env
-- приёмника; закрытый не покидает машину. Запасной везёт приёмник тем же слотом, что и выход.
ALTER TABLE entry_egress_links
	ADD COLUMN spare text REFERENCES entry_boxes(name),
	ADD COLUMN spare_pub text,
	ADD CONSTRAINT entry_egress_links_spare_check
		CHECK (spare IS NULL OR (spare <> egress AND spare <> receiver AND spare_pub IS NOT NULL));
CREATE INDEX entry_egress_links_spare ON entry_egress_links (spare);
