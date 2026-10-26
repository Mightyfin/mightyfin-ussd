'use strict';
const $=id=>document.getElementById(id), token=document.querySelector('meta[name="demo-token"]').content;
let session=null, sequence=0, busy=false, pending=null, state={}, tab='applications';
async function api(path,body){const response=await fetch(path,{method:body?'POST':'GET',headers:{'Content-Type':'application/json','X-Demo-Token':token},...(body?{body:JSON.stringify(body)}:{})});const data=await response.json();if(!response.ok){const e=new Error(data.message||data.error);e.code=data.error;throw e;}return data;}
function setBusy(value){busy=value;$('dial').disabled=value;$('send').disabled=value||!session||session.end;$('reply').disabled=value||!session||session.end;document.querySelectorAll('[data-input]').forEach(b=>b.disabled=value||!session||session.end);}
async function send(input,isDial=false){if(busy)return;$('phone-error').textContent='';setBusy(true);
 try{
  if(!pending){pending={session_id:isDial?'':session?.session_id||'',phone:$('phone').value,request_id:crypto.randomUUID(),sequence:isDial?1:sequence+1,input};}
  const reply=await api('/api/sessions',pending);session=reply;sequence=reply.sequence;pending=null;
  $('display').textContent=reply.text;$('display').scrollTop=0;$('screen-label').textContent=reply.screen_id+' / '+(reply.end?'SESSION ENDED':'IN SESSION');$('connection').textContent=reply.end?'ENDED':'CONNECTED';$('session-meta').textContent=reply.session_id+' · step '+sequence;$('reply').type='text';$('reply').inputMode=reply.input_mode||'text';$('reply').maxLength=reply.max_length||200;$('reply').placeholder=reply.input_mode==='decimal'?'e.g. 500.50':reply.input_mode==='numeric'?'Enter a number':reply.input_mode==='tel'?'e.g. 0970000001':'Enter your answer';$('reply-label').textContent='Your response';$('input-hint').textContent=reply.end?'':reply.input_hint||'';$('reply').value='';await refresh();
 }catch(err){$('reply').value='';$('phone-error').textContent=err.message;if(['SESSION_EXPIRED','SESSION_CONFIRMATION_REQUIRED','SESSION_ENDED'].includes(err.code)){pending=null;session=null;}else if(err.code&&err.code!=='LOCAL_STORAGE_UNAVAILABLE'){pending=null;}else{$('phone-error').textContent+=' Select Send again to retry the same request.';}}
 finally{setBusy(false);if(session&&!session.end)$('reply').focus();}
}
$('dial').addEventListener('click',()=>{pending=null;send('',true)});
$('reply-form').addEventListener('submit',e=>{e.preventDefault();send($('reply').value)});
document.querySelectorAll('[data-input]').forEach(b=>b.addEventListener('click',()=>send(b.dataset.input)));
$('refresh').addEventListener('click',refresh);
document.querySelectorAll('[data-tab]').forEach(b=>b.addEventListener('click',()=>{tab=b.dataset.tab;document.querySelectorAll('[data-tab]').forEach(x=>x.setAttribute('aria-selected',String(x===b)));render();}));
function element(tag,cls,text){const e=document.createElement(tag);if(cls)e.className=cls;if(text!==undefined)e.textContent=text;return e;}
function currency(value){return 'K'+(value/100).toLocaleString('en-ZM',{minimumFractionDigits:2,maximumFractionDigits:2});}
function empty(title,text){const e=element('div','empty');e.append(element('span','empty-icon','↗'),element('h3','',title),element('p','',text));$('panel').append(e);}
function card(ref,status,title,details){const e=element('article','card'),head=element('div','card-head');head.append(element('span','reference',ref),element('span','status',status));e.append(head,element('h3','',title));details.forEach(t=>e.append(element('p','',t)));return e;}
function buttons(card,ref,items,eventInput){const row=element('div','buttons');items.forEach(([action,label,danger])=>{const b=element('button','action'+(danger?' danger':''),label);b.addEventListener('click',async()=>{b.disabled=true;try{const res=await api('/api/demo/actions',{action,reference:ref,event_id:eventInput?.value||''});$('op-message').textContent=res.message;await refresh();}catch(e){$('op-message').textContent=e.message;}finally{b.disabled=false;}});row.append(b);});card.append(row);}
async function refresh(){try{state=await api('/api/demo/state');render();}catch(e){$('op-message').textContent=e.message;}}
function render(){
 const applications=Object.values(state.applications||{}),transactions=Object.values(state.transactions||{}),cases=Object.values(state.cases||{});
 $('app-count').textContent=applications.length;$('tx-count').textContent=transactions.length;$('case-count').textContent=cases.length;$('panel').replaceChildren();
 if(tab==='applications'){
  if(!applications.length){empty('Your first application starts on the phone.','Select Apply and complete registration. A saved draft will appear here as you go.');return;}
  applications.forEach(a=>{const c=card(a.id,a.status,a.product?('Demo '+(a.product==='salary'?'Salary':'Business')+' Loan'):'Application draft',[currency(a.amount_minor)+' · '+a.term_months+' months','KYC: '+a.kyc,'Next: '+a.next_action]);let actions=[];
   if(a.status==='KYC_PENDING')actions.push(['kyc_accept','Accept demo KYC']);if(a.status==='UNDER_REVIEW')actions.push(['offer','Issue demo offer'],['action_required','Request correction'],['decline','Decline',true]);if(a.status==='ACTION_REQUIRED')actions.push(['resume_review','Mark correction reviewed']);
   if(a.status==='OFFER_READY'){const offer=Object.values(state.offers||{}).find(o=>o.ApplicationID===a.id&&o.Status==='ACCEPTED');if(offer){c.append(element('p','','Offer v'+offer.Version+' accepted by customer'));actions.push(['release_payout','Release demo payout']);}else{actions.push(['revise_offer','Revise demo offer']);c.append(element('p','','Customer must review and accept on the phone.'));}}
   if(actions.length)buttons(c,a.id,actions);$('panel').append(c);
  });
 }else if(tab==='transactions'){
  if(!transactions.length){empty('Every money movement has a reference.','Demo payouts appear after offer acceptance. Repayments appear after a customer confirms a quote.');return;}
  transactions.forEach(t=>{const c=card(t.ID,t.Status,t.Kind==='PAYOUT'?'Demo payout':'Demo repayment',[currency(t.Amount)+' · '+t.Method,'Ledger: '+t.Posting,...(t.ReversalID?['Linked reversal: '+t.ReversalID]:[])]);let actions=[];
   const event=element('input','event-input');event.setAttribute('aria-label','Stable demo event ID for '+t.ID);event.value='event-'+crypto.randomUUID().slice(0,12);event.placeholder='Stable event ID';c.append(event,element('p','','Event ID: reuse an earlier ID to test duplicate protection.'));
   if(!['SUCCEEDED','FAILED','CANCELLED'].includes(t.Status))actions.push(['succeed','Confirm success'],['succeed_unposted','Success · posting delayed'],['unknown','Unknown'],['fail','Confirm failure',true]);
   if(t.Status==='SUCCEEDED'&&t.Posting!=='POSTED')actions.push(['post','Retry ledger posting']);if(t.Status==='SUCCEEDED'&&t.Posting==='POSTED'&&!t.ReversalID)actions.push(['reverse','Record reversal',true]);
   actions.push(['pending','Send late / duplicate event']);buttons(c,t.ID,actions,event);$('panel').append(c);
  });
 }else if(tab==='cases'){
  if(!cases.length){empty('Help stays accountable.','KYC handoffs, recovery and payment disputes create assigned cases with a next action.');return;}
  cases.forEach(x=>{const c=card(x.ID,x.Status,x.Category.replaceAll('_',' '),['Owner: '+x.Owner,x.NextAction,'Update deadline: '+(x.UpdateDue||'To be agreed')]);if(x.Status!=='RESOLVED')buttons(c,x.ID,[['resolve_case','Resolve demo case']]);$('panel').append(c);});
 }else{
  const audit=[...(state.audit||[])].reverse();if(!audit.length){empty('A traceable journey.','Consent, submissions and financial events appear here. PIN values are never shown.');return;}
  audit.slice(0,100).forEach(a=>{const row=element('div','activity-row');row.append(element('strong','',a.Action),element('time','',new Date(a.At).toLocaleString()+' · '+a.Actor),element('code','',a.Reference));$('panel').append(row);});
 }
}
refresh();
