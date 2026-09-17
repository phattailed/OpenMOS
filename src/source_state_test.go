package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	mosxml "airshift/openmos/internal/xml"

	"nhooyr.io/websocket"
)

// Run the real entrypoint in a subprocess so initialization failures, transport wiring and
// graceful restart are tested together. Every path and peer belongs to this test.
func TestSourceStateDirectoryEntrypoint(t *testing.T) {
	if mode := os.Getenv("OPENMOS_TEST_ENTRYPOINT"); mode != "" {
		flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
		os.Args = []string{os.Args[0]}
		if mode == "initialize" {
			os.Args = append(os.Args, "--initialize-source-state")
		} else if mode == "initialize-catalogue" {
			os.Args = append(os.Args, "--initialize-source-catalogue")
		}
		main()
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("subprocess interrupt delivery is unavailable on Windows")
	}
	for _, configuration := range []string{"environment", "yaml", "fallback", "catalogue"} {
		t.Run(configuration, func(t *testing.T) {
			testSourceStateDirectory(t, configuration)
		})
	}
}

func testSourceStateDirectory(t *testing.T, configuration string) {
	dir := t.TempDir()
	nativeDir := filepath.Join(dir, "native")
	checkpointDir := filepath.Join(dir, "checkpoint")
	catalogueDir, additionalDir := filepath.Join(dir, "catalogue"), filepath.Join(dir, "additional")
	multi := configuration == "catalogue"
	separate := configuration != "fallback"
	if !separate {
		checkpointDir = nativeDir
	}
	if err := os.Mkdir(nativeDir, 0700); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(nativeDir, "runningorders.json")
	legacy := []byte(`{"runningOrders":[{"id":"legacy","slug":"Retained"}],"stories":[],"items":[]}`)
	if separate {
		if err := os.WriteFile(legacyPath, legacy, 0600); err != nil {
			t.Fatal(err)
		}
	}
	markPath := filepath.Join(nativeDir, "device.messageid")
	if err := os.WriteFile(markPath, []byte("100"), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	yaml := "{}\n"
	envDir := ""
	if configuration == "yaml" {
		yaml = fmt.Sprintf("source:\n  statedir: %q\n", checkpointDir)
	} else if configuration == "environment" || multi {
		envDir = checkpointDir
	}
	if err := os.WriteFile(configPath, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}

	type request struct {
		id  int64
		err error
	}
	requests := make(chan request, 1)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		kind, frame, err := conn.Read(ctx)
		if r.URL.Query().Get("passive") == "true" {
			return
		}
		var id int64
		if err == nil && kind != websocket.MessageBinary {
			err = fmt.Errorf("request frame is %v, want binary", kind)
		}
		if err == nil {
			var payload []byte
			payload, err = mosxml.DecodeUCS2BE(frame)
			if err == nil {
				var env *mosxml.MosEnvelope
				env, _, _, err = mosxml.ParseEnvelope(payload)
				if err == nil {
					if env.MosID != "device" || env.NcsID != "newsroom" || env.Body.XMLName.Local != "reqMachInfo" {
						err = fmt.Errorf("unexpected request envelope: %+v", env)
					} else {
						id, err = strconv.ParseInt(env.MessageID, 10, 64)
					}
				}
			}
		}
		requests <- request{id, err}
		// Hold the connection until shutdown; the entrypoint test does not need a full handshake.
		_, _, _ = conn.Read(ctx)
	}))
	defer peer.Close()
	posted := make(chan string, 16)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Revision  uint64
			SourceID  string
			RundownID string
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if multi && body.SourceID == "source" && body.Revision > 0 {
			select {
			case posted <- r.URL.Path + ":" + body.RundownID:
			default:
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sourceId": body.SourceID, "rundownId": body.RundownID, "acceptedRevision": body.Revision, "duplicate": false, "destinationApplied": false})
	}))
	defer receiver.Close()

	command := func(ctx context.Context, mode string, enabled bool) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSourceStateDirectoryEntrypoint$")
		cmd.Dir = dir
		cmd.Env = []string{
			"OPENMOS_TEST_ENTRYPOINT=" + mode, "CONFIG_FILE=" + configPath,
			"STORAGE_BACKEND=file", "STATE_DIR=" + nativeDir,
			"SOURCE_ENABLED=" + strconv.FormatBool(enabled), "SOURCE_ID=source", "SOURCE_RUNDOWN_ID=rundown",
			"SOURCE_TRANSPORT=ws-client", "SOURCE_URL=" + receiver.URL + "/v1/openmos-snapshots", "SOURCE_TOKEN=test-token",
			"MOS_ID=device", "MOS_NCS_ID=newsroom", "MOS_CLIENT_TIMEOUT=30s", "MOS_HEARTBEAT_INTERVAL=30s",
			"SERVER_ENABLED=false", "WS_ENABLED=false", "WS_CLIENT_ENABLED=true", "WS_CLIENT_CHANNEL=ro",
			"WS_CLIENT_PASSIVE=true", "WS_CLIENT_REQUEST_LANE=true", "WS_CLIENT_PEER_URL=" + "ws" + strings.TrimPrefix(peer.URL, "http"),
			"WS_CLIENT_RECONNECT_INITIAL=1s", "WS_CLIENT_RECONNECT_MAX=1s", "LOG_LEVEL=warning",
		}
		if envDir != "" {
			cmd.Env = append(cmd.Env, "SOURCE_STATE_DIR="+envDir)
		}
		if multi {
			extra, err := json.Marshal([]map[string]string{{"rundownId": "other", "stateDir": additionalDir}})
			if err != nil {
				t.Fatal(err)
			}
			cmd.Env = append(cmd.Env, "SOURCE_CATALOGUE_STATE_DIR="+catalogueDir, "SOURCE_ADDITIONAL_RUNDOWNS="+string(extra))
		}
		return cmd
	}
	read := func(path string) []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	mark := func() int64 {
		t.Helper()
		value, err := strconv.ParseInt(strings.TrimSpace(string(read(markPath))), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	checkLegacy := func() {
		t.Helper()
		if !separate {
			return
		}
		if !bytes.Equal(read(legacyPath), legacy) {
			t.Fatal("legacy content changed")
		}
		if _, err := os.Stat(filepath.Join(nativeDir, "source-checkpoint.json")); !os.IsNotExist(err) {
			t.Fatalf("committed checkpoint must stay out of native state: %v", err)
		}
		if matches, err := filepath.Glob(filepath.Join(checkpointDir, "*.messageid")); err != nil || len(matches) != 0 {
			t.Fatalf("sender mark must stay in native state: %v, %v", matches, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	output, err := command(ctx, "initialize", true).CombinedOutput()
	cancel()
	if err != nil {
		t.Fatalf("initialize separate checkpoint beside retained legacy state: %v\n%s", err, output)
	}
	checkpointPath := filepath.Join(checkpointDir, "source-checkpoint.json")
	read(checkpointPath)
	checkLegacy()
	if mark() != 100 {
		t.Fatal("initialization changed the existing sender mark")
	}
	if multi {
		prior := read(checkpointPath)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		output, err := command(ctx, "run", true).CombinedOutput()
		cancel()
		if err == nil || !bytes.Contains(output, []byte("Cannot open additional source")) {
			t.Fatalf("normal startup provisioned an absent additional rundown: %v\n%s", err, output)
		}
		if !bytes.Equal(prior, read(checkpointPath)) || mark() != 100 {
			t.Fatal("failed source-set preflight modified original state")
		}
		for _, mode := range []string{"initialize", "initialize-catalogue"} {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			cmd := command(ctx, mode, true)
			if mode == "initialize" {
				cmd.Env = append(cmd.Env, "SOURCE_STATE_DIR="+additionalDir, "SOURCE_RUNDOWN_ID=other", "SOURCE_ADDITIONAL_RUNDOWNS=[]")
			}
			output, err := cmd.CombinedOutput()
			cancel()
			if err != nil {
				t.Fatalf("explicit additional provisioning failed: %v\n%s", err, output)
			}
		}
		if !bytes.Equal(prior, read(checkpointPath)) || mark() != 100 {
			t.Fatal("additional provisioning changed the original checkpoint or native counter")
		}
	}

	var revision uint64
	for _, step := range []struct {
		name    string
		enabled bool
	}{{"source startup", true}, {"source restart", true}, {"legacy rollback", false}} {
		if !step.enabled && !separate {
			continue // A shared committed directory deliberately cannot be opened as legacy state.
		}
		previousMark := mark()
		previousCheckpoint := read(checkpointPath)
		for len(posted) > 0 {
			<-posted
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cmd := command(ctx, "run", step.enabled)
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Start(); err != nil {
			cancel()
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		var got request
		select {
		case got = <-requests:
			if multi && step.enabled {
				want := map[string]bool{"/v1/openmos-snapshots:rundown": true, "/v1/openmos-snapshots:other": true, "/v1/openmos-catalogue:": true}
				for len(want) > 0 {
					select {
					case path := <-posted:
						delete(want, path)
					case <-ctx.Done():
						cancel()
						<-done
						t.Fatalf("entrypoint failed to publish independent streams: %v\n%s", want, &output)
					}
				}
			}
			if err := cmd.Process.Signal(os.Interrupt); err != nil {
				cancel()
				<-done
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				cancel()
				t.Fatalf("%s shutdown: %v\n%s", step.name, err, &output)
			}
		case err := <-done:
			cancel()
			t.Fatalf("%s exited before its first request: %v\n%s", step.name, err, &output)
		}
		cancel()
		if got.err != nil || got.id <= previousMark || got.id > mark() {
			t.Fatalf("%s sender continuity: request=%d prior mark=%d new mark=%d, error=%v", step.name, got.id, previousMark, mark(), got.err)
		}
		checkLegacy()
		checkpoint := read(checkpointPath)
		if step.enabled {
			var saved struct {
				Source struct{ Revision uint64 }
			}
			if err := json.Unmarshal(checkpoint, &saved); err != nil {
				t.Fatal(err)
			}
			if saved.Source.Revision <= revision {
				t.Fatalf("%s did not reopen and advance the retained source revision", step.name)
			}
			revision = saved.Source.Revision
		} else if !bytes.Equal(checkpoint, previousCheckpoint) {
			t.Fatal("legacy rollback changed the source checkpoint")
		}
		t.Logf("%s: sender mark %d -> %d, first request %d", step.name, previousMark, mark(), got.id)
	}
}
