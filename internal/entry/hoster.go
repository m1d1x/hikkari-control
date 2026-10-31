package entry

import (
	"context"
	"errors"
)

// BoxProvider is everything the hub needs from a hosting provider to run a
// machine without a human (plan 42 П9.2): what it sells and where, order one,
// learn its address, reboot it, hand it back, and say how much credit is left.
//
// Declared here, on the consumer, for the same reason [DomainRegistrar] is:
// the entry package never imports a provider. The Vultr implementation is a
// pure client in internal/vultr; the adapter that makes it a BoxProvider
// lives in internal/entrywire, beside the registrar's.
//
// # Why ordering is two calls and not one
//
// The plan writes the seam as `Create(region, plan, sshKey) → id, ip`. A
// hoster does not work that way: the order comes back at once with an id and
// no address, because the machine is still being built. So Create returns the
// instance as the hoster knows it right then, and [BoxProvider.Instance] is
// polled until it has an address. Folding the wait into Create would hide a
// multi-minute call behind a name that reads instant, and would leave the
// caller nothing to show while it waits.
//
// # Why the hub uploads its own ssh key
//
// A machine the hub ordered must be reachable by the hub the moment it boots,
// and the alternative — the hoster generating a root password and handing it
// back over the API — puts a live credential through our logs for no gain.
// EnsureSSHKey registers the hub's public key with the hoster once; Create
// names it, and the box comes up with the hub already in authorized_keys. The
// provisioner then needs no password at all, exactly as it needs none for a
// box whose key was pasted into a panel by hand.
type BoxProvider interface {
	// Regions is where this hoster sells machines.
	Regions(ctx context.Context) ([]BoxRegion, error)

	// Plans is what it sells, with the monthly price in whole cents — the
	// figure the budget gate reads and the figure recorded on the box.
	Plans(ctx context.Context) ([]BoxPlan, error)

	// EnsureSSHKey registers publicKey under name (idempotently: the same key
	// already there is reused, not duplicated) and returns the hoster's id
	// for it, to be named in an order.
	EnsureSSHKey(ctx context.Context, name, publicKey string) (string, error)

	// Create orders one machine. It returns as soon as the hoster has taken
	// the order: the instance's Address is usually empty at that point, and
	// the caller polls Instance for it.
	Create(ctx context.Context, o BoxOrder) (BoxInstance, error)

	// Instance is one machine as the hoster sees it now. ErrNoSuchInstance
	// when it is gone — which is the answer a delete wants to be able to get.
	Instance(ctx context.Context, id string) (BoxInstance, error)

	// Instances is every machine on the account. It is what lets the console
	// show an instance the hub is paying for and has no box row for — the
	// debris of an order that died between the charge and the record.
	Instances(ctx context.Context) ([]BoxInstance, error)

	// Reboot restarts the machine. The repair that uses it then waits for ssh
	// and re-probes; the hoster's answer alone proves nothing.
	Reboot(ctx context.Context, id string) error

	// Delete hands the machine back and stops the charge. Deleting one that
	// is already gone is not an error: the end state is what was asked for.
	Delete(ctx context.Context, id string) error

	// Account proves the credentials work and reads the money: the credit
	// left and what has accrued unbilled. Bad credentials are an error, not a
	// zero balance.
	Account(ctx context.Context) (HostingAccount, error)
}

// Errors the hosting half returns.
var (
	// ErrNoHoster: no hosting provider is configured and enabled, so the hub
	// cannot order or hand back a machine. A state, not a fault: a hub whose
	// boxes were all rented by hand is in it by design.
	ErrNoHoster = errors.New("entry: no hosting provider wired")
	// ErrNoSuchInstance: the hoster has no instance with that id.
	ErrNoSuchInstance = errors.New("entry: no such instance at the provider")
	// ErrNoSuchPlan: the plan or region the order named is not one this
	// hoster sells. Refused before any charge.
	ErrNoSuchPlan = errors.New("entry: provider does not sell that plan")
)

// BoxRegion is one place a hoster sells machines in.
type BoxRegion struct {
	// ID is the hoster's own id ("ams"), which an order names.
	ID string `json:"id"`
	// City and Country are for the console; a box's location is written from
	// them.
	City      string `json:"city"`
	Country   string `json:"country"`
	Continent string `json:"continent,omitempty"`
}

// BoxPlan is one machine size, with its price.
type BoxPlan struct {
	ID           string `json:"id"`
	VCPUs        int    `json:"vcpus"`
	RAMMB        int    `json:"ramMb"`
	DiskGB       int    `json:"diskGb"`
	BandwidthGB  int    `json:"bandwidthGb"`
	MonthlyCents int    `json:"monthlyCents"`
	// OverageCentsPerTB is what traffic past the plan's allowance costs; zero
	// when the hoster does not say. The fleet reads it against the plan's own
	// price per allowed terabyte (sizing.go, the quota floor).
	OverageCentsPerTB int `json:"overageCentsPerTb"`
	// Regions is where this plan can be ordered. A plan sold out of a region
	// fails at order time with the hoster's own words, which is why the
	// console filters on this rather than guessing.
	Regions []string `json:"regions"`
}

// AvailableIn says this plan can be ordered in the region.
func (p BoxPlan) AvailableIn(region string) bool {
	for _, r := range p.Regions {
		if r == region {
			return true
		}
	}
	return false
}

// BoxOrder is one machine to order.
type BoxOrder struct {
	Region string
	Plan   string
	// Label and Hostname are what the machine is called at the hoster and on
	// itself. Both are the box name, so an instance in the hoster's panel can
	// be matched to a row here by eye.
	Label    string
	Hostname string
	// SSHKeyID is an id from EnsureSSHKey; the machine boots with that key in
	// root's authorized_keys.
	SSHKeyID string
	// Tag marks the instance as ours, so the orphan check can tell a machine
	// the hub ordered from one the owner runs for something else.
	Tag string
}

// BoxInstance is a machine as the hoster sees it.
type BoxInstance struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Address is the public IPv4, empty while the machine is still being
	// built.
	Address string `json:"address"`
	Region  string `json:"region"`
	Plan    string `json:"plan"`
	// Status is the hoster's own word ("pending", "active"); shown, never
	// parsed — an address is what the hub actually waits for.
	Status       string `json:"status"`
	MonthlyCents int    `json:"monthlyCents"`
	// Tags are the instance's tags at the hoster, so the orphan check can see
	// whether the hub ordered it.
	Tags []string `json:"tags,omitempty"`
}

// HostingAccount is a hosting account's money as of one check.
type HostingAccount struct {
	// BalanceCents is the credit left, in whole cents. It can be negative on
	// a post-paid account, which is a figure to show and not an error.
	// Nil when the hoster does not report balance via API (e.g. Hetzner).
	BalanceCents *int
	// PendingCents is what has accrued this period and is not billed yet.
	PendingCents int
}
