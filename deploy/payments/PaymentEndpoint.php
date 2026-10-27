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
        $input=$request->validate(['phone'=>['required','string','regex:/^260[79][0-9]{8}$/']]);
        $phone=$input['phone'];$user=$this->owner($phone);
        if(!$user)return response()->json(['error'=>'ACCOUNT_UNAVAILABLE'],404);
        $credential=(int)$request->attributes->get('lms_integration_id');
        $path=$request->path();
        if(in_array($path,['v1/loans','v1/status-loans'],true)) {
            if($path==='v1/loans' && !self::accountReady())return response()->json(['error'=>'PAYMENT_ACCOUNT_NOT_CONFIGURED'],503);
            $query=$this->loans($user);
            if($path==='v1/status-loans') {
                // Fully repaid/closed loans must remain available for receipts.
                $query->whereExists(fn($q)=>$q->select(DB::raw(1))->from('integration_collections as c')->whereColumn('c.loan_id','l.id')->where('c.credential_id',$credential));
            } else {
                $query->whereIn('l.status',['disbursed','active','written_off'])->where(fn($q)=>$q->whereNull('l.currency_code')->orWhereIn('l.currency_code',['','ZMW']));
            }
            $rows=$query->orderBy('l.id')->limit(6)->get(['l.id']);
            if($rows->count()>5)return response()->json(['error'=>'OFFICER_ASSISTANCE_REQUIRED'],409);
            return response()->json(['loans'=>$rows->map(fn($r)=>['id'=>(int)$r->id,'label'=>'Loan ending '.str_pad(substr((string)$r->id,-6),6,'0',STR_PAD_LEFT)])]);
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
            'customer'=>['phone'=>$phone],'external_reference'=>'ussd-'.$v['request_id'],
            'consent_reference'=>'ussd-confirm-'.$v['request_id'],'requested_by'=>'ussd'];
        // Serialize different request IDs against the same loan. Never issue a
        // second prompt while a prior collection is unresolved.
        $lock=Cache::lock('ussd:collection:'.$credential.':'.$loan,60);
        if(!$lock->get())return response()->json(['status'=>'processing'],202);
        try {
            $same=DB::table('integration_collections')->where('credential_id',$credential)->where('idempotency_key',$v['request_id'])->exists();
            if(!$same) {
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
