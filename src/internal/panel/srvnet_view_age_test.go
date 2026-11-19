package panel

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// ТЗ 25, п. 7: на London строка Германии — «данные сверены только что ·
// последние данные — 21 мин назад». Причина: «сверены» ставит и входящий
// обмен (Германия сама звонит London), а картину London брал только своим
// запросом к Германии — а он не проходил. Теперь картина едет в самом
// обмене в обе стороны: раз обмен прошёл, картина свежая.
func TestКартинаСвежаяКогдаЗвонитТолькоДругойСервер(t *testing.T) {
	tn, de, lon := twoServers(t)
	// London до Германии не достучаться (например, её адрес из London не
	// виден), а сама Германия до London звонит.
	dial := lon.n.dial
	lon.n.dial = func(ctx context.Context, rec srvRec, login ssh.Signer) (net.Conn, error) {
		if rec.Host == "de.example" {
			return nil, errors.New("dial tcp: connection timed out")
		}
		return dial(ctx, rec, login)
	}
	tn.advance(21 * time.Minute)
	// Последним звонит Германия: London видит её «на связи, сверено только что».
	settle(lon, de)
	st := lon.mesh.State(context.Background())
	var srv MeshServer
	for _, s := range st.Servers {
		if s.Net {
			srv = s
		}
	}
	if !srv.Online {
		t.Fatalf("Германия звонила сама — должна быть на связи: %+v", srv)
	}
	if srv.Stale || srv.Age > 60 {
		t.Fatalf("обмен свежий, а картина старая: %+v", srv)
	}
	if d := findDev(t, st.Networks, "noutbuk"); !d.Online || d.Stale {
		t.Fatalf("ноутбук Германии по свежей картине должен быть на связи: %+v", d)
	}
}

// Ответ на обмен тоже несёт картину: отдельный запрос /srv/v1/view не нужен,
// и если он один не проходит, картина всё равно свежая.
func TestКартинаВОтветеНаОбмен(t *testing.T) {
	tn, de, lon := twoServers(t)
	tn.advance(21 * time.Minute)
	lon.n.mu.Lock()
	delete(lon.n.views, de.n.f.Self)
	lon.n.mu.Unlock()
	// Только London звонит Германии — Германия не звонит никому.
	lon.n.syncAll(context.Background())
	lon.n.mu.Lock()
	pv, ok := lon.n.views[de.n.f.Self]
	lon.n.mu.Unlock()
	if !ok || lon.n.now().Unix()-pv.Got > 5 {
		t.Fatalf("после обмена нет свежей картины Германии: %v %+v", ok, pv.Got)
	}
}
