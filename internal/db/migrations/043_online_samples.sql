-- Сколько установщиков было на связи в каждую минуту. Heartbeat тестера лишь
-- обновляет support_install_aliases.last_seen_at, истории у него нет, а график
-- «онлайн сейчас» на странице метрик продукта нужен именно по времени.
-- Hub раз в минуту записывает число установщиков с сигналом за последние
-- три минуты (support.TesterOnlineWindow); старше 35 дней стирается.
CREATE TABLE IF NOT EXISTS product_online_samples (
    ts timestamptz PRIMARY KEY,
    online integer NOT NULL
);
