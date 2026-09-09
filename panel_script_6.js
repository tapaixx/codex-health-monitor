function simulatorHealthAssessment(available,work,threshold){
  const total=Math.max(0,Number(work)||0),usable=Math.max(0,Number(available)||0),rawThreshold=Number(threshold);
  const normalizedThreshold=Number.isFinite(rawThreshold)?Math.max(1,Math.min(100,rawThreshold)):80;
  const coverage=total>0?Math.max(0,Math.min(100,usable/total*100)):0;
  const roundedCoverage=Math.round(coverage*10)/10,roundedThreshold=Math.round(normalizedThreshold*10)/10;
  return {coverage:roundedCoverage,threshold:roundedThreshold,healthy:total>0&&coverage+1e-9>=normalizedThreshold,label:total>0&&coverage+1e-9>=normalizedThreshold?'健康':'风险'};
}

function ensureSimulatorHealthUI(){
  const pairs=[['strategyAValue','strategyAHealth'],['strategyBValue','strategyBHealth']];
  for(const [valueId,healthId] of pairs){
    const value=el(valueId);
    let health=document.getElementById(healthId);
    if(!health&&value&&typeof document.createElement==='function'){
      health=document.createElement('div');
      health.id=healthId;
      health.className='strategy-health';
      if(typeof health.setAttribute==='function')health.setAttribute('role','status');
      const strategy=typeof value.closest==='function'?value.closest('.strategy'):null;
      const bar=strategy&&typeof strategy.querySelector==='function'?strategy.querySelector('.bar'):null;
      if(bar&&bar.parentNode)bar.parentNode.insertBefore(health,bar.nextSibling);
      else if(value.parentNode)value.parentNode.appendChild(health);
    }
  }
  if(!document.getElementById('simulatorHealthStyles')&&typeof document.createElement==='function'&&document.head){
    const style=document.createElement('style');
    style.id='simulatorHealthStyles';
    style.textContent='.strategy-health{display:flex;align-items:center;gap:6px;flex-wrap:wrap;margin-top:7px;font-size:10px}.strategy-health-badge{display:inline-flex;align-items:center;min-height:21px;border-radius:999px;padding:2px 7px;font-weight:760}.strategy-health.healthy .strategy-health-badge{color:#08794f;background:var(--green2)}.strategy-health.risk .strategy-health-badge{color:#c92525;background:var(--red2)}.strategy-health-detail{color:var(--muted)}.strategy.health-risk{border-color:#efb7b7;background:color-mix(in srgb,var(--red2) 42%,var(--surface))}.strategy.health-ok{border-color:#b9dfcf}@media(max-width:720px){.strategy-health{align-items:flex-start}.strategy-health-detail{width:100%}}';
    document.head.appendChild(style);
  }
}

function renderSimulatorHealthResult(id,result){
  const node=document.getElementById(id);
  if(!node)return;
  node.classList.toggle('healthy',result.healthy);
  node.classList.toggle('risk',!result.healthy);
  node.innerHTML='<span class="strategy-health-badge">'+result.label+'</span><span class="strategy-health-detail">覆盖率 '+result.coverage+'% · 阈值 '+result.threshold+'%</span>';
  const strategy=typeof node.closest==='function'?node.closest('.strategy'):null;
  if(strategy){
    strategy.classList.toggle('health-ok',result.healthy);
    strategy.classList.toggle('health-risk',!result.healthy);
  }
}

function renderSimulatorHealth(schedule,availableA,availableB,work){
  ensureSimulatorHealthUI();
  const threshold=Number(schedule&&schedule.health_threshold)||80;
  renderSimulatorHealthResult('strategyAHealth',simulatorHealthAssessment(availableA,work,threshold));
  renderSimulatorHealthResult('strategyBHealth',simulatorHealthAssessment(availableB,work,threshold));
}

const renderSimulatorWithoutHealth=renderSimulator;
renderSimulator=function(){
  const result=renderSimulatorWithoutHealth.apply(this,arguments);
  try{
    const schedule=scheduleFromUI(true),workStart=timeMin(schedule.work_start),lunchStart=timeMin(schedule.lunch_start),lunchEnd=timeMin(schedule.lunch_end),workEnd=timeMin(schedule.work_end),anchor=timeMin(schedule.anchor_time);
    const work=(lunchStart-workStart)+(workEnd-lunchEnd);
    renderSimulatorHealth(schedule,simulateAvailability(schedule,workStart),simulateAvailability(schedule,anchor),work);
  }catch(_){ }
  return result;
};
