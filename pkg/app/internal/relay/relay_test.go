package relay

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/infrapi/lib/pkg/cluster"
	"github.com/infrapi/lib/pkg/config"
	"github.com/prometheus/client_golang/prometheus"
)

const role = "writer"

// replica is one instance reduced to what the election decides: who answers.
type replica struct {
	id  string
	cl  *cluster.Cluster
	srv *httptest.Server
}

func startReplica(t *testing.T, id string, start bool) *replica {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	defaults := config.NewDefaultConfig()
	cfg, err := (&config.Variables{Path: "test", DefaultConfig: defaults, Values: map[string]string{
		"INFRAPI_CLUSTER_ENABLED": "true", "INFRAPI_CLUSTER_KV_STORE": "inmemory",
		"INFRAPI_CLUSTER_INSTANCE_ID": id, "INFRAPI_CLUSTER_INSTANCE_ADDR": "127.0.0.1",
		"INFRAPI_CLUSTER_INSTANCE_PORT": port, "INFRAPI_CLUSTER_ROLES": `["` + role + `"]`,
		"INFRAPI_CLUSTER_KEEP_INSTANCE_IN_RING_ON_SHUTDOWN": "false",
		"INFRAPI_CLUSTER_HEARTBEAT_PERIOD":                  "200ms", "INFRAPI_CLUSTER_HEARTBEAT_TIMEOUT": "2s",
	}}).GetClusterConfig()
	if err != nil {
		t.Fatal(err)
	}
	cl, err := cluster.New(&config.AppConfig{AppName: "test", AppFqdn: "test.local"}, cfg, cluster.Options{
		Roles:      []cluster.RoleSpec{{Name: role, Mode: cluster.ModeLeader}},
		Registerer: prometheus.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Any("/orders/:id", Forward(Cluster{C: cl, Role: role}, "http"), func(c *gin.Context) {
		c.JSON(http.StatusOK, map[string]string{"served_by": id, "forwarded_by": c.GetHeader(ForwardedHeader)})
	})
	srv := httptest.NewUnstartedServer(e)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	if start {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := cl.Start(ctx); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if err := cl.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return &replica{id: id, cl: cl, srv: srv}
}

func call(t *testing.T, method string, r *replica) (int, map[string]string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, r.srv.URL+"/orders/1", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil && resp.StatusCode == http.StatusOK {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body, resp.Header
}

// Every replica's writes are performed by one of them; reads stay where they
// arrive; when the leader leaves, the other takes over.
func TestOneReplicaWrites(t *testing.T) {
	a := startReplica(t, "replica-a", true)
	b := startReplica(t, "replica-b", true)

	var lead, follow *replica
	deadline := time.Now().Add(5 * time.Second)
	for lead == nil && time.Now().Before(deadline) {
		ia, _, _, errA := Cluster{C: a.cl, Role: role}.Leader()
		ib, _, _, errB := Cluster{C: b.cl, Role: role}.Leader()
		if errA == nil && errB == nil && ia == ib {
			lead, follow = a, b
			if ia == b.id {
				lead, follow = b, a
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lead == nil {
		t.Fatal("no leader elected")
	}

	for _, r := range []*replica{lead, follow} {
		code, body, hdr := call(t, http.MethodPost, r)
		if code != http.StatusOK || body["served_by"] != lead.id || hdr.Get(LeaderHeader) != lead.id {
			t.Errorf("POST to %s: %d %v, leader header %q; want %s", r.id, code, body, hdr.Get(LeaderHeader), lead.id)
		}
	}
	if _, body, _ := call(t, http.MethodPost, follow); body["forwarded_by"] != follow.id {
		t.Errorf("the relayed call does not say who relayed it: %v", body)
	}
	if _, body, _ := call(t, http.MethodGet, follow); body["served_by"] != follow.id {
		t.Errorf("a read was relayed: %v", body)
	}

	if err := lead.cl.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		code, body, _ := call(t, http.MethodPost, follow)
		if code == http.StatusOK && body["served_by"] == follow.id {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no takeover: %d %v", code, body)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Before the replica has joined there is no leader: a retryable refusal.
func TestNoLeaderIsRetryable(t *testing.T) {
	r := startReplica(t, "replica-alone", false)
	code, _, _ := call(t, http.MethodPost, r)
	if code != http.StatusServiceUnavailable {
		t.Errorf("%d, want 503", code)
	}
}
