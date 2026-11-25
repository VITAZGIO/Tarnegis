package panel

import (
	"os"
	"strings"
	"testing"
)

// ТЗ 51: «Добавить устройство» сразу отмечает все серверы сети. Раньше
// отмечался только этот (lockSelf → !!s.self), приглашение уходило с
// servers=[этот], и телефон получал в профиле один сервер вместо всех.
func TestНовоеУстройствоСоВсемиСерверами(t *testing.T) {
	data, err := os.ReadFile("assets/sections/devices.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	if strings.Contains(src, "lockSelf ? !!s.self") {
		t.Fatal("новое устройство снова получает только этот сервер")
	}
	if !strings.Contains(src, "cb.checked = (lockSelf && s.self) || !selected || selected.includes(s.id);") {
		t.Fatal("галочки серверов нового устройства не «все»")
	}
}
