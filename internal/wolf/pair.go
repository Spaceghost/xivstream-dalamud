package wolf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

// API talks to Wolf's REST API on its UNIX socket.
type API struct {
	Socket string
	client *http.Client
}

func NewAPI(socket string) *API {
	if socket == "" {
		socket = SocketPath
	}
	return &API{Socket: socket, client: &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}},
	}}
}

func (a *API) call(method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, "http://wolf"+path, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("Wolf's API at %s: %w (is Wolf running?)", a.Socket, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var status struct {
		Success *bool  `json:"success"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(data, &status)
	if resp.StatusCode >= 300 || (status.Success != nil && !*status.Success) {
		msg := status.Error
		if msg == "" {
			msg = strings.TrimSpace(string(data))
		}
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, msg)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// PendingPair is a Moonlight client waiting for its PIN.
type PendingPair struct {
	Secret   string `json:"pair_secret"`
	ClientIP string `json:"client_ip"`
}

// Client is a paired client.
type Client struct {
	ID       string `json:"client_id"`
	Settings struct {
		ControllersOverride []string `json:"controllers_override"`
	} `json:"settings"`
}

func (a *API) Pending() ([]PendingPair, error) {
	var r struct {
		Requests []PendingPair `json:"requests"`
	}
	err := a.call("GET", "/api/v1/pair/pending", nil, &r)
	return r.Requests, err
}

func (a *API) Clients() ([]Client, error) {
	var r struct {
		Clients []Client `json:"clients"`
	}
	err := a.call("GET", "/api/v1/clients", nil, &r)
	return r.Clients, err
}

func (a *API) Pair(secret, pin string) error {
	return a.call("POST", "/api/v1/pair/client", map[string]string{"pair_secret": secret, "pin": pin}, nil)
}

func (a *API) SetControllers(clientID string, types []string) error {
	return a.call("POST", "/api/v1/clients/settings", map[string]any{
		"client_id": clientID,
		"settings":  map[string]any{"controllers_override": types},
	}, nil)
}

// PairOptions for PairClient.
type PairOptions struct {
	PIN      string
	ClientIP string   // which pending client, when several wait
	Pads     []string // controllers_override for the new client (nil: leave)
	Wait     time.Duration
	Log      func(string)
}

// PairClient answers a pending Moonlight pairing with its PIN, then gives the
// new client the configured pad type.
func PairClient(a *API, o PairOptions) error {
	pending, err := a.Pending()
	if err != nil {
		return err
	}
	var chosen *PendingPair
	var ips []string
	for _, p := range pending {
		ips = append(ips, p.ClientIP)
	}
	for i, p := range pending {
		if o.ClientIP == "" || p.ClientIP == o.ClientIP {
			if chosen != nil {
				return fmt.Errorf("several clients are waiting to pair (%s): choose one with --client", strings.Join(ips, ", "))
			}
			chosen = &pending[i]
		}
	}
	if chosen == nil {
		if len(pending) == 0 {
			return fmt.Errorf("no Moonlight client is waiting to pair: add this host in Moonlight first, then run this with the PIN it shows")
		}
		return fmt.Errorf("no client from %s is waiting to pair (waiting: %s)", o.ClientIP, strings.Join(ips, ", "))
	}
	before, err := a.Clients()
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, c := range before {
		known[c.ID] = true
	}
	if err := a.Pair(chosen.Secret, o.PIN); err != nil {
		return err
	}
	o.Log("PIN accepted for " + chosen.ClientIP + "; waiting for Moonlight to finish pairing")
	if len(o.Pads) == 0 {
		return nil
	}
	deadline := time.Now().Add(o.Wait)
	for {
		after, err := a.Clients()
		if err != nil {
			return err
		}
		var fresh []string
		for _, c := range after {
			if !known[c.ID] {
				fresh = append(fresh, c.ID)
			}
		}
		if len(fresh) == 0 && time.Now().After(deadline) {
			// Re-pairing a known client keeps its id: give the pads to every
			// client that has no override yet.
			for _, c := range after {
				if len(c.Settings.ControllersOverride) == 0 {
					fresh = append(fresh, c.ID)
				}
			}
			if len(fresh) == 0 {
				o.Log("no new client appeared; pad types unchanged")
				return nil
			}
		}
		if len(fresh) > 0 {
			sort.Strings(fresh)
			for _, id := range fresh {
				if err := a.SetControllers(id, o.Pads); err != nil {
					return err
				}
				o.Log(fmt.Sprintf("client %s: pads presented as %s", id, strings.Join(o.Pads, ", ")))
			}
			return nil
		}
		time.Sleep(time.Second)
	}
}
