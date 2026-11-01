// Temporary diagnostic, not committed: lists what the saved OpenStack account
// sees, printing raw bodies of failed calls. Orders nothing.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
	"github.com/m1d1x/hikkari-control/internal/openstack"
)

type logRT struct{}

func (logRT) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err == nil && resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body = io.NopCloser(bytes.NewReader(b))
		fmt.Printf("!! %s %s -> %d %s\n", r.Method, r.URL.Path, resp.StatusCode, string(b))
	}
	return resp, err
}

func main() {
	ctx := context.Background()
	if err := cryptobox.Configure(os.Getenv); err != nil {
		panic(err)
	}
	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		panic(err)
	}
	var ct []byte
	var plan string
	if err := conn.QueryRow(ctx, `SELECT creds_ct, coalesce(default_plan,'') FROM entry_providers WHERE kind='openstack'`).Scan(&ct, &plan); err != nil {
		panic(err)
	}
	raw, err := cryptobox.Decrypt(ct)
	if err != nil {
		panic(err)
	}
	var cr map[string]string
	_ = json.Unmarshal(raw, &cr)
	keys := []string{}
	for k := range cr {
		keys = append(keys, k)
	}
	fmt.Println("cred fields:", keys, "region:", cr["region"], "project:", cr["project"], "network_id:", cr["network_id"])
	c := openstack.New(openstack.Config{AuthURL: cr["auth_url"], Username: cr["username"], Password: cr["password"], Region: cr["region"], Project: cr["project"], NetworkID: cr["network_id"]},
		openstack.WithHTTPClient(&http.Client{Transport: logRT{}}))
	ps, err := c.Plans(ctx)
	fmt.Println("plans err:", err)
	for _, p := range ps {
		mark := ""
		if p.ID == plan {
			mark = "  <== default"
		}
		fmt.Printf("  plan %s vcpu=%d ram=%dMB disk=%dGB est=%d¢%s\n", p.ID, p.VCPUs, p.RAMMB, p.DiskGB, p.MonthlyCents, mark)
	}
	ns, err := c.Networks(ctx)
	fmt.Println("networks err:", err)
	for _, n := range ns {
		fmt.Printf("  net %s %q %s external=%v\n", n.ID, n.Name, n.Status, n.External)
	}
	img, err := c.UbuntuImage(ctx)
	fmt.Println("ubuntu image:", img, err)
	ks, err := c.SSHKeys(ctx)
	fmt.Println("ssh keys:", len(ks), err)
}
