package panel

import (
	"testing"
)

// ТЗ 24: в показе Франкфурт и Лондон видят одну и ту же сеть — те же
// устройства, то же «на связи», Амстердам серый с последней картиной.
func TestDemoОднаКартинаНаДвухСерверах(t *testing.T) {
	d := &demoBackend{returning: map[string]demoReturn{}}
	d.seed()
	type pic struct {
		devs, online int
		names        map[string]bool
	}
	look := func(self string) pic {
		d.demoSelf = self
		st := d.state()
		p := pic{names: map[string]bool{}}
		for _, n := range st.Networks {
			for _, x := range n.Devices {
				p.devs++
				if x.Online {
					p.online++
				}
				p.names[n.ID+"/"+x.ID] = true
			}
		}
		var ams MeshServer
		for _, s := range st.Servers {
			if s.ID == demoAms {
				ams = s
			}
		}
		if ams.Online || !ams.Stale || ams.Age < 7*60 {
			t.Fatalf("%s: Амстердам %+v", self, ams)
		}
		if len(st.Servers) != 3 || len(st.ServerLinks) < 2 {
			t.Fatalf("%s: серверы %d, линии %d", self, len(st.Servers), len(st.ServerLinks))
		}
		return p
	}
	fra, lon := look(demoFra), look(demoLon)
	if fra.devs != lon.devs || fra.online != lon.online || len(fra.names) != len(lon.names) {
		t.Fatalf("картины разные: Франкфурт %d/%d, Лондон %d/%d", fra.online, fra.devs, lon.online, lon.devs)
	}
	for k := range fra.names {
		if !lon.names[k] {
			t.Fatalf("на Лондоне нет %s", k)
		}
	}
	// Ноутбук на двух серверах, устройство Амстердама не на связи.
	d.demoSelf = demoLon
	st := d.state()
	nb := findDev(t, st.Networks, "7c22d9")
	if len(nb.Conns) != 2 || !nb.Online {
		t.Fatalf("ноутбук: %+v", nb.Conns)
	}
	if nas := findDev(t, st.Networks, "ams0n1"); nas.Online || !nas.Stale {
		t.Fatalf("ams-nas: %+v", nas)
	}
	if len(st.Relays) == 0 {
		t.Fatal("нет пересланного соединения на схеме")
	}
}
