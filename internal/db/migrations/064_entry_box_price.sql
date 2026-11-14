-- Цена машины так, как её выставляет хостер (469 RUB у U1, 1.49 EUR у HostVDS),
-- рядом с monthly_cents в центах США. hub сам переводит её в центы по курсу ЦБ
-- (internal/fx) и обновляет цены машин с API хостера (entry.SyncBoxPrices); у
-- хостеров без API цену ставит владелец командой `hub entry-box price`.
-- NULL: цена в своей валюте не известна, monthly_cents тогда не трогается.
ALTER TABLE entry_boxes
	ADD COLUMN price_amount numeric(12, 2) CHECK (price_amount IS NULL OR price_amount > 0),
	ADD COLUMN price_currency text CHECK (price_currency IN ('USD', 'EUR', 'RUB')),
	ADD CONSTRAINT entry_boxes_price_pair CHECK ((price_amount IS NULL) = (price_currency IS NULL));
