package mobile

import (
	"encoding/json"
	"testing"
	"time"

	"sshtunnel/internal/calendar"
)

func TestRangeJSONПустой(t *testing.T) {
	if got := rangeJSON(nil); got != "[]" {
		t.Fatalf("пустой список: %q", got)
	}
	if got := CalendarRangeJSON(10, 5); got != "[]" {
		t.Fatalf("перевёрнутый промежуток: %q", got)
	}
}

func TestRangeJSONПоля(t *testing.T) {
	st := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	got := rangeJSON([]calendar.Instance{{Cal: "c1", Href: "/a.ics", Title: "День", Start: st, End: st.Add(24 * time.Hour), AllDay: true, Color: "#ff0000"}})
	var items []rangeItem
	if err := json.Unmarshal([]byte(got), &items); err != nil || len(items) != 1 {
		t.Fatalf("%v %q", err, got)
	}
	e := items[0]
	if e.Title != "День" || e.Start != st.UnixMilli() || !e.AllDay || e.Color != "#ff0000" || e.Href != "/a.ics" || e.Cal != "c1" {
		t.Fatalf("поля: %+v", e)
	}
}
