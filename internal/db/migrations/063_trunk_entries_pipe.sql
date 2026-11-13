-- Стенд входа на трубе Rust (этап 4.6, шаг R5; hikkari-control
-- internal/entry/trunkpipe.go): мозг trunkentry над hk-pipe отдельным unit
-- nekko-trunk-pipe рядом с Go-входом на том же боксе, к тому же выходу, на
-- своих портах. pipe_forward: его порты, как forward у входа ({"18443": 443});
-- NULL, когда стенда нет. Наблюдение каскада меряет оба пути (путь pipe).
ALTER TABLE trunk_entries ADD COLUMN pipe_forward jsonb;
