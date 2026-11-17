-- Выход приёмника (hikkari-client plans/2026-11-16-hidden-border-receiver-and-egress.md, H3; шаг 3 очереди).
-- Строка на приёмник: через какой выход (entry_boxes.role = 'egress') он ходит в интернет. Раньше пара
-- жила только в env на машинах; теперь hub знает, кто через кого ходит, и меняет выход одной командой.
-- slot даёт приёмнику постоянный адрес в туннеле (10.231.0.0/16 и fd77:231::/64, slot 2 занят выходом):
-- при смене выхода адрес приёмника в туннеле не меняется, а один выход держит несколько приёмников,
-- по пиру на каждый. rx_pub: открытый ключ WireGuard приёмника, нужен выходу в его списке пиров;
-- закрытый ключ не покидает машину.
CREATE TABLE entry_egress_links (
	receiver   text PRIMARY KEY REFERENCES entry_boxes(name),
	egress     text NOT NULL REFERENCES entry_boxes(name),
	slot       integer NOT NULL UNIQUE CHECK (slot BETWEEN 1 AND 65534 AND slot <> 2),
	rx_pub     text NOT NULL,
	updated_at timestamptz NOT NULL DEFAULT now(),
	CHECK (receiver <> egress)
);
CREATE INDEX entry_egress_links_egress ON entry_egress_links (egress);
