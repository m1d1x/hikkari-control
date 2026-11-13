package entry

// keepUnitsFromNeedrestartScript keeps needrestart off the hub's units.
//
// On Ubuntu, unattended-upgrades runs needrestart after every upgrade, and
// needrestart restarts any service still mapping an old library. A restart
// cuts all that a unit carries: 13.11 at 06:46:16 UTC, libc6 from
// unattended-upgrades restarted the trunk entry on aezamsk1 (cgo, so it maps
// libc) and the stand over hk-pipe beside it, and every client through the
// entry was cut (plans/research/2026-11-13-rust-pipe-r5-brain-stand.md).
// The hub's units are updated only by the hub, by a reload where the unit can
// hand over; a library they still map goes in with the next one.
//
// The file is written by every script that puts a hub unit on a box (relay,
// trunk entry and its stand, prober), so a box gets it with its next rollout
// as well as when it is first provisioned. It is harmless where needrestart
// is not installed.
const keepUnitsFromNeedrestartScript = `mkdir -p /etc/needrestart/conf.d
cat >/etc/needrestart/conf.d/50-nekko.conf.new <<'CONF'
# Written by the nekko hub: needrestart never restarts the hub's units, the
# hub updates them (a restart cuts what they carry).
$nrconf{override_rc}->{qr(^nekko-)} = 0;
CONF
mv /etc/needrestart/conf.d/50-nekko.conf.new /etc/needrestart/conf.d/50-nekko.conf
`
