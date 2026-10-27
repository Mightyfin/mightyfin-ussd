USSD public edge preparation

Deploy with:
  docker compose -f deploy/gateway/compose.yaml up -d

Cloudflare Tunnel published application (host cloudflared on this server):
  Subdomain: ussd
  Domain: mightyfinance.co.zm
  Path: empty
  Service type: HTTP
  Service URL: http://127.0.0.1:28092
  Additional settings: defaults

Cloudflare terminates public HTTPS. The tunnel connects to loopback HTTP.
Do not use HTTPS for this local listener. Do not point the tunnel at the private
LMS adapter (28091) or demo simulator (28090).

This dedicated Caddy container restarts unless stopped and publishes only on
127.0.0.1. It has no LMS credentials, customer data or upstream proxy route.
GET /healthz returns 200 with callback_ready=false: edge liveness only.
/ussd returns 503 USSD_NOT_READY until the operator protocol/authentication is
implemented. All other paths return 404. No callback is processed or recorded.
The image's Caddy executable needs NET_BIND_SERVICE because of its file capability;
other capabilities are dropped. Filesystem is read-only with ephemeral data/config.

Expected public callback: https://ussd.mightyfinance.co.zm/ussd
Health check after saving the tunnel: https://ussd.mightyfinance.co.zm/healthz

Validated locally: health=200, callback=503, private API and root=404.
Public DNS did not resolve at deployment time; external connectivity remains to
be verified after the Cloudflare route is saved. A successful health check does
not mean that the operator callback or customer journey is ready.

Rollback:
  docker compose -f deploy/gateway/compose.yaml down
This stops only the dedicated edge. The private LMS adapter and simulator remain.
