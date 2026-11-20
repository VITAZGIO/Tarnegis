package panel

// «Убрать плагин» (ТЗ 29): страница плагинов как в программе — плагин
// добавляют из каталога («+») и убирают с его страницы или из меню
// карточки. Добавить — это включить его в настройках хоста
// (/api/node/config); убрать — выключить и, если попросили, стереть его
// данные во всех сетях сервера. Отдельной операции у хоста для этого нет:
// панель собирает её из тех, что уже есть (папки, буфер, копии, машины),
// поэтому работает и со старым хостом. Новая у хоста только одна —
// /files/clear (общая папка сети).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"sshtunnel/internal/nodeapi"
)

// oldNodeHostHint — хост плагинов не знает новой операции.
const oldNodeHostHint = "хост плагинов старый и не умеет стирать общую папку — нажми «Обновить» у хоста плагинов и повтори"

// pluginOffPatch — правка настроек, выключающая плагин; false — нет такого.
func pluginOffPatch(plugin string) (nodeapi.ConfigPatch, bool) {
	off := false
	var p nodeapi.ConfigPatch
	switch plugin {
	case nodeapi.PluginMirror:
		p.Mirror = &off
	case nodeapi.PluginFiles:
		// «Можно менять» без самого плагина не нужно: добавят снова — опять
		// только чтение, пока не разрешат.
		p.Files, p.FilesWrite = &off, &off
	case nodeapi.PluginClipboard:
		p.Clipboard = &off
	case nodeapi.PluginBackup:
		p.Backup = &off
	case nodeapi.PluginMachines:
		p.Machines = &off
	case nodeapi.PluginAdblock:
		p.Adblock = &off
	case nodeapi.PluginSitecheck:
		p.Sitecheck = &off
	default:
		return p, false
	}
	return p, true
}

// RemovePlugin выключает плагин; deleteData — стереть и его данные во всех
// сетях сервера (папки, общую папку, буфер, копии, историю машин, свой
// список блокировки). Сначала выключается: устройства перестают писать,
// пока данные стираются. Ошибка посередине — то, что успело стереться,
// стёрто, остальное осталось; повтор доделает.
func (m *NodeManager) RemovePlugin(ctx context.Context, plugin string, deleteData bool) (nodeapi.State, error) {
	patch, ok := pluginOffPatch(plugin)
	if !ok {
		return nodeapi.State{}, fmt.Errorf("нет такого плагина: %q", plugin)
	}
	st, err := m.api.SetConfig(ctx, patch)
	if err != nil || !deleteData {
		return st, err
	}
	if err := m.deletePluginData(ctx, plugin, st); err != nil {
		return st, err
	}
	return m.api.State(ctx)
}

func (m *NodeManager) deletePluginData(ctx context.Context, plugin string, st nodeapi.State) error {
	var errs []string
	fail := func(what string, err error) {
		if err != nil {
			errs = append(errs, what+": "+err.Error())
		}
	}
	switch plugin {
	case nodeapi.PluginAdblock:
		// Список один на сервер: свои источники и записи панели.
		_, err := m.api.SetAdblock(ctx, nodeapi.AdblockConf{Sources: []string{}, Allow: []string{}, Deny: []string{}})
		fail("список блокировки", err)
	case nodeapi.PluginSitecheck:
		// Данных нет: проверка ничего не хранит.
	}
	for _, n := range st.Networks {
		switch plugin {
		case nodeapi.PluginMirror:
			for _, f := range n.Folders {
				fail("папка «"+f.Name+"»", m.api.RemoveFolder(ctx, nodeapi.FolderReq{Net: n.ID, ID: f.ID, DeleteData: true}))
			}
		case nodeapi.PluginFiles:
			if n.FilesBytes == 0 {
				continue
			}
			err := m.api.ClearFiles(ctx, n.ID)
			if err != nil && (strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), http.StatusText(http.StatusNotFound))) {
				return errors.New(oldNodeHostHint)
			}
			fail("общая папка", err)
		case nodeapi.PluginClipboard:
			if n.Clipboard.Items == 0 {
				continue
			}
			fail("буфер", m.api.ClearClipboard(ctx, n.ID))
		case nodeapi.PluginBackup:
			for _, b := range n.Backups {
				fail("копия «"+b.Name+"»", m.api.DeleteBackup(ctx, nodeapi.BackupReq{Net: n.ID, Scope: b.Scope, Device: b.Device, Set: b.Set}))
			}
		case nodeapi.PluginMachines:
			if n.Machines == nil {
				continue
			}
			fail("история машин", m.api.ClearMachines(ctx, n.ID))
		}
	}
	if len(errs) > 0 {
		return errors.New("плагин выключен, но не всё стёрлось — " + strings.Join(errs, "; "))
	}
	return nil
}

// handleNodePluginRemove — POST /api/node/plugin/remove
// {"plugin":"mirror","deleteData":true} → State.
func (s *Server) handleNodePluginRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Plugin     string `json:"plugin"`
		DeleteData bool   `json:"deleteData"`
	}
	if !decodePost(w, r, &req) {
		return
	}
	st, err := s.node.RemovePlugin(r.Context(), req.Plugin, req.DeleteData)
	replyErr(w, err, st)
}
