<?php
require '/var/www/html/vendor/autoload.php';$app=require '/var/www/html/bootstrap/app.php';$app->make(Illuminate\Contracts\Console\Kernel::class)->bootstrap();
config(['database.default'=>'sqlite','database.connections.sqlite'=>['driver'=>'sqlite','database'=>':memory:','prefix'=>''],'cache.default'=>'array']);
require __DIR__.'/LeadEndpoint.php';
use Illuminate\Support\Facades\DB;use Illuminate\Support\Facades\Schema;use Illuminate\Database\Schema\Blueprint;
Schema::create('users',function(Blueprint $t){$t->unsignedInteger('id')->primary();$t->string('phone')->nullable();});
Schema::create('opportunities',function(Blueprint $t){$t->id();});
Schema::create('loan_products',function(Blueprint $t){$t->id();$t->string('name');$t->integer('status');});
foreach(['2026_07_20_000001_create_leads_table.php','2026_07_20_000003_create_lead_activities_table.php'] as $file){$m=require '/var/www/html/database/migrations/'.$file;$m->up();}
$m=require '/var/www/html/database/migrations/2026_07_20_000008_add_existing_user_id_to_leads_table.php';$m->up();
DB::table('loan_products')->insert([['id'=>1,'name'=>'Test product','status'=>1],['id'=>2,'name'=>'Inactive product','status'=>0]]);
$checks=0;function check($v,$label){global $checks;if(!$v)throw new RuntimeException('FAILED: '.$label);$checks++;}
function leadCall($action,$fields=[],$phone='260970000001'){
 if(in_array($action,['save','submit'],true) && !isset($fields['lead_ref']))$fields['lead_ref']=DB::table('leads')->where('customer_contact_phone',$phone)->orderByDesc('id')->value('lead_ref');
 $data=array_merge(['phone'=>$phone,'action'=>$action,'request_id'=>hash('sha256',$action.json_encode($fields))],$fields);
 $r=Illuminate\Http\Request::create('/v1/leads','POST',$data);$r->attributes->set('lms_integration_id',15);
 $res=(new MightyFin\Ussd\LeadEndpoint)->handle($r);return [$res->getStatusCode(),json_decode($res->getContent(),true)];
}
[$status,$r]=leadCall('current');check($status===200&&$r['reference']==='','unknown caller has no lead');
[$status]=leadCall('start');check($status===422&&DB::table('leads')->count()===0,'no capture before consent');
[$status,$r]=leadCall('start',['consent'=>true]);$ref=$r['reference'];check($status===200&&str_starts_with($ref,'LEAD-'),'real lead reference');
check(DB::table('users')->count()===0,'no account, PIN or OTP created');
$l=DB::table('leads')->first();check($l->source==='USSD'&&$l->status==='new'&&$l->qualification_status==='pending'&&!$l->converted_at,'USSD lead remains unqualified and unconverted');
leadCall('start',['consent'=>true]);check(DB::table('leads')->count()===1&&DB::table('lead_activities')->count()===1,'duplicate consent reuses lead and activity');
[$status,$r]=leadCall('save',['product_id'=>1]);check($status===200&&$r['product_id']===1&&$r['next_step']==='amount','progressive product saved');
[$status]=leadCall('save',['product_id'=>2]);check($status===422,'inactive product rejected');
leadCall('save',['amount'=>'500.00']);[$status,$r]=leadCall('current');check($r['amount']==='500.00'&&$r['next_step']==='name','amount resumes after a new session');
leadCall('save',['name'=>null]);[$status,$r]=leadCall('current');check($r['next_step']==='review','optional name skipped');
leadCall('save',['amount'=>'750.00']);leadCall('save',['amount'=>'500.00']);[$status,$r]=leadCall('current');check($r['amount']==='500.00','return to an earlier valid answer applies edit');
leadCall('save',['name'=>'Example Customer']);[$status,$r]=leadCall('submit',['consent'=>true]);check($status===200&&$r['reference']===$ref&&$r['next_step']==='recorded','follow-up preserves same lead');
$n=DB::table('lead_activities')->where('activity_type','ussd_submit')->count();leadCall('submit',['consent'=>true]);check(DB::table('lead_activities')->where('activity_type','ussd_submit')->count()===$n,'submit replay has one follow-up event');
[$status,$r]=leadCall('current',[],'260970000002');check($r['reference']==='','different caller cannot retrieve lead');
leadCall('assistance',['consent'=>true]);check(DB::table('leads')->count()===1,'assistance reuses same enquiry');
DB::table('leads')->where('id',$l->id)->update(['qualification_status'=>'qualified']);[$status]=leadCall('save',['amount'=>'600.00']);check($status===409,'qualified lead requires staff continuation');
DB::table('leads')->where('id',$l->id)->update(['source'=>'Website','qualification_status'=>'pending']);[$status,$r]=leadCall('start',['consent'=>true]);check(!$r['editable']&&DB::table('leads')->count()===1,'existing other-channel lead reused without overwriting provenance');
[$status]=leadCall('save',['lead_ref'=>'LEAD-wrong','amount'=>'700.00']);check($status===409,'stale lead reference cannot edit another case');
DB::table('leads')->where('id',$l->id)->update(['status'=>'converted','converted_at'=>now()]);
[$status,$r]=leadCall('start',['consent'=>true]);check($status===200&&$r['reference']===$ref&&DB::table('leads')->count()===1,'late retry after staff conversion does not create a new lead');
check(DB::table('users')->count()===0,'lead never turns into an account automatically');
echo json_encode(['passed'=>$checks,'database'=>'isolated sqlite','production_writes'=>false]);
