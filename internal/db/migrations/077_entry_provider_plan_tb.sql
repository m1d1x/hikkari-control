-- How many terabytes a month one machine's plan at a hoster includes, as the
-- owner reads it off the hoster's page (18.11: «у аезы щас миллиарды за тб»).
-- The provider table divides a machine's rent by it to show what a terabyte
-- costs at the plan's ceiling; the measured figure, rent over what the relays
-- carried, is meaningless while three testers carry a few gigabytes.
CREATE TABLE IF NOT EXISTS entry_provider_plans (
	provider   text PRIMARY KEY,
	plan_tb    numeric(10, 2) NOT NULL CHECK (plan_tb > 0),
	updated_at timestamptz NOT NULL DEFAULT now()
);
