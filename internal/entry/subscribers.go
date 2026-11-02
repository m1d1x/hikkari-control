package entry

// subscribers.go sizes the stream pools by the subscriptions sold
// (hikkari-client plans/2026-10-31-subscriptions.md, «Ёмкость»), beside the
// traffic they carry (sizing.go).
//
// Traffic says how much the pool moves; it cannot say how many people share
// one address. A machine is one IP, and an IP that a hundred and fifty
// subscriptions dial is a list a censor reads off one handset — so past
// SubscribersPerBox the machine takes nobody new (pick), and the pool holds
// enough machines for every live subscription's batch to find room:
//
//	machines ≥ ⌈ live subscriptions × batch / SubscribersPerBox ⌉, at least the batch
//
// The batch's relays sit on distinct machines (pick), so a subscription needs
// at least `batch` machines in each lane before it holds a full batch; a
// Hysteria2 lane is one relay per region (VoiceSlotsPerRegion), so its floor
// counts one share per subscription and starts at one machine. With no
// live subscription the floor is nothing and the traffic sizing alone holds
// the pool (one machine for the first comer). The owner's one knob stays the
// money cap: past it the hub asks instead of buying (fleetbudget.go).

import (
	"context"
	"fmt"
)

// SubscribersPerBox is how many subscriptions one machine is shared by at
// most: the owner's estimate of 31.10 for how many a single IP carries before
// it is worth a censor's while. A constant, like the headroom.
const SubscribersPerBox = 150

// SubscriberCount says how many subscriptions are live now. Declared here, on
// the consumer side; internal/subscription implements it.
type SubscriberCount interface {
	CountLive(ctx context.Context) (int64, error)
}

// SetSubscribers wires the count. Unset, the pools are sized by traffic alone.
func (s *Service) SetSubscribers(c SubscriberCount) { s.subscribers = c }

// subscriberFloor is the machines a lane of `slots` relays per subscription
// needs for `live` subscriptions.
func subscriberFloor(live int64, slots int) int {
	if live <= 0 {
		return 0
	}
	n := int((live*int64(slots) + SubscribersPerBox - 1) / SubscribersPerBox)
	return max(n, slots)
}

// applySubscriberFloor raises a pool's target and keep to what its
// subscriptions need, and says so. Hysteria2 is sold in the subscription as
// well (owner, 01.11): its lanes are floored by subscriptions too, one relay
// each.
func (s *Service) applySubscriberFloor(ctx context.Context, f *FleetForecast, proto string) error {
	if s.subscribers == nil {
		return nil
	}
	live, err := s.subscribers.CountLive(ctx)
	if err != nil {
		return fmt.Errorf("entry: live subscriptions: %w", err)
	}
	slots := s.protoSlots(proto)
	floor := subscriberFloor(live, slots)
	if floor == 0 {
		return nil
	}
	f.Keep = max(f.Keep, floor)
	if floor > f.Target {
		f.Target = floor
		f.TargetWhy += fmt.Sprintf("; живых подписок %d по %d ноды, на машину не больше %d — нужно %d",
			live, slots, SubscribersPerBox, floor)
	}
	return nil
}

// StaticVoiceRegions is a VoiceRegionSource with a fixed list. Hikkari has no
// SFU shards to name voice regions after; its Hysteria2 lanes are the regions
// the hub is told to keep, so a lane exists, and is ordered for, before its
// first machine (fleet.go, FleetForecasts).
type StaticVoiceRegions []string

func (r StaticVoiceRegions) VoiceRegions(context.Context) ([]string, error) { return r, nil }
