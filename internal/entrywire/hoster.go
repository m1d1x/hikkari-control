package entrywire

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/m1d1x/hikkari-control/internal/digitalocean"
	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/hetzner"
	"github.com/m1d1x/hikkari-control/internal/openstack"
	"github.com/m1d1x/hikkari-control/internal/vdsina"
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

// hosterRefusal marks an order the hoster answered with a no — a 4xx about the
// order itself, which it gives before it creates anything — as
// entry.ErrProviderRefused. A 5xx, a reset or a timeout is left as it is: the
// machine may exist, and the hub keeps the order in its budget until it finds
// out (audit В3/С1). So are a 404 and a 429 (audit 42 recheck НН7): neither
// says the order was read — a route or a proxy, a limiter in front — and
// "4xx means nothing was made" is not a promise those two keep.
func hosterRefusal(err error) error {
	switch {
	case errors.Is(err, vultr.ErrNotFound), errors.Is(err, hetzner.ErrNotFound), errors.Is(err, digitalocean.ErrNotFound), errors.Is(err, vdsina.ErrNotFound), errors.Is(err, openstack.ErrNotFound),
		errors.Is(err, vultr.ErrRateLimited), errors.Is(err, hetzner.ErrRateLimited), errors.Is(err, digitalocean.ErrRateLimited), errors.Is(err, vdsina.ErrRateLimited), errors.Is(err, openstack.ErrRateLimited):
		return err
	case errors.Is(err, vultr.ErrOutOfCapacity), errors.Is(err, hetzner.ErrOutOfCapacity), errors.Is(err, digitalocean.ErrOutOfCapacity), errors.Is(err, vdsina.ErrOutOfCapacity), errors.Is(err, openstack.ErrOutOfCapacity):
		return fmt.Errorf("%w: %w: %w", entry.ErrProviderRefused, entry.ErrRegionCapacity, err)
	case errors.Is(err, vultr.ErrBadCredentials), errors.Is(err, hetzner.ErrBadCredentials), errors.Is(err, digitalocean.ErrBadCredentials), errors.Is(err, vdsina.ErrBadCredentials), errors.Is(err, openstack.ErrBadCredentials),
		errors.Is(err, vultr.ErrInsufficientFunds), errors.Is(err, hetzner.ErrInsufficientFunds), errors.Is(err, digitalocean.ErrInsufficientFunds), errors.Is(err, vdsina.ErrInsufficientFunds), errors.Is(err, openstack.ErrInsufficientFunds),
		is4xxStatus(err):
		return fmt.Errorf("%w: %w", entry.ErrProviderRefused, err)
	}
	return err
}

func is4xxStatus(err error) bool {
	var status int
	var vAPI *vultr.APIError
	var hAPI *hetzner.APIError
	var dAPI *digitalocean.APIError
	var vdAPI *vdsina.APIError
	var osAPI *openstack.APIError
	switch {
	case errors.As(err, &vAPI):
		status = vAPI.HTTPStatus
	case errors.As(err, &hAPI):
		status = hAPI.HTTPStatus
	case errors.As(err, &dAPI):
		status = dAPI.HTTPStatus
	case errors.As(err, &vdAPI):
		status = vdAPI.HTTPStatus
	case errors.As(err, &osAPI):
		status = osAPI.HTTPStatus
	default:
		return false
	}
	return status >= 400 && status < 500 && status != http.StatusNotFound && status != http.StatusTooManyRequests
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
	bal := acct.BalanceCents
	return entry.HostingAccount{BalanceCents: &bal, PendingCents: acct.PendingCents}, nil
}

func toBoxInstance(i vultr.Instance) entry.BoxInstance {
	return entry.BoxInstance{
		ID: i.ID, Label: i.Label, Address: i.Address, Region: i.Region,
		Plan: i.Plan, Status: i.Status, Tags: i.Tags,
	}
}

// ── Hetzner Cloud Adapter ───────────────────────────────────────────────

// HetznerHoster adapts a Hetzner Cloud client to entry.BoxProvider.
func HetznerHoster(c *hetzner.Client) entry.BoxProvider { return hetznerHoster{c: c} }

type hetznerHoster struct{ c *hetzner.Client }

func (h hetznerHoster) Regions(ctx context.Context) ([]entry.BoxRegion, error) {
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

func (h hetznerHoster) Plans(ctx context.Context) ([]entry.BoxPlan, error) {
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

func (h hetznerHoster) EnsureSSHKey(ctx context.Context, name, publicKey string) (string, error) {
	return h.c.EnsureSSHKey(ctx, name, publicKey)
}

func (h hetznerHoster) Create(ctx context.Context, o entry.BoxOrder) (entry.BoxInstance, error) {
	var tags []string
	if o.Tag != "" {
		tags = []string{o.Tag}
	}
	inst, err := h.c.Create(ctx, hetzner.CreateParams{
		Region: o.Region, Plan: o.Plan, Label: o.Label, Hostname: o.Hostname,
		SSHKeyID: o.SSHKeyID, Tags: tags,
	})
	if err != nil {
		return entry.BoxInstance{}, hosterRefusal(err)
	}
	return toBoxInstanceHetzner(inst), nil
}

func (h hetznerHoster) Instance(ctx context.Context, id string) (entry.BoxInstance, error) {
	inst, err := h.c.Instance(ctx, id)
	if errors.Is(err, hetzner.ErrNotFound) {
		return entry.BoxInstance{}, entry.ErrNoSuchInstance
	}
	if err != nil {
		return entry.BoxInstance{}, err
	}
	return toBoxInstanceHetzner(inst), nil
}

func (h hetznerHoster) Instances(ctx context.Context) ([]entry.BoxInstance, error) {
	list, err := h.c.Instances(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]entry.BoxInstance, 0, len(list))
	for _, inst := range list {
		out = append(out, toBoxInstanceHetzner(inst))
	}
	return out, nil
}

func (h hetznerHoster) Reboot(ctx context.Context, id string) error {
	if err := h.c.Reboot(ctx, id); errors.Is(err, hetzner.ErrNotFound) {
		return entry.ErrNoSuchInstance
	} else if err != nil {
		return err
	}
	return nil
}

func (h hetznerHoster) Delete(ctx context.Context, id string) error {
	if err := h.c.Delete(ctx, id); err != nil && !errors.Is(err, hetzner.ErrNotFound) {
		return err
	}
	return nil
}

func (h hetznerHoster) Account(ctx context.Context) (entry.HostingAccount, error) {
	acct, err := h.c.Account(ctx)
	if err != nil {
		return entry.HostingAccount{}, err
	}
	return entry.HostingAccount{BalanceCents: acct.BalanceCents, PendingCents: acct.PendingCents}, nil
}

func toBoxInstanceHetzner(i hetzner.Instance) entry.BoxInstance {
	return entry.BoxInstance{
		ID: i.ID, Label: i.Label, Address: i.Address, Region: i.Region,
		Plan: i.Plan, Status: i.Status, Tags: i.Tags,
	}
}

// ── DigitalOcean Adapter ────────────────────────────────────────────────

// DigitalOceanHoster adapts a DigitalOcean client to entry.BoxProvider.
func DigitalOceanHoster(c *digitalocean.Client) entry.BoxProvider { return doHoster{c: c} }

type doHoster struct{ c *digitalocean.Client }

func (h doHoster) Regions(ctx context.Context) ([]entry.BoxRegion, error) {
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

func (h doHoster) Plans(ctx context.Context) ([]entry.BoxPlan, error) {
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

func (h doHoster) EnsureSSHKey(ctx context.Context, name, publicKey string) (string, error) {
	return h.c.EnsureSSHKey(ctx, name, publicKey)
}

func (h doHoster) Create(ctx context.Context, o entry.BoxOrder) (entry.BoxInstance, error) {
	var tags []string
	if o.Tag != "" {
		tags = []string{o.Tag}
	}
	inst, err := h.c.Create(ctx, digitalocean.CreateParams{
		Region: o.Region, Plan: o.Plan, Label: o.Label, Hostname: o.Hostname,
		SSHKeyID: o.SSHKeyID, Tags: tags,
	})
	if err != nil {
		return entry.BoxInstance{}, hosterRefusal(err)
	}
	return toBoxInstanceDO(inst), nil
}

func (h doHoster) Instance(ctx context.Context, id string) (entry.BoxInstance, error) {
	inst, err := h.c.Instance(ctx, id)
	if errors.Is(err, digitalocean.ErrNotFound) {
		return entry.BoxInstance{}, entry.ErrNoSuchInstance
	}
	if err != nil {
		return entry.BoxInstance{}, err
	}
	return toBoxInstanceDO(inst), nil
}

func (h doHoster) Instances(ctx context.Context) ([]entry.BoxInstance, error) {
	list, err := h.c.Instances(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]entry.BoxInstance, 0, len(list))
	for _, inst := range list {
		out = append(out, toBoxInstanceDO(inst))
	}
	return out, nil
}

func (h doHoster) Reboot(ctx context.Context, id string) error {
	if err := h.c.Reboot(ctx, id); errors.Is(err, digitalocean.ErrNotFound) {
		return entry.ErrNoSuchInstance
	} else if err != nil {
		return err
	}
	return nil
}

func (h doHoster) Delete(ctx context.Context, id string) error {
	if err := h.c.Delete(ctx, id); err != nil && !errors.Is(err, digitalocean.ErrNotFound) {
		return err
	}
	return nil
}

func (h doHoster) Account(ctx context.Context) (entry.HostingAccount, error) {
	acct, err := h.c.Account(ctx)
	if err != nil {
		return entry.HostingAccount{}, err
	}
	bal := acct.BalanceCents
	return entry.HostingAccount{BalanceCents: &bal, PendingCents: acct.PendingCents}, nil
}

func toBoxInstanceDO(i digitalocean.Instance) entry.BoxInstance {
	return entry.BoxInstance{
		ID: i.ID, Label: i.Label, Address: i.Address, Region: i.Region,
		Plan: i.Plan, Status: i.Status, Tags: i.Tags,
	}
}

// ── VDSina Adapter ──────────────────────────────────────────────────────

// VDSinaHoster adapts a VDSina client to entry.BoxProvider.
func VDSinaHoster(c *vdsina.Client) entry.BoxProvider { return vdsinaHoster{c: c} }

type vdsinaHoster struct{ c *vdsina.Client }

func (h vdsinaHoster) Regions(ctx context.Context) ([]entry.BoxRegion, error) {
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

func (h vdsinaHoster) Plans(ctx context.Context) ([]entry.BoxPlan, error) {
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

func (h vdsinaHoster) EnsureSSHKey(ctx context.Context, name, publicKey string) (string, error) {
	return h.c.EnsureSSHKey(ctx, name, publicKey)
}

func (h vdsinaHoster) Create(ctx context.Context, o entry.BoxOrder) (entry.BoxInstance, error) {
	var tags []string
	if o.Tag != "" {
		tags = []string{o.Tag}
	}
	inst, err := h.c.Create(ctx, vdsina.CreateParams{
		Region: o.Region, Plan: o.Plan, Label: o.Label, Hostname: o.Hostname,
		SSHKeyID: o.SSHKeyID, Tags: tags,
	})
	if err != nil {
		return entry.BoxInstance{}, hosterRefusal(err)
	}
	return toBoxInstanceVDSina(inst), nil
}

func (h vdsinaHoster) Instance(ctx context.Context, id string) (entry.BoxInstance, error) {
	inst, err := h.c.Instance(ctx, id)
	if errors.Is(err, vdsina.ErrNotFound) {
		return entry.BoxInstance{}, entry.ErrNoSuchInstance
	}
	if err != nil {
		return entry.BoxInstance{}, err
	}
	return toBoxInstanceVDSina(inst), nil
}

func (h vdsinaHoster) Instances(ctx context.Context) ([]entry.BoxInstance, error) {
	list, err := h.c.Instances(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]entry.BoxInstance, 0, len(list))
	for _, inst := range list {
		out = append(out, toBoxInstanceVDSina(inst))
	}
	return out, nil
}

func (h vdsinaHoster) Reboot(ctx context.Context, id string) error {
	if err := h.c.Reboot(ctx, id); errors.Is(err, vdsina.ErrNotFound) {
		return entry.ErrNoSuchInstance
	} else if err != nil {
		return err
	}
	return nil
}

func (h vdsinaHoster) Delete(ctx context.Context, id string) error {
	if err := h.c.Delete(ctx, id); err != nil && !errors.Is(err, vdsina.ErrNotFound) {
		return err
	}
	return nil
}

func (h vdsinaHoster) Account(ctx context.Context) (entry.HostingAccount, error) {
	acct, err := h.c.Account(ctx)
	if err != nil {
		return entry.HostingAccount{}, err
	}
	bal := acct.BalanceCents
	return entry.HostingAccount{BalanceCents: &bal, PendingCents: acct.PendingCents}, nil
}

func toBoxInstanceVDSina(i vdsina.Instance) entry.BoxInstance {
	return entry.BoxInstance{
		ID: i.ID, Label: i.Label, Address: i.Address, Region: i.Region,
		Plan: i.Plan, Status: i.Status, Tags: i.Tags,
	}
}

// ── OpenStack Adapter ───────────────────────────────────────────────────

// OpenStackHoster adapts an OpenStack client to entry.BoxProvider.
func OpenStackHoster(c *openstack.Client) entry.BoxProvider { return osHoster{c: c} }

type osHoster struct{ c *openstack.Client }

func (h osHoster) Regions(ctx context.Context) ([]entry.BoxRegion, error) {
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

func (h osHoster) Plans(ctx context.Context) ([]entry.BoxPlan, error) {
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

func (h osHoster) EnsureSSHKey(ctx context.Context, name, publicKey string) (string, error) {
	return h.c.EnsureSSHKey(ctx, name, publicKey)
}

func (h osHoster) Create(ctx context.Context, o entry.BoxOrder) (entry.BoxInstance, error) {
	var tags []string
	if o.Tag != "" {
		tags = []string{o.Tag}
	}
	inst, err := h.c.Create(ctx, openstack.CreateParams{
		Region: o.Region, Plan: o.Plan, Label: o.Label, Hostname: o.Hostname,
		SSHKeyID: o.SSHKeyID, Tags: tags,
	})
	if err != nil {
		return entry.BoxInstance{}, hosterRefusal(err)
	}
	return toBoxInstanceOS(inst), nil
}

func (h osHoster) Instance(ctx context.Context, id string) (entry.BoxInstance, error) {
	inst, err := h.c.Instance(ctx, id)
	if errors.Is(err, openstack.ErrNotFound) {
		return entry.BoxInstance{}, entry.ErrNoSuchInstance
	}
	if err != nil {
		return entry.BoxInstance{}, err
	}
	return toBoxInstanceOS(inst), nil
}

func (h osHoster) Instances(ctx context.Context) ([]entry.BoxInstance, error) {
	list, err := h.c.Instances(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]entry.BoxInstance, 0, len(list))
	for _, inst := range list {
		out = append(out, toBoxInstanceOS(inst))
	}
	return out, nil
}

func (h osHoster) Reboot(ctx context.Context, id string) error {
	if err := h.c.Reboot(ctx, id); errors.Is(err, openstack.ErrNotFound) {
		return entry.ErrNoSuchInstance
	} else if err != nil {
		return err
	}
	return nil
}

func (h osHoster) Delete(ctx context.Context, id string) error {
	if err := h.c.Delete(ctx, id); err != nil && !errors.Is(err, openstack.ErrNotFound) {
		return err
	}
	return nil
}

func (h osHoster) Account(ctx context.Context) (entry.HostingAccount, error) {
	acct, err := h.c.Account(ctx)
	if err != nil {
		return entry.HostingAccount{}, err
	}
	return entry.HostingAccount{BalanceCents: acct.BalanceCents, PendingCents: acct.PendingCents}, nil
}

func toBoxInstanceOS(i openstack.Instance) entry.BoxInstance {
	return entry.BoxInstance{
		ID: i.ID, Label: i.Label, Address: i.Address, Region: i.Region,
		Plan: i.Plan, Status: i.Status, Tags: i.Tags,
	}
}

