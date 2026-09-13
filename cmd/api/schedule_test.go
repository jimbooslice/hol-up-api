package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestConfiguredScheduleMaxAge(t *testing.T) {
	for _, value := range []string{"", "24h", "1h30m"} {
		t.Setenv("SCHEDULE_MAX_AGE", value)
		if _, err := configuredScheduleMaxAge(); err != nil {
			t.Errorf("%q: %v", value, err)
		}
	}
	for _, value := range []string{"0", "-1h", "invalid secret", "NaN"} {
		t.Setenv("SCHEDULE_MAX_AGE", value)
		if _, err := configuredScheduleMaxAge(); err != errInvalidScheduleMaxAge || logError(err) != errInvalidScheduleMaxAge.Error() {
			t.Errorf("invalid value %q: %v", value, err)
		}
	}
}

func TestAPIScheduleStartupProcess(t *testing.T) {
	if os.Getenv("HOLUP_SCHEDULE_TEST_PROCESS") == "1" {
		main()
		os.Exit(0)
	}
}

func TestBlockedScheduleDoesNotDelayHTTPOrRealtime(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { once.Do(func() { close(started) }); <-r.Context().Done() }))
	defer slow.Close()
	wire := feedBytes(t, time.Now(), "A", "startup-trip")
	realtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(wire) }))
	defer realtime.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAPIScheduleStartupProcess$")
	cmd.Env = append(os.Environ(), "HOLUP_SCHEDULE_TEST_PROCESS=1", "ADDR="+address, "SCHEDULE_MAX_AGE=24h", "SCHEDULE_CACHE_DIR="+t.TempDir(), "MTA_GTFS_SUPPLEMENTED_URL="+slow.URL, "MTA_GTFS_REGULAR_URL="+slow.URL)
	for _, name := range []string{"MTA_GTFS_RT_URL", "MTA_GTFS_RT_BDFM_URL", "MTA_GTFS_RT_G_URL", "MTA_GTFS_RT_JZ_URL", "MTA_GTFS_RT_L_URL", "MTA_GTFS_RT_NQRW_URL", "MTA_GTFS_RT_NUMBERED_URL", "MTA_GTFS_RT_SI_URL"} {
		cmd.Env = append(cmd.Env, name+"="+realtime.URL)
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("schedule request never started")
	}
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, err := client.Get("http://" + address + "/api/v1/trains?route_id=A")
		if err == nil {
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if response.StatusCode == 200 && strings.Contains(string(body), `"trip_id":"startup-trip"`) {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("schedule request blocked realtime or HTTP startup")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, path := range []string{"/healthz", "/readyz", "/api/v1/routes", "/metrics", "/openapi.yaml"} {
		response, err := client.Get("http://" + address + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != 200 {
			t.Errorf("%s status=%d", path, response.StatusCode)
		}
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("shutdown: %v/%s", err, &output)
	}
	if !strings.Contains(output.String(), `"event":"api_stopped"`) {
		t.Fatalf("missing shutdown log: %s", &output)
	}
}
