package instance

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"gpanel/internal/events"
	"gpanel/internal/httpx"
)

type Handler struct{ m *Manager }

func NewHandler(m *Manager) *Handler { return &Handler{m: m} }

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/instances", h.list)
	mux.HandleFunc("POST /api/instances", h.create)
	mux.HandleFunc("GET /api/instances/{id}", h.get)
	mux.HandleFunc("PUT /api/instances/{id}", h.update)
	mux.HandleFunc("DELETE /api/instances/{id}", h.delete)
	mux.HandleFunc("POST /api/instances/{id}/{action}", h.action)
	mux.HandleFunc("GET /api/instances/{id}/terminal", h.terminal)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, h.m.List())
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	inst, ok := h.lookup(w, r)
	if ok {
		httpx.JSON(w, http.StatusOK, inst.Status())
	}
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var cfg Config
	if err := httpx.Decode(r, &cfg); err != nil {
		httpx.Error(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	cfg, err := h.m.Create(cfg)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.JSON(w, http.StatusCreated, cfg)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	var cfg Config
	if err := httpx.Decode(r, &cfg); err != nil {
		httpx.Error(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	cfg, err := h.m.Update(r.PathValue("id"), cfg)
	if err != nil {
		httpx.Error(w, statusFor(err), err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, cfg)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.m.Delete(r.PathValue("id"), r.URL.Query().Get("files") == "1"); err != nil {
		httpx.Error(w, statusFor(err), err.Error())
		return
	}
	httpx.OK(w)
}

func (h *Handler) action(w http.ResponseWriter, r *http.Request) {
	inst, ok := h.lookup(w, r)
	if !ok {
		return
	}
	var err error
	switch r.PathValue("action") {
	case "start":
		err = inst.Start()
	case "stop":
		err = inst.Stop()
	case "restart":
		err = inst.Restart()
	case "kill":
		err = inst.Kill()
	case "command":
		var req struct{ Command string }
		if err = httpx.Decode(r, &req); err == nil {
			err = inst.SendCommand(req.Command)
		}
	default:
		httpx.Error(w, http.StatusNotFound, "未知操作")
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusConflict, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, inst.Status())
}

func (h *Handler) lookup(w http.ResponseWriter, r *http.Request) (*Instance, bool) {
	inst, err := h.m.Get(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, err.Error())
		return nil, false
	}
	return inst, true
}

func statusFor(err error) int {
	if errors.Is(err, ErrNotFound) {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

// terminal protocol:
//
//	server -> client  binary frames: raw console output
//	server -> client  text frames:   {"type":"status","data":Status}
//	client -> server  text frames:   {"type":"input","data":"..."} | {"type":"resize","cols":N,"rows":N}
func (h *Handler) terminal(w http.ResponseWriter, r *http.Request) {
	inst, ok := h.lookup(w, r)
	if !ok {
		return
	}
	// Accept verifies Origin matches Host by default.
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(64 * 1024)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	history, out, detach := inst.Attach()
	defer detach()

	id := inst.Config().ID
	statusCh := make(chan Status, 8)
	unsubscribe := h.m.bus.Subscribe(func(e events.Event) {
		if e.Type == events.InstanceState && e.InstanceID == id {
			select {
			case statusCh <- e.Data.(Status):
			default:
			}
		}
	})
	defer unsubscribe()

	go func() {
		defer cancel()
		for {
			var msg struct {
				Type       string
				Data       string
				Cols, Rows uint16
			}
			typ, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageText || json.Unmarshal(data, &msg) != nil {
				continue
			}
			switch msg.Type {
			case "input":
				_ = inst.Input([]byte(msg.Data))
			case "resize":
				inst.Resize(msg.Cols, msg.Rows)
			}
		}
	}()

	write := func(typ websocket.MessageType, b []byte) error {
		wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
		defer wcancel()
		return c.Write(wctx, typ, b)
	}
	writeStatus := func(s Status) error {
		wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
		defer wcancel()
		return wsjson.Write(wctx, c, map[string]any{"type": "status", "data": s})
	}

	if writeStatus(inst.Status()) != nil || (len(history) > 0 && write(websocket.MessageBinary, history) != nil) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-out:
			if !ok {
				c.Close(websocket.StatusPolicyViolation, "client too slow")
				return
			}
			if write(websocket.MessageBinary, chunk) != nil {
				return
			}
		case s := <-statusCh:
			if writeStatus(s) != nil {
				return
			}
		}
	}
}
