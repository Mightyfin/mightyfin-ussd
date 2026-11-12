<?php
namespace MightyFin\Ussd;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Cache;
use App\Services\Integrations\CollectionService;

final class PaymentEndpoint {
    public static function accountReady(): bool {
        $type=DB::table('payment_types')->where('code','lipila_mobile_money')->where('active',true)->whereNotNull('gl_account_id')->first();
        return $type && DB::table('gl_accounts')->where('id',$type->gl_account_id)->where('account_type','asset')->exists();
    }
    private function owner(string $phone): ?int {
        $variants=[$phone,'+'.$phone,'0'.substr($phone,3),substr($phone,3)];
        $ids=DB::table('users')->whereIn('phone',$variants)->limit(2)->pluck('id');
        return $ids->count()===1?(int)$ids[0]:null;
    }
    private function loans(int $user) {
        return DB::table('loans as l')->join('applications as a','a.id','=','l.application_id')
            ->where('a.user_id',$user)->whereNull('l.deleted_at')
            ->where(fn($q)=>$q->whereNull('l.user_id')->orWhere('l.user_id',$user));
    }
    public function handle(Request $request,CollectionService $service) {
        $input=$request->validate(['phone'=>['required','string','regex:/^260[79][0-9]{8}$/'],'payer_phone'=>['sometimes','string','regex:/^260[79][0-9]{8}$/']]);
        $phone=$input['phone'];$user=$this->owner($phone);
        if($request->path()==='v1/welcome') {
            // Minimal caller greeting only; ambiguous/missing accounts reveal no identity.
            $name=$user?DB::table('users')->where('id',$user)->value('first_name'):null;
            return response()->json(['first_name'=>is_string($name)?$name:'']);
        }
        if(!$user){
            if(in_array($request->path(),['v1/loans','v1/status-loans'],true)) {
                $matches=DB::table('users')->whereIn('phone',[$phone,'+'.$phone,'0'.substr($phone,3),substr($phone,3)])->count();
                if($matches===0)return response()->json(['loans'=>[]]);
            }
            return response()->json(['error'=>'ACCOUNT_UNAVAILABLE'],404);
        }
        $credential=(int)$request->attributes->get('lms_integration_id');
        $path=$request->path();
        if(in_array($path,['v1/loans','v1/status-loans'],true)) {
            if($path==='v1/loans' && !(\App\Models\Loans::customerBalanceState($user)['is_complete']??false))return response()->json(['error'=>'ACCOUNT_REVIEW_REQUIRED'],409);
            $query=$this->loans($user);
            if($path==='v1/status-loans') {
                // Fully repaid/closed loans must remain available for receipts.
                $query->whereExists(fn($q)=>$q->select(DB::raw(1))->from('integration_collections as c')->whereColumn('c.loan_id','l.id')->where('c.credential_id',$credential));
            } else {
                $query->whereIn('l.status',['disbursed','active','written_off'])->where(fn($q)=>$q->whereNull('l.currency_code')->orWhereIn('l.currency_code',['','ZMW']));
            }
            $rows=$query->orderBy('l.id')->limit(101)->get(['l.id']);
            if($rows->count()>100)return response()->json(['error'=>'OFFICER_ASSISTANCE_REQUIRED'],409);
            $loans=[];
            foreach($rows as $r) {
                $item=['id'=>(int)$r->id,'label'=>'Loan ending '.str_pad(substr((string)$r->id,-6),6,'0',STR_PAD_LEFT)];
                if($path==='v1/loans') {
                    $state=app(\App\Services\Loans\LoanAccountStateService::class)->state(\App\Models\Loans::findOrFail($r->id));
                    if(!is_numeric($state['outstanding_balance']??null)||!is_numeric($state['amount_due']??null))return response()->json(['error'=>'BALANCE_UNAVAILABLE'],503);
                    if($state['outstanding_balance']<=0 || $state['amount_due']<=0)continue;
                    $item['due']=number_format($state['amount_due'],2,'.','');
                    $item['outstanding']=number_format($state['outstanding_balance'],2,'.','');
                }
                $loans[]=$item;
                if(count($loans)>30)return response()->json(['error'=>'OFFICER_ASSISTANCE_REQUIRED'],409);
            }
            return response()->json(['loans'=>$loans]);
        }
        $v=$request->validate(['loan_id'=>'required|integer|min:1']);$loan=(int)$v['loan_id'];
        if(!$this->loans($user)->where('l.id',$loan)->exists())return response()->json(['error'=>'LOAN_UNAVAILABLE'],404);
        if($path==='v1/status') {
            // Timestamps have second precision and IDs are random UUIDs. For
            // tied requests never prefer an older completion over uncertainty.
            $row=DB::table('integration_collections')->where('credential_id',$credential)->where('loan_id',$loan)->orderByDesc('created_at')->orderByRaw("CASE WHEN status = 'completed' THEN 1 ELSE 0 END")->orderByDesc('id')->first();
            if(!$row)return response()->json(['status'=>'none']);
            [$result,$status]=$service->verify($row);
            // A failed provider lookup is not evidence of failure/success.
            return response()->json(['status'=>$status===200?$result['status']:'unknown']);
        }
        if($path!=='v1/collect')return response()->json(['error'=>'NOT_FOUND'],404);
        $v=$request->validate(['amount'=>['required','string','regex:/^(?:[1-9][0-9]{0,6}\.\d{2}|0\.(?:0[1-9]|[1-9][0-9]))$/'],
            'request_id'=>['required','string','regex:/^[a-f0-9]{64}$/'],'confirmed'=>'required|accepted']);
        $data=['loan_id'=>$loan,'amount'=>$v['amount'],'currency'=>'ZMW','provider'=>'lipila','method'=>'mobile_money',
            'customer'=>['phone'=>$input['payer_phone']??$phone],'external_reference'=>'ussd-'.$v['request_id'],
            'consent_reference'=>'ussd-confirm-'.$v['request_id'],'requested_by'=>'ussd'];
        // Serialize different request IDs against the same loan. Never issue a
        // second prompt while a prior collection is unresolved.
        $lock=Cache::lock('ussd:collection:'.$credential.':'.$loan,60);
        if(!$lock->get())return response()->json(['status'=>'processing'],202);
        try {
            $same=DB::table('integration_collections')->where('credential_id',$credential)->where('idempotency_key',$v['request_id'])->exists();
            if(!$same) {
                $state=app(\App\Services\Loans\LoanAccountStateService::class)->state(\App\Models\Loans::findOrFail($loan));
                if(!is_numeric($state['outstanding_balance']??null)||!is_numeric($state['amount_due']??null))return response()->json(['error'=>'BALANCE_UNAVAILABLE'],503);
                if($state['outstanding_balance']<=0 || $state['amount_due']<=0 || (float)$v['amount']>(float)$state['outstanding_balance'])return response()->json(['error'=>'LOAN_BALANCE_CHANGED'],409);
                // Prevent receiving money without an explicit clearing mapping.
                // Do not guess a cash/fund-source account for provider receipts.
                if(!self::accountReady()) {
                    return response()->json(['error'=>'PAYMENT_ACCOUNT_NOT_CONFIGURED'],503);
                }
            }
            if(!$same && DB::table('integration_collections')->where('credential_id',$credential)->where('loan_id',$loan)->whereIn('status',['initiating','pending','unknown','review_required'])->exists()) {
                return response()->json(['status'=>'existing_pending'],202);
            }
            [$body,$status]=$service->create($credential,$v['request_id'],$data);
            if(!isset($body['data']))return response()->json(['error'=>'COLLECTION_UNAVAILABLE'],$status);
            return response()->json(['status'=>$body['data']['status']],$status);
        } finally {$lock->release();}
    }
}
