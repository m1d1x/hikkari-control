-- HostVDS boxes were written with the region id for a city and "Cloud" for a
-- country (the OpenStack adapter had no place for a region). The places are
-- the ones the boxes answer from (internal/openstack hostvdsPlaces). Retired
-- boxes too: their rows are history the console still draws.
UPDATE entry_boxes SET location = 'Amsterdam, NL'
 WHERE provider = 'openstack' AND provider_region = 'eu-west1' AND location = 'eu-west1, Cloud';
UPDATE entry_boxes SET location = 'Riga, LV'
 WHERE provider = 'openstack' AND provider_region = 'eu-north2' AND location = 'eu-north2, Cloud';
-- A region nobody has placed (eu-north1, eu-north3: one retired box each,
-- nothing left to ask) keeps its id and loses the made-up country.
UPDATE entry_boxes SET location = provider_region
 WHERE provider = 'openstack' AND provider_region <> '' AND location = provider_region || ', Cloud';
