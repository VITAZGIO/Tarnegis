// main.js — последним: все разделы уже подписались на события каркаса.
applyI18N();
loadSession();
// Трафик и число сессий на сервере пересчитываются раз в 15 секунд
// (cmd/ssh_tunnel_panel, syncClientsLoop) — чаще обновлять нечего.
setInterval(() => {
  if ($('viewMain').hidden) return;
  loadStatus();
  loadClients();
  emitPanel('refresh');
}, 15000);
