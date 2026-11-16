-- Роль машины вне пула релеев (hikkari-client plans/2026-11-16-hidden-border-receiver-and-egress.md,
-- шаг 3 очереди). NULL: обычная машина флота, hub сам выпускает на ней релеи и раздаёт их в подписки.
-- egress: выход в интернет за приёмником. Релеев, ключей и сессий на нём нет никогда; hub кладёт свой
-- ключ, закрывает машину (harden) и больше ничего не ставит.
-- receiver: приёмник магистрали с чистым адресом. Свой релей hub ему не выпускает: релей приходит
-- только командой receiver-spare (копия identity основного приёмника группы).
ALTER TABLE entry_boxes
	ADD COLUMN role text CHECK (role IN ('egress', 'receiver'));
