package netmap

import (
	"encoding/json"
	"testing"
)

// ТЗ 32: общая карта сервера несёт линии сервер—сервер по id; карта
// программы рисует их по именам серверов.
func TestServerLinksПоИменам(t *testing.T) {
	var out map[string]any
	json.Unmarshal([]byte(`{"devices":[],
		"servers":[{"id":"s1","name":"Frankfurt"},{"id":"s2","name":"London"},{"id":"s3","name":"Amsterdam"}],
		"serverLinks":[{"a":"s1","b":"s2","online":true,"rttMs":31},{"a":"s1","b":"s3","online":false},{"a":"s1","b":"zz"}]}`), &out)
	got := parseServerLinks(out)
	if len(got) != 2 || got[0].A != "Frankfurt" || got[0].B != "London" || got[0].RTTMs != 31 || got[1].Online {
		t.Fatalf("%+v", got)
	}
	var old map[string]any
	json.Unmarshal([]byte(`{"devices":[]}`), &old)
	if parseServerLinks(old) != nil {
		t.Fatal("старая панель: ждали nil")
	}
}
