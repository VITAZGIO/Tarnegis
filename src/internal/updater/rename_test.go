package updater

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// releaseAssets — имена файлов, которые release.yml кладёт в релиз: строки
// «cp releases/… dist/» плюс копии ssh_tunnel* под именем tarnegis* (ТЗ 53).
func releaseAssets(t *testing.T) map[string]bool {
	t.Helper()
	b, err := os.ReadFile("../../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	yml := string(b)
	if !strings.Contains(yml, `cp "$f" "dist/tarnegis${b#ssh_tunnel}"`) {
		t.Fatal("release.yml больше не копирует ssh_tunnel* под именем tarnegis* — старые и новые версии не найдут свой файл")
	}
	names := map[string]bool{}
	for _, m := range regexp.MustCompile(`cp releases/(?:\S+/)?(\S+) dist/`).FindAllStringSubmatch(yml, -1) {
		names[m[1]] = true
	}
	for n := range names {
		if strings.HasPrefix(n, OldPrefix) {
			names[NewPrefix+strings.TrimPrefix(n, OldPrefix)] = true
		}
	}
	return names
}

// assetForV11027 — выбор файла так, как его делает v1.10.27 (точное имя).
func assetForV11027(goos, goarch string, vpn, panel bool) string {
	want := ""
	switch goos {
	case "windows":
		want = "ssh_tunnel.exe"
		if vpn {
			want = "ssh_tunnel_vpn.exe"
		}
	case "linux":
		want = "ssh_tunnel_linux"
		if vpn {
			want = "ssh_tunnel_vpn_linux"
		}
		if panel {
			want = "ssh_tunnel_panel"
		}
		if goarch == "arm64" {
			want += "_arm64"
		}
	case "android":
		want = "ssh_tunnel.apk"
	}
	return want
}

type build struct {
	goos, goarch string
	vpn, panel   bool
}

var builds = []build{
	{"windows", "amd64", false, false}, {"windows", "amd64", true, false},
	{"linux", "amd64", false, false}, {"linux", "arm64", false, false},
	{"linux", "amd64", true, false}, {"linux", "arm64", true, false},
	{"linux", "amd64", false, true}, {"linux", "arm64", false, true},
	{"android", "arm64", false, false},
}

func withFlags(t *testing.T, vpn, panel bool) {
	t.Helper()
	oldV, oldP := VPN, Panel
	VPN, Panel = vpn, panel
	t.Cleanup(func() { VPN, Panel = oldV, oldP })
}

// Обновление с v1.10.27: старая программа находит в новом релизе свой файл
// по прежнему имени, новая — по новому.
func TestОбновлениеСо1_10_27НаходитФайл(t *testing.T) {
	names := releaseAssets(t)
	var rel Release
	for n := range names {
		rel.Assets = append(rel.Assets, Asset{Name: n, URL: "https://example.invalid/" + n})
	}
	// Файлы, которые старая панель качает на сервер по точному имени.
	for _, n := range []string{"ssh_tunnel_node", "ssh_tunnel_node_arm64", "ssh_tunnel_vpn_linux", "ssh_tunnel_vpn_linux_arm64", "meshd", "udprelay", "ssh_tunnel_windows.zip"} {
		if !names[n] {
			t.Errorf("в релизе нет %s — его ищет v1.10.27", n)
		}
	}
	for _, b := range builds {
		old := assetForV11027(b.goos, b.goarch, b.vpn, b.panel)
		if !names[old] {
			t.Errorf("%+v: v1.10.27 ищет %s, а в релизе его нет", b, old)
		}
		withFlags(t, b.vpn, b.panel)
		a, ok := AssetFor(rel, b.goos, b.goarch)
		want := NewPrefix + strings.TrimPrefix(old, OldPrefix)
		if !ok || a.Name != want {
			t.Errorf("%+v: новая версия выбрала %q, ждали %q", b, a.Name, want)
		}
	}
}

// Релиз до переименования (только ssh_tunnel*) новая версия тоже понимает:
// например, откат на старый номер через «обновить до версии».
func TestAssetForСтарыйРелиз(t *testing.T) {
	for _, b := range builds {
		old := assetForV11027(b.goos, b.goarch, b.vpn, b.panel)
		rel := Release{Assets: []Asset{{Name: "SHA256SUMS.txt"}, {Name: old}}}
		withFlags(t, b.vpn, b.panel)
		if a, ok := AssetFor(rel, b.goos, b.goarch); !ok || a.Name != old {
			t.Errorf("%+v: в старом релизе выбрано %q, ждали %q", b, a.Name, old)
		}
	}
	if _, ok := AssetFor(Release{Assets: []Asset{{Name: "ssh_tunnel.exe"}}}, "plan9", "amd64"); ok {
		t.Error("для неизвестной системы файл найден")
	}
}
