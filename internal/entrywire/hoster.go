package entrywire

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/vultr"
)

// Hoster adapts a Vultr client to the hub's entry.BoxProvider (roadmap 42
// П9.2). As with the registrar, the entry package declares the interface and
// its types on the consumer side and this adapter is the one place that knows
// both — mapping the hoster's wire answers into the domain's types, and its
// "already gone" into the domain's own.
func Hoster(c *vultr.Client) entry.BoxProvider { return hoster{c: c} }

type hoster struct{ c *vultr.Client }

func (h hoster) Regions(ctx context.Context) ([]entry.BoxRegion, error) {
	rs, err := h.c.Regions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]entry.BoxRegion, 0, len(rs))
	for _, r := range rs {
		out = append(out, entry.BoxRegion{ID: r.ID, City: r.City, Country: r.Country, Continent: r.Continent})
	}
	return out, nil
}

func (h hoster) Plans(ctx context.Context) ([]entry.BoxPlan, error) {
	ps, err := h.c.Plans(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]entry.BoxPlan, 0, len(ps))
	for _, p := range ps {
		out = append(out, entry.BoxPlan{
			ID: p.ID, VCPUs: p.VCPUs, RAMMB: p.RAMMB, DiskGB: p.DiskGB,
			BandwidthGB: p.BandwidthGB, MonthlyCents: p.MonthlyCents,
			OverageCentsPerTB: p.OverageCentsPerTB, Regions: p.Regions,
		})
	}
	return out, nil
}

func (h hoster) EnsureSSHKey(ctx context.Context, name, publicKey string) (string, error) {
	return h.c.EnsureSSHKey(ctx, name, publicKey)
}

func (h hoster) Create(ctx context.Context, o entry.BoxOrder) (entry.BoxInstance, error) {
	var tags []string
	if o.Tag != "" {
		tags = []string{o.Tag}
	}
	inst, err := h.c.Create(ctx, vultr.CreateParams{
		Region: o.Region, Plan: o.Plan, Label: o.Label, Hostname: o.Hostname,
		SSHKeyID: o.SSHKeyID, Tags: tags,
	})
	if err != nil {
		return entry.BoxInstance{}, hosterRefusal(err)
	}
	return toBoxInstance(inst), nil
}

// hosterRefusal marks an order Vultr answered with a no — a 4xx about the
// order itself, which it gives before it creates anything — as
// entry.ErrProviderRefused. A 5xx, a reset or a timeout is left as it is: the
// machine may exist, and the hub keeps the order in its budget until it finds
// out (audit В3/С1). So are a 404 and a 429 (audit 42 recheck НН7): neither
// says the order was read — a route or a proxy, a limiter in front — and
// "4xx means nothing was made" is not a promise those two keep.
func hosterRefusal(err error) error {
	var api *vultr.APIError
	switch {
	case errors.Is(err, vultr.ErrNotFound), errors.Is(err, vultr.ErrRateLimited):
		return err
	case errors.Is(err, vultr.ErrOutOfCapacity):
		return fmt.Errorf("%w: %w: %w", entry.ErrProviderRefused, entry.ErrRegionCapacity, err)
	case errors.Is(err, vultr.ErrBadCredentials),
		errors.Is(err, vultr.ErrInsufficientFunds),
		errors.As(err, &api) && api.HTTPStatus >= 400 && api.HTTPStatus < 500 &&
			api.HTTPStatus != http.StatusNotFound && api.HTTPStatus != http.StatusTooManyRequests:
		return fmt.Errorf("%w: %w", entry.ErrProviderRefused, err)
	}
	return err
}

func (h hoster) Instance(ctx context.Context, id string) (entry.BoxInstance, error) {
	inst, err := h.c.Instance(ctx, id)
	if errors.Is(err, vultr.ErrNotFound) {
		return entry.BoxInstance{}, entry.ErrNoSuchInstance
	}
	if err != nil {
		return entry.BoxInstance{}, err
	}
	return toBoxInstance(inst), nil
}

func (h hoster) Instances(ctx context.Context) ([]entry.BoxInstance, error) {
	list, err := h.c.Instances(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]entry.BoxInstance, 0, len(list))
	for _, inst := range list {
		out = append(out, toBoxInstance(inst))
	}
	return out, nil
}

func (h hoster) Reboot(ctx context.Context, id string) error {
	if err := h.c.Reboot(ctx, id); errors.Is(err, vultr.ErrNotFound) {
		return entry.ErrNoSuchInstance
	} else if err != nil {
		return err
	}
	return nil
}

// Delete treats an instance that is already gone as deleted: the caller asked
// for an end state, and a machine nobody is being charged for is in it.
func (h hoster) Delete(ctx context.Context, id string) error {
	if err := h.c.Delete(ctx, id); err != nil && !errors.Is(err, vultr.ErrNotFound) {
		return err
	}
	return nil
}

func (h hoster) Account(ctx context.Context) (entry.HostingAccount, error) {
	acct, err := h.c.Account(ctx)
	if err != nil {
		return entry.HostingAccount{}, err
	}
	return entry.HostingAccount{BalanceCents: acct.BalanceCents, PendingCents: acct.PendingCents}, nil
}

// toBoxInstance maps one instance. The price is not here: Vultr's instance
// object carries no monthly cost, so the plan's price is what the order
// records, and inventing one from the instance would be a second figure to
// disagree with it.
func toBoxInstance(i vultr.Instance) entry.BoxInstance {
	return entry.BoxInstance{
		ID: i.ID, Label: i.Label, Address: i.Address, Region: i.Region,
		Plan: i.Plan, Status: i.Status, Tags: i.Tags,
	}
}
