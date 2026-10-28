// Package mcstatus reports who is on a Minecraft Java Edition server: the
// online count, limit, version and MOTD from a Server List Ping on its port
// (read from server.properties), and the names of everyone online from the
// join/leave lines in logs/latest.log (the ping only samples a few names).
//
// Instances without server.properties are reported as not Minecraft; other
// games can get their own reporter alongside this one.
package mcstatus

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Status struct {
	Minecraft bool     `json:"minecraft"`         // has server.properties
	Running   bool     `json:"running"`           // the instance process is up
	Reachable bool     `json:"reachable"`         // answered the ping (fully started)
	Online    int      `json:"online"`            // players online
	Max       int      `json:"max"`               // player limit
	Players   []string `json:"players"`           // names, from the log
	Version   string   `json:"version,omitempty"` // e.g. "1.21.1" or a modded name
	MOTD      string   `json:"motd,omitempty"`
	LatencyMS int      `json:"latencyMs,omitempty"`
	Port      int      `json:"port,omitempty"`
	Error     string   `json:"error,omitempty"` // why the ping failed, while running
}

// ---- Server List Ping (https://minecraft.wiki/w/Java_Edition_protocol/Server_List_Ping)

type pingReply struct {
	Version struct {
		Name string `json:"name"`
	} `json:"version"`
	Players struct {
		Max    int `json:"max"`
		Online int `json:"online"`
		Sample []struct {
			Name string `json:"name"`
		} `json:"sample"`
	} `json:"players"`
	Description json.RawMessage `json:"description"`
}

// Ping asks the server at addr for its status.
func Ping(ctx context.Context, addr string) (*pingReply, time.Duration, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, 0, err
	}
	port, _ := strconv.Atoi(portStr)
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, 0, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))

	var hs bytes.Buffer
	putVarint(&hs, 0x00) // handshake
	putVarint(&hs, -1)   // protocol: any
	putString(&hs, host) // address the client "connected to"
	binary.Write(&hs, binary.BigEndian, uint16(port))
	putVarint(&hs, 1) // next state: status
	start := time.Now()
	if err := writePacket(conn, hs.Bytes()); err != nil {
		return nil, 0, err
	}
	if err := writePacket(conn, []byte{0x00}); err != nil { // status request
		return nil, 0, err
	}
	r := bufio.NewReader(conn)
	length, err := readVarint(r)
	if err != nil {
		return nil, 0, err
	}
	if length <= 0 || length > 1<<21 {
		return nil, 0, errors.New("状态响应长度异常")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, 0, err
	}
	latency := time.Since(start)
	br := bytes.NewReader(body)
	if id, err := readVarint(br); err != nil || id != 0x00 {
		return nil, 0, errors.New("不是状态响应")
	}
	n, err := readVarint(br)
	if err != nil || n < 0 || int64(n) > int64(br.Len()) {
		return nil, 0, errors.New("状态响应格式错误")
	}
	js := make([]byte, n)
	br.Read(js)
	var reply pingReply
	if err := json.Unmarshal(js, &reply); err != nil {
		return nil, 0, fmt.Errorf("状态 JSON 无法解析: %w", err)
	}
	return &reply, latency, nil
}

func writePacket(w io.Writer, payload []byte) error {
	var b bytes.Buffer
	putVarint(&b, int32(len(payload)))
	b.Write(payload)
	_, err := w.Write(b.Bytes())
	return err
}

func putVarint(b *bytes.Buffer, v int32) {
	u := uint32(v)
	for {
		if u&^0x7f == 0 {
			b.WriteByte(byte(u))
			return
		}
		b.WriteByte(byte(u&0x7f | 0x80))
		u >>= 7
	}
}

func putString(b *bytes.Buffer, s string) {
	putVarint(b, int32(len(s)))
	b.WriteString(s)
}

func readVarint(r io.ByteReader) (int32, error) {
	var v uint32
	for i := 0; i < 5; i++ {
		c, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		v |= uint32(c&0x7f) << (7 * i)
		if c&0x80 == 0 {
			return int32(v), nil
		}
	}
	return 0, errors.New("varint 过长")
}

var formatCode = regexp.MustCompile(`§.`)

// motdText flattens a description (plain string or chat component) to text.
func motdText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(formatCode.ReplaceAllString(s, ""))
	}
	var c struct {
		Text  string            `json:"text"`
		Extra []json.RawMessage `json:"extra"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(c.Text)
	for _, e := range c.Extra {
		b.WriteString(motdText(e))
	}
	return strings.TrimSpace(formatCode.ReplaceAllString(b.String(), ""))
}

// ---- server.properties and the log

type properties struct {
	port int
	host string
}

// readProperties returns the port (and bind address) the server listens
// on; ok is false when dir has no server.properties.
func readProperties(root *os.Root) (properties, bool) {
	b, err := root.ReadFile("server.properties")
	if err != nil {
		return properties{}, false
	}
	p := properties{port: 25565, host: "127.0.0.1"}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		switch strings.TrimSpace(k) {
		case "server-port":
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 && n < 65536 {
				p.port = n
			}
		case "server-ip":
			if ip := strings.TrimSpace(v); ip != "" && ip != "0.0.0.0" && ip != "::" {
				p.host = ip
			}
		}
	}
	return p, true
}

// joinLeave matches vanilla's "<name> joined the game" / "left the game"
// (also printed by Paper, Fabric, Forge and NeoForge servers).
var joinLeave = regexp.MustCompile(`\]: ([A-Za-z0-9_]{1,16}) (joined|left) the game`)

// playersFromLog returns who is online according to the log, ordered by
// when they (last) joined. latest.log starts afresh with each server start.
func playersFromLog(r io.Reader) []string {
	online := []string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		m := joinLeave.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		online = slices.DeleteFunc(online, func(n string) bool { return n == m[1] })
		if m[2] == "joined" {
			online = append(online, m[1])
		}
	}
	return online
}

// logCache avoids re-reading an unchanged latest.log.
type logCache struct {
	mu      sync.Mutex
	entries map[string]logEntry // by instance ID
}

type logEntry struct {
	size    int64
	mod     time.Time
	players []string
}

func (c *logCache) players(id string, root *os.Root) []string {
	st, err := root.Stat("logs/latest.log")
	if err != nil {
		return []string{}
	}
	c.mu.Lock()
	e, ok := c.entries[id]
	c.mu.Unlock()
	if ok && e.size == st.Size() && e.mod.Equal(st.ModTime()) {
		return e.players
	}
	f, err := root.Open("logs/latest.log")
	if err != nil {
		return []string{}
	}
	defer f.Close()
	players := playersFromLog(f)
	c.mu.Lock()
	c.entries[id] = logEntry{st.Size(), st.ModTime(), players}
	c.mu.Unlock()
	return players
}
