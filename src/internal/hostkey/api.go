package hostkey

// /api/hostkey — один обработчик на ПК (internal/webui) и телефон
// (android/core/mobile → AndroidApi.kt): тот же JSON, та же логика.
//
//	GET                       → {"pending":[смена…]}
//	GET ?host=…&port=…        → {"keys":[{type,fingerprint}], "pending":смена|null}
//	POST {"op":"accept","host":"h:p","fingerprint":"SHA256:…"}
//	POST {"op":"dismiss","host":"h:p"}
//	POST {"op":"forget","host":"h","port":22}
//
// смена = {"host":"h:p","server":"h","port":p,"keyType","fingerprint",
//          "old":[{type,fingerprint}],"cmd":"ssh-keygen -lf …"}

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// PendingView — смена ключа для экрана.
type PendingView struct {
	Host        string    `json:"host"`
	Server      string    `json:"server"`
	Port        int       `json:"port"`
	KeyType     string    `json:"keyType"`
	Fingerprint string    `json:"fingerprint"`
	Old         []KeyInfo `json:"old"`
	Cmd         string    `json:"cmd"`
}

func view(e *ErrChanged) *PendingView {
	if e == nil {
		return nil
	}
	h, p := splitHostPort(e.Host)
	if p == 0 {
		p = 22
	}
	old := e.Old
	if old == nil {
		old = []KeyInfo{}
	}
	return &PendingView{Host: e.Host, Server: h, Port: p, KeyType: e.KeyType, Fingerprint: e.Fingerprint,
		Old: old, Cmd: CheckCommand(e.KeyType)}
}

// ViewOf — то же для ошибки, если это смена ключа (для ответов других API).
func ViewOf(err error) *PendingView {
	var e *ErrChanged
	if errors.As(err, &e) {
		return view(e)
	}
	return nil
}

// HandleAPI отвечает на /api/hostkey. knownHostsPath — файл программы (для
// «забыть» и показа ключа; «принять» пишет в тот файл, где ключ не совпал).
func HandleAPI(knownHostsPath, method string, query map[string]string, body []byte) (any, error) {
	if method != "POST" {
		if h := strings.TrimSpace(query["host"]); h != "" {
			port, _ := strconv.Atoi(query["port"])
			keys := Known(knownHostsPath, h, port)
			if keys == nil {
				keys = []KeyInfo{}
			}
			return map[string]any{"keys": keys, "pending": view(PendingFor(h, port))}, nil
		}
		list := []*PendingView{}
		for _, e := range Pending() {
			list = append(list, view(e))
		}
		return map[string]any{"pending": list}, nil
	}
	var req struct {
		Op          string `json:"op"`
		Host        string `json:"host"`
		Port        int    `json:"port"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, errors.New("не разобрал запрос: " + err.Error())
	}
	switch req.Op {
	case "accept":
		e, err := Accept(req.Host, req.Fingerprint)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "host": e.Host}, nil
	case "dismiss":
		Dismiss(req.Host)
		return map[string]any{"ok": true}, nil
	case "forget":
		if strings.TrimSpace(req.Host) == "" {
			return nil, errors.New("не указан адрес сервера")
		}
		name := HostName(strings.TrimSpace(req.Host), req.Port)
		n, err := Forget(knownHostsPath, Aliases(knownHostsPath, name))
		if err != nil {
			return nil, err
		}
		Dismiss(HostName(strings.TrimSpace(req.Host), req.Port))
		return map[string]any{"ok": true, "removed": n}, nil
	}
	return nil, errors.New("op: accept, dismiss или forget")
}
