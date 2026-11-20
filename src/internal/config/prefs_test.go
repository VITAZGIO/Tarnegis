package config

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

func tempConfigDir(t *testing.T) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	t.Setenv("APPDATA", base)
}

func writeConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Конфиг до ТЗ 32: у двух серверов разные списки «напрямую», разные режимы
// программ и имена устройства.
const oldTwoProfiles = `{
  "activeProfile": "b",
  "profiles": [
    {"id":"a","name":"Франкфурт","host":"203.0.113.1","directHosts":["ya.ru","sber.ru"],
     "tunnelHosts":["x.com"],"filterMode":"only","filterApps":["a.exe"],"meshName":"ноут-а","poolSize":2},
    {"id":"b","name":"Лондон","host":"203.0.113.2","directHosts":["SBER.ru","gosuslugi.ru"],
     "filterMode":"except","filterApps":["steam.exe"],"meshName":"ноут","meshIncoming":true,"poolSize":6,
     "cipher":"chacha"}
  ],
  "theme":"light","background":"code"
}`

func TestПереездСпискиОбъединяются(t *testing.T) {
	tempConfigDir(t)
	writeConfig(t, oldTwoProfiles)
	TakeMigrationNotes()
	cfg := Load()
	if cfg.PrefsV != PrefsVersion {
		t.Fatalf("PrefsV = %d", cfg.PrefsV)
	}
	want := []string{"SBER.ru", "gosuslugi.ru", "ya.ru"} // активный первым, без повторов
	if !slices.Equal(cfg.Prefs.DirectHosts, want) {
		t.Fatalf("directHosts = %v, ждали %v", cfg.Prefs.DirectHosts, want)
	}
	if !slices.Equal(cfg.Prefs.TunnelHosts, []string{"x.com"}) {
		t.Fatalf("tunnelHosts = %v", cfg.Prefs.TunnelHosts)
	}
	// Режим программ и одиночные значения — активного сервера.
	if cfg.Prefs.FilterMode != "except" || !slices.Equal(cfg.Prefs.FilterApps, []string{"steam.exe"}) {
		t.Fatalf("filter = %s %v", cfg.Prefs.FilterMode, cfg.Prefs.FilterApps)
	}
	if cfg.Prefs.MeshName != "ноут" || !cfg.Prefs.MeshIncoming || cfg.Prefs.PoolSize != 6 || cfg.Prefs.Cipher != "chacha" {
		t.Fatalf("одиночные значения не активного: %+v", cfg.Prefs)
	}
	for _, p := range cfg.Profiles {
		if p.OwnRules {
			t.Fatalf("у %s после переезда «свои правила»", p.Name)
		}
	}
	notes := strings.Join(TakeMigrationNotes(), "\n")
	if !strings.Contains(notes, "ноут-а") {
		t.Fatalf("разные имена устройства не замечены: %q", notes)
	}
}

func TestСменаСервераНеМеняетСписки(t *testing.T) {
	tempConfigDir(t)
	writeConfig(t, oldTwoProfiles)
	cfg := Load()
	before := cfg.Active().DirectHosts
	cfg.ActiveProfile = "a"
	after := cfg.Active()
	if !slices.Equal(before, after.DirectHosts) {
		t.Fatalf("смена сервера поменяла список: %v → %v", before, after.DirectHosts)
	}
	if after.MeshName != "ноут" || after.FilterMode != "except" {
		t.Fatalf("у другого сервера свои значения: %+v", after)
	}
	// Цепочка и дополнительные серверы — тоже с общими настройками.
	cfg.Profiles[1].ExitVia = []string{"a"}
	if hop := cfg.ExitChain(cfg.Profiles[1])[0]; hop.Cipher != "chacha" {
		t.Fatalf("звено цепочки без общих настроек: шифр %q", hop.Cipher)
	}
}

func TestСвоиПравилаСервера(t *testing.T) {
	tempConfigDir(t)
	writeConfig(t, oldTwoProfiles)
	cfg := Load()
	cfg.SetOwnRules("a", true)
	// Включение начинается с копии общих.
	if got := cfg.Resolve(cfg.Profiles[0]).DirectHosts; !slices.Equal(got, cfg.Prefs.DirectHosts) {
		t.Fatalf("включили свои — не копия общих: %v", got)
	}
	cfg.EditRules("a", func(r *Prefs) { r.DirectHosts = []string{"только-а.ru"} })
	if got := cfg.Resolve(cfg.Profiles[0]).DirectHosts; !slices.Equal(got, []string{"только-а.ru"}) {
		t.Fatalf("свои правила не действуют: %v", got)
	}
	if slices.Contains(cfg.Prefs.DirectHosts, "только-а.ru") {
		t.Fatal("правка своих правил попала в общие")
	}
	// Имя устройства — всегда общее.
	cfg.Profiles[0].MeshName = "другое"
	if cfg.Resolve(cfg.Profiles[0]).MeshName != "ноут" {
		t.Fatal("имя устройства у сервера своё")
	}
	cfg.SetOwnRules("a", false)
	if got := cfg.Resolve(cfg.Profiles[0]).DirectHosts; !slices.Equal(got, cfg.Prefs.DirectHosts) {
		t.Fatalf("выключили свои — действуют не общие: %v", got)
	}
	// Правка у сервера без своих правил — в общие.
	cfg.EditRules("b", func(r *Prefs) { r.DirectHosts = append(r.DirectHosts, "новый.ru") })
	if !slices.Contains(cfg.Resolve(cfg.Profiles[0]).DirectHosts, "новый.ru") {
		t.Fatal("правка через сервер b не видна у a")
	}
}

func TestСохранениеИЗагрузкаДержатОбщиеИТему(t *testing.T) {
	tempConfigDir(t)
	writeConfig(t, oldTwoProfiles)
	cfg := Load()
	cfg.Theme, cfg.Background = "dark", "author"
	cfg.SetOwnRules("a", true)
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	got := Load()
	if got.Theme != "dark" || got.Background != "author" {
		t.Fatalf("тема не пережила сохранение: %q %q", got.Theme, got.Background)
	}
	if !slices.Equal(got.Prefs.DirectHosts, cfg.Prefs.DirectHosts) || got.PrefsV != PrefsVersion {
		t.Fatalf("общие настройки не пережили сохранение: %+v", got.Prefs)
	}
	if !got.Profiles[0].OwnRules || got.Profiles[1].OwnRules {
		t.Fatal("«свои правила» не пережили сохранение")
	}
	// Второй Load не переезжает заново (списки серверов уже не сливаются).
	got.Profiles[1].DirectHosts = []string{"чужое.ru"}
	if err := got.Save(); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(Load().Prefs.DirectHosts, "чужое.ru") {
		t.Fatal("переезд повторился на уже новом конфиге")
	}
}

// Новый конфиг (файла ещё нет) — сразу нового формата.
func TestНовыйКонфигСразуНовый(t *testing.T) {
	tempConfigDir(t)
	cfg := Load()
	if cfg.PrefsV != PrefsVersion || cfg.Active().PoolSize != defaultPoolSize || cfg.Active().FilterMode != "all" {
		t.Fatalf("новый конфиг: PrefsV %d, %+v", cfg.PrefsV, cfg.Active())
	}
}

// Профиль из файла или от панели: его списки заполняют общие, только если
// там пусто.
func TestИмпортСервераНеПеретираетОбщие(t *testing.T) {
	cfg := Default()
	p := Profile{ID: "x", DirectHosts: []string{"bank.ru"}, FilterMode: "only", FilterApps: []string{"a.exe"}}
	if !cfg.FillPrefsFrom(p) || !slices.Equal(cfg.Prefs.DirectHosts, []string{"bank.ru"}) || cfg.Prefs.FilterMode != "only" {
		t.Fatalf("пустые общие не заполнились: %+v", cfg.Prefs)
	}
	q := Profile{ID: "y", DirectHosts: nil, FilterMode: "all"}
	if cfg.FillPrefsFrom(q) || !slices.Equal(cfg.Prefs.DirectHosts, []string{"bank.ru"}) {
		t.Fatalf("пустой профиль панели стёр общие: %+v", cfg.Prefs)
	}
}

// Снимок настроек старого формата (Parse — профили приложения) тоже
// переезжает; конфиг из кода без PrefsV (телефон, тесты) работает по-старому.
func TestСтарыйСнимокИКонфигИзКода(t *testing.T) {
	cfg, err := Parse([]byte(oldTwoProfiles))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PrefsV != PrefsVersion || len(cfg.Prefs.DirectHosts) != 3 {
		t.Fatalf("снимок не переехал: %+v", cfg.Prefs)
	}
	code := Config{Profiles: []Profile{{ID: "p", DirectHosts: []string{"a.ru"}}}, ActiveProfile: "p"}
	if !slices.Equal(code.Active().DirectHosts, []string{"a.ru"}) {
		t.Fatal("конфиг без PrefsV потерял поля профиля")
	}
	data, _ := json.Marshal(cfg)
	if !strings.Contains(string(data), `"prefsV":1`) {
		t.Fatalf("prefsV не в JSON: %s", data)
	}
}
