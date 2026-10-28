package mcstatus

import (
	"context"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"gpanel/internal/auth"
	"gpanel/internal/httpx"
)

// Source is what the reporter needs to know about instances.
type Source struct {
	IDs     func() []string
	WorkDir func(id string) (string, error)
	Running func(id string) bool
}

type Handler struct {
	src  Source
	logs logCache

	mu    sync.Mutex
	cache map[string]cached // status per instance, briefly, so polling pages don't ping on every request
}

type cached struct {
	at time.Time
	st Status
}

const cacheFor = 5 * time.Second

func NewHandler(src Source) *Handler {
	return &Handler{src: src, logs: logCache{entries: map[string]logEntry{}}, cache: map[string]cached{}}
}

func (h *Handler) Register(mux httpx.Router) {
	mux.HandleFunc("GET /api/instances/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, h.status(r.Context(), r.PathValue("id")))
	})
	// Every instance the caller can see, for lists and the dashboard.
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		p := auth.From(r.Context())
		out := map[string]Status{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, id := range h.src.IDs() {
			if !p.CanSee(id) {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				st := h.status(r.Context(), id)
				mu.Lock()
				out[id] = st
				mu.Unlock()
			}()
		}
		wg.Wait()
		httpx.JSON(w, http.StatusOK, out)
	})
}

func (h *Handler) status(ctx context.Context, id string) Status {
	h.mu.Lock()
	c, ok := h.cache[id]
	h.mu.Unlock()
	if ok && time.Since(c.at) < cacheFor {
		return c.st
	}
	st := h.probe(ctx, id)
	h.mu.Lock()
	h.cache[id] = cached{time.Now(), st}
	h.mu.Unlock()
	return st
}

func (h *Handler) probe(ctx context.Context, id string) Status {
	st := Status{Players: []string{}}
	dir, err := h.src.WorkDir(id)
	if err != nil {
		return st
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return st
	}
	defer root.Close()
	props, ok := readProperties(root)
	if !ok {
		return st
	}
	st.Minecraft, st.Port = true, props.port
	st.Running = h.src.Running(id)
	if !st.Running {
		return st
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	reply, latency, err := Ping(ctx, net.JoinHostPort(props.host, strconv.Itoa(props.port)))
	if err != nil {
		st.Error = "未响应（可能仍在启动）"
		return st
	}
	st.Reachable = true
	st.Online, st.Max = reply.Players.Online, reply.Players.Max
	st.Version, st.MOTD = reply.Version.Name, motdText(reply.Description)
	st.LatencyMS = int(latency.Milliseconds())
	st.Players = h.logs.players(id, root)
	// No usable log (custom logging): fall back to the ping's sample.
	if len(st.Players) == 0 && st.Online > 0 {
		for _, s := range reply.Players.Sample {
			st.Players = append(st.Players, s.Name)
		}
	}
	return st
}
