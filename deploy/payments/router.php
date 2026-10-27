<?php
// Private sidecar: reuse the deployed Laravel app/services without altering LMS routes.
require '/var/www/html/vendor/autoload.php';
$app=require '/var/www/html/bootstrap/app.php';
$app->make(Illuminate\Contracts\Console\Kernel::class)->bootstrap();
require __DIR__.'/PaymentEndpoint.php';
$request=Illuminate\Http\Request::capture();
try {
    if($request->method()==='GET' && $request->path()==='healthz') {
        response()->json(['status'=>'ok','service'=>'ussd-lms-payments','payment_account_configured'=>MightyFin\Ussd\PaymentEndpoint::accountReady(),
            'provider_configured'=>app(App\Services\Integrations\CollectionGateway::class)->ready('lipila','mobile_money') && (bool)config('services.lipila.webhook_secret')])->send();return;
    }
    if($request->method()!=='POST' || !in_array($request->path(),['v1/loans','v1/status-loans','v1/collect','v1/status'],true)) {
        response()->json(['error'=>'NOT_FOUND'],404)->send();return;
    }
    $request->headers->set('Accept','application/json');
    $middleware=new App\Http\Middleware\AuthenticateLmsIntegration();
    $response=$middleware->handle($request,function($r)use($app){
        $credential=$r->attributes->get('lms_integration_id');
        $limit='ussd-payments:rate:'.$credential;
        if(Illuminate\Support\Facades\RateLimiter::tooManyAttempts($limit,120))return response()->json(['error'=>'RATE_LIMITED'],429);
        Illuminate\Support\Facades\RateLimiter::hit($limit,60);
        return (new MightyFin\Ussd\PaymentEndpoint())->handle($r,$app->make(App\Services\Integrations\CollectionService::class));
    },'payments:ussd');
    $response->send();
} catch(Illuminate\Validation\ValidationException $e) {
    response()->json(['error'=>'INVALID_PAYMENT_INPUT'],422)->send();
} catch(Throwable $e) {
    // Never emit tokens, customer data, stack traces or provider internals.
    response()->json(['error'=>'PAYMENTS_UNAVAILABLE'],503)->send();
}
