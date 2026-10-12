// Package sunshine talks to Sunshine's web API: pairing a Moonlight client by
// its PIN without opening the web UI.
package sunshine

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Spaceghost/xivstream-dalamud/internal/plan"
)

// Pair submits a Moonlight PIN. Sunshine's web UI listens on base+1 on the
// machine it runs on; t is that machine (a container, or this one).
func Pair(t plan.Target, port int, user, password, pin, name, client string) error {
	return pair(t, port, user, password, pin, name, client, time.Sleep)
}

func pair(t plan.Target, port int, user, password, pin, name, client string, wait func(time.Duration)) error {
	if len(pin) != 4 || strings.Trim(pin, "0123456789") != "" {
		return fmt.Errorf("a Moonlight PIN is four digits")
	}
	if len(name) == 0 || len(name) > 128 {
		return fmt.Errorf("a client name must contain between 1 and 128 bytes")
	}
	url := fmt.Sprintf("https://127.0.0.1:%d/api/pin", port)
	for i := 0; i < 5; i++ {
		status, out, err := request(t, user, password, url, "GET", "")
		if err != nil {
			return err
		}
		body := map[string]string{"pin": pin, "name": name}
		switch status {
		case 404, 405: // Older Sunshine versions accept a PIN without an ID.
			if client != "" {
				return fmt.Errorf("this Sunshine version cannot select a pairing client")
			}
		case 200:
			var pending struct {
				Pairings *[]struct {
					ID      string `json:"id"`
					Address string `json:"address"`
				} `json:"pairings"`
			}
			if json.Unmarshal([]byte(out), &pending) != nil || pending.Pairings == nil {
				return fmt.Errorf("Sunshine returned an invalid pending-pairing response")
			}
			var ids []string
			for _, p := range *pending.Pairings {
				if client == "" || client == p.Address || client == p.ID {
					ids = append(ids, p.ID)
				}
			}
			if len(ids) == 0 {
				if i < 4 {
					wait(3 * time.Second)
				}
				continue
			}
			if len(ids) != 1 {
				return fmt.Errorf("several Sunshine clients are waiting; select one with --client IP or pairing ID")
			}
			if _, err := hex.DecodeString(ids[0]); err != nil || len(ids[0]) != 32 {
				return fmt.Errorf("Sunshine returned an invalid pairing ID")
			}
			body["pairing_id"] = ids[0]
		default:
			return fmt.Errorf("Sunshine pending-pairing request failed (HTTP %d)", status)
		}
		data, _ := json.Marshal(body)
		status, out, err = request(t, user, password, url, "POST", string(data))
		if err != nil {
			return err
		}
		if status != 200 {
			return fmt.Errorf("Sunshine PIN submission failed (HTTP %d)", status)
		}
		var reply struct {
			Status bool `json:"status"`
		}
		if json.Unmarshal([]byte(out), &reply) != nil {
			return fmt.Errorf("Sunshine returned an invalid PIN response")
		}
		if reply.Status {
			return nil
		}
		return fmt.Errorf("Sunshine did not accept the PIN; start pairing in Moonlight again")
	}
	return fmt.Errorf("no matching Sunshine pairing request; start pairing in Moonlight first")
}

func request(t plan.Target, user, password, url, method, body string) (int, string, error) {
	out, err := t.RunInput([]byte(password), "sh", "-c",
		`curl -sSk -m 10 -u "$1:$(cat)" -H content-type:application/json -X "$2" --data "$3" -w '\nXIVSTREAM_HTTP_STATUS:%{http_code}' "$4"`, "-", user, method, body, url)
	if err != nil {
		return 0, "", fmt.Errorf("Sunshine API transport failed")
	}
	const marker = "XIVSTREAM_HTTP_STATUS:"
	sep := strings.LastIndex(out, marker)
	if sep < 0 {
		return 0, "", fmt.Errorf("Sunshine API returned no HTTP status")
	}
	status, err := strconv.Atoi(strings.TrimSpace(out[sep+len(marker):]))
	if err != nil {
		return 0, "", fmt.Errorf("Sunshine API returned an invalid HTTP status")
	}
	return status, out[:sep], nil
}
