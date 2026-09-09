function syncWindowOptimizedSimulatorOnly(){
  if(typeof document==='undefined'||typeof document.querySelector!=='function')return;
  const panel=document.querySelector('.schedule-panel');
  if(!panel)return;
  panel.classList.toggle('window-simulator-only',selectedMode()==='window_optimized');
}

const toggleModeBeforeSimulatorOnly=toggleMode;
toggleMode=function(){
  const result=toggleModeBeforeSimulatorOnly.apply(this,arguments);
  syncWindowOptimizedSimulatorOnly();
  return result;
};

syncWindowOptimizedSimulatorOnly();
