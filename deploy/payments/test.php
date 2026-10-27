<?php
// Isolated SQLite + fake provider/poster. Never load production credentials.
require '/var/www/html/vendor/autoload.php';$app=require '/var/www/html/bootstrap/app.php';
$app->make(Illuminate\Contracts\Console\Kernel::class)->bootstrap();
config(['database.default'=>'sqlite','database.connections.sqlite'=>['driver'=>'sqlite','database'=>':memory:','prefix'=>''],'cache.default'=>'array','app.key'=>'base64:'.base64_encode(str_repeat('x',32))]);
require __DIR__.'/PaymentEndpoint.php';
use Illuminate\Support\Facades\DB;use Illuminate\Support\Facades\Schema;use Illuminate\Database\Schema\Blueprint;
Schema::create('payment_types',function(Blueprint $t){$t->id();$t->string('code');$t->boolean('active');$t->integer('gl_account_id')->nullable();});
Schema::create('gl_accounts',function(Blueprint $t){$t->id();$t->string('account_type');});
DB::table('payment_types')->insert(['id'=>1,'code'=>'lipila_mobile_money','active'=>true,'gl_account_id'=>77]);
DB::table('gl_accounts')->insert(['id'=>77,'account_type'=>'asset']);
Schema::create('users',function(Blueprint $t){$t->id();$t->string('phone');});
Schema::create('applications',function(Blueprint $t){$t->id();$t->integer('user_id');});
Schema::create('loans',function(Blueprint $t){$t->id();$t->integer('application_id');$t->integer('user_id')->nullable();$t->string('status');$t->string('currency_code')->nullable();$t->timestamp('deleted_at')->nullable();});
$m=require '/var/www/html/database/migrations/2026_10_01_170000_create_integration_collections.php';$m->up();
DB::table('users')->insert([['id'=>1,'phone'=>'0970000001'],['id'=>2,'phone'=>'260970000002']]);
DB::table('applications')->insert([['id'=>101,'user_id'=>1],['id'=>102,'user_id'=>2]]);
DB::table('loans')->insert([['id'=>201,'application_id'=>101,'user_id'=>1,'status'=>'active','currency_code'=>'ZMW'],['id'=>202,'application_id'=>102,'user_id'=>2,'status'=>'active','currency_code'=>'ZMW']]);
class FakeGateway extends App\Services\Integrations\CollectionGateway {
 public int $calls=0;public bool $uncertain=false;public bool $mismatch=false;
 public function ready(string $p,string $m):bool{return true;}
 public function base(string $p):string{return 'https://provider.invalid';}
 public function initiate(object $r,array $d):?array{$this->calls++;if($this->uncertain)throw new RuntimeException('uncertain');return null;}
 public function verify(object $r):array{return ['status'=>'successful','reference'=>$r->reference,'amount'=>$this->mismatch?'99.00':$r->amount,'currency'=>'ZMW','method'=>'mobile-money'];}
}
class FakePoster extends App\Services\Integrations\CollectionRepaymentPoster {public int $calls=0;public function post(object $r):int{return 900+(++$this->calls);}}
$gateway=new FakeGateway;$poster=new FakePoster;$service=new App\Services\Integrations\CollectionService($gateway,$poster);$endpoint=new MightyFin\Ussd\PaymentEndpoint;
$checks=0;function check($ok,$name){global $checks;if(!$ok)throw new RuntimeException('FAILED: '.$name);$checks++;}
function callEndpoint($path,$data){global $endpoint,$service;$r=Illuminate\Http\Request::create('/v1/'.$path,'POST',$data);$r->attributes->set('lms_integration_id',7);$res=$endpoint->handle($r,$service);return [$res->getStatusCode(),json_decode($res->getContent(),true)];}
$base=['phone'=>'260970000001','loan_id'=>201,'amount'=>'10.00','request_id'=>str_repeat('a',64),'confirmed'=>true];
[$status,$body]=callEndpoint('loans',['phone'=>$base['phone']]);check($status===200 && count($body['loans'])===1 && $body['loans'][0]['id']===201,'real loan id, not application id');
[$status]=callEndpoint('collect',array_replace($base,['loan_id'=>202]));check($status===404 && $gateway->calls===0,'foreign loan denied');
DB::table('users')->insert(['id'=>3,'phone'=>'+260970000001']);[$status]=callEndpoint('loans',['phone'=>$base['phone']]);check($status===404,'duplicate normalized phone fails closed');DB::table('users')->where('id',3)->delete();
foreach(['0.00','1e3','-1.00','10.001','10000000.00'] as $amount){try{callEndpoint('collect',array_replace($base,['amount'=>$amount]));check(false,'bad amount');}catch(Illuminate\Validation\ValidationException $e){check(true,'bad amount rejected');}}
try{callEndpoint('collect',array_replace($base,['confirmed'=>false]));check(false,'unconfirmed');}catch(Illuminate\Validation\ValidationException $e){check(true,'confirmation required');}
DB::table('payment_types')->where('id',1)->update(['gl_account_id'=>null]);
[$status,$body]=callEndpoint('collect',$base);check($status===503 && $gateway->calls===0,'missing accounting mapping cannot initiate');
DB::table('payment_types')->where('id',1)->update(['gl_account_id'=>77]);
[$status,$body]=callEndpoint('collect',$base);check($status===201 && $body['status']==='pending' && $gateway->calls===1,'initiate once');
[$status,$body]=callEndpoint('collect',$base);check($status===200 && $gateway->calls===1,'duplicate replay');
[$status]=callEndpoint('collect',array_replace($base,['amount'=>'11.00']));check($status===409 && $gateway->calls===1,'changed payload same key rejected');
[$status,$body]=callEndpoint('collect',array_replace($base,['request_id'=>str_repeat('b',64)]));check($status===202 && $body['status']==='existing_pending' && $gateway->calls===1,'new session cannot duplicate pending collection');
[$status,$body]=callEndpoint('status',$base);check($status===200 && $body['status']==='completed' && $poster->calls===1,'verified posting');
callEndpoint('status',$base);check($poster->calls===1,'posting not duplicated');
$gateway->uncertain=true;$second=array_replace($base,['request_id'=>str_repeat('c',64)]);[$status,$body]=callEndpoint('collect',$second);check($status===202 && $body['status']==='unknown','uncertain initiation preserved');
callEndpoint('collect',$second);check($gateway->calls===2,'unknown initiation not resent');
$gateway->mismatch=true;[$status,$body]=callEndpoint('status',$base);check($body['status']==='unknown' && $poster->calls===1,'mismatch never posts');
DB::table('loans')->where('id',201)->update(['status'=>'closed']);
[$status,$body]=callEndpoint('status-loans',['phone'=>$base['phone']]);check(count($body['loans'])===1,'closed loan receipt remains accessible');
[$status,$body]=callEndpoint('loans',['phone'=>$base['phone']]);check(count($body['loans'])===0,'closed loan cannot initiate another payment');
echo json_encode(['passed'=>$checks,'database'=>'isolated sqlite','provider'=>'fake','posting'=>'fake','real_financial_actions'=>false]).PHP_EOL;
