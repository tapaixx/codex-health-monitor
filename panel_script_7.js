const PANEL_PRIVACY_KEY='codex-health-monitor:privacy-masked';

function panelPrivacyState(){
  if(!window.__codexHealthPrivacy){
    let masked=true;
    try{
      const stored=typeof sessionStorage!=='undefined'?sessionStorage.getItem(PANEL_PRIVACY_KEY):null;
      if(stored==='0')masked=false;
      else if(stored==='1')masked=true;
    }catch(_){ }
    window.__codexHealthPrivacy={masked};
  }
  return window.__codexHealthPrivacy;
}

function maskEmail(value){
  const raw=String(value??'').trim();
  if(!raw||raw==='-')return raw||'-';
  const at=raw.lastIndexOf('@');
  if(at<=0||at===raw.length-1)return maskIdentifier(raw);
  const local=raw.slice(0,at),domain=raw.slice(at+1),parts=domain.split('.');
  const localKeep=local.length>=5?2:1;
  const maskedLocal=local.slice(0,localKeep)+'***';
  const host=parts.shift()||'';
  const maskedHost=(host.slice(0,1)||'*')+'***';
  const suffix=parts.length?'.'+parts.join('.'):'';
  return maskedLocal+'@'+maskedHost+suffix;
}

function maskIdentifier(value){
  const raw=String(value??'').trim();
  if(!raw||raw==='-')return raw||'-';
  if(raw.length<=4)return raw.charAt(0)+'••'+raw.charAt(raw.length-1);
  const keep=raw.length>=16?4:raw.length>=10?3:2;
  return raw.slice(0,keep)+'••••'+raw.slice(-keep);
}

function maskSensitiveText(value){
  let text=String(value??'');
  if(!text)return text;
  text=text.replace(/[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}/gi,match=>maskEmail(match));
  text=text.replace(/\b[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\b/gi,match=>maskIdentifier(match));
  text=text.replace(/\b(?:acct|account|auth|org|user|project|session|request|req)[_-][A-Za-z0-9_-]{6,}\b/gi,match=>maskIdentifier(match));
  text=text.replace(/\b[0-9a-f]{20,}\b/gi,match=>maskIdentifier(match));
  return text;
}

function privacyDisplayEmail(value){return panelPrivacyState().masked?maskEmail(value):String(value??'')}
function privacyDisplayIdentifier(value){return panelPrivacyState().masked?maskIdentifier(value):String(value??'')}
function privacyDisplayText(value){return panelPrivacyState().masked?maskSensitiveText(value):String(value??'')}

function privacyEyeMarkup(masked){
  if(masked)return '<svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><path d="M2.5 12s3.5-6 9.5-6 9.5 6 9.5 6-3.5 6-9.5 6-9.5-6-9.5-6Z"/><circle cx="12" cy="12" r="2.5"/></svg>';
  return '<svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><path d="m3 3 18 18"/><path d="M10.7 6.2A10.7 10.7 0 0 1 12 6c6 0 9.5 6 9.5 6a15.6 15.6 0 0 1-2.2 2.8M6.5 6.5C4 8.2 2.5 12 2.5 12s3.5 6 9.5 6c1.7 0 3.2-.5 4.5-1.2M9.8 9.8a3 3 0 0 0 4.4 4.4"/></svg>';
}

function updatePrivacyToggleButton(){
  const button=typeof document!=='undefined'?document.getElementById('privacyToggle'):null;
  if(!button)return;
  const masked=panelPrivacyState().masked;
  button.innerHTML=privacyEyeMarkup(masked);
  button.classList.toggle('is-masked',masked);
  button.setAttribute('title',masked?'显示敏感信息':'隐藏敏感信息');
  button.setAttribute('aria-label',masked?'显示敏感信息':'隐藏敏感信息');
  button.setAttribute('aria-pressed',String(!masked));
}

function installPrivacyToggle(){
  if(typeof document==='undefined'||typeof document.querySelector!=='function'||typeof document.createElement!=='function')return;
  const actions=document.querySelector('.accounts-card .actions');
  if(!actions||document.getElementById('privacyToggle')){updatePrivacyToggleButton();return}
  const button=document.createElement('button');
  button.id='privacyToggle';
  button.type='button';
  button.className='button icon-button privacy-toggle';
  button.addEventListener('click',()=>{
    const state=panelPrivacyState();
    state.masked=!state.masked;
    try{if(typeof sessionStorage!=='undefined')sessionStorage.setItem(PANEL_PRIVACY_KEY,state.masked?'1':'0')}catch(_){ }
    updatePrivacyToggleButton();
    if(currentAccounts.length)renderAccounts(currentAccounts);
    if(historyRows.length)renderHistoryPage();
    showNotice(state.masked?'敏感信息已隐藏':'敏感信息已显示');
  });
  const refresh=document.getElementById('refresh');
  if(refresh&&refresh.parentNode===actions)actions.insertBefore(button,refresh);else actions.appendChild(button);
  updatePrivacyToggleButton();
}

function applyV5Copy(){
  if(typeof document==='undefined'||typeof document.querySelector!=='function')return;
  const quotaLabel=document.querySelector('label[for="quotaMinutes"]');
  if(quotaLabel)quotaLabel.textContent='单窗口预计可用时长（分钟）';
}

const quotaInfoHTMLWithoutPrivacy=quotaInfoHTML;
quotaInfoHTML=function(state,authIndex){
  if(!state||!panelPrivacyState().masked)return quotaInfoHTMLWithoutPrivacy(state,authIndex);
  return quotaInfoHTMLWithoutPrivacy({...state,error:maskSensitiveText(state.error||''),resetError:maskSensitiveText(state.resetError||'')},authIndex);
};

const resetInfoHTMLWithoutPrivacy=resetInfoHTML;
resetInfoHTML=function(state,authIndex){
  if(!state||!panelPrivacyState().masked)return resetInfoHTMLWithoutPrivacy(state,authIndex);
  return resetInfoHTMLWithoutPrivacy({...state,error:maskSensitiveText(state.error||''),resetError:maskSensitiveText(state.resetError||'')},authIndex);
};

const showNoticeWithoutPrivacy=showNotice;
showNotice=function(message,isError=false){
  return showNoticeWithoutPrivacy(privacyDisplayText(message),isError);
};

renderAccounts=function(accounts){
  currentAccounts=accounts.map(a=>({...a}));
  const excluded=excludedSet();
  for(const account of currentAccounts)account.selected=!excluded.has(accountSelectionKey(account));
  const selected=currentAccounts.filter(a=>a.selected).length;
  el('accountsMeta').textContent=currentAccounts.length+' 个 Codex 账号（已选择 '+selected+' 个；新账号默认生效）';
  el('summaryAccounts').textContent=selected+' 个';
  el('accounts').innerHTML=currentAccounts.length?currentAccounts.map(account=>{
    const rawEmail=account.email||'-',email=privacyDisplayEmail(rawEmail),avatar=panelPrivacyState().masked?'•':(rawEmail==='-'?'?':rawEmail.trim().charAt(0));
    const authIndex=normalizeAuthIndex(account.auth_index),displayAuth=privacyDisplayIdentifier(authIndex||'-'),displayAccountID=privacyDisplayIdentifier(account.account_id||'-');
    const rawError=account.error_message||'-',displayError=privacyDisplayText(rawError);
    return '<tr><td class="select-cell"><input class="account-check" type="checkbox" data-key="'+escapeHTML(accountSelectionKey(account))+'" '+(account.selected?'checked':'')+' aria-label="'+escapeHTML(email)+' 生效"></td><td><div class="account"><span class="avatar">'+escapeHTML(avatar)+'</span><span class="account-email">'+escapeHTML(email)+'</span></div></td><td><span class="mono">'+escapeHTML(displayAuth)+'</span></td><td><span class="mono">'+escapeHTML(displayAccountID)+'</span></td><td>'+statusHTML(account)+cooldownHTML(account)+'</td><td class="quota-cell">'+quotaInfoHTML(quotaStateForAccount(account),authIndex)+'</td><td class="reset-cell">'+resetInfoHTML(quotaStateForAccount(account),authIndex)+'</td><td>'+httpHTML(account.http_status)+'</td><td>'+latencyText(account.latency_ms)+'</td><td class="date">'+dateText(account.checked_at)+'</td><td><span class="error" title="'+escapeHTML(displayError)+'">'+escapeHTML(displayError)+'</span></td></tr>';
  }).join(''):emptyHTML(11,'没有 Codex 账号');
};

renderHistoryPage=function(){
  const total=historyRows.length,pageCount=Math.max(1,Math.ceil(total/historyPageSize));
  historyPage=Math.max(1,Math.min(historyPage,pageCount));
  const start=(historyPage-1)*historyPageSize;
  const rows=historyRows.slice(start,start+historyPageSize).map(({run,account})=>run.error_code&&!(run.accounts||[]).length
    ?'<tr><td class="date">'+dateText(run.started_at)+'</td><td class="date">'+dateText(run.planned_at)+'</td><td>'+escapeHTML(triggerLabel(run.trigger))+'</td><td>-</td><td>'+statusHTML({status:'response_error'})+'</td><td>-</td><td>-</td><td><span class="error">'+escapeHTML(privacyDisplayText(run.error_message||run.error_code))+'</span></td></tr>'
    :'<tr><td class="date">'+dateText(run.started_at)+'</td><td class="date">'+dateText(run.planned_at)+'</td><td>'+escapeHTML(triggerLabel(run.trigger))+'</td><td><span class="account-email">'+escapeHTML(privacyDisplayEmail(account.email||('#'+account.auth_index)))+'</span></td><td>'+statusHTML(account)+cooldownHTML(account)+'</td><td>'+httpHTML(account.http_status)+'</td><td>'+latencyText(account.latency_ms)+'</td><td><span class="error">'+escapeHTML(privacyDisplayText(account.error_message||'-'))+'</span></td></tr>');
  el('history').innerHTML=rows.length?rows.join(''):emptyHTML(8,'暂无检测记录');
  el('historyRange').textContent=total?'第 '+(start+1)+'–'+Math.min(start+historyPageSize,total)+' 条，共 '+total+' 条':'共 0 条账号记录';
  el('historyPageInfo').textContent=historyPage+' / '+pageCount;
  el('historyPrev').disabled=historyPage<=1;
  el('historyNext').disabled=historyPage>=pageCount;
};

function installV5PanelEnhancements(){
  applyV5Copy();
  installPrivacyToggle();
  if(currentAccounts.length)renderAccounts(currentAccounts);
  if(historyRows.length)renderHistoryPage();
}

if(typeof document!=='undefined'&&typeof document.querySelector==='function')installV5PanelEnhancements();
