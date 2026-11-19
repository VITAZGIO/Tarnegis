package panel

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// ТЗ 22: ролей у серверов нет — слова «главный сервер», «побочный», «роль
// сервера» не встречаются ни в строках страницы панели, ни в Книге панели.
// («Главный экран» — о другом и разрешён.)
var roleWords = regexp.MustCompile(`(?i)главн\S*\s+(\S+\s+)?сервер|побочн|рол\S*\s+(этого\s+)?сервер|main server|secondary (server|one)|"secondary"`)

func TestНетСловОРоляхСерверов(t *testing.T) {
	raw, err := pageHTML()
	if err != nil {
		t.Fatal(err)
	}
	page := string(raw)
	start := strings.Index(page, "const I18N = {")
	if start < 0 {
		t.Fatal("нет таблицы переводов")
	}
	end := strings.Index(page[start:], "\n};")
	tables := page[start : start+end]
	// Таблица простого доступа и прочие строки страницы — тоже.
	for _, chunk := range []string{tables, page[start+end:]} {
		for _, line := range strings.Split(chunk, "\n") {
			l := strings.TrimSpace(line)
			if strings.HasPrefix(l, "//") || strings.HasPrefix(l, "/*") || strings.HasPrefix(l, "<!--") {
				continue
			}
			if m := roleWords.FindString(l); m != "" && !strings.Contains(l, "MeshRole") {
				t.Errorf("на странице «%s»: %s", m, l)
			}
		}
	}
	err = fs.WalkDir(bookText, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(bookText, path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(data), "\n") {
			if m := roleWords.FindString(line); m != "" {
				t.Errorf("в Книге %s «%s»: %s", path, m, line)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
