-- Параметры команды тестеру: адреса для probe_targets. У остальных команд NULL.
ALTER TABLE support_tester_commands ADD COLUMN IF NOT EXISTS params jsonb;
