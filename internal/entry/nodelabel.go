package entry

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// nodeCities turns what a box row knows of its place into the city a subscriber
// reads. Keys are a hoster's region id or a catalog city, lowercased. HostVDS's
// catalog names only the region, so eu-north2 stands here for the city it is in.
var nodeCities = map[string]string{
	"eu-north2":        "рига",
	"riga":             "рига",
	"amsterdam":        "амстердам",
	"ashburn":          "эшберн",
	"atlanta":          "атланта",
	"berlin":           "берлин",
	"bucharest":        "бухарест",
	"chicago":          "чикаго",
	"dallas":           "даллас",
	"dubai":            "дубай",
	"falkenstein":      "фалькенштайн",
	"frankfurt":        "франкфурт",
	"helsinki":         "хельсинки",
	"hillsboro":        "хиллсборо",
	"istanbul":         "стамбул",
	"london":           "лондон",
	"los angeles":      "лос-анджелес",
	"madrid":           "мадрид",
	"manchester":       "манчестер",
	"miami":            "майами",
	"milan":            "милан",
	"moscow":           "москва",
	"new york":         "нью-йорк",
	"nuremberg":        "нюрнберг",
	"paris":            "париж",
	"saint petersburg": "петербург",
	"singapore":        "сингапур",
	"stockholm":        "стокгольм",
	"tallinn":          "таллин",
	"tokyo":            "токио",
	"vienna":           "вена",
	"vilnius":          "вильнюс",
	"warsaw":           "варшава",
	"yerevan":          "ереван",
}

// nodeCity is the city part of a node's label. A place missing from nodeCities
// still gets a name of its own rather than none, in the hoster's spelling.
func nodeCity(providerRegion, location string) string {
	place := strings.ToLower(strings.TrimSpace(strings.SplitN(location, ",", 2)[0]))
	region := strings.ToLower(strings.TrimSpace(providerRegion))
	for _, key := range []string{region, place} {
		if city, ok := nodeCities[key]; ok {
			return city
		}
	}
	if place != "" {
		return strings.ReplaceAll(place, " ", "-")
	}
	if region != "" {
		return region
	}
	return "узел"
}

// nodeLabel is the first free name in city: numbers stay small however many
// machines the fleet has churned, because a slot outlives its machines.
func nodeLabel(city string, taken map[string]bool) string {
	for i := 1; ; i++ {
		label := fmt.Sprintf("%s-%03d", city, i)
		if !taken[label] {
			return label
		}
	}
}

// labelFleetNodes names every serving node that has no label, and renames one
// whose slot was rebound to a machine in another city: the name says where the
// node is. Runs under the node registry's lock (syncFleetNodes).
func labelFleetNodes(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT n.slug, coalesce(n.label, ''), b.provider_region, b.location
 FROM entry_nodes n JOIN entry_relays r ON r.id=n.relay_id JOIN entry_boxes b ON b.name=r.box
 WHERE r.status='alive' AND b.retired_at IS NULL ORDER BY n.bound_at, n.slug`)
	if err != nil {
		return err
	}
	type stale struct{ slug, city string }
	var relabel []stale
	for rows.Next() {
		var slug, label, region, location string
		if err := rows.Scan(&slug, &label, &region, &location); err != nil {
			rows.Close()
			return err
		}
		city := nodeCity(region, location)
		if !strings.HasPrefix(label, city+"-") {
			relabel = append(relabel, stale{slug, city})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(relabel) == 0 {
		return err
	}
	rows, err = tx.Query(ctx, `SELECT label FROM entry_nodes WHERE label IS NOT NULL`)
	if err != nil {
		return err
	}
	taken := map[string]bool{}
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			rows.Close()
			return err
		}
		taken[label] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, n := range relabel {
		label := nodeLabel(n.city, taken)
		if _, err := tx.Exec(ctx, `UPDATE entry_nodes SET label=$2 WHERE slug=$1`, n.slug, label); err != nil {
			return err
		}
		taken[label] = true
	}
	return nil
}
