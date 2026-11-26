package main

import (
	"html"
	"strings"
)

// waitPage — страница окна, пока служба не ответила (ТЗ 32): «Служба
// запускается…» с крутилкой; через svcWaitLimit — причина, путь к журналу и
// кнопка «Перезапустить службу». Служба ответила — страница перезагружается
// и показывает программу. Своя, маленькая: интерфейс программы отдаёт служба,
// а её как раз нет.
func waitPage(lang, token string) string {
	t := waitText["ru"]
	if lang == "en" {
		t = waitText["en"]
	}
	r := strings.NewReplacer(
		"{{lang}}", html.EscapeString(lang),
		"{{token}}", html.EscapeString(token),
		"{{title}}", t[0], "{{starting}}", t[1], "{{failed}}", t[2],
		"{{hint}}", t[3], "{{restart}}", t[4], "{{restarting}}", t[5],
		"{{state}}", t[6], "{{log}}", t[7], "{{restartFailed}}", t[8],
	)
	return r.Replace(waitHTML)
}

var waitText = map[string][9]string{
	"ru": {
		"Tarnegis VPN",
		"Служба запускается…",
		"Служба Tarnegis VPN не отвечает",
		"Окно продолжает ждать: как только служба ответит, программа откроется сама.",
		"Перезапустить службу",
		"Перезапускаю службу — подтверди запрос Windows…",
		"Состояние службы:",
		"Журнал службы:",
		"Перезапуск не удался:",
	},
	"en": {
		"Tarnegis VPN",
		"Starting the service…",
		"The Tarnegis VPN service does not answer",
		"The window keeps waiting: the program opens by itself as soon as the service answers.",
		"Restart the service",
		"Restarting the service — confirm the Windows prompt…",
		"Service state:",
		"Service log:",
		"Restart failed:",
	},
}

const waitHTML = `<!doctype html>
<html lang="{{lang}}"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{title}}</title>
<style>
:root{--bg:#0f1420;--fg:#e6e9ef;--mut:#8a93a6;--acc:#3b82f6;--err:#f87171;--card:#161d2c}
@media (prefers-color-scheme: light){:root{--bg:#f4f6fa;--fg:#1b2130;--mut:#5d6578;--card:#fff}}
html,body{margin:0;height:100%;background:var(--bg);color:var(--fg);font:14px/1.45 "Segoe UI",system-ui,sans-serif}
main{min-height:100%;display:flex;flex-direction:column;align-items:center;justify-content:center;padding:24px;box-sizing:border-box;text-align:center;gap:14px}
.spin{width:38px;height:38px;border:3px solid color-mix(in srgb,var(--acc) 25%,transparent);border-top-color:var(--acc);border-radius:50%;animation:s 0.9s linear infinite}
@keyframes s{to{transform:rotate(360deg)}}
h1{font-size:17px;margin:0}
.mut{color:var(--mut);font-size:12.5px;max-width:340px;word-break:break-word}
.err{color:var(--err)}
button{background:var(--acc);color:#fff;border:0;border-radius:8px;padding:9px 16px;font:inherit;cursor:pointer}
button:disabled{opacity:.6;cursor:default}
.box{background:var(--card);border-radius:10px;padding:12px 14px;max-width:360px;text-align:left}
[hidden]{display:none!important}
</style></head><body><main>
<div class="spin" id="spin"></div>
<h1 id="head">{{starting}}</h1>
<div id="fail" hidden>
  <div class="box">
    <div class="err" id="why"></div>
    <div class="mut">{{state}} <span id="state"></span></div>
    <div class="mut">{{log}} <span id="log"></span></div>
  </div>
  <p class="mut">{{hint}}</p>
  <button id="restart">{{restart}}</button>
  <p class="mut" id="rs"></p>
</div>
</main>
<script>
const $=id=>document.getElementById(id);
$('restart').onclick=()=>{fetch('/svcwait/restart?t={{token}}',{method:'POST'});$('restart').disabled=true;};
async function tick(){
  try{
    const s=await (await fetch('/svcwait/status',{cache:'no-store'})).json();
    if(s.ready){location.reload();return;}
    $('fail').hidden=!s.failed;
    $('head').textContent=s.failed?'{{failed}}':'{{starting}}';
    $('head').className=s.failed?'err':'';
    $('why').textContent=s.error||'';
    $('state').textContent=s.state||'';
    $('log').textContent=s.log||'';
    const running=s.restart==='running';
    $('restart').disabled=running;
    $('rs').textContent=running?'{{restarting}}':(s.restart?'{{restartFailed}} '+s.restart.replace(/^failed: /,''):'');
  }catch(e){}
  setTimeout(tick,700);
}
tick();
</script></body></html>`
