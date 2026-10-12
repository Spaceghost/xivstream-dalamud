package sunshine

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Spaceghost/xivstream-dalamud/internal/plan"
)

func TestPairHTTP(t *testing.T) {
	for _, tool := range []string{"sh", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("transport integration requires %s", tool)
		}
	}
	const id = "0123456789abcdef0123456789abcdef"
	const single = `{"pairings":[{"id":"` + id + `","address":"100.83.231.2"}]}`
	const multiple = `{"pairings":[{"id":"` + id + `","address":"100.83.231.2"},{"id":"abcdef0123456789abcdef0123456789","address":"100.64.0.2"}]}`
	for _, tc := range []struct {
		name, pending, client, wantError, wantID   string
		getStatus, postStatus, wantGets, wantPosts int
		delayed                                    bool
	}{
		{name: "current API", pending: single, wantID: id, wantGets: 1, wantPosts: 1},
		{name: "legacy API", getStatus: 404, wantGets: 1, wantPosts: 1},
		{name: "ambiguous", pending: multiple, wantError: "several", wantGets: 1},
		{name: "address selection", pending: multiple, client: "100.83.231.2", wantID: id, wantGets: 1, wantPosts: 1},
		{name: "ID selection", pending: multiple, client: id, wantID: id, wantGets: 1, wantPosts: 1},
		{name: "invalid ID", pending: `{"pairings":[{"id":"bad"}]}`, wantError: "invalid pairing ID", wantGets: 1},
		{name: "unauthorized", getStatus: 401, wantError: "HTTP 401", wantGets: 1},
		{name: "invalid response", pending: `{}`, wantError: "invalid pending", wantGets: 1},
		{name: "no pending request", pending: `{"pairings":[]}`, wantError: "no matching", wantGets: 5},
		{name: "request arrives late", pending: single, delayed: true, wantID: id, wantGets: 2, wantPosts: 1},
		{name: "PIN failure", pending: single, postStatus: 400, wantError: "HTTP 400", wantID: id, wantGets: 1, wantPosts: 1},
		{name: "legacy selection rejected", getStatus: 405, client: "100.83.231.2", wantError: "cannot select", wantGets: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			gets, posts := 0, 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				user, password, ok := r.BasicAuth()
				if !ok || user != "tester" || password != "local-test-password" {
					t.Error("missing authentication")
				}
				if r.URL.Path != "/api/pin" {
					t.Error("wrong API path")
				}
				if r.Method == "GET" {
					gets++
					if tc.getStatus != 0 {
						w.WriteHeader(tc.getStatus)
						return
					}
					if tc.delayed && gets == 1 {
						w.Write([]byte(`{"pairings":[]}`))
						return
					}
					w.Write([]byte(tc.pending))
					return
				}
				posts++
				if r.Method != "POST" {
					t.Error("wrong submission method")
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["pin"] != "9966" || body["name"] != "test client" || body["pairing_id"] != tc.wantID {
					t.Errorf("unexpected payload: %v", body)
				}
				if tc.postStatus != 0 {
					w.WriteHeader(tc.postStatus)
					return
				}
				w.Write([]byte(`{"status":true}`))
			}))
			defer server.Close()
			_, rawPort, _ := net.SplitHostPort(server.Listener.Addr().String())
			port, _ := strconv.Atoi(rawPort)
			err := pair(plan.Local{}, port, "tester", "local-test-password", "9966", "test client", tc.client, func(time.Duration) {})
			if tc.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("expected %q, got %v", tc.wantError, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if gets != tc.wantGets || posts != tc.wantPosts {
				t.Fatalf("GET/POST count: %d/%d, want %d/%d", gets, posts, tc.wantGets, tc.wantPosts)
			}
		})
	}
}
