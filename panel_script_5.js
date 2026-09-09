function serverQuotaRuntimeState(){
  if(!window.__codexHealthQuotaRuntime)window.__codexHealthQuotaRuntime={scope:null,attempted:false,loading:false,snapshots:new Map(),refreshAll:{status:'idle'}};
  return window.__codexHealthQuotaRuntime;
}

function syncQuotaCacheScope(){
  const state=serverQuotaRuntimeState(),scope=managementKey()||'';
  if(state.scope===scope)return;
  state.scope=scope;
  state.attempted=false;
  state.loading=false;
  state.snapshots.clear();
  state.refreshAll={status:'idle'};
  quotaCache.clear();
  quotaLoading.clear();
  quotaResetting.clear();
}

function normalizeServerQuotaSnapshot(raw){
  if(!raw||typeof raw!=='object')return null;
  return {
    authIndex:normalizeAuthIndex(raw.auth_index),
    status:String(raw.status||''),
    fetchedAt:raw.fetched_at||null,
    lastAttemptAt:raw.last_attempt_at||null,
    windows:Array.isArray(raw.windows)?raw.windows.map(window=>({
      minutes:numericValue(window?.minutes)||0,
      used:numericValue(window?.used)||0,
      remaining:numericValue(window?.remaining)||0,
      resetAt:window?.reset_at||null,
    })):[],
    resetCredits:Array.isArray(raw.reset_credits)?raw.reset_credits.map(item=>({
      id:String(item?.id||''),
      expiresAtMs:item?.expires_at||null,
    })):[],
    resetAvailableCount:numericValue(raw.reset_available_count),
    resetApplicableCount:numericValue(raw.reset_applicable_count),
    resetError:String(raw.reset_error||''),
    error:String(raw.error||''),
  };
}

function applyServerQuotaView(payload){
  const state=serverQuotaRuntimeState();
  const next=new Map();
  for(const raw of payload?.quotas||[]){
    const snapshot=normalizeServerQuotaSnapshot(raw);
    if(snapshot?.authIndex)next.set(snapshot.authIndex,snapshot);
  }
  state.snapshots=next;
  state.refreshAll=payload?.refresh_all&&typeof payload.refresh_all==='object'?payload.refresh_all:{status:'idle'};
  quotaRefreshAllRunning=state.refreshAll.status==='refreshing';
  syncQuotaRefreshAllButton();
}

function applyServerQuotaSnapshot(raw){
  const snapshot=normalizeServerQuotaSnapshot(raw);
  if(!snapshot?.authIndex)return null;
  serverQuotaRuntimeState().snapshots.set(snapshot.authIndex,snapshot);
  return snapshot;
}

async function loadQuotaCacheOnce(){
  syncQuotaCacheScope();
  const state=serverQuotaRuntimeState();
  if(state.attempted||state.loading)return;
  state.attempted=true;
  state.loading=true;
  try{
    const payload=await api('/quota',{timeoutMs:6000});
    applyServerQuotaView(payload);
    if(currentAccounts.length)renderAccounts(currentAccounts);
  }catch(_){
    // The quota cache is optional panel state. Account/status loading must not be blocked by it.
  }finally{
    state.loading=false;
  }
}

function pruneQuotaCache(accounts){
  const keep=new Set((accounts||[]).map(account=>normalizeAuthIndex(account?.auth_index)).filter(Boolean));
  const snapshots=serverQuotaRuntimeState().snapshots;
  for(const key of snapshots.keys())if(!keep.has(key))snapshots.delete(key);
}

function quotaStateForAccount(account){
  const key=normalizeAuthIndex(account?.auth_index);
  return key?serverQuotaRuntimeState().snapshots.get(key):undefined;
}

function quotaUpdatedText(value){
  if(!value)return '';
  const date=new Date(value);
  if(Number.isNaN(date.getTime()))return '';
  const two=n=>String(n).padStart(2,'0');
  return two(date.getMonth()+1)+'/'+two(date.getDate())+' '+two(date.getHours())+':'+two(date.getMinutes())+':'+two(date.getSeconds());
}

function quotaInfoHTML(state,authIndex){
  const key=escapeHTML(authIndex||'');
  if(!state)return '<button class="quota-action quota-refresh-one" type="button" data-auth-index="'+key+'">刷新额度</button>';
  const hasData=Array.isArray(state.windows)&&state.windows.length>0;
  if(!hasData&&state.status==='refreshing')return '<span class="quota-loading">刷新中…</span>';
  if(!hasData&&state.status==='error')return '<div class="quota-error-wrap"><span class="quota-error" title="'+escapeHTML(state.error||'')+'">刷新失败</span><button class="quota-action quota-refresh-one" type="button" data-auth-index="'+key+'">重试</button></div>';
  const rows=(state.windows||[]).map(window=>{
    const pct=Math.round(window.remaining),tone=quotaTone(window.remaining);
    return '<div class="quota-row"><span class="quota-label">'+escapeHTML(quotaDurationLabel(window.minutes))+'</span><span class="quota-bar"><span class="quota-bar-fill '+tone+'" style="width:'+Number(window.remaining).toFixed(2)+'%"></span></span><strong class="quota-pct">'+pct+'%</strong><span class="quota-reset">'+escapeHTML(shortDateText(window.resetAt))+'</span></div>';
  }).join('');
  const updated=quotaUpdatedText(state.fetchedAt);
  let stateText='';
  if(state.status==='refreshing')stateText='<span class="quota-loading">刷新中…</span>';
  else if(state.status==='error')stateText='<span class="quota-error" title="'+escapeHTML(state.error||'')+'">刷新失败</span>';
  const action=state.status==='refreshing'?'':'<button class="quota-action quota-refresh-one" type="button" data-auth-index="'+key+'">'+(state.status==='error'?'重试':'刷新')+'</button>';
  return '<div class="quota-stack">'+rows+'<div class="quota-card-actions"><span class="quota-updated">'+(updated?'更新于 '+escapeHTML(updated):'')+'</span>'+stateText+action+'</div></div>';
}

function quotaResetOrdinal(index){
  const names=['零','一','二','三','四','五','六','七','八','九','十'];
  return index<=10?'第'+names[index]+'次重置':'第'+index+'次重置';
}

function resetInfoHTML(state,authIndex){
  if(!state||(!state.fetchedAt&&state.status!=='success'))return '';
  const key=escapeHTML(authIndex||'');
  const credits=(state.resetCredits||[]).filter(item=>item&&item.expiresAtMs).sort((a,b)=>new Date(a.expiresAtMs)-new Date(b.expiresAtMs));
  const count=state.resetApplicableCount??state.resetAvailableCount??(credits.length||null);
  const canReset=state.status==='success'&&Number(count||0)>0;
  const isResetting=quotaResetting.has(normalizeAuthIndex(authIndex));
  let body='';
  if(credits.length){
    body='<div class="reset-stack">'+credits.map((item,index)=>'<span class="reset-credit-row"><strong>'+escapeHTML(quotaResetOrdinal(index+1))+'</strong><span>'+escapeHTML(shortDateText(item.expiresAtMs))+'</span></span>').join('')+'</div>';
    if(count!==null&&Number(count)>credits.length)body+='<span class="reset-count">另有 '+escapeHTML(String(Number(count)-credits.length))+' 次可用</span>';
  }else if(count!==null){
    body='<span class="reset-count">可重置 '+escapeHTML(String(count))+' 次</span>';
  }else if(state.resetError){
    body='<span class="reset-error" title="'+escapeHTML(state.resetError)+'">获取失败</span>';
  }
  if(canReset)body+='<div class="reset-actions"><button class="quota-action quota-reset-one" type="button" data-auth-index="'+key+'" '+(isResetting?'disabled':'')+'>'+(isResetting?'重置中…':'重置额度')+'</button></div>';
  return body;
}

function syncQuotaRefreshAllButton(){
  const button=el('quotaRefreshAll');
  if(!button)return;
  const backendRunning=serverQuotaRuntimeState().refreshAll?.status==='refreshing';
  const running=quotaRefreshAllRunning||backendRunning;
  button.disabled=running;
  button.classList.toggle('is-loading',running);
  const label=button.querySelector('.quota-refresh-all-label');
  if(label)label.textContent=running?'刷新中':'刷新全部额度';
}

function markQuotaRefreshing(authIndex){
  const key=normalizeAuthIndex(authIndex),state=serverQuotaRuntimeState();
  const previous=state.snapshots.get(key)||{authIndex:key,windows:[],resetCredits:[]};
  state.snapshots.set(key,{...previous,status:'refreshing',lastAttemptAt:new Date().toISOString(),error:''});
}

async function refreshQuotaForAccount(account,options={}){
  const silent=options.silent===true,key=normalizeAuthIndex(account?.auth_index);
  if(!key){if(!silent)showNotice('该账号缺少 auth_index，无法刷新额度',true);return false}
  if(quotaAccountDisabled(account)){if(!silent)showNotice('已停用账号不刷新额度',true);return false}
  if(quotaLoading.has(key))return null;
  quotaLoading.add(key);
  markQuotaRefreshing(key);
  renderAccounts(currentAccounts);
  try{
    const payload=await api('/quota/refresh',{method:'POST',body:JSON.stringify({auth_index:key}),timeoutMs:55000});
    const snapshot=applyServerQuotaSnapshot(payload?.snapshot);
    if(snapshot?.status==='success'){
      if(!silent)showNotice((account.email||key)+' 额度已刷新');
      return true;
    }
    if(!silent)showNotice(snapshot?.error||'额度刷新失败',true);
    return false;
  }catch(error){
    const previous=serverQuotaRuntimeState().snapshots.get(key)||{authIndex:key,windows:[],resetCredits:[]};
    serverQuotaRuntimeState().snapshots.set(key,{...previous,status:'error',error:error instanceof Error?error.message:'额度刷新失败'});
    if(!silent)handleActionError(error);
    return false;
  }finally{
    quotaLoading.delete(key);
    renderAccounts(currentAccounts);
  }
}

async function refreshAllQuotas(){
  if(quotaRefreshAllRunning||serverQuotaRuntimeState().refreshAll?.status==='refreshing')return;
  const targets=currentAccounts.filter(account=>normalizeAuthIndex(account.auth_index)&&!quotaAccountDisabled(account));
  if(!targets.length){showNotice('没有可刷新的 Codex 账号',true);return}
  quotaRefreshAllRunning=true;
  serverQuotaRuntimeState().refreshAll={status:'refreshing',total:targets.length,completed:0,success:0,failed:0};
  for(const account of targets)markQuotaRefreshing(account.auth_index);
  renderAccounts(currentAccounts);
  syncQuotaRefreshAllButton();
  try{
    const payload=await api('/quota/refresh-all',{method:'POST',body:'{}',timeoutMs:200000});
    applyServerQuotaView(payload);
    const summary=payload?.refresh_all||{};
    const success=Number(summary.success||0),failed=Number(summary.failed||0);
    showNotice('额度刷新完成：成功 '+success+' 个'+(failed?'，失败 '+failed+' 个':''),failed>0&&success===0);
  }catch(error){
    handleActionError(error);
    serverQuotaRuntimeState().refreshAll={status:'error'};
  }finally{
    quotaRefreshAllRunning=false;
    syncQuotaRefreshAllButton();
    renderAccounts(currentAccounts);
  }
}

async function resetQuotaForAccount(account){
  const key=normalizeAuthIndex(account?.auth_index),state=serverQuotaRuntimeState().snapshots.get(key);
  if(!key||!state||state.status!=='success')return;
  const count=state.resetApplicableCount??state.resetAvailableCount??(state.resetCredits?.length||0);
  if(Number(count||0)<=0||quotaResetting.has(key))return;
  if(typeof window.confirm==='function'&&!window.confirm('确认消耗 1 次重置额度？此操作会立即重置当前 Codex 额度窗口。'))return;
  quotaResetting.add(key);
  renderAccounts(currentAccounts);
  try{
    const payload=await api('/quota/reset',{method:'POST',body:JSON.stringify({auth_index:key}),timeoutMs:65000});
    const snapshot=applyServerQuotaSnapshot(payload?.snapshot);
    if(snapshot?.status==='success')showNotice((account.email||key)+' 额度已重置');
    else showNotice(snapshot?.error||'额度重置失败',true);
  }catch(error){
    handleActionError(error);
  }finally{
    quotaResetting.delete(key);
    renderAccounts(currentAccounts);
  }
}

if(typeof window!=='undefined'&&typeof window.fetch==='function'){
  syncQuotaCacheScope();
  void loadQuotaCacheOnce();
}
