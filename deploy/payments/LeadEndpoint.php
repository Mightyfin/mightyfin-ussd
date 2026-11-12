<?php
namespace MightyFin\Ussd;
use App\Models\Lead;
use App\Models\LeadActivity;
use App\Models\LoanProduct;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Str;

// A prospect enquiry, not registration, approval, KYC completion or disbursement.
final class LeadEndpoint {
    private function current(string $phone) {
        $variants=[$phone,'+'.$phone,'0'.substr($phone,3),substr($phone,3)];
        return Lead::where('lead_type','customer')->whereIn('customer_contact_phone',$variants)
            ->whereNull('converted_at')->whereNotIn('status',['lost','disqualified','converted'])
            ->orderByDesc('id')->first();
    }
    private function representation(?Lead $lead):array {
        if(!$lead)return ['reference'=>'','editable'=>false];
        $editable=!$lead->converted_at && !$lead->trashed() && $lead->source==='USSD' && $lead->status==='new' && !in_array($lead->qualification_status,['qualified','completed'],true);
        return ['reference'=>$lead->lead_ref,'editable'=>$editable,
            'product_id'=>$editable?$lead->requested_product_id:null,
            'amount'=>$editable?$lead->customer_est_loan_amount:null,
            'name'=>$editable?($lead->customer_name??''):'',
            'next_step'=>$editable?$this->nextStep($lead):''];
    }
    private function nextStep(Lead $lead):string {
        $last=LeadActivity::where('lead_id',$lead->id)->whereIn('activity_type',['ussd_save','ussd_submit'])->orderByDesc('id')->first();
        if(!$last)return 'products';
        if($last->activity_type==='ussd_submit')return 'recorded';
        foreach(['product_id'=>'amount','amount'=>'name','name'=>'review'] as $field=>$step) {
            if(str_contains($last->notes,'saved field: '.$field))return $step;
        }
        return 'products';
    }
    public function handle(Request $request) {
        $v=$request->validate(['phone'=>['required','string','regex:/^260[79][0-9]{8}$/'],
            'action'=>'required|in:current,catalogue,start,save,submit,assistance',
            'request_id'=>['required','string','regex:/^[a-f0-9]{64}$/'],
            'lead_ref'=>'required_if:action,save,submit|string|max:40',
            'consent'=>'sometimes|accepted','product_id'=>'sometimes|integer|min:1',
            'amount'=>['nullable','string','numeric','gt:0','regex:/^(?:[1-9][0-9]{0,12}|0)(?:\.[0-9]{1,2})?$/'],
            'name'=>['nullable','string','min:3','max:80','regex:/^[\pL\pM][\pL\pM\s.\x{0027}-]+$/u']]);
        $phone=$v['phone'];$action=$v['action'];
        if($action==='catalogue')return response()->json(['products'=>LoanProduct::where('status',1)->orderBy('name')->limit(100)->get(['id','name','min_principal_amount','max_principal_amount'])->map(fn($p)=>['id'=>$p->id,'name'=>mb_substr($p->name,0,80),'minimum'=>is_numeric($p->min_principal_amount)?number_format($p->min_principal_amount,2,'.',''):null,'maximum'=>is_numeric($p->max_principal_amount)?number_format($p->max_principal_amount,2,'.',''):null])]);
        if($action==='current') {
            $result=$this->representation($this->current($phone));
            $ids=DB::table('users')->whereIn('phone',[$phone,'+'.$phone,'0'.substr($phone,3),substr($phone,3)])->limit(2)->pluck('id');
            $result['has_loan']=false;$result['application_reference']='';
            if($ids->count()===1) {
                $id=(int)$ids[0];
                $result['has_loan']=DB::table('loans as l')->join('applications as a','a.id','=','l.application_id')
                    ->where('a.user_id',$id)->whereNull('l.deleted_at')->whereIn('l.status',['active','disbursed','written_off'])
                    ->where(fn($q)=>$q->whereNull('l.user_id')->orWhere('l.user_id',$id))->exists();
                $application=DB::table('applications')->where('user_id',$id)->where('closed',0)->whereIn('status',[0,1,2,3])->orderByDesc('id')->first(['id']);
                if($application)$result['application_reference']='APP-'.$application->id;
            }
            return response()->json($result);
        }
        if(in_array($action,['start','submit','assistance'],true) && !filter_var($v['consent']??false,FILTER_VALIDATE_BOOLEAN))return response()->json(['error'=>'CONSENT_REQUIRED'],422);
        $lock=Cache::lock('ussd:lead:'.hash('sha256',$phone),15);
        if(!$lock->get())return response()->json(['error'=>'RETRY_SAME_REQUEST'],409);
        try { return DB::transaction(function()use($v,$phone,$action,$request){
            $event='ussd:'.substr($v['request_id'],0,40);
            if($action!=='save') {
                $replay=Lead::withTrashed()->whereIn('customer_contact_phone',[$phone,'+'.$phone,'0'.substr($phone,3),substr($phone,3)])
                    ->whereHas('activities',fn($q)=>$q->where('outcome',$event))->first();
                if($replay) {
                    if($replay->trashed())return response()->json(['error'=>'LEAD_UNAVAILABLE'],409);
                    return response()->json($this->representation($replay));
                }
            }
            $lead=$this->current($phone);
            if($lead)$lead=Lead::whereKey($lead->id)->lockForUpdate()->first();
            if(!$lead && !in_array($action,['start','assistance'],true))return response()->json(['error'=>'LEAD_NOT_FOUND'],404);
            if(!$lead) {
                $owners=DB::table('users')->whereIn('phone',[$phone,'+'.$phone,'0'.substr($phone,3),substr($phone,3)])->limit(2)->pluck('id');
                $lead=Lead::create(['uuid'=>(string)Str::orderedUuid(),'lead_type'=>'customer','customer_type'=>'individual',
                    'source'=>'USSD','status'=>'new','qualification_status'=>'pending','qualification_checklist_key'=>'customer_individual_default',
                    'customer_contact_phone'=>$phone,'existing_user_id'=>$owners->count()===1?(int)$owners[0]:null]);
                $lead->lead_ref='LEAD-'.str_pad((string)$lead->id,6,'0',STR_PAD_LEFT);$lead->save();
            }
            if(in_array($action,['save','submit'],true) && $v['lead_ref']!==$lead->lead_ref)return response()->json(['error'=>'LEAD_CHANGED'],409);
            $editable=$this->representation($lead)['editable'];
            $event='ussd:'.substr($v['request_id'],0,40);
            $replayed=LeadActivity::where('lead_id',$lead->id)->where('outcome',$event)->exists();
            if($replayed && $action!=='save')return response()->json($this->representation($lead));
            $changed=false;
            if($action==='save') {
                if(!$editable)return response()->json(['error'=>'OFFICER_CONTINUATION_REQUIRED','reference'=>$lead->lead_ref],409);
                if(isset($v['product_id'])) {
                    if(!LoanProduct::whereKey($v['product_id'])->where('status',1)->exists())return response()->json(['error'=>'PRODUCT_UNAVAILABLE'],422);
                    $lead->requested_product_id=$v['product_id'];
                }
                if(array_key_exists('amount',$v)) {
                    if($v['amount']!==null) {
                        $product=LoanProduct::whereKey($lead->requested_product_id)->where('status',1)
                            ->whereNotNull('min_principal_amount')->where('min_principal_amount','>=',0)
                            ->where('max_principal_amount','>',0)->whereColumn('max_principal_amount','>=','min_principal_amount');
                        if(!(clone $product)->exists())return response()->json(['error'=>'PRODUCT_RANGE_UNAVAILABLE'],422);
                        if(!$product->where('min_principal_amount','<=',$v['amount'])->where('max_principal_amount','>=',$v['amount'])->exists())return response()->json(['error'=>'AMOUNT_OUTSIDE_PRODUCT_RANGE'],422);
                    }
                    $lead->customer_est_loan_amount=$v['amount'];
                }
                if(array_key_exists('name',$v))$lead->customer_name=$v['name'];
                $changed=$lead->isDirty();if($changed)$lead->save();
            }
            if($action==='submit' && $editable && !$lead->requested_product_id)return response()->json(['error'=>'PRODUCT_REQUIRED'],422);
            $next=$action==='submit'?'Loan officer to continue enquiry, qualification and outstanding requirements.':
                ($action==='assistance'?'Customer support or loan officer to contact the caller.':'Continue progressive USSD capture or officer follow-up.');
            if(!$replayed || $changed)LeadActivity::create(['lead_id'=>$lead->id,'activity_type'=>'ussd_'.$action,'activity_date'=>now(),
                'notes'=>'USSD channel; integration credential '.(int)$request->attributes->get('lms_integration_id').
                    (in_array($action,['start','submit','assistance'],true)?'; customer consent to capture/contact v1 recorded.':'; saved field: '.(isset($v['product_id'])?'product_id':(array_key_exists('amount',$v)?'amount':'name'))),
                'outcome'=>$event,'next_action'=>$next]);
            return response()->json($this->representation($lead));
        }); } finally {$lock->release();}
    }
}
