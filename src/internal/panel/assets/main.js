applyI18N();
loadSession();
// Трафик и число сессий на сервере пересчитываются раз в 15 секунд
// (cmd/ssh_tunnel_panel, syncClientsLoop) — чаще обновлять нечего.
setInterval(() => {
  if ($('viewMain').hidden) return;
  loadStatus();
  loadClients();
  loadTraffic();
}, 15000);
