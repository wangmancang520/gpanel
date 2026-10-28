package mcstatus

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// fakeServer answers one Server List Ping per connection with reply,
// checking the handshake on the way.
func fakeServer(t *testing.T, reply string) (port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				for i := 0; i < 2; i++ { // handshake, status request
					n, err := readVarint(r)
					if err != nil {
						return
					}
					p := make([]byte, n)
					io.ReadFull(r, p)
					if i == 0 {
						br := bytes.NewReader(p)
						readVarint(br) // packet id
						readVarint(br) // protocol
						hl, _ := readVarint(br)
						host := make([]byte, hl)
						br.Read(host)
						var port uint16
						binary.Read(br, binary.BigEndian, &port)
						next, _ := readVarint(br)
						if string(host) != "127.0.0.1" || next != 1 || int(port) != ln.Addr().(*net.TCPAddr).Port {
							t.Errorf("handshake host=%q port=%d next=%d", host, port, next)
						}
					}
				}
				var body bytes.Buffer
				putVarint(&body, 0)
				putString(&body, reply)
				writePacket(c, body.Bytes())
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestPing(t *testing.T) {
	port := fakeServer(t, `{"version":{"name":"NeoForge 1.21.1","protocol":767},
		"players":{"max":20,"online":2,"sample":[{"name":"Alex","id":"x"}]},
		"description":{"text":"§a你好，","extra":[{"text":"新蒸程"},"§r!"]}}`)
	reply, _, err := Ping(context.Background(), "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	if reply.Version.Name != "NeoForge 1.21.1" || reply.Players.Online != 2 || reply.Players.Max != 20 {
		t.Fatalf("reply = %+v", reply)
	}
	if got := motdText(reply.Description); got != "你好，新蒸程!" { // "§r!" is text after a reset code
		t.Errorf("motd = %q", got)
	}
	if got := motdText(json.RawMessage(`"§6A §lplain§r motd"`)); got != "A plain motd" {
		t.Errorf("plain motd = %q", got)
	}
	if _, _, err := Ping(context.Background(), "127.0.0.1:1"); err == nil {
		t.Error("ping to a closed port succeeded")
	}
}

func TestVarint(t *testing.T) {
	for _, v := range []int32{0, 1, 127, 128, 255, 25565, 2097151, -1} {
		var b bytes.Buffer
		putVarint(&b, v)
		got, err := readVarint(bytes.NewReader(b.Bytes()))
		if err != nil || got != v {
			t.Errorf("varint %d -> % x -> %d, %v", v, b.Bytes(), got, err)
		}
	}
}

func TestPlayersFromLog(t *testing.T) {
	log := `[10:01:00] [Server thread/INFO] [minecraft/DedicatedServer]: Done (4.4s)! For help, type "help"
[10:02:00] [Server thread/INFO] [minecraft/MinecraftServer]: Steve joined the game
[10:03:00] [Server thread/INFO]: Alex_2 joined the game
[10:04:00] [Server thread/INFO]: Steve left the game
[10:05:00] [Server thread/INFO]: <Alex_2> I joined the game yesterday
[10:06:00] [Server thread/INFO]: Notch joined the game
[10:07:00] [Server thread/INFO]: Steve joined the game
`
	got := playersFromLog(strings.NewReader(log))
	if want := []string{"Alex_2", "Notch", "Steve"}; !slices.Equal(got, want) {
		t.Fatalf("players = %v, want %v", got, want)
	}
}

func TestProbe(t *testing.T) {
	dir := t.TempDir()
	running := false
	h := NewHandler(Source{
		IDs:     func() []string { return []string{"mc", "other"} },
		WorkDir: func(id string) (string, error) { return filepath.Join(dir, id), nil },
		Running: func(string) bool { return running },
	})
	os.MkdirAll(filepath.Join(dir, "other"), 0o755)
	if st := h.probe(context.Background(), "other"); st.Minecraft {
		t.Error("instance without server.properties reported as Minecraft")
	}

	port := fakeServer(t, `{"version":{"name":"Paper 1.21.8"},"players":{"max":10,"online":1},"description":"hi"}`)
	mc := filepath.Join(dir, "mc")
	os.MkdirAll(filepath.Join(mc, "logs"), 0o755)
	os.WriteFile(filepath.Join(mc, "server.properties"), []byte("#comment\nmotd=x\nserver-port="+strconv.Itoa(port)+"\nserver-ip=\n"), 0o644)
	os.WriteFile(filepath.Join(mc, "logs", "latest.log"), []byte("[x] [Server thread/INFO]: Steve joined the game\n"), 0o644)

	if st := h.probe(context.Background(), "mc"); !st.Minecraft || st.Running || st.Reachable {
		t.Fatalf("stopped instance: %+v", st)
	}
	running = true
	st := h.probe(context.Background(), "mc")
	if !st.Reachable || st.Online != 1 || st.Max != 10 || st.Version != "Paper 1.21.8" || !slices.Equal(st.Players, []string{"Steve"}) || st.Port != port {
		t.Fatalf("running instance: %+v", st)
	}
	// Starting up: process running, port not answering yet.
	os.WriteFile(filepath.Join(mc, "server.properties"), []byte("server-port=1\n"), 0o644)
	if st := h.probe(context.Background(), "mc"); !st.Running || st.Reachable || st.Error == "" {
		t.Fatalf("not answering: %+v", st)
	}
	// status() caches for a few seconds.
	first := h.status(context.Background(), "mc")
	os.Remove(filepath.Join(mc, "server.properties"))
	if again := h.status(context.Background(), "mc"); again.Minecraft != first.Minecraft {
		t.Error("status not cached")
	}
}
