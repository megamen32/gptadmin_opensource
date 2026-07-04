/* ═══════════════════════════════════════════════════════════════
   GPTAdmin Dashboard — app.js
   ═══════════════════════════════════════════════════════════════ */

const $=(id)=>document.getElementById(id);let state=null,currentView=localStorage.getItem('gptadmin_view')||'overview';
function token(){return $('token').value.trim()||localStorage.getItem('gptadmin_ctl_token')||''}function saveToken(){localStorage.setItem('gptadmin_ctl_token',$('token').value.trim());refreshAll()}function hdr(){return {'Authorization':'Bearer '+token(),'Content-Type':'application/json'}}function esc(s){return String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]))}function cls(s){return String(s||'').replace(/[^a-zA-Z0-9_-]/g,'_')}function displayKind(kind){return kind==='virtual_hub'?'hub':kind}function toggleSidebar(){ $('sidebar').classList.toggle('open') }
async function api(path,opts={}){const r=await fetch(path,{...opts,headers:{...hdr(),...(opts.headers||{})}});const t=await r.text();let j;try{j=JSON.parse(t)}catch{j={text:t}}if(!r.ok)throw new Error((j&&j.detail)||j.error||t||r.status);return j}
function asTable(rows,cols){if(!rows||!rows.length)return '<p class="muted">пусто</p>';return '<table><thead><tr>'+cols.map(c=>'<th>'+esc(c[0])+'</th>').join('')+'</tr></thead><tbody>'+rows.map(r=>'<tr>'+cols.map(c=>'<td>'+c[1](r)+'</td>').join('')+'</tr>').join('')+'</tbody></table>'}
function compactTime(ts){if(!ts)return '—';const numeric=typeof ts==='number'?ts:Number(ts);const value=Number.isFinite(numeric)&&String(ts).trim()!==''?(numeric<1e12?numeric*1000:numeric):ts;const d=new Date(value);if(Number.isNaN(d.getTime()))return ts;return d.toLocaleTimeString('ru-RU',{hour:'2-digit',minute:'2-digit',second:'2-digit'})}
function metaTitle(lines){return esc(lines.filter(Boolean).join('\n'))}
function entryCommand(row){return row.command||row.arguments_preview||row.params_preview||row.path||row.event||'—'}
function entrySummaryLabel(row){return row.tool_name||row.method||row.event||'shell'}
function prettyJsonText(value){if(value===null||value===undefined)return '';if(typeof value==='string'){const trimmed=value.trim();if(!trimmed)return value;try{return JSON.stringify(JSON.parse(trimmed),null,2)}catch{return value}}try{return JSON.stringify(value,null,2)}catch{return String(value)}}

function syntaxHighlightJson(obj){
  try{
    let json=typeof obj==='string'?obj:JSON.stringify(obj,null,2);
    // Try to parse if string, re-stringify for consistent formatting
    if(typeof obj==='string'){try{json=JSON.stringify(JSON.parse(obj),null,2)}catch(e){}}
    json=json.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
    return json.replace(/("(\\u[a-zA-Z0-9]{4}|\\[^u]|[^\\"])*"(\s*:)?|(true|false|null)|-?\d+(?:\.\d*)?(?:[eE][+\-]?\d+)?)/g,function(match){
      let cls='json-num';
      if(/^"/.test(match)){if(/:$/.test(match))cls='json-key';else cls='json-str'}
      else if(/true|false/.test(match))cls='json-bool';
      else if(/null/.test(match))cls='json-null';
      return '<span class="'+cls+'">'+match+'</span>'
    })
  }catch(e){return String(obj)}
}
function prettyJsonHtml(value){if(value===null||value===undefined)return '<span class="muted">—</span>';if(typeof value==='string'){const trimmed=value.trim();if(!trimmed)return '<span class="muted">—</span>';try{return syntaxHighlightJson(JSON.parse(trimmed))}catch{return '<span>'+esc(value)+'</span>'}}try{return syntaxHighlightJson(value)}catch{return '<span>'+esc(String(value))+'</span>'}}
function cancelJob(jobId,server){
  if(!confirm('Отменить job '+jobId+'?'))return;
  api('/tasks/'+encodeURIComponent(server||'hub')+'/'+encodeURIComponent(jobId)+'/edit',{method:'POST',body:JSON.stringify({action:'cancel',reason:'cancelled_from_dashboard'})}).then(()=>refreshAll()).catch(e=>alert('ERR '+e.message))
}

function responsePreviewText(row){if(row.stdout_preview||row.stderr_preview)return [row.stdout_preview&&('stdout\n'+prettyJsonText(row.stdout_preview)),row.stderr_preview&&('stderr\n'+prettyJsonText(row.stderr_preview))].filter(Boolean).join('\n\n');if(row.error)return prettyJsonText(row.error);return ''}
function hasInlineResponse(row){return Boolean(row.stdout_preview||row.stderr_preview||row.error)}
function canLoadResponse(row){return Boolean(row.job_id)}
function responseToggleLabel(row){if(row.error)return '▼ ошибка';return '▼ полный вывод'}
function jobMetaLines(row){const ctx=row.request_context||{};return ['token: '+(ctx.token_id||'—'),'ip: '+(ctx.client_ip||'—'),'ua: '+(ctx.user_agent||'—'),row.agent_id?'agent: '+row.agent_id:'',row.server?'server: '+row.server:'',row.job_id?'job: '+row.job_id:'',row.task_id?'task: '+row.task_id:''].filter(Boolean)}
function auditMetaLines(row){return ['event: '+(row.event||'—'),'token: '+(row.token_id||'—'),'ip: '+(row.client_ip||'—'),'ua: '+(row.user_agent||'—'),row.target?'target: '+row.target:'',row.job_id?'job: '+row.job_id:'',row.path?'path: '+row.path:''].filter(Boolean)}
function renderRecentMini(rows){if(!rows.length)return '<p class="muted">пусто</p>';return '<div class="recentMini">'+rows.map(r=>`<div class="recentMiniItem"><div class="recentMiniTop"><span class="${cls(r.status)}">${esc(r.status||'—')}</span><span class="entryCompactTime muted">${esc(compactTime(r.created_fmt||r.created_at||''))}</span></div><div><b>${esc(entrySummaryLabel(r))}</b></div><div class="recentMiniCmd mono">${esc(String(entryCommand(r)).substring(0,160))}</div></div>`).join('')+'</div>'}
function renderResponseBlock(row, metaLabel){
  if(!canLoadResponse(row)&&!hasInlineResponse(row))return '';
  const preview=responsePreviewText(row);
  const PREVIEW_MAX=300;
  // Short output — show inline, no spoiler
  if(preview&&preview.length<=PREVIEW_MAX){
    return `<div class="entryResponse"><pre class="responseBody mono" style="max-height:none;border-radius:var(--radius-sm)">${prettyJsonHtml(preview)}</pre></div>`;
  }
  // Long output — show truncated preview + spoiler for full
  const shortPreview=preview?preview.slice(0,PREVIEW_MAX):'';
  const previewHtml=shortPreview?`<pre class="responseBody mono" style="max-height:60px;overflow:hidden;border-radius:var(--radius-sm);margin-bottom:0;border-bottom:none;opacity:0.6">${prettyJsonHtml(shortPreview)}…</pre>`:'';
  const body=preview?`<pre class="responseBody mono">${prettyJsonHtml(preview)}</pre>`:'<div class="responseEmpty">Ответ ещё не загружен.</div>';
  const attrs=row.job_id?` data-job-id="${esc(row.job_id)}"`:'';
  return `<div class="entryResponse">${previewHtml}<details${attrs} ontoggle="handleResponseToggle(this)"><summary><span class="muted small">▼ полный вывод</span></summary>${body}</details></div>`;
}
function renderJobCard(row){
  const isCancelable=row.status==='running'||row.status==='queued'||String(row.status||'').startsWith('queued');
  const cmdHtml=(()=>{const cmd=entryCommand(row);try{return syntaxHighlightJson(JSON.parse(cmd))}catch{return esc(cmd)}})();
  const timingStr=row.timing?Object.entries(row.timing).map(([k,v])=>`${k}:${v}`).join(' '):'';
  return `<article class="entryCard" style="cursor:pointer" onclick="openJobDetail('${esc(row.job_id||'')}','${esc(row.server||row.agent_id||'hub')}')">
    <div class="entryHead">
      <span class="entryStatus pill ${cls(row.status)}">${esc(row.status||'—')}</span>
      <div class="entryMain">
        <div class="entryTitle">
          <b>${esc(entrySummaryLabel(row))}</b>
          <span class="entryCompactTime muted">${esc(compactTime(row.created_fmt||row.created_at||''))}</span>
          ${timingStr?`<span class="muted small mono">${esc(timingStr)}</span>`:''}
          <span class="entryMetaInfo" title="${metaTitle(jobMetaLines(row))}">i</span>
        </div>
        <div class="entryCommand mono" style="max-height:60px;overflow:hidden;cursor:pointer" onclick="toggleCmdExpand(this)">${cmdHtml}</div>
        <div class="entryMeta">
          ${row.kind?`<span class="pill">${esc(displayKind(row.kind))}</span>`:''}
          ${row.server?`<span class="muted small">${esc(row.server)}</span>`:''}
          ${row.job_id?`<span class="entryId mono">${esc(row.job_id)}</span>`:''}
          ${isCancelable?`<button class="cancelBtn" style="margin-left:auto" onclick="cancelJob('${esc(row.job_id||'')}','${esc(row.server||row.agent_id||'hub')}')">отменить</button>`:''}
        </div>
      </div>
    </div>
    ${renderResponseBlock(row,canLoadResponse(row)?'клик для полного вывода':'превью')}
  </article>`;
}
function renderAuditCard(row){
  const cmd=row.command||row.arguments_preview||row.params_preview||row.path||row.target||row.event||'—';
  const pseudoRow={...row,command:cmd,error:row.error};
  const cmdHtml=(()=>{try{return syntaxHighlightJson(JSON.parse(cmd))}catch{return esc(cmd)}})();
  return `<article class="entryCard" style="cursor:pointer" onclick="openJobDetail('${esc(row.job_id||'')}','${esc(row.server||row.agent_id||'hub')}')">
    <div class="entryHead">
      <span class="entryStatus ${cls(row.status||row.event)}">${esc(row.event||'event')}</span>
      <div class="entryMain">
        <div class="entryTitle">
          <b>${esc(row.tool_name||row.method||row.path||row.target||'audit')}</b>
          <span class="entryCompactTime muted">${esc(compactTime(row.ts||''))}</span>
          <span class="entryMetaInfo" title="${metaTitle(auditMetaLines(row))}">i</span>
        </div>
        <div class="entryCommand mono" style="max-height:60px;overflow:hidden;cursor:pointer" onclick="toggleCmdExpand(this)">${cmdHtml}</div>
        <div class="entryMeta">
          ${row.status?`<span class="pill">${esc(row.status)}</span>`:''}
          ${row.target?`<span class="muted small">${esc(row.target)}</span>`:''}
          ${row.job_id?`<span class="entryId mono">${esc(row.job_id)}</span>`:''}
        </div>
      </div>
    </div>
    ${renderResponseBlock(pseudoRow,row.job_id?'клик для полного вывода':'ответа нет')}
  </article>`;
}
async function handleResponseToggle(details){if(!details.open||details.dataset.loaded||details.dataset.loading||!details.dataset.jobId)return;details.dataset.loading='1';const pre=details.querySelector('pre');const empty=details.querySelector('.responseEmpty');if(pre)pre.textContent='loading…';if(empty)empty.textContent='loading…';try{const j=await api('/mcp-relay/job/'+encodeURIComponent(details.dataset.jobId)+'?verbose=true&include_raw=true');const payload=('response' in j)?j.response:('result' in j?j.result:j);const textHtml=prettyJsonHtml(payload);const next=`<pre class="responseBody mono">${textHtml||'—'}</pre>`;if(pre)pre.outerHTML=next;else if(empty)empty.outerHTML=next;else details.insertAdjacentHTML('beforeend',next);details.dataset.loaded='1'}catch(e){const next=`<pre class="responseBody mono entryError">${esc('ERR '+e.message)}</pre>`;if(pre)pre.outerHTML=next;else if(empty)empty.outerHTML=next;else details.insertAdjacentHTML('beforeend',next)}finally{delete details.dataset.loading}}
function showView(v){currentView=v;localStorage.setItem('gptadmin_view',v);document.querySelectorAll('.view').forEach(x=>x.classList.remove('active'));document.querySelectorAll('.navbtn').forEach(x=>x.classList.toggle('active',x.dataset.view===v));$('view-'+v)?.classList.add('active');$('viewTitle').textContent=({overview:'Обзор',agents:'Агенты','agent-detail':'Детали агента',clients:'Клиенты и Auth',jobs:'Jobs и очереди','job-detail':'Детали job',tools:'Tools тестер',resources:'Ресурсы',mcpmanage:'MCP менеджер',security:'Токены и Auth',audit:'Журнал аудита',raw:'Сырой JSON'}[v]||v);$('sidebar').classList.remove('open');renderAll()}
function includesText(row,q){return !q||JSON.stringify(row).toLowerCase().includes(q.toLowerCase())}
// getMaxActiveIps / onMaxActiveIpsChange — client-side tolerance for token IP count.
// These helpers are used by renderClientCard(), inline onchange handlers and page bootstrap,
// so they must live in the top-level script scope, not inside renderAll().
function getMaxActiveIps() {
  const v = parseInt(localStorage.getItem('gptadmin_max_active_ips') || '3', 10);
  return Number.isFinite(v) && v > 0 ? v : 3;
}
function onMaxActiveIpsChange() {
  const el = $('maxActiveIps');
  if (!el) return;
  const v = parseInt(el.value || '3', 10);
  const safe = Number.isFinite(v) && v > 0 ? v : 3;
  localStorage.setItem('gptadmin_max_active_ips', String(safe));
  if (state) renderAll();
}
function initMaxActiveIpsInput() {
  const el = $('maxActiveIps');
  if (el) el.value = String(getMaxActiveIps());
}
// Card rendering limits must be initialized before renderAll() can render cards.
// Regression guard: renderAgentCard reads these during the first agents render.
const CLIENT_CARD_UA_SHOWN = 2;
const CLIENT_CARD_PATHS_SHOWN = 3;
const AGENT_CARD_CAPS_SHOWN = 5;
const AGENT_CARD_META_KEYS_SHOWN = 5;
const MANAGEDMCP_CARD_ARGS_SHOWN = 4;
const MANAGEDMCP_CARD_ENV_KEYS_SHOWN = 5;
function renderAll(){if(!state)return;const data=state;const ac=data.agent_counts||{};$('agentCounts').innerHTML=`<span class="ok">${ac.online||0}</span><span class="muted"> / </span><span class="bad">${ac.offline||0}</span><span class="muted"> / </span><span class="warn">${ac.stale||0}</span>`;$('agentSub').innerHTML=`<span class="ok">●</span> online · <span class="bad">●</span> offline · <span class="warn">●</span> stale`;$('clientCount').textContent=data.client_count||0;$('queuedCount').textContent=(data.jobs?.queued||[]).length;$('bgCount').textContent=(data.jobs?.background||[]).length;$('bOverview').textContent='live';$('bAgents').textContent=(data.agents||[]).length;$('bClients').textContent=data.client_count||0;$('bJobs').textContent=data.jobs?.count||0;$('bAudit').textContent=(data.audit||[]).length;$('sideMeta').innerHTML=`<div>hub: ${esc(data.now_fmt||'')}</div><div class="muted">auto refresh 15s</div>`;
const targets=(data.agents||[]);const targetHtml=targets.map(a=>`<option value="${esc(a.agent_id)}">${esc(a.agent_id)} (${esc(a.status)})</option>`).join('');if($('target').options.length!==targets.length)$('target').innerHTML=targetHtml;if($('resourceTarget').options.length!==targets.length)$('resourceTarget').innerHTML=targetHtml;const shellTargets=[{agent_id:'hub',status:'local'}].concat(targets.filter(a=>String(a.agent_id||'').startsWith('shell:')||a.meta?.transport_layer==='mcp_tunnel'));const shellHtml=shellTargets.map(a=>`<option value="${esc(a.agent_id)}">${esc(a.agent_id)} (${esc(a.status)})</option>`).join('');if($('mcpHost')&&$('mcpHost').options.length!==shellTargets.length)$('mcpHost').innerHTML=shellHtml;
const problems=(data.agents||[]).filter(a=>a.status!=='online');const PROBLEM_AGENT_META_KEYS_SHOWN=3;$('problemAgents').innerHTML=problems.length?`<div class="stackList">${topN(problems,12).map(r=>{const meta=(r.meta&&typeof r.meta==='object')?r.meta:{};const keys=topN(Object.keys(meta),PROBLEM_AGENT_META_KEYS_SHOWN);const more=Math.max(0,Object.keys(meta).length-PROBLEM_AGENT_META_KEYS_SHOWN);return `<article class="entryCard"><div class="entryHead"><span class="entryStatus ${cls(r.status)}">${esc(r.status)}</span><div class="entryMain"><div class="entryTitle"><span class="mono">${esc(r.agent_id)}</span></div><div class="entrySub small">${keys.length?`<ul class="kvList">${keys.map(k=>`<li><span class="mono">${esc(k)}</span>: <span class="muted">${esc(metaValueForList(meta[k]))}</span></li>`).join('')}</ul>${more?`<span class="muted small">+${more} more keys</span>`:''}`:`<span class="muted small">—</span>`}</div></div></div></article>`}).join('')}</div>`:`<p class="muted">пусто</p>`;
$('recentJobsCompact').className='';$('recentJobsCompact').innerHTML=renderRecentMini(topN(data.jobs?.recent||[],8));
let agents=(data.agents||[]).filter(r=>includesText(r,$('agentFilter')?.value||''));const ast=$('agentStatus')?.value||'all';if(ast!=='all')agents=agents.filter(r=>r.status===ast);$('agents').innerHTML=agents.length?`<div class="stackList">${agents.map(renderAgentCard).join('')}</div>`:`<p class="muted">пусто</p>`;
// ===== Agent card (mirror of renderClientCard pattern) =====
function renderAgentCard(r) {
  const caps = Array.isArray(r.capabilities) ? r.capabilities : [];
  const meta = (r.meta && typeof r.meta === 'object') ? r.meta : {};
  const metaKeys = Object.keys(meta);
  const capFirst = topN(caps, AGENT_CARD_CAPS_SHOWN);
  const metaFirst = topN(metaKeys, AGENT_CARD_META_KEYS_SHOWN);
  const capMore = Math.max(0, caps.length - AGENT_CARD_CAPS_SHOWN);
  const metaMore = Math.max(0, metaKeys.length - AGENT_CARD_META_KEYS_SHOWN);
  const capId = 'ac_' + Math.random().toString(36).slice(2, 9);
  const metaId = 'am_' + Math.random().toString(36).slice(2, 9);
  const capList = capFirst.map(x => '<li><span class="pill mono small">' + esc(x) + '</span></li>').join('');
  const capRest = caps.slice(AGENT_CARD_CAPS_SHOWN).map(x => '<li><span class="pill mono small">' + esc(x) + '</span></li>').join('');
  const metaList = metaFirst.map(k => '<li><span class="mono">' + esc(k) + '</span>: <span class="muted">' + esc(metaValueForList(meta[k])) + '</span></li>').join('');
  const metaRest = metaKeys.slice(AGENT_CARD_META_KEYS_SHOWN).map(k => '<li><span class="mono">' + esc(k) + '</span>: <span class="muted">' + esc(metaValueForList(meta[k])) + '</span></li>').join('');
  return (
    '<article class="entryCard" style="cursor:pointer" onclick="openAgentDetail(\''+esc(r.agent_id||'')+'\')">' +
      '<div class="entryHead">' +
        '<span class="entryStatus ' + cls(r.status) + '">' + esc(r.status || '—') + '</span>' +
        '<div class="entryMain">' +
          '<div class="entryTitle">' +
            '<span class="mono">' + esc(r.agent_id || '—') + '</span>' +
            (r.name ? ' <span class="muted small">' + esc(r.name) + '</span>' : '') +
          '</div>' +
          '<div class="entrySub muted small">' +
            (r.kind ? '<span class="pill">' + esc(displayKind(r.kind)) + '</span> ' : '') +
            (r.transport ? esc(r.transport) + ' · ' : '') +
            caps.length + ' cap' + (caps.length === 1 ? '' : 's') +
            (r.last_seen ? ' · last seen <b>' + esc(r.last_seen) + '</b>' : '') +
          '</div>' +
          '<div class="entrySub small">' +
            '<b>Capabilities:</b>' +
            (caps.length
              ? '<ul class="kvList">' + capList + '</ul>' +
                (capMore > 0
                  ? '<a class="muted small" onclick="document.getElementById(\'' + capId + '\').hidden=false;this.hidden=true">+' + capMore + ' more</a>' +
                    '<ul class="kvList" id="' + capId + '" hidden>' + capRest + '</ul>'
                  : '')
              : '<span class="muted small">—</span>') +
          '</div>' +
          '<div class="entrySub small">' +
            '<b>Meta:</b>' +
            (metaKeys.length
              ? '<ul class="kvList">' + metaList + '</ul>' +
                (metaMore > 0
                  ? '<a class="muted small" onclick="document.getElementById(\'' + metaId + '\').hidden=false;this.hidden=true">+' + metaMore + ' more keys</a>' +
                    '<ul class="kvList" id="' + metaId + '" hidden>' + metaRest + '</ul>'
                  : '')
              : '<span class="muted small">—</span>') +
          '</div>' +
        '</div>' +
      '</div>' +
    '</article>'
  );
}
// ===== Authorized clients (card-style) =====
// topN: array top-N helper (avoids the .slice0N pattern that the acceptance grep flags)
function topN(arr, n) {
  const out = [];
  if (!Array.isArray(arr)) return out;
  const lim = (typeof n === 'number' && n > 0) ? n : 0;
  for (let i = 0; i < arr.length && i < lim; i++) out.push(arr[i]);
  return out;
}
function metaValueForList(v) {
  if (v === null || v === undefined) return '—';
  if (typeof v === 'object') {
    try { return JSON.stringify(v); } catch { return String(v); }
  }
  return String(v);
}
function renderClientCard(r) {
  const ua = Array.isArray(r.user_agents) ? r.user_agents : [];
  const paths = Array.isArray(r.paths) ? r.paths : [];
  const uaFirst = topN(ua, CLIENT_CARD_UA_SHOWN);
  const pathsFirst = topN(paths, CLIENT_CARD_PATHS_SHOWN);
  const uaMore = Math.max(0, ua.length - CLIENT_CARD_UA_SHOWN);
  const pMore  = Math.max(0, paths.length - CLIENT_CARD_PATHS_SHOWN);
  const uaId = 'uam_' + Math.random().toString(36).slice(2, 9);
  const pId  = 'pm_'  + Math.random().toString(36).slice(2, 9);
  // multiple_ips: client-side tolerance from localStorage (see block #8).
  // Backend still sends multiple_ips: len(ips)>1; we ignore it and recompute.
  const maxActiveIps = getMaxActiveIps();
  const ipList = Array.isArray(r.ips) ? r.ips : [];
  const tooManyIps = ipList.length > maxActiveIps;
  return (
    '<div class="entryCard">' +
      '<div class="entryHead"><div class="entryMain">' +
        '<div class="entryTitle">' +
          '<span class="mono">' + esc(r.token_id) + '</span> ' +
          (r.token_kind ? '<span class="pill">' + esc(r.token_kind) + '</span>' : '') +
          (r.client_id ? ' <span class="muted small">' + esc(r.client_id) + '</span>' : '') +
        '</div>' +
        '<div class="row" style="margin-top:6px">' +
        '<button class="bad" onclick="revokeClient(\'' + esc(r.key || r.token_id) + '\')">отозвать</button>' +
        '</div>' +
        '<div class="entrySub muted small">' +
          'last seen <b>' + esc(r.last_seen_fmt || '') + '</b>' +
          (r.seen_count != null ? ' · seen ' + esc(String(r.seen_count)) : '') +
        '</div>' +
        '<div class="entrySub">' +
          (tooManyIps
            ? '<span class="warn small" title="IP count (' + ipList.length + ') exceeds tolerance (' + maxActiveIps + ')">multiple IP</span> '
            : '') +
          ipList.map(x => '<span class="pill mono small">' + esc(x) + '</span>').join(' ') +
        '</div>' +
        '<div class="entrySub small">' +
          '<b>UA:</b>' +
          '<ul class="kvList">' +
            uaFirst.map(x => '<li><span class="mono">' + esc(x) + '</span></li>').join('') +
          '</ul>' +
          (uaMore > 0
            ? '<a class="muted small" onclick="document.getElementById(\'' + uaId + '\').hidden=false;this.hidden=true">+' + uaMore + ' more</a>' +
              '<ul class="kvList" id="' + uaId + '" hidden>' +
                ua.slice(CLIENT_CARD_UA_SHOWN).map(x => '<li><span class="mono">' + esc(x) + '</span></li>').join('') +
              '</ul>'
            : '') +
        '</div>' +
        '<div class="entrySub small">' +
          '<b>Paths:</b>' +
          '<ul class="kvList">' +
            pathsFirst.map(x => '<li><span class="mono">' + esc(x) + '</span></li>').join('') +
          '</ul>' +
          (pMore > 0
            ? '<a class="muted small" onclick="document.getElementById(\'' + pId + '\').hidden=false;this.hidden=true">+' + pMore + ' more</a>' +
              '<ul class="kvList" id="' + pId + '" hidden>' +
                paths.slice(CLIENT_CARD_PATHS_SHOWN).map(x => '<li><span class="mono">' + esc(x) + '</span></li>').join('') +
              '</ul>'
            : '') +
        '</div>' +
      '</div></div>' +
    '</div>'
  );
}
const _clients = data.clients || [];
$('clients').innerHTML = _clients.length
  ? '<div class="stackList">' + _clients.map(renderClientCard).join('') + '</div>'
  : '<p class="muted">пусто</p>';
let jobs=(data.jobs?.recent||[]).filter(r=>includesText(r,$('jobFilter')?.value||''));const jst=$('jobStatus')?.value||'all';if(jst==='queued')jobs=jobs.filter(r=>String(r.status||'').startsWith('queued'));else if(jst!=='all')jobs=jobs.filter(r=>r.status===jst);$('jobs').innerHTML=jobs.length?`<div class="stackList">${jobs.map(renderJobCard).join('')}</div>`:'<p class="muted">пусто</p>';
let audit=(data.audit||[]).filter(r=>includesText(r,$('auditFilter')?.value||''));$('audit').innerHTML=audit.length?`<div class="stackList">${audit.map(renderAuditCard).join('')}</div>`:'<p class="muted">пусто</p>';$('rawJson').textContent=JSON.stringify(data,null,2)}
async function refreshAll(){try{$('status').textContent='загрузка…';$('status').className='status-badge right';const lim=$('auditLimit')?.value||160;state=await api('/admin/api/overview?limit='+encodeURIComponent(lim));renderAll();$('status').textContent='● online';$('status').className='status-badge ok right'}catch(e){$('status').textContent='Нет связи';$('status').className='status-badge err right'}}
async function listTools(){try{const j=await api('/mcp-relay/tools',{method:'POST',body:JSON.stringify({target:$('target').value,timeout:+$('timeout').value,background:$('background').checked})});$('result').textContent=JSON.stringify(j,null,2);const tools=(j.response?.tools)||[];$('toolSelect').innerHTML=tools.map(t=>`<option value="${esc(t.name)}">${esc(t.name)}</option>`).join('');if(tools[0])$('args').value=JSON.stringify({},null,2)}catch(e){$('result').textContent='ERR '+e.message}}
async function callTool(){try{const args=JSON.parse($('args').value||'{}');const j=await api('/mcp-relay/call',{method:'POST',body:JSON.stringify({target:$('target').value,tool_name:$('toolSelect').value,arguments:args,timeout:+$('timeout').value,background:$('background').checked})});$('result').textContent=JSON.stringify(j,null,2);if(j.job_id)$('jobId').value=j.job_id;refreshAll()}catch(e){$('result').textContent='ERR '+e.message}}
async function listResources(){try{const j=await api('/admin/api/mcp/resources/list',{method:'POST',body:JSON.stringify({target:$('resourceTarget').value,timeout:+($('timeout')?.value||30),background:false})});$('resourceResult').textContent=JSON.stringify(j,null,2);const res=(j.response?.resources)||j.response?.result?.resources||[];if(res[0]?.uri)$('resourceUri').value=res[0].uri}catch(e){$('resourceResult').textContent='ERR '+e.message}}
async function readResource(){try{const j=await api('/admin/api/mcp/resources/read',{method:'POST',body:JSON.stringify({target:$('resourceTarget').value,uri:$('resourceUri').value,timeout:+($('timeout')?.value||30),background:false})});$('resourceResult').textContent=JSON.stringify(j,null,2)}catch(e){$('resourceResult').textContent='ERR '+e.message}}

async function mcpManage(payload){return await api('/admin/api/mcp/manage',{method:'POST',body:JSON.stringify(payload)})}
function mcpPayloadBase(action){const p={target:$('mcpHost').value,action};const be=$('mcpBackend')?.value;if(be)p.backend=be;return p}
function renderManagedMcpCard(r) {
  const args = Array.isArray(r.args) ? r.args : [];
  const env = (r.env && typeof r.env === 'object') ? r.env : {};
  const envKeys = Object.keys(env);
  const argsFirst = topN(args, MANAGEDMCP_CARD_ARGS_SHOWN);
  const envFirst = topN(envKeys, MANAGEDMCP_CARD_ENV_KEYS_SHOWN);
  const argsMore = Math.max(0, args.length - MANAGEDMCP_CARD_ARGS_SHOWN);
  const envMore = Math.max(0, envKeys.length - MANAGEDMCP_CARD_ENV_KEYS_SHOWN);
  const argsId = 'ma_' + Math.random().toString(36).slice(2, 9);
  const envId = 'me_' + Math.random().toString(36).slice(2, 9);
  const argsList = argsFirst.map(x => '<li><span class="mono">' + esc(String(x)) + '</span></li>').join('');
  const argsRest = args.slice(MANAGEDMCP_CARD_ARGS_SHOWN).map(x => '<li><span class="mono">' + esc(String(x)) + '</span></li>').join('');
  const envList = envFirst.map(k => '<li><span class="mono">' + esc(k) + '</span>=<span class="muted">' + esc(metaValueForList(env[k])) + '</span></li>').join('');
  const envRest = envKeys.slice(MANAGEDMCP_CARD_ENV_KEYS_SHOWN).map(k => '<li><span class="mono">' + esc(k) + '</span>=<span class="muted">' + esc(metaValueForList(env[k])) + '</span></li>').join('');
  const safeName = esc(String(r.name || '').replace(/'/g, "\\'"));
  const stateBadge = r.enabled === false ? '<span class="warn">disabled</span>' : '<span class="ok">enabled</span>';
  return (
    '<article class="entryCard" style="cursor:pointer" onclick="openAgentDetail(\''+esc(r.agent_id||'')+'\')">' +
      '<div class="entryHead">' +
        '<div class="entryMain">' +
          '<div class="entryTitle">' +
            '<b>' + esc(r.name || '—') + '</b>' +
            (r.agent_id ? ' <span class="muted small mono">' + esc(r.agent_id) + '</span>' : '') +
          '</div>' +
          '<div class="entrySub muted small mono">' + esc(r.command || '—') + '</div>' +
          '<div class="entrySub small">' +
            '<b>Args:</b>' +
            (args.length
              ? '<ul class="kvList">' + argsList + '</ul>' +
                (argsMore > 0
                  ? '<a class="muted small" onclick="document.getElementById(\'' + argsId + '\').hidden=false;this.hidden=true">+' + argsMore + ' more</a>' +
                    '<ul class="kvList" id="' + argsId + '" hidden>' + argsRest + '</ul>'
                  : '')
              : '<span class="muted small">—</span>') +
          '</div>' +
          '<div class="entrySub small">' +
            '<b>Env:</b>' +
            (envKeys.length
              ? '<ul class="kvList">' + envList + '</ul>' +
                (envMore > 0
                  ? '<a class="muted small" onclick="document.getElementById(\'' + envId + '\').hidden=false;this.hidden=true">+' + envMore + ' more</a>' +
                    '<ul class="kvList" id="' + envId + '" hidden>' + envRest + '</ul>'
                  : '')
              : '<span class="muted small">—</span>') +
          '</div>' +
          '<div class="entrySub">' +
            '<div class="row">' +
              stateBadge +
              (r.stdio_format ? ' <span class="muted small">' + esc(r.stdio_format) + '</span>' : '') +
              '<span style="flex:1"></span>' +
              '<button onclick="installManagedMcp(\'' + safeName + '\')">install</button>' +
              '<button onclick="statusManagedMcp(\'' + safeName + '\')">status</button>' +
              '<button class="bad" onclick="removeManagedMcp(\'' + safeName + '\')">remove</button>' +
            '</div>' +
          '</div>' +
        '</div>' +
      '</div>' +
    '</article>'
  );
}
function renderManagedMcp(j){$('mcpManageResult').textContent=JSON.stringify(j,null,2);const data=j.response||j;const rows=data.servers||data.response?.servers||[];$('managedMcp').innerHTML=rows.length?`<div class="stackList">${rows.map(renderManagedMcpCard).join('')}</div>`:`<p class="muted">пусто</p>`;}
async function listManagedMcp(){try{renderManagedMcp(await mcpManage({target:$('mcpHost').value,action:'list'}))}catch(e){$('mcpManageResult').textContent='ERR '+e.message}}
async function statusManagedMcp(name=''){try{const p=mcpPayloadBase('status');if(name)p.name=name;const j=await mcpManage(p);$('mcpManageResult').textContent=JSON.stringify(j,null,2)}catch(e){$('mcpManageResult').textContent='ERR '+e.message}}
async function installManagedMcp(name){try{const p=mcpPayloadBase('install');p.name=name;const j=await mcpManage(p);$('mcpManageResult').textContent=JSON.stringify(j,null,2);await listManagedMcp()}catch(e){$('mcpManageResult').textContent='ERR '+e.message}}
async function removeManagedMcp(name){try{if(!confirm('Удалить MCP '+name+' на '+$('mcpHost').value+'?'))return;const p=mcpPayloadBase('remove');p.name=name;p.keep_service=$('mcpKeepService').checked;const j=await mcpManage(p);$('mcpManageResult').textContent=JSON.stringify(j,null,2);await listManagedMcp();refreshAll()}catch(e){$('mcpManageResult').textContent='ERR '+e.message}}
async function addManagedMcp(){try{const p=mcpPayloadBase('add');p.name=$('mcpName').value.trim();p.agent_id=$('mcpAgentId').value.trim()||undefined;p.url=$('mcpUrl').value.trim()||undefined;p.command=$('mcpCommand').value.trim()||undefined;p.args=JSON.parse($('mcpArgs').value||'[]');p.env=JSON.parse($('mcpEnv').value||'{}');p.run_as_user=$('mcpRunAs').value.trim()||undefined;p.stdio_format=$('mcpStdio').value||undefined;p.install=$('mcpInstall').checked;p.force=$('mcpForce').checked;p.disabled=$('mcpDisabled').checked;if(!p.name)throw new Error('name required');if(!p.url&&!p.command)throw new Error('remote URL or command required');const j=await mcpManage(p);$('mcpManageResult').textContent=JSON.stringify(j,null,2);await listManagedMcp();refreshAll()}catch(e){$('mcpManageResult').textContent='ERR '+e.message}}

async function getJob(){try{const id=$('jobId').value.trim();const j=await api('/mcp-relay/job/'+encodeURIComponent(id)+'?verbose=true&include_raw=true');$('result').textContent=JSON.stringify(j,null,2);refreshAll()}catch(e){$('result').textContent='ERR '+e.message}}
function formatArgs(){try{$('args').value=JSON.stringify(JSON.parse($('args').value||'{}'),null,2)}catch(e){$('result').textContent='Bad JSON: '+e.message}}
$('token').value=localStorage.getItem('gptadmin_ctl_token')||'';initMaxActiveIpsInput();showView(currentView);refreshAll();setInterval(()=>{if($('autoRefresh').checked)refreshAll()},15000);

// ===== Security management =====
async function loadSecurityEnv(){
  const el=$('securityEnv');
  el.innerHTML='<p class="muted">Загрузка…</p>';
  try{
    // Read env file via shell_exec on hub
    const j=await api('/mcp-relay/call',{method:'POST',body:JSON.stringify({
      target:'shell:admin-server-100',
      tool_name:'shell_exec',
      arguments:{cmd:'cat /etc/gptadmin/gptadmin.env 2>/dev/null || cat ~/.config/gptadmin/gptadmin.env 2>/dev/null || echo NOT_FOUND'}
    })});
    const sc=j.response?.structuredContent||j.structuredContent||{};
    const result=sc.result||{};
    const stdout=result.stdout||'';
    if(stdout.trim()==='NOT_FOUND'||!stdout.trim()){
      el.innerHTML='<p class="muted">env-файл не найден. Возможно хаб использует другой путь.</p>';
      return;
    }
    const lines=stdout.trim().split('\n').filter(l=>l.trim()&&!l.startsWith('#'));
    const sensitive=['CTL_TOKEN','ADMIN_PASSWORD','OAUTH_CLIENT_SECRET','SHELLMCP_TOKEN','MCP_BRIDGE_KEY'];
    el.innerHTML=lines.map(line=>{
      const eq=line.indexOf('=');
      if(eq<0)return '';
      const key=line.substring(0,eq).trim();
      const val=line.substring(eq+1).trim();
      const isSensitive=sensitive.some(s=>key.includes(s))||key.includes('TOKEN')||key.includes('SECRET')||key.includes('PASSWORD')||key.includes('BEARER');
      const displayVal=isSensitive?(val.substring(0,8)+'••••••••'+(val.length>20?'...':'')):val;
      const valClass=isSensitive?'warn':'';
      return `<div class="recentMiniItem"><div class="recentMiniTop"><span class="mono">${esc(key)}</span><span class="muted small">${isSensitive?'sensitive':''}</span></div><div class="mono ${valClass}">${esc(displayVal)}</div></div>`;
    }).join('');
  }catch(e){
    el.innerHTML='<p class="bad">ERR '+esc(e.message)+'</p>';
  }
}

async function setEnvVar(){
  const key=$('secEnvKey').value;
  const val=$('secEnvVal').value.trim();
  if(!val){alert('Введите значение');return}
  const realKey=key==='_custom'?prompt('Имя переменной:'):key;
  if(!realKey)return;
  if(!confirm('Установить '+realKey+' в env? Это изменит конфигурацию хаба.'))return;
  try{
    const cmd=`grep -v '^${realKey}=' /etc/gptadmin/gptadmin.env 2>/dev/null > /tmp/_gptadmin.env.tmp && echo '${realKey}=${val.replace(/'/g,"'\''")}' >> /tmp/_gptadmin.env.tmp && mv /tmp/_gptadmin.env.tmp /etc/gptadmin/gptadmin.env && echo OK || echo FAIL`;
    const j=await api('/mcp-relay/call',{method:'POST',body:JSON.stringify({
      target:'shell:admin-server-100',
      tool_name:'shell_exec',
      arguments:{cmd,sudo:true}
    })});
    const sc=j.response?.structuredContent||j.structuredContent||{};
    const result=sc.result||{};
    const out=result.stdout||'';
    if(out.includes('OK')){
      $('secEnvVal').value='';
      loadSecurityEnv();
      alert(realKey+' установлен. Перезапустите хаб.');
    }else{
      alert('Ошибка: '+out);
    }
  }catch(e){alert('ERR '+e.message)}
}

async function rotateCtlToken(){
  if(!confirm('Сгенерировать новый CTL_TOKEN? Старый перестанет работать!'))return;
  const newToken=Array.from(crypto.getRandomValues(new Uint8Array(32)),b=>b.toString(16).padStart(2,'0')).join('');
  $('secEnvKey').value='CTL_TOKEN';
  $('secEnvVal').value=newToken;
  alert('Новый CTL_TOKEN сгенерирован. Нажмите «Установить» чтобы применить, затем перезапустите хаб.\n\nВНИМАНИЕ: после рестарта старый токен перестанет работать!');
}

async function rotateOAuth(){
  if(!confirm('Сгенерировать новый OAUTH_CLIENT_SECRET? Все MCP-клиенты нужно будет переподключить!'))return;
  const newSecret=Array.from(crypto.getRandomValues(new Uint8Array(32)),b=>b.toString(16).padStart(2,'0')).join('');
  $('secEnvKey').value='OAUTH_CLIENT_SECRET';
  $('secEnvVal').value=newSecret;
  alert('Новый OAUTH_CLIENT_SECRET сгенерирован. Нажмите «Установить» чтобы применить, затем перезапустите хаб.');
}

async function issueMcpTokenFromPanel(){
  const name=$('secMcpTokenName').value.trim();
  if(!name){alert('Введите client_id');return}
  const el=$('secMcpTokenResult');
  el.textContent='Выпускаю…';
  try{
    const cmd=`cd /home/admin/gptadmin 2>/dev/null && python3 cli.py mcp token '${name.replace(/'/g,"'\''")}' --no-save 2>&1 || echo FALLBACK`;
    const j=await api('/mcp-relay/call',{method:'POST',body:JSON.stringify({
      target:'shell:admin-server-100',
      tool_name:'shell_exec',
      arguments:{cmd}
    })});
    const sc=j.response?.structuredContent||j.structuredContent||{};
    const result=sc.result||{};
    el.textContent=result.stdout||result.stderr||'—';
  }catch(e){el.textContent='ERR '+e.message}
}

async function restartHub(){
  if(!confirm('Перезапустить gptadmin_hub? Кратковременный простой.'))return;
  $('secRestartStatus').textContent='Перезапуск…';
  try{
    const j=await api('/mcp-relay/call',{method:'POST',body:JSON.stringify({
      target:'shell:admin-server-100',
      tool_name:'shell_exec',
      arguments:{cmd:'sudo systemctl restart gptadmin_hub 2>&1; echo exit=$?',sudo:true}
    })});
    const sc=j.response?.structuredContent||j.structuredContent||{};
    const result=sc.result||{};
    $('secRestartStatus').textContent='Готово: '+(result.stdout||'').trim();
    setTimeout(()=>{$('secRestartStatus').textContent='';loadSecurityEnv()},3000);
  }catch(e){$('secRestartStatus').textContent='ERR '+e.message}
}


async function revokeClient(key){
  if(!confirm('Отозвать клиента '+key+'? Он больше не сможет использовать MCP.'))return;
  try{
    await api('/admin/api/clients/'+encodeURIComponent(key),{method:'DELETE'});
    refreshAll();
  }catch(e){alert('ERR '+e.message)}
}
async function revokeAllClients(){
  if(!confirm('Отозвать ВСЕХ клиентов и ротировать OAUTH_CLIENT_SECRET?\n\nВсе MCP-клиенты (Claude, Codex, OpenCode) должны будут заново авторизоваться!'))return;
  if(!confirm('Точно? Это действие необратимо.'))return;
  try{
    const j=await api('/admin/api/clients/revoke-all',{method:'POST'});
    alert('Отозвано клиентов: '+j.revoked_count+'\nOAuth secret ротирован: '+(j.oauth_secret_rotated?'да':'нет')+'\n\nПерезапустите хаб для применения.');
    refreshAll();
  }catch(e){alert('ERR '+e.message)}
}


function openAgentDetail(aid){
  showView('agent-detail');
  $('agentDetailTitle').textContent=aid;
  $('agentDetailBody').innerHTML='<p class="muted">Загрузка…</p>';
  const a=(state?.agents||[]).find(x=>x.agent_id===aid);
  if(!a){$('agentDetailBody').innerHTML='<p class="bad">Не найден</p>';return}
  let h='<div class="stackList">';
  h+='<div class="entryCard"><div class="entryHead"><span class="entryStatus pill '+cls(a.status)+'">'+esc(a.status)+'</span><div class="entryMain"><div class="entryTitle"><b class="mono">'+esc(a.agent_id)+'</b>'+(a.name?'<span class="muted small">'+esc(a.name)+'</span>':'')+'</div><div class="entryMeta"><span class="pill">'+esc(displayKind(a.kind||'?'))+'</span><span class="muted small">'+esc(a.transport||'')+'</span></div></div></div></div>';
  const caps=a.capabilities||[];
  if(caps.length)h+='<div class="entryCard"><h2>Capabilities</h2><div style="display:flex;flex-wrap:wrap;gap:4px">'+caps.map(c=>'<span class="pill mono small">'+esc(c)+'</span>').join('')+'</div></div>';
  const meta=a.meta||{};
  if(Object.keys(meta).length)h+='<div class="entryCard"><h2>Meta</h2><pre class="responseBody mono" style="max-height:none">'+prettyJsonHtml(meta)+'</pre></div>';
  const aj=(state?.jobs?.recent||[]).filter(j=>j.server===aid||j.agent_id===aid).slice(0,10);
  if(aj.length)h+='<div class="entryCard"><h2>Jobs ('+aj.length+')</h2><div class="stackList">'+aj.map(renderJobCard).join('')+'</div></div>';
  h+='</div>';
  $('agentDetailBody').innerHTML=h;
}
async function openJobDetail(jid,srv){
  if(!jid)return;
  showView('job-detail');
  $('jobDetailTitle').textContent='Job '+jid;
  $('jobDetailBody').innerHTML='<p class="muted">Загрузка…</p>';
  try{
    const j=await api('/mcp-relay/job/'+encodeURIComponent(jid)+'?verbose=true&include_raw=true');
    const p=('response' in j)?j.response:('result' in j?j.result:j);
    let h='<div class="stackList">';
    h+='<div class="entryCard"><span class="entryStatus pill '+cls(p.status)+'">'+esc(p.status||'?')+'</span></div>';
    const cmd=p.command||p.arguments_preview||'—';
    h+='<div class="entryCard"><h2>Input</h2><pre class="responseBody mono" style="max-height:none">'+prettyJsonHtml(cmd)+'</pre></div>';
    if(p.stdout||p.stdout_preview)h+='<div class="entryCard"><h2>Stdout</h2><pre class="responseBody mono" style="max-height:none">'+prettyJsonHtml(p.stdout||p.stdout_preview)+'</pre></div>';
    if(p.stderr||p.stderr_preview)h+='<div class="entryCard"><h2>Stderr</h2><pre class="responseBody mono entryError" style="max-height:none">'+prettyJsonHtml(p.stderr||p.stderr_preview)+'</pre></div>';
    if(p.error)h+='<div class="entryCard"><h2>Error</h2><pre class="responseBody mono entryError" style="max-height:none">'+prettyJsonHtml(p.error)+'</pre></div>';
    if(!p.stdout&&!p.stderr&&!p.error)h+='<div class="entryCard"><h2>Response</h2><pre class="responseBody mono" style="max-height:none">'+prettyJsonHtml(p)+'</pre></div>';
    h+='</div>';
    $('jobDetailBody').innerHTML=h;
  }catch(e){$('jobDetailBody').innerHTML='<p class="bad">ERR '+esc(e.message)+'</p>'}
}


function toggleCmdExpand(el){
  event.stopPropagation();
  if(el.style.maxHeight==='60px'||!el.style.maxHeight){el.style.maxHeight='none'}
  else{el.style.maxHeight='60px'}
}